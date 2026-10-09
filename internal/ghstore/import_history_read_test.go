package ghstore

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestPreservedImportHistoryBulkReadsExportAndPassiveAuthority(t *testing.T) {
	c, _, _, comments := importBatchFixture(t)
	source := importTestItem("td-source")
	source.SourceNamespace = "project"
	original := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	source.Logs = []models.Log{{ID: "log-old", IssueID: source.Issue.ID, SessionID: "original-worker", Message: "Original progress", Type: models.LogTypeProgress, Timestamp: original}}
	source.Handoffs = []models.Handoff{{ID: "handoff-old", IssueID: source.Issue.ID, SessionID: "original-worker", Done: []string{"Original handoff"}, Timestamp: original}}
	if _, err := c.executeImport(context.Background(), []ImportItem{source}, false); err != nil {
		t.Fatal(err)
	}
	base := c.run
	calls := 0
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if !slices.Contains(args, "GET") || payload != nil {
			t.Fatal("read attempted write", args)
		}
		calls++
		if strings.Contains(args[5], "/issues/4/comments?") {
			return json.Marshal([][]apiComment{*comments})
		}
		return base(ctx, dir, payload, args...)
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err != nil || calls != 2 {
		t.Fatal(snapshot, err, calls)
	}
	activity, err := snapshot.Activity("gh-4")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, a := range activity {
		if a.PreservedHistory == nil {
			continue
		}
		found++
		if a.Kind != "comment" || !a.Native || a.SessionID != "" || a.OperationID != "" || !a.PreservedHistory.Complete {
			t.Fatal("historical authority promoted", a)
		}
		var recovered ImportItem
		if err := json.Unmarshal(a.PreservedHistory.Data, &recovered); err != nil || len(recovered.Logs) != 1 || recovered.Logs[0].SessionID != "original-worker" || !recovered.Logs[0].Timestamp.Equal(original) || len(recovered.Handoffs) != 1 {
			t.Fatal(recovered, err)
		}
		// The derived payload must not permit mutation of a cached snapshot.
		a.PreservedHistory.Data[0] = 'x'
	}
	if found != 1 || calls != 2 {
		t.Fatal(found, calls)
	}
	again, err := snapshot.Activity("gh-4")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range again {
		if a.PreservedHistory != nil && !json.Valid(a.PreservedHistory.Data) {
			t.Fatal("snapshot was mutated through activity")
		}
	}
	rows, err := c.ExportIssues(context.Background(), true, true)
	if err != nil || calls != 4 {
		t.Fatal(rows, err, calls)
	}
	found = 0
	for _, row := range rows {
		for _, a := range row.Activity {
			if a.PreservedHistory != nil {
				found++
			}
		}
	}
	if found != 1 {
		t.Fatal("export lost preserved history", rows)
	}
	items := make([]ImportItem, 0, len(rows))
	for i := range rows {
		items = append(items, ImportItem{Issue: rows[i].Record.Issue, GitHub: &rows[i]})
	}
	encoded, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseImportJSON(encoded)
	if err != nil {
		t.Fatal("formatted export failed preserved-history validation", err)
	}
	for i := range decoded {
		for j := range decoded[i].GitHub.Activity {
			a := &decoded[i].GitHub.Activity[j]
			if a.PreservedHistory == nil {
				continue
			}
			a.Native = false
			if err := ValidateImportItems(decoded); err == nil {
				t.Fatal("preserved history was promoted to active activity")
			}
			a.Native = true
			a.PreservedHistory.Data = json.RawMessage(`{}`)
			if err := ValidateImportItems(decoded); err == nil {
				t.Fatal("changed decoded history accepted with original digest")
			}
		}
	}
	// Existing direct per-issue activity API exposes the same derived payload.
	direct, err := c.ListActivity(context.Background(), "gh-4")
	if err != nil {
		t.Fatal(err)
	}
	found = 0
	for _, a := range direct {
		if a.PreservedHistory != nil {
			found++
		}
	}
	if found != 1 {
		t.Fatal("direct activity read lost preserved history", direct)
	}
}

func TestPreservedHistoryPartialSetReportsMissingFirstPart(t *testing.T) {
	source := largeImportSource(t)
	parts, err := sourceImportProvenance(source, "gh-1")
	if err != nil || len(parts) < 2 {
		t.Fatal(parts, err)
	}
	body, err := renderImportProvenance(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	comment := apiComment{ID: 10, Body: body, IssueURL: "https://api.github.com/repos/owner/repo/issues/1"}
	c, _ := snapshotFixture([][]apiIssue{{{Number: 1, State: "open"}}}, [][]apiComment{{comment}}, nil)
	snapshot, err := c.ReadSnapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	activity, err := snapshot.Activity("gh-1")
	if err != nil || len(activity) != 1 {
		t.Fatal(activity, err)
	}
	doc := activity[0].PreservedHistory
	if doc == nil || doc.Complete || doc.PartsPresent != 1 || doc.PartsExpected != len(parts) || len(doc.Data) != 0 || activity[0].Message != body {
		t.Fatal("partial history silently dropped or claimed complete", activity)
	}
}

func TestPreservedHistoryReadRejectsEntireMissingCompletedArchive(t *testing.T) {
	source := importTestItem("td-source")
	digest, _ := source.Digest()
	receipt := importReceipt{Version: 1, SourceKey: source.SourceKey(), SourceDigest: digest, TargetID: "gh-1", TargetBasis: digest, SourceParts: 1}
	body, err := renderImportReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	comment := apiComment{ID: 10, Body: body, IssueURL: "https://api.github.com/repos/owner/repo/issues/1"}
	c, _ := snapshotFixture([][]apiIssue{{{Number: 1, State: "open"}}}, [][]apiComment{{comment}}, nil)
	if _, err := c.ReadSnapshot(context.Background(), true); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatal("missing completed archive silently vanished from read", err)
	}
}
