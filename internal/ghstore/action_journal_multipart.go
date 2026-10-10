package ghstore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	actionMultipartJournalStart = actionJournalPrefix + "v20\n"
	actionJournalPartPrefix     = "<!-- td:action-part:"
	actionJournalPartStart      = actionJournalPartPrefix + "v1\n"
	actionJournalPartBytes      = 48_000 // Base64 boundaries; space for framing.
	actionJournalMaxParts       = 128
	actionJournalEncodedBudget  = actionJournalPartBytes * actionJournalMaxParts
)

type actionJournalPart struct {
	OperationID string `json:"operation_id"`
	CarrierID   string `json:"carrier_id"`
	Transfer    string `json:"transfer"`
	Index       int    `json:"index"`
	Count       int    `json:"count"`
	Data        string `json:"data"`
}

type actionJournalPartRef struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	SHA256    string    `json:"sha256"`
}

// A manifest refers to actual native comments, never future IDs or timestamps.
// It is published last. Unreferenced parts cannot authorize any target write.
type actionJournalManifest struct {
	OperationID string                 `json:"operation_id"`
	CarrierID   string                 `json:"carrier_id"`
	Transfer    string                 `json:"transfer"`
	EncodedSize int                    `json:"encoded_size"`
	Envelope    actionJournalEnvelope  `json:"envelope"`
	Parts       []actionJournalPartRef `json:"parts"`
}

// journalPublication separates pure capacity planning from native publication.
// Multipart parts must be verified before a manifest can authorize a write.
type journalPublication struct {
	operationID string
	carrierID   string
	body        string
	partBodies  []string
	summary     string
	manifest    actionJournalManifest
}

func planActionJournalPublication(a ActionJournal) (*journalPublication, error) {
	body, err := renderEncodedActionJournal(a)
	if err != nil {
		return nil, err
	}
	carrier := a.CarrierID
	if carrier == "" {
		carrier = a.IssueID
	}
	if len(body) <= actionCommentBudget {
		return &journalPublication{body: body, operationID: a.OperationID, carrierID: carrier}, nil
	}
	index := strings.Index(body, actionCompressedJournalStart)
	if index < 0 {
		return nil, fmt.Errorf("multipart journal requires a compressed archive")
	}
	var envelope actionJournalEnvelope
	if err := strictJournalJSON([]byte(strings.TrimSuffix(body[index+len(actionCompressedJournalStart):], markerEnd)), &envelope); err != nil {
		return nil, err
	}
	count := (len(envelope.Data) + actionJournalPartBytes - 1) / actionJournalPartBytes
	if count < 1 || count > actionJournalMaxParts {
		return nil, fmt.Errorf("multipart journal exceeds the encoded transport budget")
	}
	if _, err := Number(carrier); err != nil {
		return nil, fmt.Errorf("multipart journal requires a carrier identity: %w", err)
	}
	plan := &journalPublication{operationID: a.OperationID, carrierID: carrier, summary: body[:index], manifest: actionJournalManifest{
		OperationID: a.OperationID, CarrierID: carrier, Transfer: journalTransportHash(body),
		EncodedSize: len(envelope.Data), Envelope: envelope, Parts: make([]actionJournalPartRef, count),
	}}
	plan.manifest.Envelope.Data = ""
	for i := 0; i < count; i++ {
		part := actionJournalPart{OperationID: a.OperationID, CarrierID: carrier, Transfer: plan.manifest.Transfer,
			Index: i, Count: count, Data: envelope.Data[i*actionJournalPartBytes : min((i+1)*actionJournalPartBytes, len(envelope.Data))]}
		partBody, err := renderJournalPart(part)
		if err != nil {
			return nil, err
		}
		plan.partBodies = append(plan.partBodies, partBody)
		// Capacity only. These placeholders are never published as observations.
		plan.manifest.Parts[i] = actionJournalPartRef{ID: 1<<63 - 1, CreatedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), SHA256: journalTransportHash(partBody)}
	}
	if _, err := renderJournalManifest(plan.summary, plan.manifest); err != nil {
		return nil, err
	}
	return plan, nil
}

func journalTransportHash(body string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
}

func renderJournalPart(part actionJournalPart) (string, error) {
	data, err := json.Marshal(part)
	if err != nil {
		return "", err
	}
	body := fmt.Sprintf("td action part %s: %d/%d\n\n%s%s%s", part.OperationID, part.Index+1, part.Count, actionJournalPartStart, data, markerEnd)
	if len(body) > actionCommentBudget {
		return "", fmt.Errorf("journal part exceeds the comment transport budget")
	}
	return body, nil
}

