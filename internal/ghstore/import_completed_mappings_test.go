package ghstore

import (
	"context"
	"errors"
	"testing"
)

func TestCompletedImportMappingsRequiresNativeCompletionAndHistory(t *testing.T) {
	for _, stage := range []string{"complete", "pending", "fields_only", "missing_history", "changed_target", "changed_source", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			source := importTestItem("td-gh-1")
			source.SourceNamespace = "original"
			body, err := encodeBody(source.Issue.Description, metadata{OperationID: importOperationID(source.SourceKey()), Type: source.Issue.Type, Priority: source.Issue.Priority})
			if err != nil {
				t.Fatal(err)
			}
			item := apiIssue{Number: 1, Title: source.Issue.Title, Body: body, State: "open"}
			basis, err := importTargetBasis(item)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := source.Digest()
			if err != nil {
				t.Fatal(err)
			}
			history := importSourceProvenanceComments(t, source, "gh-1", 1)
			receipt := importReceipt{Version: 1, SourceKey: source.SourceKey(), SourceDigest: digest, TargetID: "gh-1", TargetBasis: basis, SourceParts: len(history)}
			if stage == "fields_only" {
				receipt.SourceParts = 0
			}
			raw, err := renderImportReceipt(receipt)
			if err != nil {
				t.Fatal(err)
			}
			comments := append([]apiComment{}, history...)
			if stage == "missing_history" {
				comments = nil
			}
			if stage != "pending" {
				comments = append(comments, apiComment{ID: 10, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: raw})
			}
			if stage == "changed_target" {
				item.Title = "external edit"
			}
			if stage == "changed_source" {
				source.Issue.Title = "source edit"
			}
			client, calls := snapshotFixture([][]apiIssue{{item}}, [][]apiComment{comments}, nil)
			ctx := context.Background()
			if stage == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			ids, err := client.CompletedImportMappings(ctx, []ImportItem{source})
			if stage == "complete" {
				if err != nil || len(ids) != 1 || ids[source.SourceKey()] != "gh-1" || len(*calls) != 2 {
					t.Fatal("verified reference lost or N+1", ids, err, *calls)
				}
			} else if err == nil || ids != nil {
				t.Fatal("partial/changed import became completed mapping", ids, err)
			}
			if stage == "cancelled" && (!errors.Is(err, context.Canceled) || len(*calls) != 0) {
				t.Fatal("cancelled observation accessed GitHub", err)
			}
		})
	}
}
