package ghstore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const actionImportArchiveStart = actionJournalPrefix + "v34\n"
const importArchiveChunkBytes = actionJournalPartBytes / 4 * 3

// Documents reuse immutable v1 journal parts. Each chunk has its own transfer
// identity, so the existing per-transfer limit does not become a new total
// import-history limit. Root metadata remains bounded by the normal transport.
type importArchiveDocument struct {
	Name   string                 `json:"name"`
	Size   int                    `json:"size"`
	SHA256 string                 `json:"sha256"`
	Parts  []actionJournalPartRef `json:"parts"`
}

type importArchiveManifest struct {
	Journal   ActionJournal           `json:"journal"`
	Documents []importArchiveDocument `json:"documents"`
}

type importArchivePublication struct {
	manifest       importArchiveManifest
	parts          []actionJournalPart
	bodies         []string
	originalDigest string
}

func importOriginalDigest(command *ActionImportCommand) (string, error) {
	data, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	return journalTransportHash(string(data)), nil
}

func importArchiveTransfer(a ActionJournal, doc importArchiveDocument, index int) string {
	return journalTransportHash(fmt.Sprintf("td-import-document-v1\n%s\n%s\n%s\n%d\n%s\n%d", a.OperationID, a.CarrierID, doc.Name, doc.Size, doc.SHA256, index))
}

func renderImportArchiveManifest(manifest importArchiveManifest) (string, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	a := manifest.Journal
	return fmt.Sprintf("td import action %s (%s): %s\n\n%s%s%s", a.OperationID, a.SessionID, a.Phase, actionImportArchiveStart, data, markerEnd), nil
}

// Pure preparation includes root sizing with maximum-width native references.
// Those probes are never published. bindParts accepts actual verified comments
// and regenerates the root; an incomplete upload cannot authorize a mutation.
func planImportArchivePublication(a ActionJournal) (*journalPublication, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	frozen, err := copyCreationJournal(a)
	if err != nil {
		return nil, err
	}
	p := &importArchivePublication{manifest: importArchiveManifest{Journal: frozen, Documents: []importArchiveDocument{}}}
	p.originalDigest, err = importOriginalDigest(frozen.ImportCommand)
	if err != nil {
		return nil, err
	}
	add := func(name string, data []byte) error {
		if len(data) == 0 {
			return fmt.Errorf("import archive document %s is empty", name)
		}
		count := (len(data)-1)/importArchiveChunkBytes + 1
		doc := importArchiveDocument{Name: name, Size: len(data), SHA256: journalTransportHash(string(data)), Parts: make([]actionJournalPartRef, count)}
		for index, offset := 0, 0; offset < len(data); index, offset = index+1, offset+importArchiveChunkBytes {
			part := actionJournalPart{OperationID: a.OperationID, CarrierID: a.CarrierID, Transfer: importArchiveTransfer(a, doc, index), Index: 0, Count: 1,
				Data: base64.StdEncoding.EncodeToString(data[offset:min(offset+importArchiveChunkBytes, len(data))])}
			body, err := renderJournalPart(part)
			if err != nil {
				return err
			}
			p.parts, p.bodies = append(p.parts, part), append(p.bodies, body)
			doc.Parts[index] = actionJournalPartRef{ID: 1<<63 - 1, CreatedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), SHA256: journalTransportHash(body)}
		}
		p.manifest.Documents = append(p.manifest.Documents, doc)
		return nil
	}
	if err := add("input", []byte(frozen.ImportCommand.Input)); err != nil {
		return nil, err
	}
	frozen.ImportCommand.Input = ""
	for n := range frozen.ImportCommand.Members {
		member := &frozen.ImportCommand.Members[n]
		if member.Before == nil {
			continue
		}
		data, err := json.Marshal(member.Before)
		if err != nil {
			return nil, err
		}
		if err := add("before:"+member.TargetID, data); err != nil {
			return nil, err
		}
		member.Before = nil
	}
	if _, err := p.rootPlan(p.manifest); err != nil {
		return nil, fmt.Errorf("import archive root capacity: %w", err)
	}
	return &journalPublication{operationID: a.OperationID, carrierID: a.CarrierID, importArchive: p}, nil
}

func (p *importArchivePublication) rootPlan(manifest importArchiveManifest) (*journalPublication, error) {
	body, err := renderImportArchiveManifest(manifest)
	if err != nil {
		return nil, err
	}
	body, err = encodeJournalBody(body)
	if err != nil {
		return nil, err
	}
	return planJournalBodyPublication(body, manifest.Journal.OperationID, manifest.Journal.CarrierID)
}

func (p *importArchivePublication) bindParts(parts []apiComment) (*journalPublication, error) {
	if len(parts) != len(p.parts) || len(parts) == 0 {
		return nil, fmt.Errorf("import archive upload is incomplete")
	}
	manifest := p.manifest
	manifest.Documents = append([]importArchiveDocument(nil), p.manifest.Documents...)
	seen, offset := map[int64]bool{}, 0
	for d := range manifest.Documents {
		doc := &manifest.Documents[d]
		doc.Parts = make([]actionJournalPartRef, len(doc.Parts))
		for n := range doc.Parts {
			part := parts[offset]
			if !validJournalPartNative(part) || !journalNativeCarrierMatches(part, manifest.Journal.CarrierID) || seen[part.ID] || part.Body != p.bodies[offset] || part.IssueURL != parts[0].IssueURL || !strings.EqualFold(part.User.Login, parts[0].User.Login) {
				return nil, fmt.Errorf("import archive part %d identity, content, author or clock is unverified", offset)
			}
			seen[part.ID] = true
			doc.Parts[n] = actionJournalPartRef{ID: part.ID, CreatedAt: part.CreatedAt, SHA256: journalTransportHash(part.Body)}
			offset++
		}
	}
	return p.rootPlan(manifest)
}

