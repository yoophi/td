package ghstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/marcus/td/internal/transferlimit"
)

const workspaceArchiveStart = "<!-- td:workspace-archive:v1\n"
const workspaceArchivePrefix = "<!-- td:workspace-archive:"

// WorkspaceArchiveEvidence is passive source data. Native author/clocks and
// mappings are observations, not authenticated execution/undo/review authority.
type WorkspaceArchiveEvidence struct {
	ID             int64             `json:"id"`
	Author         string            `json:"author"`
	CreatedAt      time.Time         `json:"created_at"`
	OperationID    string            `json:"operation_id"`
	SourceSHA256   string            `json:"source_sha256"`
	SourceEnvelope json.RawMessage   `json:"source_envelope"`
	Mappings       map[string]string `json:"mappings"`
}

type workspaceArchiveManifest struct {
	Version      int                    `json:"version"`
	Repository   string                 `json:"repository"`
	CarrierID    string                 `json:"carrier_id"`
	OperationID  string                 `json:"operation_id"`
	SourceSHA256 string                 `json:"source_sha256"`
	Bytes        int                    `json:"bytes"`
	DataSHA256   string                 `json:"data_sha256"`
	Transfer     string                 `json:"transfer"`
	Mappings     map[string]string      `json:"mappings"`
	Parts        []actionJournalPartRef `json:"parts"`
}

// WorkspaceArchivePlan performs pure capacity planning; placeholder references
// can never be published. The writer must bind verified native part observations.
type WorkspaceArchivePlan struct {
	manifest workspaceArchiveManifest
	bodies   []string
}

func (p *WorkspaceArchivePlan) PartCount() int { return len(p.bodies) }

func workspaceArchiveTransfer(m workspaceArchiveManifest) string {
	return journalTransportHash("td-workspace-archive-v1\n" + m.Repository + "\n" + m.CarrierID + "\n" + m.OperationID + "\n" + m.DataSHA256)
}

// Source semantic validation belongs to the transfer controller. This boundary
// binds immutable JSON bytes/header SHA, never turns an archived payload into
// active rows. The eventual controller must validate its envelope before reuse.
func workspaceArchiveSourceSHA(data []byte) (string, error) {
	var header struct {
		SHA256 string `json:"sha256"`
	}
	if !json.Valid(data) || json.Unmarshal(data, &header) != nil || !importDigestPattern.MatchString(header.SHA256) {
		return "", fmt.Errorf("workspace archive requires a source envelope with SHA256")
	}
	return header.SHA256, nil
}

func PlanWorkspaceArchive(repository, carrier, operation string, source []byte, mappings map[string]string, maxBytes int64) (*WorkspaceArchivePlan, error) {
	limit, err := transferlimit.Bytes(maxBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(source)) > limit {
		return nil, transferlimit.ErrCapacity
	}
	if !repositoryPattern.MatchString(repository) || !canonicalGroupID(carrier) || operation == "" || strings.IndexFunc(operation, func(r rune) bool {
		allowed := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		return !allowed
	}) >= 0 {
		return nil, fmt.Errorf("workspace archive requires repository, carrier and operation identities")
	}
	encodedSize := base64.StdEncoding.EncodedLen(len(source))
	count := (encodedSize + actionJournalPartBytes - 1) / actionJournalPartBytes
	if count < 1 || count > actionJournalMaxParts {
		return nil, fmt.Errorf("%w: GitHub workspace archive needs %d parts; maximum is %d", transferlimit.ErrCapacity, count, actionJournalMaxParts)
	}
	sha, err := workspaceArchiveSourceSHA(source)
	if err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(source)
	copyMap := map[string]string{}
	for key, id := range mappings {
		if key == "" || id == "" || strings.ContainsAny(key+id, "\r\n\x00") {
			return nil, fmt.Errorf("invalid passive workspace mapping")
		}
		copyMap[key] = id
	}
	m := workspaceArchiveManifest{Version: 1, Repository: repository, CarrierID: carrier, OperationID: operation, SourceSHA256: sha, Bytes: len(source), DataSHA256: journalTransportHash(string(source)), Mappings: copyMap, Parts: make([]actionJournalPartRef, count)}
	m.Transfer = workspaceArchiveTransfer(m)
	p := &WorkspaceArchivePlan{manifest: m}
	for i := 0; i < count; i++ {
		body, err := renderJournalPart(actionJournalPart{OperationID: operation, CarrierID: carrier, Transfer: m.Transfer, Index: i, Count: count, Data: encoded[i*actionJournalPartBytes : min((i+1)*actionJournalPartBytes, len(encoded))]})
		if err != nil {
			return nil, err
		}
		p.bodies = append(p.bodies, body)
		p.manifest.Parts[i] = actionJournalPartRef{ID: 1<<63 - 1, CreatedAt: time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), SHA256: journalTransportHash(body)}
	}
	if _, err := renderWorkspaceArchiveManifest(p.manifest); err != nil {
		return nil, err
	}
	return p, nil
}

func renderWorkspaceArchiveManifest(m workspaceArchiveManifest) (string, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	body := "td workspace source archive " + m.OperationID + "\n\n" + workspaceArchiveStart + string(data) + markerEnd
	if len(body) > actionCommentBudget {
		return "", fmt.Errorf("%w: workspace archive manifest exceeds GitHub comment budget", transferlimit.ErrCapacity)
	}
	return body, nil
}

