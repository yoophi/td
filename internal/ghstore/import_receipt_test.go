package ghstore

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestImportCompletionReceiptPlanSkipSourceChangeAndTargetConflict(t *testing.T) {
	source := importTestItem("td-source")
	source.SourceNamespace = "project-a"
	body, err := encodeBody(source.Issue.Description, metadata{OperationID: importOperationID(source.SourceKey()), Type: source.Issue.Type, Priority: source.Issue.Priority})
	if err != nil {
		t.Fatal(err)
	}
	item := apiIssue{Number: 1, Title: source.Issue.Title, Body: body, State: "open"}
	basis, err := importTargetBasis(item)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := source.Digest()
	history := importSourceProvenanceComments(t, source, "gh-1", 1)
	receiptBody, err := renderImportReceipt(importReceipt{Version: 1, SourceKey: source.SourceKey(), SourceDigest: digest, TargetID: "gh-1", TargetBasis: basis, SourceParts: len(history)})
	if err != nil {
		t.Fatal(err)
	}
	issues := [][]apiIssue{{item}}
	comments := [][]apiComment{append(history, apiComment{ID: 10, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: receiptBody})}
	c, calls := snapshotFixture(issues, comments, nil)
	c.cache = &snapshotCache{root: filepath.Join(t.TempDir(), "td", "gh-issue", "v2"), repo: c.repo, credential: cacheHash(t.Name())}
	run := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if payload != nil || !slices.Contains(args, "GET") {
			t.Fatal("plan attempted a write", args)
		}
		return run(ctx, dir, payload, args...)
	}
	plan, err := c.PlanImport(context.Background(), []ImportItem{source}, false)
	if err != nil || plan.Rows[0].Action != "skip" || plan.Rows[0].SourceChanged || len(*calls) != 2 {
		t.Fatalf("completed import not skipped with bulk reads: %+v %v calls=%v", plan, err, *calls)
	}
	// Comment-induced native clocks do not turn an unchanged issue into a
	// conflict. Every plan re-reads both collections instead of trusting cache.
	issues[0][0].UpdatedAt = time.Now().UTC()
	plan, err = c.PlanImport(context.Background(), []ImportItem{source}, false)
	if err != nil || plan.Rows[0].Action != "skip" || len(*calls) != 4 {
		t.Fatal("native clock invalidated unchanged content", plan, err)
	}
	source.Issue.Title = "Changed source"
	if _, err := c.PlanImport(context.Background(), []ImportItem{source}, false); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatal("changed source silently accepted", err)
	}
	plan, err = c.PlanImport(context.Background(), []ImportItem{source}, true)
	if err != nil || plan.Rows[0].Action != "overwrite" || !plan.Rows[0].SourceChanged {
		t.Fatal("force did not expose changed source", plan, err)
	}
	issues[0][0].Title = "Destination edited by someone else"
	for _, force := range []bool{false, true} {
		_, err := c.PlanImport(context.Background(), []ImportItem{source}, force)
		var conflict *ConflictError
		if !errors.As(err, &conflict) || conflict.ID != "gh-1" {
			t.Fatalf("destination edits accepted with force=%v: %v", force, err)
		}
	}
}

func TestImportReceiptDoesNotGrantActivityOrReviewPrivileges(t *testing.T) {
	receipt := importReceipt{Version: 1, SourceKey: "legacy:project:td-one", SourceDigest: strings.Repeat("a", 64), TargetID: "gh-1", TargetBasis: strings.Repeat("b", 64)}
	body, err := renderImportReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	activity, err := decodeActivity(apiComment{ID: 1, Body: body}, "gh-1")
	if err != nil || !activity.Native || activity.Kind != "comment" || activity.OperationID != "" || activity.SessionID != "" {
		t.Fatalf("receipt became an active authored event: %+v %v", activity, err)
	}
	if _, err := decodeImportReceipt("ordinary native comment"); err != nil {
		t.Fatal(err)
	}
	for _, altered := range []string{
		strings.Replace(body, "completed", "not completed", 1),
		strings.Replace(body, "receipt:v1", "receipt:v2", 1),
		strings.Replace(body, `"version":1`, `"version":1,"unknown":true`, 1),
		body + " edited",
		body + "\n" + body,
	} {
		if _, err := decodeImportReceipt(altered); err == nil {
			t.Fatal("edited/unknown receipt accepted", altered)
		}
	}
}

