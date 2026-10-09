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

func TestImportedHistoryProjectionPortableIdentityAndReviewIsolation(t *testing.T) {
	c, _, _, comments := importBatchFixture(t)
	source := importTestItem("td-source")
	source.SourceNamespace = "project"
	original := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	source.Logs = []models.Log{{ID: "log-old", IssueID: source.Issue.ID, SessionID: "old-worker", Message: "Original progress", Type: models.LogTypeProgress, Timestamp: original}}
	source.Handoffs = []models.Handoff{{ID: "handoff-old", IssueID: source.Issue.ID, SessionID: "old-worker", Done: []string{"Original handoff"}, Timestamp: original}}
	if _, err := c.executeImport(context.Background(), []ImportItem{source}, false); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err != nil {
		t.Fatal(err)
	}
	activity, err := snapshot.Activity("gh-4")
	if err != nil {
		t.Fatal(err)
	}
	logs, handoffs, err := ProjectImportedHistory(activity, "gh-4")
	if err != nil || len(logs) != 1 || len(handoffs) != 1 || !logs[0].Historical || logs[0].SessionID != "old-worker" || logs[0].IssueID != "gh-4" || logs[0].OriginalID != "log-old" || !logs[0].Timestamp.Equal(original) {
		t.Fatal(logs, handoffs, err)
	}
	originalLogID, originalHandoffID := logs[0].ID, handoffs[0].ID
	NamespacePortableHistorySessions(logs, handoffs)
	firstSession := logs[0].SessionID
	NamespacePortableHistorySessions(logs, handoffs)
	if logs[0].SessionID != firstSession || firstSession == "old-worker" || !strings.HasPrefix(firstSession, "imported-") || logs[0].OriginalSessionID != "old-worker" {
		t.Fatal("historical export impersonated active session or renamespaced", logs)
	}
	// An exported historical row retains its original identity through a later
	// import envelope, rather than adding another hash/namespace per migration.
	next := importTestItem("td-next")
	next.Logs, next.Handoffs = logs, handoffs
	next.Logs[0].IssueID, next.Handoffs[0].IssueID = next.Issue.ID, next.Issue.ID
	bytes, _ := json.Marshal(next)
	digest, _ := next.Digest()
	doc := &models.PreservedHistory{Kind: "source", SourceKey: next.SourceKey(), SourceDigest: digest, ContentDigest: digest, PartsPresent: 1, PartsExpected: 1, Complete: true, Data: bytes}
	againLogs, againHandoffs, err := ProjectImportedHistory([]models.Activity{{ID: "ghc-99", Kind: "comment", Native: true, PreservedHistory: doc}}, "gh-99")
	if err != nil || againLogs[0].ID != originalLogID || againHandoffs[0].ID != originalHandoffID || againLogs[0].SessionID != "old-worker" {
		t.Fatal("re-import lost historical identity", againLogs, againHandoffs, err)
	}
	// Review uses only native protocol activities, not projected historical rows.
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "/issues/4/comments?") {
			return json.Marshal([][]apiComment{*comments})
		}
		return base(ctx, dir, payload, args...)
	}
	record, err := c.Get(context.Background(), "gh-4")
	if err != nil {
		t.Fatal(err)
	}
	_, handoff, err := c.reviewHandoff(context.Background(), record, TransitionOptions{SessionID: "current-reviewer"})
	if err != nil || handoff.SessionID != "current-reviewer" || !slices.Equal(handoff.Done, []string{"Auto-generated for review submission"}) {
		t.Fatal("old history satisfied current review handoff", handoff, err)
	}
}
