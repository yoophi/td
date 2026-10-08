package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

// All identities here are test fixtures, not attestations of actual review.
type reviewFixture struct {
	client        *Client
	issue         map[string]any
	comments      []apiComment
	events        []nativeStateEvent
	writes, posts int
	beforePatch   func()
}

func newReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	f := &reviewFixture{issue: map[string]any{"number": 1, "state": "open", "title": "Fixture"}, events: []nativeStateEvent{}, comments: []apiComment{}}
	f.client = &Client{stateLabels: fixtureStateLabels(), repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		switch {
		case slices.Contains(args, "repos/owner/repo/issues/1/events?per_page=100"):
			return json.Marshal([][]nativeStateEvent{f.events})
		case slices.Contains(args, "repos/owner/repo/issues/1/comments?per_page=100"):
			return json.Marshal([][]apiComment{f.comments})
		case slices.Contains(args, "repos/owner/repo/issues/1/comments") && slices.Contains(args, "POST"):
			var fields map[string]string
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			f.posts++
			now := time.Now().UTC()
			comment := apiComment{ID: int64(f.posts), Body: fields["body"], CreatedAt: now, UpdatedAt: now}
			f.comments = append(f.comments, comment)
			return json.Marshal(comment)
		case slices.Contains(args, "repos/owner/repo/issues/1"):
			if slices.Contains(args, "PATCH") {
				f.writes++
				if f.beforePatch != nil {
					f.beforePatch()
				}
				var fields map[string]any
				if err := json.Unmarshal(payload, &fields); err != nil {
					t.Fatal(err)
				}
				if next, ok := fields["state"].(string); ok && next != f.issue["state"] {
					f.nativeState(next)
				}
				for key, value := range fields {
					f.issue[key] = value
				}
			}
			return fixtureIssueJSON(f.issue)
		default:
			return nil, fmt.Errorf("unexpected fixture request: %v", args)
		}
	}}
	return f
}

func (f *reviewFixture) nativeState(state string) {
	f.issue["state"] = state
	event := "closed"
	if state == "open" {
		event = "reopened"
	}
	f.events = append(f.events, nativeStateEvent{ID: int64(len(f.events) + 1), Event: event})
}

func (f *reviewFixture) transition(t *testing.T, action string, options TransitionOptions) *Record {
	t.Helper()
	got, _, err := f.client.Transition(context.Background(), "1", action, options)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return got
}

func TestReviewLifecycleAndNativeReclose(t *testing.T) {
	f := newReviewFixture(t)
	worker := TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted}
	f.transition(t, "start", worker)
	r := f.transition(t, "review", worker)
	if r.Status != models.StatusInReview || f.posts != 1 || r.Details.ReviewHandoffID != "ghc-1" {
		t.Fatalf("review: %+v posts=%d", r, f.posts)
	}
	before := f.writes
	if _, _, err := f.client.Transition(context.Background(), "1", "approve", worker); err == nil || f.writes != before {
		t.Fatal("unacknowledged self approval wrote")
	}
	worker.SelfReview, worker.Reason = true, "Test fixture self review"
	r = f.transition(t, "approve", worker)
	if r.Status != models.StatusClosed || !r.Details.Reviews[0].SelfReview || r.ClosedBySession != worker.SessionID {
		t.Fatalf("approval: %+v", r)
	}
	_, noop, err := f.client.Transition(context.Background(), "1", "approve", worker)
	if err != nil || !noop {
		t.Fatalf("repeat approval: noop=%v %v", noop, err)
	}
	f.nativeState("open")
	f.nativeState("closed")
	before = f.writes
	if _, _, err := f.client.Transition(context.Background(), "1", "approve", worker); err == nil || !strings.Contains(err.Error(), "history changed") || f.writes != before {
		t.Fatalf("native reclose accepted: %v", err)
	}
}