func TestImportReceiptNormalizedLegacyIDDoesNotWeakenNativeIdentity(t *testing.T) {
	for _, key := range []string{"legacy:td-gh-1", "legacy:project:td-gh-1", "github:owner/repo:gh-1"} {
		r := importReceipt{Version: 1, SourceKey: key, SourceDigest: strings.Repeat("a", 64), TargetID: "gh-9", TargetBasis: strings.Repeat("b", 64)}
		if err := r.validate(); err != nil {
			t.Fatal("valid source receipt rejected", key, err)
		}
	}
	for _, key := range []string{"github:owner/repo:td-gh-1", "github:owner/repo:td-abc", "github:owner/repo:gh-01", "legacy:project:td-gh-01", "legacy:bad source:td-gh-1"} {
		r := importReceipt{Version: 1, SourceKey: key, SourceDigest: strings.Repeat("a", 64), TargetID: "gh-9", TargetBasis: strings.Repeat("b", 64)}
		if err := r.validate(); err == nil {
			t.Fatal("invalid source receipt accepted", key)
		}
	}
}

func TestImportTargetBasisIgnoresNativeClocksAndLabelOrderingOnly(t *testing.T) {
	item := apiIssue{Number: 1, State: "open", Title: "Imported", Body: "Body", Labels: []struct {
		Name string `json:"name"`
	}{{Name: "bug"}, {Name: "td:priority:P1"}}}
	basis, err := importTargetBasis(item)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item.CreatedAt, item.UpdatedAt, item.ClosedAt = now, now, &now
	slices.Reverse(item.Labels)
	got, err := importTargetBasis(item)
	if err != nil || got != basis {
		t.Fatal("server clocks or label order changed basis", err)
	}
	for _, change := range []func(*apiIssue){
		func(i *apiIssue) { i.Title += " external" },
		func(i *apiIssue) { i.Body += " external" },
		func(i *apiIssue) { i.State = string(models.StatusClosed) },
		func(i *apiIssue) { i.Labels = i.Labels[:1] },
	} {
		altered := item
		change(&altered)
		got, err := importTargetBasis(altered)
		if err != nil || got == basis {
			t.Fatal("writable issue field omitted from conflict basis", err)
		}
	}
}

func TestImportReceiptBindingLatestVersionAndEmptyLabels(t *testing.T) {
	item := apiIssue{Number: 1, State: "open"}
	basis, err := importTargetBasis(item)
	if err != nil {
		t.Fatal(err)
	}
	item.Labels = []struct {
		Name string `json:"name"`
	}{}
	got, err := importTargetBasis(item)
	if err != nil || got != basis {
		t.Fatal("empty labels representation changed content basis", err)
	}
	receipt := importReceipt{Version: 1, SourceKey: "legacy:project:td-source", SourceDigest: strings.Repeat("a", 64), TargetID: "gh-1", TargetBasis: basis}
	old, _ := renderImportReceipt(receipt)
	receipt.SourceDigest = strings.Repeat("b", 64)
	latest, _ := renderImportReceipt(receipt)
	snapshot := &Snapshot{entries: []snapshotEntry{{Issue: item, Comments: []apiComment{{ID: 1, Body: old}, {ID: 2, Body: latest}}}}}
	receipts, _, err := snapshotImportReceipts(snapshot)
	if err != nil || receipts[receipt.SourceKey].SourceDigest != receipt.SourceDigest {
		t.Fatal("old import version selected", receipts, err)
	}
	wrong := receipt
	wrong.TargetID = "gh-2"
	badBody, _ := renderImportReceipt(wrong)
	snapshot.entries[0].Comments = []apiComment{{ID: 3, Body: badBody}}
	if _, _, err := snapshotImportReceipts(snapshot); err == nil {
		t.Fatal("receipt bound to another issue accepted")
	}
	snapshot.entries[0].Comments = []apiComment{{ID: 1, Body: old}}
	snapshot.entries = append(snapshot.entries, snapshotEntry{Issue: apiIssue{Number: 2, State: "open"}, Comments: []apiComment{{ID: 3, Body: badBody}}})
	if _, _, err := snapshotImportReceipts(snapshot); err == nil {
		t.Fatal("one source mapped to multiple completed targets")
	}
}

