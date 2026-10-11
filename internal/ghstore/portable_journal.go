package ghstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

const portableJournalFormat = "td-journal"
const maxPortableJournalBytes = 64 << 20

// PortableJournalSource preserves source-native attribution and IDs as data.
// Its actions are never installed as executable destination journal records.
type PortableJournalSource struct {
	Repository string                `json:"repository"`
	Actions    []ActionJournalRecord `json:"actions"`
	SHA256     string                `json:"sha256"`
}

// PortableJournalBinding links references without rewriting original actions,
// snapshots, authors, sessions, native clocks or their checksums.
type PortableJournalBinding struct {
	SourceSHA256     string            `json:"source_sha256"`
	TargetRepository string            `json:"target_repository"`
	Targets          map[string]string `json:"targets"`
}

// PortableJournalBundle flattens previously restored sources instead of
// recursively archiving archives. A checksum detects corruption, not forgery.
type PortableJournalBundle struct {
	Format   string                   `json:"format"`
	Version  int                      `json:"version"`
	Sources  []PortableJournalSource  `json:"sources"`
	Bindings []PortableJournalBinding `json:"bindings"`
	SHA256   string                   `json:"sha256"`
}

func portableJournalHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) > maxPortableJournalBytes {
		return "", fmt.Errorf("portable journal exceeds the %d-byte capacity; no destination write attempted", maxPortableJournalBytes)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func (s PortableJournalSource) checksum() (string, error) {
	s.SHA256 = ""
	return portableJournalHash(s)
}

func (b PortableJournalBundle) checksum() (string, error) {
	b.SHA256 = ""
	return portableJournalHash(b)
}

func canonicalPortableIssue(id string) bool {
	number, err := Number(id)
	return err == nil && id == fmt.Sprintf("gh-%d", number)
}

// Only structured reference fields and ID-indexed maps are interpreted.
// Text and embedded source JSON remain opaque, source-qualified history.
func (s PortableJournalSource) references() (map[string]bool, error) {
	data, err := json.Marshal(s.Actions)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	refs := map[string]bool{}
	var visit func(any, string)
	visit = func(value any, key string) {
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if canonicalPortableIssue(key) {
					refs[key] = true
				}
				visit(child, key)
			}
		case []any:
			for _, child := range value {
				visit(child, key)
			}
		case string:
			if slices.Contains([]string{"id", "issue_id", "carrier_id", "target_id", "parent_id", "depends_on_id", "related_issue_id", "blocker_id", "roots", "original_ids", "depends_on", "blocks"}, key) && canonicalPortableIssue(value) {
				refs[value] = true
			}
		}
	}
	visit(value, "")
	return refs, nil
}

func (s PortableJournalSource) validate() error {
	if !repositoryPattern.MatchString(s.Repository) || s.Actions == nil {
		return fmt.Errorf("portable journal source requires a repository and actions array")
	}
	want, err := s.checksum()
	if err != nil {
		return err
	}
	if s.SHA256 != want {
		return fmt.Errorf("portable journal source checksum mismatch")
	}
	ids, operations := map[string]bool{}, map[string]bool{}
	for _, action := range s.Actions {
		number, err := CommentNumber(action.ID)
		if err != nil || action.ID != fmt.Sprintf("ghc-%d", number) || ids[action.ID] || operations[action.OperationID] || strings.TrimSpace(action.Author) == "" || action.CreatedAt.IsZero() || action.UpdatedAt.Before(action.CreatedAt) {
			return fmt.Errorf("portable journal has invalid or duplicate native identity: %s", action.ID)
		}
		ids[action.ID], operations[action.OperationID] = true, true
		if err := action.validate(); err != nil {
			return fmt.Errorf("portable journal %s: %w", action.ID, err)
		}
		if action.URL != "" {
			carrier := action.CarrierID
			if carrier == "" {
				carrier = action.IssueID
			}
			issue, err := Number(carrier)
			u, parseErr := url.Parse(action.URL)
			if err != nil || parseErr != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || !strings.EqualFold(u.Path, fmt.Sprintf("/%s/issues/%d", s.Repository, issue)) || u.Fragment != fmt.Sprintf("issuecomment-%d", number) {
				return fmt.Errorf("portable journal %s URL differs from its source identity", action.ID)
			}
		}
	}
	// Preserve the complete member set, including prepared/uncertain records.
	// Existing ownership validators reject missing or conflicting references.
	if _, err := CollapseActionGroups(s.Actions); err != nil {
		return fmt.Errorf("portable journal references: %w", err)
	}
	return nil
}

