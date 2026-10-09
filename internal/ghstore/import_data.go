package ghstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/models"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
)

// ImportItem is the portable JSON issue envelope. GitHub data is provenance,
// never proof of a current review or permission to impersonate its author.
type ImportItem struct {
	SourceNamespace string                   `json:"source_namespace,omitempty"`
	GitHub          *ExportedIssue           `json:"github,omitempty"`
	Issue           models.Issue             `json:"issue"`
	Logs            []models.Log             `json:"logs"`
	Handoffs        []models.Handoff         `json:"handoffs"`
	Dependencies    []models.IssueDependency `json:"dependencies"`
	Files           []models.IssueFile       `json:"files"`
}

func (e *ImportItem) UnmarshalJSON(data []byte) error {
	return e.decodeJSON(data, false)
}
func (e *ImportItem) decodeJSON(data []byte, strict bool) error {
	var aux struct {
		SourceNamespace string             `json:"source_namespace,omitempty"`
		GitHub          *ExportedIssue     `json:"github,omitempty"`
		Issue           models.Issue       `json:"issue"`
		Logs            []models.Log       `json:"logs"`
		Handoffs        []models.Handoff   `json:"handoffs"`
		Handoff         *models.Handoff    `json:"handoff"`
		Files           []models.IssueFile `json:"files"`
		Dependencies    json.RawMessage    `json:"dependencies"`
	}
	decode := json.Unmarshal
	if strict {
		decode = decodeImportJSON
	}
	if err := decode(data, &aux); err != nil {
		return err
	}
	*e = ImportItem{SourceNamespace: aux.SourceNamespace, GitHub: aux.GitHub, Issue: aux.Issue, Logs: aux.Logs, Handoffs: aux.Handoffs, Files: aux.Files}
	if len(e.Handoffs) == 0 && aux.Handoff != nil {
		e.Handoffs = []models.Handoff{*aux.Handoff}
	}
	if len(aux.Dependencies) > 0 && string(aux.Dependencies) != "null" {
		if err := decode(aux.Dependencies, &e.Dependencies); err != nil {
			var old []string
			if err := decode(aux.Dependencies, &old); err != nil {
				return fmt.Errorf("invalid import dependencies: %w", err)
			}
			e.Dependencies = nil
			for _, id := range old {
				e.Dependencies = append(e.Dependencies, models.IssueDependency{DependsOnID: id, RelationType: "depends_on"})
			}
		}
	}
	return nil
}
func decodeImportJSON(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing import JSON")
	}
	return nil
}
func ParseImportJSON(data []byte) ([]ImportItem, error) {
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '[' {
		return nil, fmt.Errorf("import JSON must be an array")
	}
	var raw []json.RawMessage
	if err := decodeImportJSON(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid import JSON: %w", err)
	}
	items := make([]ImportItem, len(raw))
	for i := range raw {
		if err := items[i].decodeJSON(raw[i], true); err != nil {
			return nil, fmt.Errorf("invalid import item %d: %w", i+1, err)
		}
	}
	for i := range items {
		e := &items[i]
		if e.GitHub != nil {
			continue
		}
		if e.Issue.Type == "" {
			e.Issue.Type = models.TypeTask
		}
		if e.Issue.Priority == "" {
			e.Issue.Priority = models.PriorityP2
		}
		if e.Issue.Status == "" {
			e.Issue.Status = models.StatusOpen
		}
		for j := range e.Dependencies {
			if e.Dependencies[j].RelationType == "" {
				e.Dependencies[j].RelationType = "depends_on"
			}
		}
	}
	if err := ValidateImportItems(items); err != nil {
		return nil, err
	}
	return items, nil
}

var importIDPattern = regexp.MustCompile(`^(td-[A-Za-z0-9]+|gh-[1-9][0-9]*)$`)
var importNamespacePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// SetImportSourceNamespace qualifies legacy identities, without altering
// repository-qualified GitHub identities. Never override a declared source.
func SetImportSourceNamespace(items []ImportItem, namespace string) error {
	if namespace == "" {
		return nil
	}
	if !importNamespacePattern.MatchString(namespace) {
		return fmt.Errorf("invalid import source namespace; use 1-128 letters, digits, '.', '_', '/', or '-', starting with a letter or digit")
	}
	legacy := 0
	for _, item := range items {
		if item.GitHub != nil {
			continue
		}
		legacy++
		if item.SourceNamespace != "" && item.SourceNamespace != namespace {
			return fmt.Errorf("source namespace %q disagrees with declared namespace %q for %s", namespace, item.SourceNamespace, item.Issue.ID)
		}
	}
	if legacy == 0 && len(items) != 0 {
		return fmt.Errorf("--source applies to legacy exports; GitHub exports already identify their repository")
	}
	for i := range items {
		if items[i].GitHub == nil {
			items[i].SourceNamespace = namespace
		}
	}
	return ValidateImportItems(items)
}

