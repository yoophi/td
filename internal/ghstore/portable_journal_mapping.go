package ghstore

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// JournalMapping keys are repository-qualified source issue IDs, for example
// github:owner/repo:gh-12. Values are IDs in the selected destination repository.
type JournalMapping map[string]string

func ParseJournalMapping(data []byte) (JournalMapping, error) {
	var mapping JournalMapping
	if err := decodeImportJSON(data, &mapping); err != nil || mapping == nil {
		return nil, fmt.Errorf("journal mapping must be a JSON object of source keys to destination IDs: %v", err)
	}
	for key, target := range mapping {
		fields := strings.Split(key, ":")
		if len(fields) != 3 || fields[0] != "github" || !repositoryPattern.MatchString(fields[1]) || fields[1] != strings.ToLower(fields[1]) || !canonicalPortableIssue(fields[2]) || !canonicalPortableIssue(target) {
			return nil, fmt.Errorf("invalid journal mapping %q -> %q; use github:owner/repo:gh-N -> gh-N", key, target)
		}
	}
	return mapping, nil
}

func journalSourceKey(repo, id string) string { return "github:" + strings.ToLower(repo) + ":" + id }

// BindJournal copies the bundle and records mappings without rewriting any
// original history. Existing mappings may be extended but never overwritten.
func BindJournal(bundle *PortableJournalBundle, repository string, mapping JournalMapping) (*PortableJournalBundle, error) {
	if bundle == nil {
		return nil, fmt.Errorf("portable journal is required")
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	bound, err := ParsePortableJournal(data)
	if err != nil {
		return nil, err
	}
	if !repositoryPattern.MatchString(repository) {
		return nil, fmt.Errorf("invalid journal destination repository")
	}
	used := map[string]bool{}
	for _, source := range bound.Sources {
		refs, err := source.references()
		if err != nil {
			return nil, err
		}
		binding := PortableJournalBinding{SourceSHA256: source.SHA256, TargetRepository: repository, Targets: map[string]string{}}
		index := -1
		for i, previous := range bound.Bindings {
			if previous.SourceSHA256 == source.SHA256 && strings.EqualFold(previous.TargetRepository, repository) {
				index, binding = i, previous
			}
		}
		for ref := range refs {
			key := journalSourceKey(source.Repository, ref)
			if target, found := mapping[key]; found {
				if old, found := binding.Targets[ref]; found && old != target {
					return nil, fmt.Errorf("journal mapping conflicts with preserved binding: %s", key)
				}
				binding.Targets[ref], used[key] = target, true
			}
		}
		if index >= 0 {
			bound.Bindings[index] = binding
		} else if len(binding.Targets) > 0 {
			bound.Bindings = append(bound.Bindings, binding)
		}
	}
	for key := range mapping {
		if !used[key] {
			return nil, fmt.Errorf("journal mapping references unknown source issue %s", key)
		}
	}
	bound.SHA256, err = bound.checksum()
	if err != nil {
		return nil, err
	}
	return bound, bound.Validate()
}

// Merge keeps distinct historical snapshots of a source repository, dedupes
// identical sources and flattens archive hops. Conflicting bindings are errors.
func mergePortableJournals(bundles ...*PortableJournalBundle) (*PortableJournalBundle, error) {
	merged := &PortableJournalBundle{Format: portableJournalFormat, Version: 1, Sources: []PortableJournalSource{}, Bindings: []PortableJournalBinding{}}
	sources, bindings := map[string]PortableJournalSource{}, map[string]PortableJournalBinding{}
	for _, bundle := range bundles {
		if err := bundle.Validate(); err != nil {
			return nil, err
		}
		for _, source := range bundle.Sources {
			sources[source.SHA256] = source
		}
		for _, binding := range bundle.Bindings {
			key := binding.SourceSHA256 + ":" + strings.ToLower(binding.TargetRepository)
			previous, exists := bindings[key]
			if !exists {
				previous = PortableJournalBinding{SourceSHA256: binding.SourceSHA256, TargetRepository: binding.TargetRepository, Targets: map[string]string{}}
			}
			for source, target := range binding.Targets {
				if old, found := previous.Targets[source]; found && old != target {
					return nil, fmt.Errorf("conflicting journal archive mappings for %s in %s", source, binding.TargetRepository)
				}
				previous.Targets[source] = target
			}
			bindings[key] = previous
		}
	}
	for _, source := range sources {
		merged.Sources = append(merged.Sources, source)
	}
	for _, binding := range bindings {
		merged.Bindings = append(merged.Bindings, binding)
	}
	sort.Slice(merged.Sources, func(i, j int) bool { return merged.Sources[i].SHA256 < merged.Sources[j].SHA256 })
	sort.Slice(merged.Bindings, func(i, j int) bool {
		a, b := merged.Bindings[i], merged.Bindings[j]
		return a.SourceSHA256+strings.ToLower(a.TargetRepository) < b.SourceSHA256+strings.ToLower(b.TargetRepository)
	})
	var err error
	merged.SHA256, err = merged.checksum()
	if err != nil {
		return nil, err
	}
	return merged, merged.Validate()
}