func renderJournalManifest(summary string, manifest actionJournalManifest) (string, error) {
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	body := summary + actionMultipartJournalStart + string(data) + markerEnd
	if len(body) > actionCommentBudget {
		return "", fmt.Errorf("journal manifest exceeds the comment transport budget")
	}
	return body, nil
}

func decodeJournalPart(body string) (*actionJournalPart, error) {
	index := strings.Index(body, actionJournalPartPrefix)
	if index < 0 {
		return nil, nil
	}
	block := body[index:]
	if len(body) > actionCommentBudget || !strings.HasPrefix(block, actionJournalPartStart) || !strings.HasSuffix(block, markerEnd) {
		return nil, fmt.Errorf("invalid journal part framing or size")
	}
	var part actionJournalPart
	if err := strictJournalJSON([]byte(strings.TrimSuffix(strings.TrimPrefix(block, actionJournalPartStart), markerEnd)), &part); err != nil {
		return nil, err
	}
	if part.Count <= 0 || part.Count > actionJournalMaxParts || part.Index < 0 || part.Index >= part.Count || len(part.Data) == 0 || len(part.Data) > actionJournalPartBytes || part.OperationID == "" || len(part.Transfer) != 64 {
		return nil, fmt.Errorf("invalid journal part identity, index or size")
	}
	if _, err := Number(part.CarrierID); err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(part.Data)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != part.Data {
		return nil, fmt.Errorf("journal part base64 is invalid or noncanonical")
	}
	canonical, err := renderJournalPart(part)
	if err != nil || canonical != body {
		return nil, fmt.Errorf("journal part was edited or is noncanonical")
	}
	return &part, nil
}

// bindParts takes only verified POST/readback observations. It never invents a
// missing part; publication callers must recheck the collection before use.
func (p *journalPublication) bindParts(parts []apiComment) (string, error) {
	if len(p.partBodies) == 0 {
		if len(parts) != 0 {
			return "", fmt.Errorf("inline journal cannot bind parts")
		}
		return p.body, nil
	}
	if len(parts) != len(p.partBodies) {
		return "", fmt.Errorf("journal part upload is incomplete; no manifest can be published")
	}
	manifest := p.manifest
	manifest.Parts = make([]actionJournalPartRef, len(parts))
	seen := map[int64]bool{}
	for i, part := range parts {
		if !validJournalPartNative(part) || !journalNativeCarrierMatches(part, manifest.CarrierID) || seen[part.ID] || part.Body != p.partBodies[i] || part.IssueURL != parts[0].IssueURL || !strings.EqualFold(part.User.Login, parts[0].User.Login) {
			return "", fmt.Errorf("journal part %d identity, content, author or clock is unverified", i)
		}
		seen[part.ID] = true
		manifest.Parts[i] = actionJournalPartRef{ID: part.ID, CreatedAt: part.CreatedAt, SHA256: journalTransportHash(part.Body)}
	}
	return renderJournalManifest(p.summary, manifest)
}

func validJournalPartNative(part apiComment) bool {
	return part.ID > 0 && part.User.Login != "" && part.IssueURL != "" && !part.CreatedAt.IsZero() && part.CreatedAt.Equal(part.UpdatedAt)
}

func journalNativeCarrierMatches(comment apiComment, carrier string) bool {
	number, err := Number(carrier)
	if err != nil {
		return false
	}
	u, err := url.Parse(comment.IssueURL)
	if err != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.EscapedPath() != u.Path {
		return false
	}
	fields := strings.Split(u.Path, "/")
	return len(fields) == 6 && fields[0] == "" && fields[1] == "repos" && fields[2] != "" && fields[3] != "" && fields[4] == "issues" && fields[5] == fmt.Sprint(number)
}

type journalPartCollection struct {
	byID       map[int64]apiComment
	parts      map[int64]*actionJournalPart
	byTransfer map[string][]*actionJournalPart
}

// Parse each part once per carrier, even when it contains many manifests.
func collectJournalParts(comments []apiComment) (*journalPartCollection, error) {
	collection := &journalPartCollection{byID: make(map[int64]apiComment, len(comments)), parts: map[int64]*actionJournalPart{}, byTransfer: map[string][]*actionJournalPart{}}
	for _, comment := range comments {
		if _, duplicate := collection.byID[comment.ID]; duplicate {
			return nil, fmt.Errorf("duplicate multipart collection comment %d", comment.ID)
		}
		collection.byID[comment.ID] = comment
		part, err := decodeJournalPart(comment.Body)
		if err != nil {
			return nil, err
		}
		if part != nil {
			collection.parts[comment.ID] = part
			collection.byTransfer[part.Transfer] = append(collection.byTransfer[part.Transfer], part)
		}
	}
	return collection, nil
}