func (e ImportItem) SourceKey() string {
	return e.ReferenceKey(e.Issue.ID)
}
func (e ImportItem) ReferenceKey(id string) string {
	if e.GitHub != nil {
		return "github:" + strings.ToLower(e.GitHub.Repository) + ":" + id
	}
	if e.SourceNamespace != "" {
		return "legacy:" + e.SourceNamespace + ":" + id
	}
	return "legacy:" + id
}
func (e ImportItem) Digest() (string, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}
func ValidateImportItems(items []ImportItem) error {
	byID := map[string]int{}
	for i, e := range items {
		if e.SourceNamespace != "" && (e.GitHub != nil || !importNamespacePattern.MatchString(e.SourceNamespace)) {
			return fmt.Errorf("invalid source namespace for %s; only legacy exports can declare a source namespace", e.Issue.ID)
		}
		if !importIDPattern.MatchString(e.Issue.ID) {
			return fmt.Errorf("import item %d requires a canonical source issue ID", i+1)
		}
		if _, ok := byID[e.SourceKey()]; ok {
			return fmt.Errorf("duplicate source issue ID %s", e.Issue.ID)
		}
		byID[e.SourceKey()] = i
		if err := validateIssue(&e.Issue); err != nil {
			return fmt.Errorf("import %s: %w", e.Issue.ID, err)
		}
		if !slices.Contains([]models.Status{models.StatusOpen, models.StatusInProgress, models.StatusBlocked, models.StatusInReview, models.StatusClosed}, e.Issue.Status) {
			return fmt.Errorf("invalid import status for %s", e.Issue.ID)
		}
		if strings.Contains(e.Issue.Description, markerPrefix) {
			return fmt.Errorf("import %s description contains reserved metadata", e.Issue.ID)
		}
		if e.Issue.DeferCount < 0 {
			return fmt.Errorf("import %s has a negative defer count", e.Issue.ID)
		}
		for _, date := range []*string{e.Issue.DueDate, e.Issue.DeferUntil} {
			if date != nil {
				d := IssueDetails{DueDate: date}
				if err := d.validate(); err != nil {
					return fmt.Errorf("import %s: %w", e.Issue.ID, err)
				}
			}
		}
		if e.GitHub != nil {
			g := e.GitHub
			if g.Version != 1 || !repositoryPattern.MatchString(g.Repository) || g.Record.ID != e.Issue.ID || fmt.Sprintf("gh-%d", g.Record.Number) != e.Issue.ID || !slices.Contains([]string{"open", "closed"}, g.NativeState) {
				return fmt.Errorf("invalid GitHub source envelope for %s", e.Issue.ID)
			}
			a, _ := json.Marshal(e.Issue)
			b, _ := json.Marshal(g.Record.Issue)
			if !bytes.Equal(a, b) {
				return fmt.Errorf("GitHub source and common issue disagree for %s", e.Issue.ID)
			}
			description, meta, _, err := decodeBody(g.Body)
			if err != nil {
				return fmt.Errorf("import source %s: %w", e.Issue.ID, err)
			}
			if (meta.EntityKind != "" && meta.EntityKind != "issue") || description != e.Issue.Description {
				return fmt.Errorf("source %s is not a matching task issue", e.Issue.ID)
			}
			if meta.Type != e.Issue.Type || meta.Priority != e.Issue.Priority || meta.Points != e.Issue.Points || meta.Acceptance != e.Issue.Acceptance || string(nativeStatus(e.Issue.Status)) != g.NativeState {
				return fmt.Errorf("GitHub source metadata disagrees for %s", e.Issue.ID)
			}
			originalDetails, _ := json.Marshal(meta.Details)
			declaredDetails, _ := json.Marshal(g.Record.Details)
			if !bytes.Equal(originalDetails, declaredDetails) {
				return fmt.Errorf("GitHub source details disagree for %s", e.Issue.ID)
			}
			raw := apiIssue{Number: g.Record.Number, Title: g.Record.Title, Body: g.Body, State: g.NativeState, URL: g.Record.URL, CreatedAt: g.Record.CreatedAt, UpdatedAt: g.Record.UpdatedAt, ClosedAt: g.Record.ClosedAt}
			for _, label := range g.Record.Labels {
				raw.Labels = append(raw.Labels, struct {
					Name string `json:"name"`
				}{Name: label})
			}
			derived, err := raw.record()
			if err != nil {
				return fmt.Errorf("invalid native source %s: %w", e.Issue.ID, err)
			}
			if len(derived.Labels) == 0 && e.Issue.Labels == nil {
				derived.Labels = nil
			}
			derivedJSON, _ := json.Marshal(derived.Issue)
			if !bytes.Equal(a, derivedJSON) {
				return fmt.Errorf("native body and source issue disagree for %s", e.Issue.ID)
			}
			if g.Record.Details != nil {
				if err := g.Record.Details.validate(); err != nil {
					return fmt.Errorf("source details %s: %w", e.Issue.ID, err)
				}
			}
			seen := map[string]bool{}
			for _, a := range g.Activity {
				if a.ID == "" || seen[a.ID] || (a.IssueID != "" && a.IssueID != e.Issue.ID) {
					return fmt.Errorf("invalid or duplicate source activity for %s", e.Issue.ID)
				}
				seen[a.ID] = true
				if a.Kind != "comment" && a.Kind != "log" && a.Kind != "handoff" {
					return fmt.Errorf("invalid source activity kind %q", a.Kind)
				}
			}
		}
		seen := map[string]bool{}
		for _, f := range e.Files {
			if f.FilePath == "" || f.FilePath == "." || path.IsAbs(f.FilePath) || path.Clean(f.FilePath) != f.FilePath || strings.HasPrefix(f.FilePath, "../") || strings.Contains(f.FilePath, "\\") || seen[f.FilePath] || (f.IssueID != "" && f.IssueID != e.Issue.ID) || !slices.Contains([]models.FileRole{models.FileRoleImplementation, models.FileRoleTest, models.FileRoleReference, models.FileRoleConfig}, f.Role) {
				return fmt.Errorf("invalid imported file for %s", e.Issue.ID)
			}
			seen[f.FilePath] = true
		}
		for _, l := range e.Logs {
			if l.ID == "" || strings.TrimSpace(l.Message) == "" || (l.IssueID != "" && l.IssueID != e.Issue.ID) || !slices.Contains([]models.LogType{models.LogTypeProgress, models.LogTypeSecurity, models.LogTypeBlocker, models.LogTypeDecision, models.LogTypeHypothesis, models.LogTypeTried, models.LogTypeResult, models.LogTypeOrchestration}, l.Type) {
				return fmt.Errorf("invalid imported log for %s", e.Issue.ID)
			}
		}
		for _, h := range e.Handoffs {
			if h.ID == "" || (h.IssueID != "" && h.IssueID != e.Issue.ID) || len(h.Done)+len(h.Remaining)+len(h.Decisions)+len(h.Uncertain) == 0 {
				return fmt.Errorf("invalid imported handoff for %s", e.Issue.ID)
			}
		}
	}
	for _, e := range items {
		refs := []string{}
		if e.Issue.ParentID != "" {
			refs = append(refs, e.Issue.ParentID)
		}
		seen := map[string]bool{}
		for _, d := range e.Dependencies {
			key := d.RelationType + ":" + d.DependsOnID
			if (d.IssueID != "" && d.IssueID != e.Issue.ID) || !slices.Contains([]string{"depends_on", "blocks"}, d.RelationType) || seen[key] {
				return fmt.Errorf("invalid dependency for %s", e.Issue.ID)
			}
			seen[key] = true
			refs = append(refs, d.DependsOnID)
		}
		for _, id := range refs {
			if _, ok := byID[e.ReferenceKey(id)]; !ok || id == e.Issue.ID {
				return fmt.Errorf("import %s has missing or self reference %s; include the referenced issue", e.Issue.ID, id)
			}
		}
		ancestors := map[string]bool{e.Issue.ID: true}
		parent := e.Issue.ParentID
		for parent != "" {
			if ancestors[parent] {
				return fmt.Errorf("import parent cycle at %s", parent)
			}
			ancestors[parent] = true
			parent = items[byID[e.ReferenceKey(parent)]].Issue.ParentID
		}
	}
	return nil
}
