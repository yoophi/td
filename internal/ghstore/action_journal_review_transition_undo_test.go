package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/reviewpolicy"
)

// Reviewer names and sessions in this helper are synthetic test identities.
func reviewTransitionUndoFixture(t *testing.T, action string) (*journalWriteFixture, func(string), string) {
	t.Helper()
	reviewed := action != "review" && action != "review-handoff"
	f, mode := fileUndoFixture(t, reviewed, false)
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(action, "approve") && action != "approve-replace" {
		issue := f.issues[1]
		desc, m, _, _ := decodeBody(issue.Body)
		m.Details.Reviews = nil
		m.Details.ReviewerSession = ""
		m.Details.ReviewedAt = nil
		if action == "approve-self" || action == "approve-attributed" {
			m.Details.ImplementerSession = "ses_actor"
			data, _ := json.Marshal(issue)
			record, err := decodeIssue(data)
			if err != nil {
				t.Fatal(err)
			}
			m.Details.ReviewBasis = reviewBasis(record, *m.Details)
		}
		issue.Body, _ = encodeBody(desc, m)
		f.issues[1] = issue
	}
	original := f.issues[1].Body
	o := TransitionOptions{SessionID: "ses_actor", Mode: reviewpolicy.ModeTrusted, Reason: "Synthetic review transition"}
	switch action {
	case "review":
		o.Minor = true
	case "review-handoff":
		action = "review"
	case "approve-self":
		o.SelfReview = true
		action = "approve"
	case "approve-attributed":
		o.ReviewedBy = "Synthetic fixture reviewer"
		action = "approve"
	case "approve-changes", "approve-replace":
		o.Decision = reviewpolicy.DecisionChangesRequested
		action = "approve"
	}
	if action == "approve" {
		o.RecordOnly = true
	}
	_, _, err := f.client.Transition(context.Background(), "gh-1", action, o)
	if err != nil {
		t.Fatal(err)
	}
	mode("")
	return f, mode, original
}

func TestReviewTransitionUndoRestoresExactOriginal(t *testing.T) {
	for _, action := range []string{"review", "review-handoff", "approve", "approve-self", "approve-attributed", "approve-changes", "approve-replace", "reject"} {
		t.Run(action, func(t *testing.T) {
			f, _, original := reviewTransitionUndoFixture(t, action)
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			before, after := *a.Before, *a.After
			writes := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || result.Issue.nativeBody != original || result.Journal.Phase != "undone" || f.targetWrites != writes+1 {
				t.Fatal(result, err)
			}
			if !reflect.DeepEqual(before, *result.Journal.Before) || !reflect.DeepEqual(after, *result.Journal.After) {
				t.Fatal("original snapshots changed")
			}
			writes, receipts := f.targetWrites, f.receiptWrites
			result, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || !result.AlreadyUndone || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal("repeat wrote", result, err)
			}
		})
	}
}

func TestReviewTransitionUndoRejectsChangedOriginalEvidence(t *testing.T) {
	for _, action := range []string{"approve", "reject"} {
		for _, mode := range []string{"events", "handoff", "handoff-deleted", "events-after-intent", "handoff-after-patch", "events-after-receipt", "target-after-final-review", "events-denied"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				f, setMode, _ := reviewTransitionUndoFixture(t, action)
				setMode(mode)
				writes := f.targetWrites
				result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
				if result != nil || err == nil {
					t.Fatal("stale proof succeeded", result, err)
				}
				var conflict *ConflictError
				if mode == "events-denied" {
					if errors.As(err, &conflict) || !strings.Contains(err.Error(), "HTTP 403") || f.targetWrites != writes {
						t.Fatal("lost transport cause", err)
					}
				} else if !errors.As(err, &conflict) {
					t.Fatal("not conflict", err)
				}
			})
		}
	}
}

func TestReviewTransitionUndoRejectsForgedEffects(t *testing.T) {
	for _, mode := range []string{"actor", "claim", "review-author", "review-time", "review-summary", "review-issue", "session-prefix", "dependency", "basis", "native-close", "old-review-author"} {
		t.Run(mode, func(t *testing.T) {
			action := "approve"
			if mode == "old-review-author" {
				action = "reject"
			}
			f, _, _ := reviewTransitionUndoFixture(t, action)
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			desc, m, _, _ := decodeBody(a.After.Body)
			d := m.Details
			switch mode {
			case "actor":
				d.Transitions[len(d.Transitions)-1].SessionID = "ses_foreign"
			case "claim":
				d.ImplementerSession = "ses_foreign"
			case "review-author":
				d.Reviews[len(d.Reviews)-1].ReviewerSession = "ses_foreign"
			case "review-time":
				d.Reviews[len(d.Reviews)-1].CreatedAt = a.Before.UpdatedAt
			case "review-summary":
				d.Reviews[len(d.Reviews)-1].Summary = "Different reason"
			case "review-issue":
				d.Reviews[len(d.Reviews)-1].IssueID = "gh-2"
			case "session-prefix":
				d.Sessions[len(d.Sessions)-1].SessionID = "ses_foreign"
			case "dependency":
				d.Dependencies = []string{"gh-2"}
			case "basis":
				d.ReviewBasis = "forged"
			case "native-close":
				a.After.State = "closed"
			case "old-review-author":
				d.Reviews[0].ReviewerSession = "ses_foreign"
			}
			a.After.Body, err = encodeBody(desc, m)
			if err != nil {
				t.Fatal(err)
			}
			if err := scalarUndoScope(a); err == nil {
				t.Fatal("forged effect accepted", mode)
			}
		})
	}
}

func TestReviewTransitionUndoFailuresNeverReplay(t *testing.T) {
	for _, mode := range []string{"undo-actor", "target-conflict", "target-denied", "target-uncertain", "receipt-denied", "receipt-uncertain", "journal-concurrent", "undo-later"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := reviewTransitionUndoFixture(t, "approve")
			f.mode = mode
			f.commentReads = 0
			writes := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if result != nil || err == nil || f.targetWrites > writes+1 {
				t.Fatal("failed inverse succeeded/replayed", result, err)
			}
		})
	}
}
