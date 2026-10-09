package ghstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func prioritySyncFixture(t *testing.T) *reviewFixture {
	t.Helper()
	f := newReviewFixture(t)
	body, err := encodeBody("Legacy managed fixture", metadata{Type: models.TypeTask, Priority: models.PriorityP2})
	if err != nil {
		t.Fatal(err)
	}
	f.issue["body"] = body
	f.issue["labels"] = []map[string]string{{"name": "bug"}, {"name": "TD:PRIORITY:P0"}, {"name": "td:priority:P1"}, {"name": "td:priority:custom"}}
	f.client.stateLabels["td:priority:p2"] = true
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if !slices.Contains(args, "GET") && !strings.Contains(args[5], "/issues/1") {
			t.Fatal("unexpected label write in preview")
		}
		if strings.Contains(args[5], "/issues?state=all") {
			data, err := fixtureIssueJSON(f.issue)
			return append(append([]byte("[["), data...), []byte(`,{"number":2,"title":"Unmanaged","state":"open"}]]`)...), err
		}
		return base(ctx, dir, payload, args...)
	}
	return f
}

func TestPriorityLabelCreationPermissionFailurePreservesCause(t *testing.T) {
	permission := errors.New("HTTP 403: fixture label permission denied")
	posts := 0
	c := &Client{repo: "owner/priority-permission", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		if !strings.Contains(args[5], "/labels") {
			t.Fatal("issue write attempted before label setup")
		}
		if slices.Contains(args, "GET") {
			return []byte(`[[]]`), nil
		}
		posts++
		return nil, permission
	}}
	err := c.ensurePriorityLabel(context.Background(), models.PriorityP1)
	if posts != 1 || !errors.Is(err, permission) || !strings.Contains(err.Error(), "issue write not attempted") {
		t.Fatalf("posts=%d error=%v", posts, err)
	}
}

func TestPriorityLabelSyncReportsEarlierAppliedRepairs(t *testing.T) {
	c, issues, writes := hierarchyFixture(t)
	for _, n := range []int{1, 2} {
		body, err := encodeBody("Managed", metadata{Type: models.TypeTask, Priority: models.PriorityP2})
		if err != nil {
			t.Fatal(err)
		}
		issues[n]["body"] = body
	}
	c.stateLabels["td:priority:p2"] = true
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		// Listing returns descending order, so gh-2 is repaired first.
		if slices.Contains(args, "PATCH") && strings.HasSuffix(args[5], "/issues/1") {
			return nil, errors.New("HTTP 403: fixture second repair denied")
		}
		return base(ctx, dir, payload, args...)
	}
	result, err := c.SyncPriorityLabels(context.Background(), true)
	if err == nil || result.Applied != 1 || *writes != 1 || !strings.Contains(err.Error(), "1 priority label repairs saved") {
		t.Fatalf("result=%+v writes=%d err=%v", result, *writes, err)
	}
}

func TestPriorityLabelPreviewApplyAndLegacyRead(t *testing.T) {
	f := prioritySyncFixture(t)
	r, err := f.client.Get(context.Background(), "1")
	if err != nil || r.Priority != models.PriorityP2 || r.PriorityLabelWarning() == "" {
		t.Fatalf("metadata lost: %+v %v", r, err)
	}
	result, err := f.client.SyncPriorityLabels(context.Background(), false)
	if err != nil || len(result.Repairs) != 1 || result.Applied != 0 || f.writes != 0 {
		t.Fatalf("preview writes: %+v %v", result, err)
	}
	result, err = f.client.SyncPriorityLabels(context.Background(), true)
	if err != nil || result.Applied != 1 {
		t.Fatalf("apply: %+v %v", result, err)
	}
	r, err = f.client.Get(context.Background(), "1")
	if err != nil || r.PriorityLabelWarning() != "" || !slices.Contains(r.Labels, "bug") || !slices.Contains(r.Labels, "td:priority:custom") {
		t.Fatalf("labels: %+v %v", r, err)
	}
	writes := f.writes
	result, err = f.client.SyncPriorityLabels(context.Background(), true)
	if err != nil || len(result.Repairs) != 0 || f.writes != writes {
		t.Fatalf("not idempotent: %+v %v", result, err)
	}
}

