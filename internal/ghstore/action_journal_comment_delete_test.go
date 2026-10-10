package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func deletedCommentFixture(t *testing.T, mode string) *commentWriteFixture {
	t.Helper()
	f := newCommentWriteFixture(t)
	clock := f.issues[1].UpdatedAt
	original := apiComment{ID: 201, IssueURL: "https://api.github.com/repos/owner/repo/issues/1",
		Body: "Original raw comment\nsecond line", CreatedAt: clock.Add(-time.Minute), UpdatedAt: clock}
	original.User.Login = "other-author"
	f.native = []apiComment{original}
	if strings.HasPrefix(mode, "intent-") || strings.HasPrefix(mode, "receipt-") || mode == "target-conflict" || mode == "target-later" {
		f.journalWriteFixture.mode = mode
	} else {
		f.mode = mode
	}
	base := f.client.run
	reads := 0
	postDeletePointReads := 0
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		method, path := args[4], strings.TrimPrefix(args[5], "repos/owner/repo")
		if path == "/issues/1" && method == "GET" && f.deletes > 0 {
			postDeletePointReads++
		}
		if path == "/issues/1" && method == "GET" && mode == "late-delete-clock" && postDeletePointReads == 3 {
			issue := f.issues[1]
			issue.UpdatedAt = issue.UpdatedAt.Add(time.Second)
			f.issues[1] = issue
		}
		if path == "/issues/1" && method == "GET" && ((mode == "delayed-delete-clock" && postDeletePointReads == 2) || (mode == "persistent-delete-clock" && postDeletePointReads >= 2)) {
			issue := f.issues[1]
			issue.UpdatedAt = issue.UpdatedAt.Add(-time.Second)
			return json.Marshal(issue)
		}
		if path == "/issues/1/comments?per_page=100" {
			if f.deletes > 0 && mode == "verification-error" {
				return nil, errors.New("HTTP 403 deletion verification denied")
			}
			return json.Marshal([][]apiComment{f.native})
		}
		if path == "/issues/comments/201" && method == "GET" {
			reads++
			if mode == "edited" && reads == 2 {
				f.native[0].Body = "External edit"
			}
		}
		if path == "/issues/comments?per_page=100" && f.deletes > 0 && mode == "snapshot-error" {
			return nil, errors.New("HTTP 403 fresh collection denied")
		}
		return base(ctx, dir, payload, args...)
	}
	return f
}

func TestCommentDeletionJournalPreservesOriginalAndRejectsFakeRestore(t *testing.T) {
	f := deletedCommentFixture(t, "")
	original := f.native[0]
	if err := f.client.DeleteComment(context.Background(), "gh-1", "ghc-201"); err != nil {
		t.Fatal(err)
	}
	journal, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || journal == nil {
		t.Fatal(journal, err)
	}
	if journal.Action != "comment_delete" || journal.Phase != "applied" || journal.CommentBefore.Body != original.Body || journal.CommentBefore.Author != "other-author" || journal.CommentBefore.ID != "ghc-201" || !journal.CommentBefore.CreatedAt.Equal(original.CreatedAt) || !journal.CommentBefore.UpdatedAt.Equal(original.UpdatedAt) {
		t.Fatalf("original deleted comment lost: %+v", journal)
	}
	if !strings.Contains(f.comment.Body, actionDeletedCommentJournalStart) || f.deletes != 1 || f.intentWrites != 1 || f.receiptWrites != 1 {
		t.Fatal("unexpected version or write counts", f)
	}
	beforeReceipt := *f.comment
	if err := f.client.DeleteComment(context.Background(), "gh-1", "ghc-201"); err == nil || f.deletes != 1 || f.intentWrites != 1 || *f.comment != beforeReceipt {
		t.Fatal("repeat deletion replayed or lost original receipt", err)
	}
	result, err := f.client.UndoAction(context.Background(), journal.ID, "ses_actor")
	var scope *UndoScopeError
	if result != nil || !errors.As(err, &scope) || !strings.Contains(err.Error(), "original ID") || f.deletes != 1 || f.writes != 0 || f.receiptWrites != 1 {
		t.Fatal("irreversible deletion claimed undo or lost alternative", result, err)
	}
}

func TestCommentDeletionJournalFailuresDoNotReplayOrClaimSuccess(t *testing.T) {
	for _, mode := range []string{"intent-denied", "intent-response", "intent-uncertain", "intent-read", "target-conflict", "edited", "delete-denied", "delete-lost", "delete-noop", "delete-target-changed", "verification-error", "snapshot-error", "receipt-denied", "receipt-uncertain", "target-later"} {
		t.Run(mode, func(t *testing.T) {
			f := deletedCommentFixture(t, mode)
			err := f.client.DeleteComment(context.Background(), "gh-1", "ghc-201")
			if err == nil {
				t.Fatal("unverified deletion succeeded")
			}
			want := 1
			if strings.HasPrefix(mode, "intent-") || mode == "target-conflict" || mode == "edited" {
				want = 0
			}
			if f.deletes != want || f.intentWrites != 1 {
				t.Fatal("unexpected DELETE retry or intent count", f.deletes, f.intentWrites, err)
			}
			if want == 1 && (!strings.Contains(err.Error(), "unverified") || !strings.Contains(err.Error(), "ghc-101")) {
				t.Fatal("recovery receipt identity missing", err)
			}
			if mode == "delete-denied" && !strings.Contains(err.Error(), "HTTP 403 delete denied") {
				t.Fatal("original permission error lost", err)
			}
			if f.comment != nil && mode != "intent-response" {
				journal, decodeErr := decodeActionJournal(*f.comment, "gh-9")
				if decodeErr != nil || journal.Phase == "undone" {
					t.Fatal("invalid or falsely restored history", journal, decodeErr)
				}
				if journal.Phase != "prepared" && mode != "receipt-uncertain" && mode != "target-later" {
					t.Fatal("unverified deletion was marked applied", journal)
				}
			}
		})
	}
}

