package ghstore

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/marcus/td/internal/reviewpolicy"
)

// All identities here are synthetic fixture data, never review attestations.
func nativeUndoPlanFixture(t *testing.T, variant string) (*ActionJournalRecord, *Record) {
	t.Helper()
	reviewed := variant != "close-unreviewed"
	f, _ := fileUndoFixture(t, reviewed, variant == "reopen")
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	if variant == "approve-new" {
		issue := f.issues[1]
		desc, m, _, _ := decodeBody(issue.Body)
		m.Details.Reviews = nil
		m.Details.ReviewerSession = ""
		m.Details.ReviewedAt = nil
		issue.Body, _ = encodeBody(desc, m)
		f.issues[1] = issue
	}
	action := "close"
	if variant == "reopen" {
		action = "reopen"
	}
	if variant == "approve-new" || variant == "approve-existing" {
		action = "approve"
	}
	o := TransitionOptions{SessionID: "ses_actor", Mode: reviewpolicy.ModeTrusted, Reason: "Synthetic native transition"}
	if variant == "close-unreviewed" {
		o.AdminReason = "Synthetic explicit closure"
	}
	if _, _, err := f.client.Transition(context.Background(), "gh-1", action, o); err != nil {
		t.Fatal(err)
	}
	a, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.client.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	return a, current
}

func TestNativeUndoCompensationPlanPreservesAuditAndInvalidatesReview(t *testing.T) {
	for _, variant := range []string{"close-unreviewed", "close-approved", "approve-new", "approve-existing", "reopen"} {
		t.Run(variant, func(t *testing.T) {
			a, current := nativeUndoPlanFixture(t, variant)
			original, _ := json.Marshal(a)
			at := time.Now().UTC().Add(time.Minute)
			plan, err := a.PlanNativeIssueUndo(current, "ses_actor", "github-author", "td-op-native-inverse", at)
			if err != nil {
				t.Fatal(err)
			}
			body, ok := plan.Payload["body"].(string)
			if !ok {
				t.Fatal(plan)
			}
			desc, m, _, err := decodeBody(body)
			if err != nil {
				t.Fatal(err)
			}
			beforeDesc, bm, _, _ := decodeBody(a.Before.Body)
			_, am, _, _ := decodeBody(a.After.Body)
			if desc != beforeDesc || plan.Payload["title"] != a.Before.Title || plan.Payload["state"] != a.Before.State || !reflect.DeepEqual(plan.Payload["labels"], a.Before.Labels) {
				t.Fatal("content/state not compensated", plan)
			}
			d := m.Details
			if d.ReviewerSession != "" || d.ReviewedAt != nil || d.ReviewBasis != "" || d.ReviewEvents != "" || activeApproval(d) != nil {
				t.Fatal("old approval revived", d)
			}
			if d.Status != bm.Details.Status || d.ImplementerSession != bm.Details.ImplementerSession || len(d.Reviews) != len(am.Details.Reviews) || !reflect.DeepEqual(d.Sessions, am.Details.Sessions) {
				t.Fatal("checkpoint/audit lost", d)
			}
			if len(d.Transitions) != len(am.Details.Transitions)+1 {
				t.Fatal("undo provenance missing")
			}
			tr := d.Transitions[len(d.Transitions)-1]
			if tr.Action != "undo_native" || tr.OperationID != plan.UndoOperationID || tr.SessionID != "ses_actor" || tr.From != am.Details.Status || tr.To != bm.Details.Status || !tr.At.Equal(at) {
				t.Fatal(tr)
			}
			for i, row := range d.Reviews {
				originalRow := am.Details.Reviews[i]
				if originalRow.SupersededAt == nil {
					if row.SupersededAt == nil || !row.SupersededAt.Equal(at) {
						t.Fatal("approval still active")
					}
					originalRow.SupersededAt = &at
				}
				if !reflect.DeepEqual(originalRow, row) {
					t.Fatal("review reattributed or erased")
				}
			}
			after, _ := json.Marshal(a)
			if string(original) != string(after) {
				t.Fatal("journal snapshots mutated")
			}
			if plan.ExactHistoricalRestore || plan.CausalUndoChainEligible || plan.RequiresNewReview != (variant != "close-unreviewed") {
				t.Fatal("policy misrepresented", plan)
			}
		})
	}
}

func TestNativeUndoCompensationPlanRejectsWrongActorAndUncertainState(t *testing.T) {
	for _, mode := range []string{"session", "author", "revision", "phase", "identity", "transition-identity", "time"} {
		t.Run(mode, func(t *testing.T) {
			a, current := nativeUndoPlanFixture(t, "close-approved")
			session, actor, op := "ses_actor", "github-author", "td-op-inverse"
			at := time.Now().UTC().Add(time.Minute)
			switch mode {
			case "session":
				session = "ses_foreign"
			case "author":
				actor = "foreign-author"
			case "revision":
				current.nativeBody += "foreign change"
			case "phase":
				a.Phase = "undo_prepared"
				a.UndoOperationID = "td-op-original-uncertain"
			case "identity":
				op = a.OperationID
			case "transition-identity":
				_, meta, _, _ := decodeBody(a.After.Body)
				op = meta.Details.Transitions[len(meta.Details.Transitions)-1].OperationID
			case "time":
				at = time.Time{}
			}
			if plan, err := a.PlanNativeIssueUndo(current, session, actor, op, at); plan != nil || err == nil {
				t.Fatal("unsafe plan accepted", plan, err)
			}
		})
	}
}

func TestNativeUndoCompensationPlanRejectsForgedWriterEffects(t *testing.T) {
	for _, mode := range []string{"claim", "dependency", "review-author", "session-author", "old-history", "title", "reason", "delete", "same-native-state"} {
		t.Run(mode, func(t *testing.T) {
			a, current := nativeUndoPlanFixture(t, "approve-new")
			desc, m, _, _ := decodeBody(a.After.Body)
			switch mode {
			case "claim":
				m.Details.ImplementerSession = "ses_foreign"
			case "dependency":
				m.Details.Dependencies = []string{"gh-2"}
			case "review-author":
				m.Details.Reviews[len(m.Details.Reviews)-1].ReviewerSession = "ses_foreign"
			case "session-author":
				m.Details.Sessions[len(m.Details.Sessions)-1].SessionID = "ses_foreign"
			case "old-history":
				m.Details.Transitions = nil
			case "title":
				a.After.Title = "Foreign title"
			case "reason":
				m.LastStateReason = "Wrong reason"
			case "delete":
				m.Details.DeletedAt = &a.After.UpdatedAt
			case "same-native-state":
				a.Before.State = "closed"
			}
			a.After.Body, _ = encodeBody(desc, m)
			a.After.Revision, _ = a.After.checksum()
			current.nativeBody = a.After.Body
			current.Title = a.After.Title
			current.nativeState = a.After.State
			// Scope is checked before remote identity/revision validation.
			if _, ok, err := nativeUndoScope(a); err == nil && ok {
				t.Fatal("forged native effect accepted", mode)
			}
		})
	}
}
