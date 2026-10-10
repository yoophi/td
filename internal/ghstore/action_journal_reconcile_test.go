package ghstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReconcileCreationPreviewApplyAndRepeatNeverReplaysPost(t *testing.T) {
	f := newJournalWriteFixture(t, "create-uncertain")
	f.client.stateLabels = fixtureStateLabels()
	if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.createWithOperationID(context.Background(), journalCreationFixtureIssue(), "op-create"); err == nil {
		t.Fatal("uncertain POST accepted")
	}
	f.mode = ""
	before := *f.comment
	preview, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", false)
	if err != nil || preview.Applied || preview.TargetWriteReplayed || preview.Issue.ID != "gh-2" || *f.comment != before || f.receiptWrites != 0 {
		t.Fatal("preview wrote or lost result", preview, err)
	}
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.Applied || result.TargetWriteReplayed || result.Journal.Phase != "applied" || result.Journal.SessionID != "ses_actor" || result.Journal.Reconciliation.SessionID != "ses_recovery" || result.Journal.Reconciliation.Actor != "github-author" || f.creationWrites != 1 || f.intentWrites != 1 || f.receiptWrites != 1 {
		t.Fatal("recovery replayed or lost attribution", result, err)
	}
	if !strings.Contains(f.comment.Body, actionReconciledJournalStart) {
		t.Fatal("recovery lacks versioned schema")
	}
	result, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.AlreadyReconciled || result.Applied || f.receiptWrites != 1 || f.creationWrites != 1 {
		t.Fatal("repeat recovery wrote", result, err)
	}
}

func TestReconcileCommentDeletionPreservesOriginalAndDoesNotReplayDelete(t *testing.T) {
	f := deletedCommentFixture(t, "delete-lost")
	if err := f.client.DeleteComment(context.Background(), "gh-1", "ghc-201"); err == nil {
		t.Fatal("uncertain DELETE accepted")
	}
	f.mode = ""
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.Applied || result.Journal.CommentBefore.Body != "Original raw comment\nsecond line" || result.Journal.CommentBefore.Author != "other-author" || result.Journal.SessionID != "ses_actor" || f.deletes != 1 || f.intentWrites != 1 || f.receiptWrites != 1 {
		t.Fatal("deletion recovery lost original or replayed", result, err)
	}
	result, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.AlreadyReconciled || f.deletes != 1 || f.receiptWrites != 1 {
		t.Fatal("repeat deletion recovery wrote", result, err)
	}
}

func TestReconcileCreationRejectsUnobservedChangedDuplicateOrForeignResult(t *testing.T) {
	for _, mode := range []string{"absent", "changed", "duplicate", "foreign-actor", "foreign-creator", "receipt-denied", "receipt-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f := newJournalWriteFixture(t, "create-uncertain")
			f.client.stateLabels = fixtureStateLabels()
			if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.client.createWithOperationID(context.Background(), journalCreationFixtureIssue(), "op-create"); err == nil {
				t.Fatal("uncertain POST accepted")
			}
			f.mode = ""
			switch mode {
			case "absent":
				delete(f.issues, 2)
			case "changed":
				issue := f.issues[2]
				issue.Title = "External edit"
				f.issues[2] = issue
			case "duplicate":
				issue := f.issues[2]
				issue.Number = 3
				f.issues[3] = issue
			case "foreign-actor":
				f.mode = "undo-actor"
			case "foreign-creator":
				issue := f.issues[2]
				issue.User.Login = "other-author"
				f.issues[2] = issue
			default:
				f.mode = mode
			}
			result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
			if result != nil || err == nil || f.creationWrites != 1 || f.intentWrites != 1 {
				t.Fatal("bad recovery succeeded or replayed", result, err)
			}
			want := 0
			if strings.HasPrefix(mode, "receipt-") {
				want = 1
			}
			if f.receiptWrites != want {
				t.Fatal("unexpected receipt writes", f.receiptWrites, err)
			}
			if mode == "receipt-uncertain" {
				f.mode = ""
				result, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
				if err != nil || !result.AlreadyReconciled || f.receiptWrites != 1 {
					t.Fatal("lost receipt response caused rewrite", result, err)
				}
			}
		})
	}
}

func TestReconcileDeletionRejectsStillPresentOrChangedTargetWithoutWrites(t *testing.T) {
	for _, mode := range []string{"present", "changed"} {
		f := deletedCommentFixture(t, "delete-lost")
		original := f.native[0]
		if err := f.client.DeleteComment(context.Background(), "gh-1", "ghc-201"); err == nil {
			t.Fatal("uncertain DELETE accepted")
		}
		f.mode = ""
		if mode == "present" {
			f.native = append(f.native, original)
		} else {
			issue := f.issues[1]
			issue.Title = "External edit"
			f.issues[1] = issue
		}
		if result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true); result != nil || err == nil || f.deletes != 1 || f.receiptWrites != 0 {
			t.Fatal("deletion recovery ignored later state or replayed", result, err)
		}
	}
}

func TestReconciliationCannotMasqueradeAsLegacyOrReplaceAttribution(t *testing.T) {
	f := deletedCommentFixture(t, "delete-lost")
	if err := f.client.DeleteComment(context.Background(), "gh-1", "ghc-201"); err == nil {
		t.Fatal("uncertain DELETE accepted")
	}
	f.mode = ""
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{actionJournalStart, actionDeletedCommentJournalStart} {
		comment := *f.comment
		comment.Body = strings.Replace(comment.Body, actionReconciledJournalStart, header, 1)
		if _, err := decodeActionJournal(comment, "gh-9"); err == nil {
			t.Fatal("recovery accepted under older schema")
		}
	}
	for _, mode := range []string{"actor", "session", "state"} {
		a := result.Journal.ActionJournal
		recovery := *a.Reconciliation
		a.Reconciliation = &recovery
		switch mode {
		case "actor":
			recovery.Actor = "forged-actor"
		case "session":
			recovery.SessionID = "invalid/session"
		case "state":
			state := *recovery.State
			state.Title = "Forged result"
			state.Revision, _ = state.checksum()
			recovery.State = &state
		}
		body, err := renderActionJournal(a)
		if err == nil {
			comment := *f.comment
			comment.Body = body
			_, err = decodeActionJournal(comment, "gh-9")
		}
		if err == nil {
			t.Fatal("forged recovery accepted", mode)
		}
	}
	issue := f.issues[1]
	issue.UpdatedAt = issue.UpdatedAt.Add(time.Second)
	f.issues[1] = issue
	if _, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true); err == nil || f.receiptWrites != 1 || f.deletes != 1 {
		t.Fatal("recovered result overwritten after later clock change", err)
	}
}
