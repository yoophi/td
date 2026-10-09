package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"slices"
	"strings"
	"testing"
)

func TestLogicalDeleteRestorePreservesWorkflowAndNativeState(t *testing.T) {
	ctx := context.Background()
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "closed"}[closed], func(t *testing.T) {
			f := newReviewFixture(t)
			f.transition(t, "start", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
			if closed {
				f.transition(t, "close", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted, AdminReason: "Fixture explicit close"})
			}
			base := f.client.run
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "DELETE") {
					t.Fatal("destructive GitHub delete")
				}
				if slices.Contains(args, "PATCH") {
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(payload, &fields); err != nil {
						t.Fatal(err)
					}
					if _, exists := fields["state"]; exists {
						t.Fatal("deletion changed native state")
					}
				}
				return base(ctx, dir, payload, args...)
			}
			before, err := f.client.Get(ctx, "1")
			if err != nil {
				t.Fatal(err)
			}
			result, noop, err := f.client.SetDeletedObserved(ctx, before, true, "fixture-deleter", "Fixture reason")
			if err != nil || noop || result.DeletedAt == nil || result.Status != before.Status || result.ImplementerSession != before.ImplementerSession {
				t.Fatalf("delete: %+v %v %v", result, noop, err)
			}
			if before.DeletedAt != nil {
				t.Fatal("observation mutated")
			}
			if _, err := f.client.Get(ctx, "1"); err == nil {
				t.Fatal("deleted visible")
			}
			visible, err := f.client.List(ctx, true)
			if err != nil || len(visible) != 0 {
				t.Fatalf("list: %v %v", visible, err)
			}
			all, err := f.client.ListIncludingDeleted(ctx, true)
			if err != nil || len(all) != 1 {
				t.Fatalf("history list: %v %v", all, err)
			}
			writes := f.writes
			again, noop, err := f.client.SetDeletedObserved(ctx, result, true, "fixture-deleter", "Repeat")
			if err != nil || !noop || f.writes != writes || !again.DeletedAt.Equal(*result.DeletedAt) {
				t.Fatal("repeat delete wrote or lost timestamp")
			}
			restored, noop, err := f.client.SetDeletedObserved(ctx, result, false, "fixture-restorer", "Fixture restore")
			if err != nil || noop || restored.DeletedAt != nil || restored.Status != before.Status || restored.ImplementerSession != before.ImplementerSession {
				t.Fatalf("restore: %+v %v", restored, err)
			}
			history := restored.Details.Transitions
			if len(history) < 2 || history[len(history)-2].Action != "delete" || history[len(history)-2].SessionID != "fixture-deleter" || history[len(history)-1].Action != "restore" || history[len(history)-1].SessionID != "fixture-restorer" {
				t.Fatalf("lost shared history: %v", history)
			}
			writes = f.writes
			if _, noop, err := f.client.SetDeletedObserved(ctx, restored, false, "fixture-restorer", ""); err != nil || !noop || f.writes != writes {
				t.Fatal("repeat restore wrote")
			}
		})
	}
}

func TestLogicalDeleteConflictsAndExternalNativeClosure(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	before, err := f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	f.issue["title"] = "External edit"
	if _, _, err := f.client.SetDeletedObserved(ctx, before, true, "fixture-actor", ""); err == nil || f.writes != 0 {
		t.Fatal("stale observation wrote")
	}
	before, err = f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.client.SetDeletedObserved(ctx, before, true, "", ""); err == nil || f.writes != 0 {
		t.Fatal("blank actor wrote")
	}
	deleted, _, err := f.client.SetDeletedObserved(ctx, before, true, "fixture-actor", "")
	if err != nil {
		t.Fatal(err)
	}
	f.nativeState("closed")
	if _, _, err := f.client.SetDeletedObserved(ctx, deleted, false, "fixture-actor", ""); err == nil {
		t.Fatal("stale restore wrote")
	}
	current, err := f.client.GetIncludingDeleted(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	restored, _, err := f.client.SetDeletedObserved(ctx, current, false, "fixture-actor", "")
	if err != nil || restored.Status != models.StatusClosed {
		t.Fatalf("external close lost: %+v %v", restored, err)
	}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") {
			return nil, errors.New("fixture denied")
		}
		return base(ctx, dir, payload, args...)
	}
	if _, _, err := f.client.SetDeletedObserved(ctx, restored, true, "fixture-actor", ""); err == nil || !strings.Contains(err.Error(), "fixture denied") {
		t.Fatalf("permission failure swallowed: %v", err)
	}
}
