package ghstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func prepareCommentUndoFixture(t *testing.T, kind string) *commentWriteFixture {
	t.Helper()
	f := newCommentWriteFixture(t)
	_, err := f.client.AppendActivity(context.Background(), "gh-1", models.Activity{Kind: kind, SessionID: "ses_actor", Message: "Saved", LogType: models.LogTypeProgress, Done: []string{"Saved"}})
	if err != nil {
		t.Fatal(err)
	}
	f.commentReads = 0
	return f
}

func TestCommentUndoPreservesAuditAndReplaysWithoutDelete(t *testing.T) {
	for _, kind := range []string{"comment", "log", "handoff"} {
		t.Run(kind, func(t *testing.T) {
			f := prepareCommentUndoFixture(t, kind)
			original, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || result.AlreadyUndone || len(f.native) != 0 || f.deletes != 1 || f.targetWrites != 0 || result.Journal.Phase != "undone" {
				t.Fatal(result, err, f)
			}
			if result.Journal.CommentAfter.Body != original.CommentAfter.Body || result.Journal.Restored == nil {
				t.Fatal("audit snapshot lost", result)
			}
			writes := f.receiptWrites
			result, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || !result.AlreadyUndone || f.deletes != 1 || f.receiptWrites != writes {
				t.Fatal("replay wrote", result, err)
			}
		})
	}
}

func TestCommentUndoRejectsActorSessionAndChangedObjects(t *testing.T) {
	for _, mode := range []string{"session", "undo-actor", "target", "comment-body", "comment-clock", "comment-author", "intent-denied"} {
		t.Run(mode, func(t *testing.T) {
			f := prepareCommentUndoFixture(t, "comment")
			session := "ses_actor"
			switch mode {
			case "session":
				session = "ses_other"
			case "undo-actor":
				f.journalWriteFixture.mode = mode
			case "target":
				issue := f.issues[1]
				issue.UpdatedAt = issue.UpdatedAt.Add(time.Second)
				f.issues[1] = issue
			case "comment-body":
				f.native[0].Body = "External edit"
			case "comment-clock":
				f.native[0].UpdatedAt = f.native[0].UpdatedAt.Add(time.Second)
			case "comment-author":
				f.native[0].User.Login = "other-author"
			case "intent-denied":
				f.journalWriteFixture.mode = "receipt-denied"
			}
			result, err := f.client.UndoAction(context.Background(), "ghc-101", session)
			if err == nil || result != nil || f.deletes != 0 || len(f.native) != 1 {
				t.Fatal("unsafe DELETE", result, err, f)
			}
		})
	}
}

func TestCommentUndoUncertaintyNeverRetriesOrInventsReceipt(t *testing.T) {
	for _, mode := range []string{"delete-denied", "delete-lost", "delete-noop", "delete-target-changed", "undo-receipt-denied", "undo-receipt-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f := prepareCommentUndoFixture(t, "comment")
			if strings.HasPrefix(mode, "undo-receipt-") {
				f.journalWriteFixture.mode = mode
			} else {
				f.mode = mode
			}
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err == nil || result != nil || f.deletes != 1 || !strings.Contains(err.Error(), "unverified") {
				t.Fatal(result, err, f)
			}
			if mode == "delete-denied" && !strings.Contains(err.Error(), "HTTP 403 delete denied") {
				t.Fatal(err)
			}
			journal, decodeErr := decodeActionJournal(*f.comment, "gh-9")
			if decodeErr != nil || (journal.Phase != "undo_prepared" && mode != "undo-receipt-uncertain") {
				t.Fatal(journal, decodeErr)
			}
			f.mode, f.journalWriteFixture.mode = "", ""
			result, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if f.deletes != 1 {
				t.Fatal("uncertain DELETE retried")
			}
			if mode == "undo-receipt-uncertain" {
				if err != nil || !result.AlreadyUndone {
					t.Fatal(result, err)
				}
			} else if err == nil {
				t.Fatal("unfinished operation reported success")
			}
		})
	}
}

func TestCommentUndoReplayDetectsReappearedNativeComment(t *testing.T) {
	f := prepareCommentUndoFixture(t, "comment")
	native := f.native[0]
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	f.native = append(f.native, native)
	if result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil || result != nil || f.deletes != 1 {
		t.Fatal(result, err)
	}
}