func (p *WorkspaceArchivePlan) bindParts(parts []apiComment) (string, error) {
	if p == nil || len(parts) != len(p.bodies) || len(parts) == 0 {
		return "", fmt.Errorf("workspace archive upload is incomplete")
	}
	m := p.manifest
	m.Parts = make([]actionJournalPartRef, len(parts))
	seen := map[int64]bool{}
	number, err := Number(m.CarrierID)
	if err != nil {
		return "", err
	}
	uri := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", m.Repository, number)
	for i, part := range parts {
		if !validJournalPartNative(part) || seen[part.ID] || part.IssueURL != uri || part.Body != p.bodies[i] || !strings.EqualFold(part.User.Login, parts[0].User.Login) {
			return "", fmt.Errorf("workspace archive part %d native identity/content/author is unverified", i)
		}
		seen[part.ID] = true
		m.Parts[i] = actionJournalPartRef{ID: part.ID, CreatedAt: part.CreatedAt, SHA256: journalTransportHash(part.Body)}
	}
	return renderWorkspaceArchiveManifest(m)
}

func decodeWorkspaceArchive(root apiComment, repository, carrier string, collection *journalPartCollection, maxBytes int64) (*WorkspaceArchiveEvidence, error) {
	index := strings.Index(root.Body, workspaceArchivePrefix)
	if index < 0 {
		return nil, nil
	}
	block := root.Body[index:]
	if !strings.HasPrefix(block, workspaceArchiveStart) || !strings.HasSuffix(block, markerEnd) || len(root.Body) > actionCommentBudget {
		return nil, fmt.Errorf("invalid workspace archive framing/size")
	}
	var m workspaceArchiveManifest
	if err := strictJournalJSON([]byte(strings.TrimSuffix(strings.TrimPrefix(block, workspaceArchiveStart), markerEnd)), &m); err != nil {
		return nil, err
	}
	canonical, err := renderWorkspaceArchiveManifest(m)
	limit, lerr := transferlimit.Bytes(maxBytes)
	if lerr != nil {
		return nil, lerr
	}
	if m.Bytes < 1 || int64(m.Bytes) > limit {
		return nil, transferlimit.ErrCapacity
	}
	encodedSize := base64.StdEncoding.EncodedLen(m.Bytes)
	count := (encodedSize + actionJournalPartBytes - 1) / actionJournalPartBytes
	if err != nil || canonical != root.Body || m.Version != 1 || m.Repository != repository || !repositoryPattern.MatchString(repository) || m.CarrierID != carrier || !canonicalGroupID(carrier) || m.Transfer != workspaceArchiveTransfer(m) || count < 1 || count > actionJournalMaxParts || len(m.Parts) != count || !importDigestPattern.MatchString(m.SourceSHA256) || !importDigestPattern.MatchString(m.DataSHA256) {
		return nil, fmt.Errorf("invalid workspace archive identity/size/binding")
	}
	number, _ := Number(carrier)
	uri := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", repository, number)
	if !validJournalPartNative(root) || root.IssueURL != uri || collection == nil {
		return nil, fmt.Errorf("workspace archive requires immutable native root and complete carrier collection")
	}
	if len(collection.byTransfer[m.Transfer]) != count {
		return nil, fmt.Errorf("workspace archive has missing or duplicate transport parts")
	}
	encoded := strings.Builder{}
	seen := map[int64]bool{}
	for i, ref := range m.Parts {
		part, ok := collection.byID[ref.ID]
		decoded := collection.parts[ref.ID]
		if !ok || decoded == nil || seen[ref.ID] || ref.ID == root.ID || !validJournalPartNative(part) || part.IssueURL != uri || !strings.EqualFold(part.User.Login, root.User.Login) || !part.CreatedAt.Equal(ref.CreatedAt) || part.CreatedAt.After(root.CreatedAt) || journalTransportHash(part.Body) != ref.SHA256 || decoded.Transfer != m.Transfer || decoded.CarrierID != carrier || decoded.OperationID != m.OperationID || decoded.Count != count || decoded.Index != i {
			return nil, fmt.Errorf("workspace archive part %d missing/changed/foreign", i)
		}
		seen[ref.ID] = true
		encoded.WriteString(decoded.Data)
	}
	if encoded.Len() != encodedSize {
		return nil, fmt.Errorf("workspace archive encoded size differs")
	}
	data, err := base64.StdEncoding.DecodeString(encoded.String())
	if err != nil || len(data) != m.Bytes || journalTransportHash(string(data)) != m.DataSHA256 {
		return nil, fmt.Errorf("workspace archive source bytes differ")
	}
	sha, err := workspaceArchiveSourceSHA(data)
	if err != nil || sha != m.SourceSHA256 {
		return nil, fmt.Errorf("workspace archive source header differs")
	}
	if _, err := PlanWorkspaceArchive(repository, carrier, m.OperationID, data, m.Mappings, maxBytes); err != nil {
		return nil, fmt.Errorf("invalid workspace archive inputs: %w", err)
	}
	return &WorkspaceArchiveEvidence{ID: root.ID, Author: root.User.Login, CreatedAt: root.CreatedAt, OperationID: m.OperationID, SourceSHA256: m.SourceSHA256, SourceEnvelope: data, Mappings: m.Mappings}, nil
}