// Resolve from one native collection. Snapshots already have it; a point-read
// writer must obtain a fresh paginated carrier collection, not per-part GETs.
func decodeMultipartActionJournal(root apiComment, carrier string, comments []apiComment) (*ActionJournalRecord, error) {
	collection, err := collectJournalParts(comments)
	if err != nil {
		return nil, err
	}
	return decodeJournalFromCollection(root, carrier, collection)
}

func decodeJournalFromCollection(root apiComment, carrier string, collection *journalPartCollection) (*ActionJournalRecord, error) {
	index := strings.Index(root.Body, actionMultipartJournalStart)
	if index < 0 {
		return decodeActionJournal(root, carrier)
	}
	block := root.Body[index:]
	if len(root.Body) > actionCommentBudget || !strings.HasSuffix(block, markerEnd) {
		return nil, fmt.Errorf("multipart journal framing or size is invalid")
	}
	var manifest actionJournalManifest
	if err := strictJournalJSON([]byte(strings.TrimSuffix(strings.TrimPrefix(block, actionMultipartJournalStart), markerEnd)), &manifest); err != nil {
		return nil, err
	}
	canonical, err := renderJournalManifest(root.Body[:index], manifest)
	count := (manifest.EncodedSize + actionJournalPartBytes - 1) / actionJournalPartBytes
	if err != nil || canonical != root.Body || manifest.CarrierID != carrier || manifest.OperationID == "" || manifest.Envelope.Data != "" || manifest.EncodedSize <= 0 || manifest.EncodedSize > actionJournalEncodedBudget || count != len(manifest.Parts) || len(manifest.Transfer) != 64 {
		return nil, fmt.Errorf("multipart journal manifest identity, encoding, size or canonical form is invalid")
	}
	if root.ID <= 0 || root.User.Login == "" || root.CreatedAt.IsZero() || root.UpdatedAt.Before(root.CreatedAt) || !journalNativeCarrierMatches(root, carrier) {
		return nil, fmt.Errorf("multipart journal native identity or clock is invalid")
	}
	if collection == nil {
		return nil, fmt.Errorf("multipart journal requires its complete carrier comment collection")
	}
	indexes := map[int]bool{}
	for _, part := range collection.byTransfer[manifest.Transfer] {
		if indexes[part.Index] || part.OperationID != manifest.OperationID || part.CarrierID != carrier || part.Count != count {
			return nil, fmt.Errorf("duplicate or mismatched multipart journal part")
		}
		indexes[part.Index] = true
	}
	var encoded strings.Builder
	seen := map[int64]bool{}
	for i, ref := range manifest.Parts {
		part, found := collection.byID[ref.ID]
		if !found || seen[ref.ID] || ref.ID == root.ID || !validJournalPartNative(part) || part.IssueURL != root.IssueURL || !strings.EqualFold(part.User.Login, root.User.Login) || !part.CreatedAt.Equal(ref.CreatedAt) || part.CreatedAt.After(root.UpdatedAt) || journalTransportHash(part.Body) != ref.SHA256 {
			return nil, fmt.Errorf("multipart journal part %d is missing, changed, duplicated or has different native identity", ref.ID)
		}
		seen[ref.ID] = true
		value := collection.parts[ref.ID]
		if value == nil || value.Transfer != manifest.Transfer || value.OperationID != manifest.OperationID || value.CarrierID != carrier || value.Index != i || value.Count != count {
			return nil, fmt.Errorf("multipart journal part %d does not match its manifest", ref.ID)
		}
		if i < count-1 && len(value.Data) != actionJournalPartBytes {
			return nil, fmt.Errorf("multipart journal nonfinal part has incorrect size")
		}
		encoded.WriteString(value.Data)
	}
	envelope := manifest.Envelope
	envelope.Data = encoded.String()
	data, err := json.Marshal(envelope)
	if err != nil || len(envelope.Data) != manifest.EncodedSize {
		return nil, fmt.Errorf("multipart journal encoded size differs from manifest")
	}
	encodedBody := root.Body[:index] + actionCompressedJournalStart + string(data) + markerEnd
	if journalTransportHash(encodedBody) != manifest.Transfer {
		return nil, fmt.Errorf("multipart journal transfer checksum differs from manifest")
	}
	plain, err := decodeJournalEnvelopeBounded(actionCompressedJournalStart+string(data)+markerEnd, actionJournalEncodedBudget+actionCommentBudget)
	if err != nil {
		return nil, err
	}
	logical := root
	logical.Body = root.Body[:index] + plain
	record, err := decodeActionJournal(logical, carrier)
	if err != nil {
		return nil, err
	}
	if record == nil || record.OperationID != manifest.OperationID {
		return nil, fmt.Errorf("multipart journal operation differs from manifest")
	}
	record.compressedBody = root.Body // Preserve the native manifest for fences.
	return record, nil
}
