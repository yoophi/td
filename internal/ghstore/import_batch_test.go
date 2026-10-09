package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func importBatchFixture(t *testing.T) (*Client, map[int]map[string]any, *int, *[]apiComment) {
	t.Helper()
	c, issues, writes := hierarchyFixture(t)
	comments := []apiComment{}
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		endpoint := args[5]
		if strings.Contains(endpoint, "/issues/comments?") {
			return json.Marshal([][]apiComment{comments})
		}
		if strings.HasSuffix(endpoint, "/comments") && slices.Contains(args, "POST") {
			var n int
			if _, err := fmt.Sscanf(endpoint, "repos/owner/repo/issues/%d/comments", &n); err != nil {
				t.Fatal(err)
			}
			var fields map[string]string
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			comment := apiComment{ID: int64(len(comments) + 1), Body: fields["body"], IssueURL: fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", n)}
			comments = append(comments, comment)
			return json.Marshal(comment)
		}
		return base(ctx, dir, payload, args...)
	}
	return c, issues, writes, &comments
}

func TestImportBatchRelationshipsHistoryAndUncertainWriteResume(t *testing.T) {
	for _, lost := range []string{"none", "allocate", "source", "intent", "patch", "complete"} {
		t.Run(lost, func(t *testing.T) {
			c, _, writes, comments := importBatchFixture(t)
			child, parent := importTestItem("td-child"), importTestItem("td-parent")
			child.SourceNamespace, parent.SourceNamespace = "project", "project"
			child.Issue.ParentID = parent.Issue.ID
			child.Issue.Status = models.StatusInProgress
			child.Dependencies = []models.IssueDependency{{DependsOnID: parent.Issue.ID, RelationType: "depends_on"}}
			items := []ImportItem{child, parent}
			base := c.run
			failure := errors.New("fixture response lost after remote write")
			failed := false
			bulkReads := 0
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") && (strings.Contains(args[5], "/issues?state=all") || strings.Contains(args[5], "/issues/comments?")) {
					bulkReads++
				}
				data, err := base(ctx, dir, payload, args...)
				match := lost == "allocate" && slices.Contains(args, "POST") && strings.HasSuffix(args[5], "/issues") ||
					lost == "patch" && slices.Contains(args, "PATCH") ||
					lost == "source" && strings.Contains(string(payload), "td:import-provenance:") ||
					lost == "intent" && strings.Contains(string(payload), "td:import-intent:") ||
					lost == "complete" && strings.Contains(string(payload), "td:import-receipt:")
				if !failed && match && err == nil {
					failed = true
					return nil, failure
				}
				return data, err
			}
			result, err := c.executeImport(context.Background(), items, false)
			if lost != "none" {
				if !errors.Is(err, failure) || !failed || !strings.Contains(err.Error(), "no automatic retry") {
					t.Fatal("lost response was hidden/retried", result, err)
				}
				result, err = c.executeImport(context.Background(), items, false)
			}
			if err != nil {
				t.Fatal(result, err)
			}
			mapping := map[string]string{}
			for _, row := range result.Plan.Rows {
				mapping[row.SourceKey] = row.TargetID
			}
			got, err := c.Get(context.Background(), mapping[child.SourceKey()])
			if err != nil || got.ParentID != mapping[parent.SourceKey()] || !slices.Equal(got.Details.Dependencies, []string{mapping[parent.SourceKey()]}) || got.Status != models.StatusInProgress {
				t.Fatal(got, err)
			}
			if *writes != 4 {
				t.Fatal("creation/PATCH repeated after uncertain write", *writes)
			}
			if lost == "none" && bulkReads != 6 {
				t.Fatal("completion used per-issue bulk collection reads", bulkReads)
			}
			saved := len(*comments)
			beforeReads := bulkReads
			result, err = c.executeImport(context.Background(), items, false)
			if err != nil || result.Skipped != 2 || result.Created != 0 || result.Applied != 0 || len(*comments) != saved || *writes != 4 {
				t.Fatal("completed batch repeated writes", result, err, len(*comments), saved)
			}
			if bulkReads-beforeReads != 2 {
				t.Fatal("completed replay performed more than one bulk observation", bulkReads-beforeReads)
			}
		})
	}
}

func TestImportBatchOverwriteArchivesPriorDestination(t *testing.T) {
	c, issues, _, comments := importBatchFixture(t)
	record, err := c.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	source := ImportItem{Issue: record.Issue, GitHub: &ExportedIssue{Version: 1, Repository: c.repo, Record: *record, Body: "", NativeState: "open", Activity: []models.Activity{}}}
	source.Issue.Title = "Restored source title"
	source.GitHub.Record.Title = source.Issue.Title
	result, err := c.executeImport(context.Background(), []ImportItem{source}, true)
	if err != nil || result.Completed != 1 || issues[1]["title"] != source.Issue.Title {
		t.Fatal(result, err)
	}
	var archiveDigest string
	for _, comment := range *comments {
		intent, err := decodeImportIntent(comment.Body)
		if err != nil {
			t.Fatal(err)
		}
		if intent != nil {
			archiveDigest = intent.DestinationArchiveDigest
		}
	}
	if archiveDigest == "" {
		t.Fatal("overwrite intent did not bind prior archive")
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := snapshotImportProvenance(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := source.Digest()
	ref := importProvenancePart{SourceKey: source.SourceKey(), SourceDigest: digest, TargetID: "gh-1", Kind: "destination", ContentDigest: archiveDigest}
	data, err := reassembleImportProvenance(inventory[ref.key()])
	if err != nil {
		t.Fatal(err)
	}
	var archive importDestinationArchive
	if err := json.Unmarshal(data, &archive); err != nil || archive.Issue.Title != "Fixture 1" || archive.Repository != c.repo {
		t.Fatal("prior destination lost", archive, err)
	}
	// Completion also binds the prior destination archive: deleting it must
	// not let later import replays silently skip over lost overwrite history.
	kept := []apiComment{}
	for _, comment := range *comments {
		part, err := decodeImportProvenance(comment.Body)
		if err != nil {
			t.Fatal(err)
		}
		if part == nil || part.Kind != "destination" {
			kept = append(kept, comment)
		}
	}
	*comments = kept
	if _, err := c.PlanImport(context.Background(), []ImportItem{source}, true); err == nil || !strings.Contains(err.Error(), "prior destination archive") {
		t.Fatal("lost overwrite history silently accepted", err)
	}
}

func TestImportBatchInterruptedOverwriteKeepsOriginalArchive(t *testing.T) {
	c, issues, writes, comments := importBatchFixture(t)
	record, err := c.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	source := ImportItem{Issue: record.Issue, GitHub: &ExportedIssue{Version: 1, Repository: c.repo, Record: *record, NativeState: "open", Activity: []models.Activity{}}}
	source.Issue.Title, source.GitHub.Record.Title = "Restored title", "Restored title"
	base := c.run
	failure := errors.New("lost overwrite PATCH response")
	lost := false
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if !lost && err == nil && slices.Contains(args, "PATCH") {
			lost = true
			return nil, failure
		}
		return data, err
	}
	if _, err := c.executeImport(context.Background(), []ImportItem{source}, true); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	saved := len(*comments)
	result, err := c.executeImport(context.Background(), []ImportItem{source}, true)
	if err != nil || result.Completed != 1 || result.Applied != 0 || *writes != 1 || len(*comments) != saved+1 || issues[1]["title"] != source.Issue.Title {
		t.Fatal("overwrite replay lost archive/repeated PATCH", result, err, *writes, len(*comments), saved)
	}
}
