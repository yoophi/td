package storetransfer

import (
	"context"
	"fmt"

	"github.com/marcus/td/internal/ghstore"
)

// BindGitHubTaskIDs bridges the importer's checked source keys back to original
// qualified workspace keys. It validates shape/completeness only; callers must
// obtain completed via Client.CompletedImportMappings, not fabricate receipts.
func BindGitHubTaskIDs(source *Source, completed map[string]string) (map[string]string, error) {
	input, err := BuildTaskImportInput(source)
	if err != nil {
		return nil, err
	}
	if source.Envelope.Store != "sqlite" {
		return nil, fmt.Errorf("GitHub task binding requires a SQLite workspace source")
	}
	if len(completed) != len(input.Keys) {
		return nil, fmt.Errorf("completed import mapping does not cover the source tasks exactly")
	}
	result := map[string]string{}
	used := map[string]bool{}
	for key, importKey := range input.Keys {
		id, ok := completed[importKey]
		n, err := ghstore.Number(id)
		if !ok || err != nil || id != fmt.Sprintf("gh-%d", n) || used[id] {
			return nil, fmt.Errorf("source %s has missing, duplicate or noncanonical completed task binding", key)
		}
		used[id] = true
		result[key] = id
	}
	return result, nil
}

// ObserveGitHubEntityInputs verifies native task completions before preparing
// dependent note/board writes. It reuses one bulk import observation and never
// writes a task or trusts an externally supplied successful-result counter.
func ObserveGitHubEntityInputs(ctx context.Context, source *Source, client *ghstore.Client, reusedKeys map[string]bool) (*GitHubEntityInputs, map[string]string, error) {
	if client == nil {
		return nil, nil, fmt.Errorf("explicit GitHub destination client required")
	}
	input, err := BuildTaskImportInput(source)
	if err != nil {
		return nil, nil, err
	}
	if source.Envelope.Store != "sqlite" {
		return nil, nil, fmt.Errorf("GitHub entity observation requires a SQLite source")
	}
	completed, err := client.CompletedImportMappings(ctx, input.Items)
	if err != nil {
		return nil, nil, err
	}
	bindings, err := BindGitHubTaskIDs(source, completed)
	if err != nil {
		return nil, nil, err
	}
	entities, err := BuildGitHubEntityInputs(source, bindings, reusedKeys)
	if err != nil {
		return nil, nil, err
	}
	return entities, bindings, nil
}