func TestImportCompletionRequiresHistoryAndFieldsOnlyReceiptResumes(t *testing.T) {
	source := importTestItem("td-source")
	source.SourceNamespace = "project-a"
	body, err := encodeBody(source.Issue.Description, metadata{OperationID: importOperationID(source.SourceKey()), Type: source.Issue.Type, Priority: source.Issue.Priority})
	if err != nil {
		t.Fatal(err)
	}
	item := apiIssue{Number: 1, Title: source.Issue.Title, Body: body, State: "open"}
	basis, _ := importTargetBasis(item)
	digest, _ := source.Digest()
	receipt := importReceipt{Version: 1, SourceKey: source.SourceKey(), SourceDigest: digest, TargetID: "gh-1", TargetBasis: basis}
	receiptBody, _ := renderImportReceipt(receipt)
	comments := [][]apiComment{{{ID: 10, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: receiptBody}}}
	c, calls := snapshotFixture([][]apiIssue{{item}}, comments, nil)
	run := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if payload != nil || !slices.Contains(args, "GET") {
			t.Fatal("unverified history caused a write", args)
		}
		return run(ctx, dir, payload, args...)
	}
	plan, err := c.PlanImport(context.Background(), []ImportItem{source}, false)
	if err != nil || plan.Rows[0].Action != "resume_history" || plan.Rows[0].ResumeState != "history_pending" || plan.Rows[0].HistoryVerified {
		t.Fatal(plan, err)
	}
	before := len(*calls)
	if _, _, _, err := c.applyImportIssue(context.Background(), source, plan.Rows[0], importRelations{}); err == nil || len(*calls) != before {
		t.Fatal("history-only replay entered issue apply", err, *calls)
	}
	changed := source
	changed.Issue.Title += " changed"
	if _, err := c.PlanImport(context.Background(), []ImportItem{changed}, true); err == nil || !strings.Contains(err.Error(), "original input") {
		t.Fatal("force replaced unarchived original history", err)
	}
	if err := c.completeImport(context.Background(), receipt); err == nil {
		t.Fatal("fields-only completion published")
	}
	receipt.SourceParts = 1
	receiptBody, _ = renderImportReceipt(receipt)
	comments[0][0].Body = receiptBody
	if _, err := c.PlanImport(context.Background(), []ImportItem{source}, false); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatal("deleted history allowed skip", err)
	}
	if err := c.completeImport(context.Background(), receipt); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatal("completion without history allowed", err)
	}
}

func TestImportCompletionHistoryRejectsChangedArchive(t *testing.T) {
	source := importTestItem("td-source")
	parts, err := sourceImportProvenance(source, "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	receipt := importReceipt{Version: 1, SourceKey: source.SourceKey(), SourceDigest: parts[0].SourceDigest, TargetID: "gh-1", TargetBasis: strings.Repeat("b", 64), SourceParts: len(parts)}
	group := make(map[int]importProvenancePart, len(parts))
	for _, part := range parts {
		group[part.Index] = part
	}
	inventory := importProvenanceInventory{parts[0].key(): group}
	if verified, err := verifyImportReceiptHistory(receipt, inventory); err != nil || !verified {
		t.Fatal(verified, err)
	}
	parts[0].Data = "e30="
	group[0] = parts[0]
	if verified, err := verifyImportReceiptHistory(receipt, inventory); err == nil || verified {
		t.Fatal("modified archive verified", verified, err)
	}
}