func TestReviewPolicyModesRetainImplementationHistory(t *testing.T) {
	for _, mode := range []reviewpolicy.Mode{reviewpolicy.ModeStrict, reviewpolicy.ModeBalanced, reviewpolicy.ModeDelegated, reviewpolicy.ModeTrusted} {
		t.Run(string(mode), func(t *testing.T) {
			f := newReviewFixture(t)
			worker := TransitionOptions{SessionID: "fixture-worker", Mode: mode}
			f.transition(t, "start", worker)
			f.transition(t, "unstart", worker)
			f.transition(t, "start", TransitionOptions{SessionID: "fixture-other", Mode: mode})
			f.transition(t, "review", worker)
			before := f.writes
			if _, _, err := f.client.Transition(context.Background(), "1", "approve", worker); err == nil || f.writes != before {
				t.Fatal("released implementation history bypassed policy")
			}
			r := f.transition(t, "approve", TransitionOptions{SessionID: "fixture-independent", Mode: mode})
			if r.Status != models.StatusClosed || r.ReviewerSession != "fixture-independent" {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestRecordOnlyApprovalSeparatesReviewerAndCloser(t *testing.T) {
	f := newReviewFixture(t)
	worker := TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeDelegated}
	f.transition(t, "review", worker)
	reviewer := TransitionOptions{SessionID: "fixture-reviewer", Mode: worker.Mode, RecordOnly: true, Reason: "Fixture independent approval"}
	r := f.transition(t, "approve", reviewer)
	if r.Status != models.StatusInReview || r.ClosedBySession != "" {
		t.Fatalf("record only: %+v", r)
	}
	before := f.writes
	if _, _, err := f.client.Transition(context.Background(), "1", "approve", worker); err == nil || f.writes != before {
		t.Fatal("close on other's approval without reason")
	}
	worker.Reason = "Use recorded approval"
	r = f.transition(t, "approve", worker)
	if r.Status != models.StatusClosed || r.ReviewerSession != reviewer.SessionID || r.ClosedBySession != worker.SessionID || len(r.Details.Reviews) != 1 {
		t.Fatalf("attribution: %+v", r)
	}
}

func TestReviewInvalidationPreventsWrites(t *testing.T) {
	for _, kind := range []string{"title", "events", "handoff-edit", "handoff-delete"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviewFixture(t)
			f.transition(t, "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeStrict})
			switch kind {
			case "title":
				f.issue["title"] = "Changed after submission"
			case "events":
				f.nativeState("closed")
				f.nativeState("open")
			case "handoff-edit":
				f.comments[0].UpdatedAt = f.comments[0].UpdatedAt.Add(time.Second)
			case "handoff-delete":
				f.comments = nil
			}
			before := f.writes
			if _, _, err := f.client.Transition(context.Background(), "1", "approve", TransitionOptions{SessionID: "fixture-reviewer", Mode: reviewpolicy.ModeStrict}); err == nil || f.writes != before {
				t.Fatalf("stale review wrote: %v", err)
			}
		})
	}
}

func TestReviewOptionsRejectBlankExceptionsBeforeRequests(t *testing.T) {
	for _, options := range []TransitionOptions{
		{AdminReason: " \t"}, {SelfCloseException: "\n"}, {ReviewedBy: " \t"},
	} {
		options.Mode = reviewpolicy.ModeTrusted
		options.SessionID = "fixture-worker"
		client := &Client{stateLabels: fixtureStateLabels(), run: func(context.Context, string, []byte, ...string) ([]byte, error) {
			t.Fatal("invalid options made request")
			return nil, nil
		}}
		if _, _, err := client.Transition(context.Background(), "1", "close", options); err == nil {
			t.Fatalf("accepted %+v", options)
		}
	}
}

func TestRejectAndAdminClosePreserveAudit(t *testing.T) {
	f := newReviewFixture(t)
	worker := TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted}
	f.transition(t, "review", worker)
	r := f.transition(t, "reject", TransitionOptions{SessionID: "fixture-reviewer", Mode: worker.Mode, Reason: "Needs fixes"})
	if r.Status != models.StatusOpen || r.ImplementerSession != "" || r.Details.Reviews[0].Decision != reviewpolicy.DecisionChangesRequested {
		t.Fatalf("reject: %+v", r)
	}
	if _, _, err := f.client.Transition(context.Background(), "1", "close", worker); err == nil {
		t.Fatal("released worker closed without review")
	}
	worker.AdminReason = "Fixture administrative cancellation"
	r = f.transition(t, "close", worker)
	if r.Status != models.StatusClosed || r.Details.Transitions[len(r.Details.Transitions)-1].AdminReason != worker.AdminReason {
		t.Fatal("lost exception audit")
	}
	worker.AdminReason = ""
	if _, _, err := f.client.Transition(context.Background(), "1", "approve", worker); err == nil {
		t.Fatal("administrative close counted as approval")
	}
	r = f.transition(t, "reopen", worker)
	if r.Status != models.StatusOpen || r.ReviewerSession != "" || r.ImplementerSession != "" {
		t.Fatalf("reopen: %+v", r)
	}
}