func TestDeletedCommentJournalRejectsVersionMismatchAndForgedState(t *testing.T) {
	f := deletedCommentFixture(t, "")
	if err := f.client.DeleteComment(context.Background(), "gh-1", "ghc-201"); err != nil {
		t.Fatal(err)
	}
	record, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{actionJournalStart, actionCommentJournalStart, actionEntityJournalStart} {
		comment := *f.comment
		comment.Body = strings.Replace(comment.Body, actionDeletedCommentJournalStart, version, 1)
		if _, err := decodeActionJournal(comment, "gh-9"); err == nil {
			t.Fatal("deletion accepted under older schema", version)
		}
	}
	for _, mode := range []string{"missing-original", "foreign-issue", "log", "handoff", "fake-result", "fake-undo", "original-on-update"} {
		t.Run(mode, func(t *testing.T) {
			a := record.ActionJournal
			original := *a.CommentBefore
			a.CommentBefore = &original
			switch mode {
			case "missing-original":
				a.CommentBefore = nil
			case "foreign-issue":
				original.IssueID = "gh-2"
			case "log":
				original.Body, _ = renderActivity(activityData{Kind: "log", OperationID: "log-op", SessionID: "ses_actor", Message: "Audit", LogType: "progress"})
			case "handoff":
				original.Body, _ = renderActivity(activityData{Kind: "handoff", OperationID: "handoff-op", SessionID: "ses_actor", Done: []string{"Audit"}})
			case "fake-result":
				a.Phase = "prepared"
			case "fake-undo":
				a.Phase, a.UndoOperationID, a.Restored = "undone", "op-undo", a.After
			case "original-on-update":
				a.Action = "update"
			}
			original.Revision, _ = original.checksum()
			if _, err := renderActionJournal(a); err == nil {
				t.Fatal("forged deleted comment state accepted")
			}
		})
	}
}

func TestCommentDeletionObservesConvergingClockWithoutRepeatingDelete(t *testing.T) {
	f := deletedCommentFixture(t, "delayed-delete-clock")
	if err := f.client.DeleteComment(context.Background(), "gh-1", "ghc-201"); err != nil {
		t.Fatalf("own deletion clock propagation prevented verified success: %v", err)
	}
	journal, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || journal.Phase != "applied" || f.deletes != 1 || f.intentWrites != 1 || f.receiptWrites != 1 {
		t.Fatal("clock observation replayed mutation or lost verified receipt", journal, err, f.deletes)
	}
}

func TestCommentDeletionClockObservationIsBoundedAndKeepsPreparedOutcome(t *testing.T) {
	f := deletedCommentFixture(t, "persistent-delete-clock")
	err := f.client.DeleteComment(context.Background(), "gh-1", "ghc-201")
	var lag *commentDeletionClockLagError
	if !errors.As(err, &lag) || f.deletes != 1 || f.intentWrites != 1 || f.receiptWrites != 0 {
		t.Fatal("persistent clock mismatch succeeded or replayed writes", err, f.deletes)
	}
	journal, decodeErr := decodeActionJournal(*f.comment, "gh-9")
	if decodeErr != nil || journal.Phase != "prepared" {
		t.Fatal("unverified outcome became applied", journal, decodeErr)
	}
}

func TestCommentDeletionClockWaitHonorsCancellationWithoutReplay(t *testing.T) {
	f := deletedCommentFixture(t, "delayed-delete-clock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if f.deletes > 0 && strings.Contains(args[5], "/issues?state=all&per_page=100") {
			cancel()
		}
		return data, err
	}
	err := f.client.DeleteComment(ctx, "gh-1", "ghc-201")
	if !errors.Is(err, context.Canceled) || f.deletes != 1 || f.intentWrites != 1 || f.receiptWrites != 0 {
		t.Fatal("cancellation ignored or unverified outcome saved", err, f.deletes)
	}
}

func TestCommentDeletionSettlesClockBeforePublishingAppliedReceipt(t *testing.T) {
	f := deletedCommentFixture(t, "late-delete-clock")
	if err := f.client.DeleteComment(context.Background(), "gh-1", "ghc-201"); err != nil {
		t.Fatalf("late own DELETE clock prevented verified receipt: %v", err)
	}
	journal, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || journal.Phase != "applied" || !journal.After.UpdatedAt.Equal(f.issues[1].UpdatedAt) || f.deletes != 1 || f.intentWrites != 1 || f.receiptWrites != 1 {
		t.Fatal("receipt published before clock stabilized or mutation replayed", journal, err)
	}
}
