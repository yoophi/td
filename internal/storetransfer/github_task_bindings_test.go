package storetransfer

import (
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubTaskBindingsBridgeStableProxiesToOriginalWorkspaceQueries(t *testing.T) {
	s, _ := githubEntitySourceFixture(t)
	input, err := BuildTaskImportInput(s)
	if err != nil {
		t.Fatal(err)
	}
	completed := map[string]string{}
	for i, item := range input.Items {
		completed[item.SourceKey()] = []string{"gh-11", "gh-22"}[i]
	}
	bindings, err := BindGitHubTaskIDs(s, completed)
	if err != nil {
		t.Fatal(err)
	}
	entities, err := BuildGitHubEntityInputs(s, bindings, nil)
	if err != nil || entities.Boards[0].Query != `id = "gh-22"` || entities.Boards[0].Options.Positions[0].IssueID != "gh-22" {
		t.Fatal("proxy IDs leaked into original workspace references", err)
	}
	for _, stage := range []string{"missing", "extra", "wrong_key", "duplicate", "noncanonical"} {
		t.Run(stage, func(t *testing.T) {
			bad := map[string]string{}
			for key, id := range completed {
				bad[key] = id
			}
			switch stage {
			case "missing":
				delete(bad, input.Items[0].SourceKey())
			case "extra":
				bad["unknown"] = "gh-33"
			case "wrong_key":
				delete(bad, input.Items[0].SourceKey())
				bad["unknown"] = "gh-11"
			case "duplicate":
				for key := range bad {
					bad[key] = "gh-11"
				}
			case "noncanonical":
				bad[input.Items[0].SourceKey()] = "gh-011"
			}
			if result, err := BindGitHubTaskIDs(s, bad); err == nil || result != nil {
				t.Fatal("invalid binding returned partial result")
			}
		})
	}
	// A client/result object is not required for structural conversion, but
	// native verification is mandatory in the observation entry point.
	var client *ghstore.Client
	if entities, ids, err := ObserveGitHubEntityInputs(t.Context(), s, client, nil); err == nil || entities != nil || ids != nil {
		t.Fatal("missing verified destination client accepted")
	}
}
