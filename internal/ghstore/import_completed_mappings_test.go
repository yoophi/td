package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCompletedImportMappingsAdoptionRequiresOriginalNativeCommand(t *testing.T) {
	for _, stage := range []string{"complete", "all_adopted", "pending", "untracked", "other_session", "other_author", "malformed_author", "changed_fields", "changed_comments"} {
		t.Run(stage, func(t *testing.T) {
			f := newWritableOwnedAdoptionFixture(t, stage == "all_adopted")
			if err := f.client.TrackActions("ses_owner"); err != nil {
				t.Fatal(err)
			}
			if stage != "pending" {
				worker := *f.client
				worker.journal = nil
				if _, err := worker.executeImport(f.ctx, f.items, false); err != nil {
					t.Fatal(err)
				}
				forward, err := f.client.observeImportCompletion(f.ctx, f.journal, f.items, false, CreationObservation{Proof: "verified", SessionID: "ses_owner", Actor: "github-author", ObservedAt: time.Now().UTC()})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.client.completeImportCommand(f.ctx, f.journal, forward); err != nil {
					t.Fatal(err)
				}
			}
			switch stage {
			case "untracked":
				f.client.journal = nil
			case "other_session":
				f.client.journal.SessionID = "ses_other"
			case "changed_fields":
				f.native[1]["title"] = "external retained edit"
			case "changed_comments":
				f.extra = append(f.extra, apiComment{ID: 9999, Body: "external comment", IssueURL: "https://api.github.com/repos/owner/repo/issues/1", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
			}
			base, collections, actors := f.client.run, 0, 0
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if args[4] != "GET" {
					t.Fatal("mapping observation attempted a write", args)
				}
				if strings.Contains(args[5], "per_page=") {
					collections++
				}
				if args[5] == "user" {
					actors++
					if stage == "other_author" {
						return []byte(`{"login":"another-author"}`), nil
					}
					if stage == "malformed_author" {
						return []byte(`{"login":`), nil
					}
				}
				return base(ctx, dir, payload, args...)
			}
			ids, err := f.client.CompletedImportMappings(context.Background(), f.items)
			if stage == "complete" || stage == "all_adopted" {
				if err != nil || len(ids) != 2 || ids[f.items[0].SourceKey()] != "gh-1" || collections != 2 || actors != 1 {
					t.Fatal("lost verified binding or N+1 observation", ids, err, collections, actors)
				}
			} else if err == nil || ids != nil {
				t.Fatal("unverified adoption mapping accepted", ids, err)
			}
			if stage == "malformed_author" {
				var syntax *json.SyntaxError
				if !errors.As(err, &syntax) {
					t.Fatal("original author response error was hidden", err)
				}
			}
		})
	}
}

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