func (b PortableJournalBundle) Validate() error {
	if b.Format != portableJournalFormat || b.Version != 1 || b.Sources == nil || b.Bindings == nil {
		return fmt.Errorf("unsupported portable journal format/version or missing arrays")
	}
	want, err := b.checksum()
	if err != nil {
		return err
	}
	if b.SHA256 != want {
		return fmt.Errorf("portable journal bundle checksum mismatch")
	}
	sources := map[string]PortableJournalSource{}
	for _, source := range b.Sources {
		if err := source.validate(); err != nil {
			return err
		}
		if _, exists := sources[source.SHA256]; exists {
			return fmt.Errorf("portable journal repeats source %s", source.SHA256)
		}
		sources[source.SHA256] = source
	}
	bindings := map[string]bool{}
	qualifiedTargets := map[string]string{}
	qualifiedSources := map[string]string{}
	for _, binding := range b.Bindings {
		source, found := sources[binding.SourceSHA256]
		if !found || !repositoryPattern.MatchString(binding.TargetRepository) || binding.Targets == nil {
			return fmt.Errorf("portable journal binding has an unknown source, invalid repository or missing targets")
		}
		refs, err := source.references()
		if err != nil {
			return err
		}
		key := binding.SourceSHA256 + "\n" + strings.ToLower(binding.TargetRepository)
		if bindings[key] {
			return fmt.Errorf("portable journal repeats a source/destination binding")
		}
		bindings[key] = true
		used := map[string]bool{}
		for id, target := range binding.Targets {
			if !refs[id] || !canonicalPortableIssue(target) || used[target] {
				return fmt.Errorf("portable journal mapping %s -> %s is unknown, noncanonical or not one-to-one", id, target)
			}
			used[target] = true
			qualified := journalSourceKey(source.Repository, id) + ":" + strings.ToLower(binding.TargetRepository)
			if old, exists := qualifiedTargets[qualified]; exists && old != target {
				return fmt.Errorf("portable journal source snapshots have conflicting destination mappings for %s", qualified)
			}
			qualifiedTargets[qualified] = target
			reverse := strings.ToLower(source.Repository) + ":" + strings.ToLower(binding.TargetRepository) + ":" + target
			if old, exists := qualifiedSources[reverse]; exists && old != id {
				return fmt.Errorf("portable journal source snapshots have a many-to-one destination mapping for %s", reverse)
			}
			qualifiedSources[reverse] = id
		}
	}
	return nil
}

func ParsePortableJournal(data []byte) (*PortableJournalBundle, error) {
	if len(data) > maxPortableJournalBytes {
		return nil, fmt.Errorf("portable journal exceeds the %d-byte capacity", maxPortableJournalBytes)
	}
	var bundle PortableJournalBundle
	if err := decodeImportJSON(data, &bundle); err != nil {
		return nil, fmt.Errorf("invalid portable journal: %w", err)
	}
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	return &bundle, nil
}

// MarshalPortableJournal keeps emitted files inside the same decoded/input
// budget as the reader. Pretty-print expansion must not produce a successful
// export which cannot be read back by this format.
func MarshalPortableJournal(bundle *PortableJournalBundle) ([]byte, error) {
	if bundle == nil {
		return nil, fmt.Errorf("portable journal is required")
	}
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if len(data) > maxPortableJournalBytes {
		return nil, fmt.Errorf("portable journal exceeds the %d-byte input/output capacity", maxPortableJournalBytes)
	}
	return data, nil
}

func newPortableJournal(repo string, actions []ActionJournalRecord) (*PortableJournalBundle, error) {
	// Own the mutable values. Export must not alter the source observation.
	data, err := json.Marshal(actions)
	if err != nil {
		return nil, err
	}
	var owned []ActionJournalRecord
	if err := json.Unmarshal(data, &owned); err != nil {
		return nil, err
	}
	if owned == nil {
		owned = []ActionJournalRecord{}
	}
	source := PortableJournalSource{Repository: repo, Actions: owned}
	source.SHA256, err = source.checksum()
	if err != nil {
		return nil, err
	}
	bundle := &PortableJournalBundle{Format: portableJournalFormat, Version: 1, Sources: []PortableJournalSource{source}, Bindings: []PortableJournalBinding{}}
	bundle.SHA256, err = bundle.checksum()
	if err != nil {
		return nil, err
	}
	return bundle, bundle.Validate()
}

func equalPortableJournal(a, b *PortableJournalBundle) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}
