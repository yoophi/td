package ghstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/auditlog"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func auditReviewFixture(t *testing.T) *reviewFixture {
	t.Helper()
	f := newReviewFixture(t)
	if _, _, err := f.client.Transition(context.Background(), "1", "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted}); err != nil {
		t.Fatal(err)
	}
	return f
}

func auditApproveOptions() TransitionOptions {
	return TransitionOptions{SessionID: "fixture-worker", AgentType: "fixture-agent", Mode: reviewpolicy.ModeTrusted, SelfReview: true, Reason: "Fixture honest self-review"}
}

func TestGitHubAuditCorrelatesIntentAndConfirmedReview(t *testing.T) {
	for _, recordOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(recordOnly), func(t *testing.T) {
			f := auditReviewFixture(t)
			var events []auditlog.SecurityEvent
			f.client.audit = func(event auditlog.SecurityEvent) error { events = append(events, event); return nil }
			o := auditApproveOptions()
			o.RecordOnly = recordOnly
			result, _, err := f.client.Transition(context.Background(), "1", "approve", o)
			if err != nil || len(events) != 2 {
				t.Fatalf("%+v %v", events, err)
			}
			if events[0].Outcome != "attempted" || events[1].Outcome != "confirmed" || events[0].OperationID == "" || events[0].OperationID != events[1].OperationID || events[1].SessionID != o.SessionID || events[1].AgentType != o.AgentType || events[1].Repository != "owner/repo" || events[1].Scope != "device-local" {
				t.Fatalf("incorrect audit attribution: %+v", events)
			}
			if !strings.Contains(events[1].Reason, "self_review") || recordOnly && !strings.HasPrefix(events[1].Reason, "record_only") {
				t.Fatal("review exception absent")
			}
			if !result.Details.Reviews[len(result.Details.Reviews)-1].SelfReview {
				t.Fatal("shared self-review lost")
			}
		})
	}
	f := auditReviewFixture(t)
	calls := 0
	f.client.audit = func(auditlog.SecurityEvent) error { calls++; return nil }
	if _, _, err := f.client.Transition(context.Background(), "1", "approve", TransitionOptions{SessionID: "fixture-independent", Mode: reviewpolicy.ModeBalanced}); err != nil || calls != 0 {
		t.Fatalf("independent review marked exceptional: calls=%d err=%v", calls, err)
	}
}

func TestGitHubAuditFailuresDoNotPretendRollback(t *testing.T) {
	for _, phase := range []string{"intent", "confirmed", "uncertain"} {
		t.Run(phase, func(t *testing.T) {
			f := auditReviewFixture(t)
			var events []auditlog.SecurityEvent
			f.client.audit = func(event auditlog.SecurityEvent) error {
				events = append(events, event)
				if (phase == "intent" && event.Outcome == "attempted") || (phase == "confirmed" && event.Outcome == "confirmed") {
					return errors.New("fixture audit disk failure")
				}
				return nil
			}
			if phase == "uncertain" {
				base := f.client.run
				f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
					if slices.Contains(args, "PATCH") {
						return nil, errors.New("fixture lost response")
					}
					return base(ctx, dir, payload, args...)
				}
			}
			before := f.writes
			_, _, err := f.client.Transition(context.Background(), "1", "approve", auditApproveOptions())
			if err == nil {
				t.Fatal("audit/write failure hidden")
			}
			if phase == "intent" && (f.writes != before || !strings.Contains(err.Error(), "no issue transition write attempted")) {
				t.Fatalf("intent failure wrote issue: %v", err)
			}
			if phase == "confirmed" {
				r, _ := f.client.Get(context.Background(), "1")
				if r.Status != models.StatusClosed || !strings.Contains(err.Error(), "is saved as closed") {
					t.Fatalf("confirmed write pretended rollback: %v", err)
				}
			}
			if phase == "uncertain" && (len(events) != 2 || events[1].Outcome != "uncertain") {
				t.Fatalf("uncertain write unrecorded: %+v", events)
			}
		})
	}
}

func TestGitHubAuditAttributedReviewAndCloseExceptions(t *testing.T) {
	for _, action := range []string{"attributed", "admin", "self-close"} {
		t.Run(action, func(t *testing.T) {
			f := auditReviewFixture(t)
			var events []auditlog.SecurityEvent
			f.client.audit = func(event auditlog.SecurityEvent) error { events = append(events, event); return nil }
			o := auditApproveOptions()
			command := "approve"
			if action == "attributed" {
				o.SelfReview = false
				o.ReviewedBy = "fixture explicit attribution"
			} else {
				if _, _, err := f.client.Transition(context.Background(), "1", "reject", TransitionOptions{SessionID: o.SessionID, Mode: o.Mode}); err != nil {
					t.Fatal(err)
				}
				command = "close"
				o.SelfReview = false
				if action == "admin" {
					o.AdminReason = "Fixture administrative override"
				} else {
					o.SelfCloseException = "Fixture self-close exception"
				}
			}
			if _, _, err := f.client.Transition(context.Background(), "1", command, o); err != nil || len(events) != 2 {
				t.Fatalf("%+v %v", events, err)
			}
			want := map[string]string{"attributed": "attributed_review", "admin": "admin_close", "self-close": "self_close_exception"}[action]
			if !strings.Contains(events[1].Reason, want) {
				t.Fatalf("exception absent: %+v", events)
			}
		})
	}
}

func TestGitHubAuditBalancedCreatorException(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	observed, err := f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	d, err := observed.CopyDetails()
	if err != nil {
		t.Fatal(err)
	}
	d.CreatorSession = "fixture-creator"
	if _, err := f.client.UpdateObserved(ctx, observed, Changes{Details: &d}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.client.Transition(ctx, "1", "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeBalanced}); err != nil {
		t.Fatal(err)
	}
	var events []auditlog.SecurityEvent
	f.client.audit = func(event auditlog.SecurityEvent) error { events = append(events, event); return nil }
	_, _, err = f.client.Transition(ctx, "1", "approve", TransitionOptions{SessionID: "fixture-creator", Mode: reviewpolicy.ModeBalanced, Reason: "Fixture balanced creator exception"})
	if err != nil || len(events) != 2 || !strings.HasPrefix(events[1].Reason, "creator_approval_exception") {
		t.Fatalf("%+v %v", events, err)
	}
}
