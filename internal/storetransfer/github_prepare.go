package storetransfer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/marcus/td/internal/ghstore"
)

// PreparedGitHubWorkspace freezes preflight inputs. Provisional IDs are used
// only to validate queries/positions and capacity, never published as receipts.
// It is not a durable checkpoint or authority to overwrite an observed entity.
type PreparedGitHubWorkspace struct {
	source     *Source
	target     *Source
	identities *IdentityPlan
	reused     map[string]bool
	archive    []byte
}

func PrepareGitHubWorkspace(source *Source, target *InspectedTarget, bindings map[string]string, maxBytes int64) (*PreparedGitHubWorkspace, error) {
	if source == nil || target == nil || target.Observation == nil || target.Spec.Store != "gh-issue" || target.Spec.ProjectDir == "" || target.Spec.Remote == "" || target.Spec.Namespace != target.Observation.Envelope.Namespace {
		return nil, fmt.Errorf("explicit validated SQLite source and GitHub destination required")
	}
	s, err := source.Envelope.decode()
	if err != nil {
		return nil, err
	}
	t, err := target.Observation.Envelope.decode()
	if err != nil {
		return nil, err
	}
	if s.SQLite == nil || t.GitHub == nil || t.GitHub.Repository != target.Spec.Namespace {
		return nil, fmt.Errorf("SQLite to GitHub workspace preflight required")
	}
	identities, err := PlanIdentities(s, t, bindings)
	if err != nil {
		return nil, err
	}
	_, err = BuildTaskImportInput(s)
	if err != nil {
		return nil, err
	}
	probeIDs := map[string]string{}
	reused := map[string]bool{}
	occupied := map[string]bool{}
	for _, a := range identities.Assignments {
		if a.TargetID != "" {
			occupied[a.TargetID] = true
		}
		if a.Action == "adopt" {
			reused[a.SourceKey] = true
		}
	}
	// Maximum-width placeholders conservatively test reference encoding. They
	// are not sent to GitHub, returned as real bindings, or installed in archives.
	probeNumber := int(^uint(0) >> 1)
	for _, a := range identities.Assignments {
		if a.Kind != "task" {
			continue
		}
		id := a.TargetID
		if id == "" {
			for occupied["gh-"+strconv.Itoa(probeNumber)] {
				probeNumber--
			}
			id = "gh-" + strconv.Itoa(probeNumber)
			probeNumber--
			occupied[id] = true
		}
		probeIDs[a.SourceKey] = id
	}
	entities, err := BuildGitHubEntityInputs(s, probeIDs, reused)
	if err != nil {
		return nil, err
	}
	for _, b := range entities.Boards {
		if b.Builtin {
			continue
		}
		for _, existing := range t.GitHub.Boards {
			if existing.Details.DeletedAt == nil && strings.EqualFold(existing.Name, b.Name) {
				return nil, fmt.Errorf("GitHub board name %q conflicts with %s; explicitly reuse an existing board or choose a distinct source name", b.Name, existing.ID)
			}
		}
	}
	var data bytes.Buffer
	if err := s.Write(&data, maxBytes); err != nil {
		return nil, err
	}
	// Probe the final archive's worst-width mapping/root size before bootstrap
	// or any entity creation. Placeholder carrier/comment IDs are never published.
	probeMappings := map[string]string{}
	for _, a := range identities.Assignments {
		id := a.TargetID
		if id == "" {
			prefix := map[string]string{"task": "gh-", "note": "nt-gh-", "board": "bd-gh-"}[a.Kind]
			if prefix == "" {
				return nil, fmt.Errorf("unsupported GitHub identity kind %q", a.Kind)
			}
			id = prefix + strconv.Itoa(int(^uint(0)>>1))
		}
		probeMappings[a.SourceKey] = id
	}
	if _, err := ghstore.PlanWorkspaceArchive(t.GitHub.Repository, "gh-"+strconv.Itoa(int(^uint(0)>>1)), "td-workspace-XXXXXXXXXXXXXXXXXXXXXXXXXX", data.Bytes(), probeMappings, maxBytes); err != nil {
		return nil, fmt.Errorf("GitHub workspace archive preflight: %w", err)
	}
	return &PreparedGitHubWorkspace{source: s, target: t, identities: identities, reused: reused, archive: data.Bytes()}, nil
}

func (p *PreparedGitHubWorkspace) SourceSHA256() string { return p.source.Envelope.SHA256 }

// TaskInputs returns a fresh conversion, never the preparation's mutable slices.
func (p *PreparedGitHubWorkspace) TaskInputs() (*TaskImportInput, error) {
	return BuildTaskImportInput(p.source)
}

// OriginalArchive returns original bytes only, not the capacity probe mappings.
func (p *PreparedGitHubWorkspace) OriginalArchive() []byte { return bytes.Clone(p.archive) }

// ReusedBindings reports explicit existing-entity reuse, not new allocations.
func (p *PreparedGitHubWorkspace) ReusedBindings() map[string]string {
	result := map[string]string{}
	for _, a := range p.identities.Assignments {
		if p.reused[a.SourceKey] {
			result[a.SourceKey] = a.TargetID
		}
	}
	return result
}

// CheckTarget ignores the observation clock only. Source/native content,
// membership, author/clocks, comments and receipts remain in the comparison.
// This is a conservative freshness check, not an atomic GitHub CAS guarantee.
func (p *PreparedGitHubWorkspace) CheckTarget(current *Source) error {
	if p == nil || current == nil {
		return fmt.Errorf("prepared GitHub workspace and fresh observation required")
	}
	observed, err := current.Envelope.decode()
	if err != nil {
		return err
	}
	if observed.GitHub == nil || observed.Envelope.Namespace != p.target.Envelope.Namespace {
		return fmt.Errorf("GitHub workspace destination differs from prepared repository")
	}
	observed.GitHub.ObservedAt = p.target.GitHub.ObservedAt
	a, err := json.Marshal(observed.GitHub)
	if err != nil {
		return err
	}
	b, err := json.Marshal(p.target.GitHub)
	if err != nil {
		return err
	}
	if !bytes.Equal(a, b) {
		return fmt.Errorf("GitHub workspace destination changed since preparation; inspect before writes")
	}
	return nil
}