func TestPriorityLabelSyncStopsOnConcurrentRevisionChange(t *testing.T) {
	f := prioritySyncFixture(t)
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		out, err := base(ctx, dir, payload, args...)
		if strings.Contains(args[5], "/issues?state=all") {
			f.issue["title"] = "Concurrent edit after plan"
		}
		return out, err
	}
	result, err := f.client.SyncPriorityLabels(context.Background(), true)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || result.Applied != 0 || f.writes != 0 {
		t.Fatalf("conflict ignored: %+v writes=%d err=%v", result, f.writes, err)
	}
}

func TestPriorityLabelSelectionValidatesAllTargetsBeforeWriting(t *testing.T) {
	f := prioritySyncFixture(t)
	for _, id := range []string{"gh-2", "gh-999", "invalid"} {
		_, err := f.client.SyncPriorityLabels(context.Background(), true, "gh-1", id)
		if err == nil || f.writes != 0 {
			t.Fatalf("selection %s wrote before validation: %v", id, err)
		}
	}
	result, err := f.client.SyncPriorityLabels(context.Background(), true, "1", "gh-1")
	if err != nil || result.Applied != 1 {
		t.Fatalf("duplicate normalized selection: %+v %v", result, err)
	}
}

func TestPriorityLabelsFollowMetadataOnManagedUpdates(t *testing.T) {
	f := prioritySyncFixture(t)
	title := "Repair on ordinary edit"
	r, err := f.client.Update(context.Background(), "1", Changes{Title: &title})
	if err != nil || r.Priority != models.PriorityP2 || r.PriorityLabelWarning() != "" || !slices.Contains(r.Labels, "bug") {
		t.Fatalf("ordinary edit did not repair priority: %+v %v", r, err)
	}
	priority := models.PriorityP0
	r, err = f.client.Update(context.Background(), "1", Changes{Priority: &priority})
	if err != nil || r.Priority != priority || r.PriorityLabelWarning() != "" {
		t.Fatalf("priority change: %+v %v", r, err)
	}
	// External labels cannot override the authoritative metadata.
	f.issue["labels"] = []map[string]string{{"name": "td:priority:P4"}}
	r, err = f.client.Get(context.Background(), "1")
	if err != nil || r.Priority != priority || r.PriorityLabelDiagnostic == "" {
		t.Fatalf("external label became authoritative: %+v %v", r, err)
	}
	clear := []string{}
	r, err = f.client.Update(context.Background(), "1", Changes{Labels: &clear})
	if err != nil || !slices.Equal(r.Labels, []string{"td:open", "td:priority:P0"}) {
		t.Fatalf("reserved labels cleared: %+v %v", r, err)
	}
}

func TestPriorityMirrorDoesNotClaimUnmanagedNativeLabels(t *testing.T) {
	f := newReviewFixture(t)
	f.issue["labels"] = []map[string]string{{"name": "td:priority:P0"}, {"name": "bug"}}
	closed := models.StatusClosed
	r, err := f.client.Update(context.Background(), "1", Changes{Status: &closed})
	if err != nil || r.managed || !slices.Equal(r.Labels, []string{"td:priority:P0", "bug", "td:closed"}) {
		t.Fatalf("unmanaged label ownership changed: %+v %v", r, err)
	}
}

func TestPriorityRepairPreviewIncludesStateMirrorRepair(t *testing.T) {
	f := prioritySyncFixture(t)
	f.issue["labels"] = []map[string]string{{"name": "bug"}, {"name": "td:closed"}}
	plan, err := f.client.SyncPriorityLabels(context.Background(), false)
	if err != nil || len(plan.Repairs) != 1 || !slices.Equal(plan.Repairs[0].After, []string{"bug", "td:open", "td:priority:P2"}) {
		t.Fatalf("preview hid state repair: %+v %v", plan, err)
	}
}