func importArchiveNeedsCollection(body string) bool {
	return strings.Contains(body, actionImportArchiveStart) || strings.Contains(body, actionCompressedJournalStart+`{"schema":"v34",`)
}

func journalNeedsCollection(body string) bool {
	return strings.Contains(body, actionMultipartJournalStart) || importArchiveNeedsCollection(body)
}

func decodeInlineJournalFromCollection(root apiComment, carrier string, collection *journalPartCollection) (*ActionJournalRecord, error) {
	if !importArchiveNeedsCollection(root.Body) {
		return decodeActionJournal(root, carrier)
	}
	logical := root
	if index := strings.Index(root.Body, actionCompressedJournalStart); index >= 0 {
		block, err := decodeJournalEnvelope(root.Body[index:])
		if err != nil {
			return nil, err
		}
		logical.Body = root.Body[:index] + block
	}
	record, err := decodeImportArchiveJournal(logical, carrier, collection)
	if err != nil {
		return nil, err
	}
	record.compressedBody = root.Body
	return record, nil
}

func decodeImportArchiveJournal(root apiComment, carrier string, collection *journalPartCollection) (*ActionJournalRecord, error) {
	index := strings.Index(root.Body, actionImportArchiveStart)
	if index < 0 || !strings.HasSuffix(root.Body, markerEnd) || len(root.Body) > actionDecodedBudget+actionCommentBudget {
		return nil, fmt.Errorf("invalid import archive manifest framing or size")
	}
	var manifest importArchiveManifest
	if err := strictJournalJSON([]byte(strings.TrimSuffix(root.Body[index+len(actionImportArchiveStart):], markerEnd)), &manifest); err != nil {
		return nil, err
	}
	canonical, err := renderImportArchiveManifest(manifest)
	a := &manifest.Journal
	if err != nil || canonical != root.Body || a.ImportCommand == nil || a.CarrierID != carrier || a.ImportCommand.Input != "" || collection == nil {
		return nil, fmt.Errorf("import archive requires its canonical root and complete carrier collection")
	}
	if root.ID <= 0 || root.User.Login == "" || root.CreatedAt.IsZero() || root.UpdatedAt.Before(root.CreatedAt) || !journalNativeCarrierMatches(root, carrier) {
		return nil, fmt.Errorf("import archive root has invalid native identity or clocks")
	}
	names := []string{"input"}
	for _, member := range a.ImportCommand.Members {
		if member.Before != nil {
			return nil, fmt.Errorf("import archive root embeds a native observation")
		}
		if member.TargetID != "" {
			names = append(names, "before:"+member.TargetID)
		}
	}
	if len(names) != len(manifest.Documents) {
		return nil, fmt.Errorf("import archive documents differ from original membership")
	}
	seen, native := map[int64]bool{}, map[string]*ImportNativeState{}
	for n, doc := range manifest.Documents {
		if doc.Name != names[n] || doc.Size <= 0 || len(doc.Parts) != (doc.Size-1)/importArchiveChunkBytes+1 || !importDigestPattern.MatchString(doc.SHA256) {
			return nil, fmt.Errorf("import archive document name, size or digest is invalid")
		}
		var data bytes.Buffer // Never allocate an unverified declared size.
		for partIndex, ref := range doc.Parts {
			part, found := collection.byID[ref.ID]
			if !found || seen[ref.ID] || ref.ID == root.ID || !validJournalPartNative(part) || part.IssueURL != root.IssueURL || !strings.EqualFold(part.User.Login, root.User.Login) || !part.CreatedAt.Equal(ref.CreatedAt) || part.CreatedAt.After(root.UpdatedAt) || journalTransportHash(part.Body) != ref.SHA256 {
				return nil, fmt.Errorf("import archive part %d is missing, changed, duplicated or foreign", ref.ID)
			}
			seen[ref.ID] = true
			value := collection.parts[ref.ID]
			transfer := importArchiveTransfer(*a, doc, partIndex)
			if value == nil || value.Transfer != transfer || len(collection.byTransfer[transfer]) != 1 || value.OperationID != a.OperationID || value.CarrierID != carrier || value.Index != 0 || value.Count != 1 {
				return nil, fmt.Errorf("import archive part %d differs from the owning command/document", ref.ID)
			}
			chunk, err := base64.StdEncoding.Strict().DecodeString(value.Data)
			want := min(importArchiveChunkBytes, doc.Size-partIndex*importArchiveChunkBytes)
			if err != nil || len(chunk) != want {
				return nil, fmt.Errorf("import archive part %d has incorrect chunk size", ref.ID)
			}
			data.Write(chunk)
		}
		if data.Len() != doc.Size || journalTransportHash(data.String()) != doc.SHA256 {
			return nil, fmt.Errorf("import archive document %s has incorrect size/digest", doc.Name)
		}
		if doc.Name == "input" {
			a.ImportCommand.Input = data.String()
			continue
		}
		var state ImportNativeState
		if err := strictJournalJSON(data.Bytes(), &state); err != nil {
			return nil, err
		}
		canonical, err := json.Marshal(state)
		if err != nil || !bytes.Equal(canonical, data.Bytes()) {
			return nil, fmt.Errorf("import original native observation is not canonical")
		}
		native[doc.Name] = &state
	}
	for n := range a.ImportCommand.Members {
		member := &a.ImportCommand.Members[n]
		member.Before = native["before:"+member.TargetID]
	}
	// Use the same full semantic validation and native attribution rules as the
	// original v33 logical model. Only its transport references are different.
	root.Body, err = renderActionJournal(*a)
	if err != nil {
		return nil, err
	}
	return decodeActionJournal(root, carrier)
}
