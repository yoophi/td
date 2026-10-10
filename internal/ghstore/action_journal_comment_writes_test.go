package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

type commentWriteFixture struct {
	*journalWriteFixture
	native  []apiComment
	writes  int
	deletes int
	mode    string
}

func newCommentWriteFixture(t *testing.T) *commentWriteFixture {
	t.Helper()
	f := &commentWriteFixture{journalWriteFixture: newJournalWriteFixture(t, "")}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		method, path := args[4], strings.TrimPrefix(args[5], "repos/owner/repo")
		encode := func(v any) ([]byte, error) { return json.Marshal(v) }
		switch {
		case path == "/issues/1/comments" && method == "POST":
			f.writes++
			if f.mode == "denied" {
				return nil, errors.New("HTTP 403 native comment denied")
			}
			var input struct{ Body string }
			if err := json.Unmarshal(payload, &input); err != nil {
				return nil, err
			}
			clock := f.issues[1].UpdatedAt.Add(time.Second)
			comment := apiComment{ID: int64(201 + len(f.native)), Body: input.Body,
				IssueURL: "https://api.github.com/repos/owner/repo/issues/1", CreatedAt: clock, UpdatedAt: clock}
			comment.User.Login = "github-author"
			if f.mode == "membership" {
				comment.IssueURL = "https://api.github.com/repos/owner/repo/issues/2"
			}
			if f.mode == "author" {
				comment.User.Login = "other-author"
			}
			f.native = append(f.native, comment)
			issue := f.issues[1]
			issue.UpdatedAt = clock
			if f.mode == "target-changed" {
				issue.Title = "Concurrent title"
			}
			f.issues[1] = issue
			if f.mode == "lost" {
				return nil, errors.New("connection lost after native comment")
			}
			return encode(comment)
		case path == "/issues/comments?per_page=100":
			page := append([]apiComment{}, f.native...)
			if f.comment != nil {
				page = append(page, *f.comment)
			}
			if f.mode == "duplicate" && len(f.native) > 0 {
				duplicate := f.native[0]
				duplicate.ID++
				page = append(page, duplicate)
			}
			return encode([][]apiComment{page})
		case strings.HasPrefix(path, "/issues/comments/20") && method == "DELETE":
			f.deletes++
			if f.mode == "delete-denied" {
				return nil, errors.New("HTTP 403 delete denied")
			}
			if f.mode != "delete-noop" {
				f.native = nil
			}
			issue := f.issues[1]
			issue.UpdatedAt = issue.UpdatedAt.Add(time.Second)
			if f.mode == "delete-target-changed" {
				issue.Title = "Concurrent title"
			}
			f.issues[1] = issue
			if f.mode == "delete-lost" {
				return nil, errors.New("connection lost after DELETE")
			}
			return nil, nil
		case strings.HasPrefix(path, "/issues/comments/20") && method == "GET":
			if f.mode == "readback-denied" {
				return nil, errors.New("HTTP 403 comment readback denied")
			}
			comment := f.native[len(f.native)-1]
			if f.mode == "readback-changed" {
				comment.Body = "Edited comment"
			}
			return encode(comment)
		default:
			return base(ctx, dir, payload, args...)
		}
	}
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestJournaledActivityRecordsNativeResultAndRejectsDuplicateOperation(t *testing.T) {
	for _, kind := range []string{"comment", "log", "handoff"} {
		t.Run(kind, func(t *testing.T) {
			f := newCommentWriteFixture(t)
			input := models.Activity{Kind: kind, SessionID: "ses_actor", OperationID: "activity-shared-op", Message: "Saved", LogType: models.LogTypeProgress, Done: []string{"Saved"}}
			result, err := f.client.AppendActivity(context.Background(), "gh-1", input)
			if err != nil || result.ID != "ghc-201" || f.writes != 1 {
				t.Fatal(result, err, f.writes)
			}
			journal, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil || journal.Action != "comment_create" || journal.Phase != "applied" || journal.CommentAfter.ID != result.ID || journal.CommentAfter.Author != "github-author" || journal.CommentInput.OperationID != input.OperationID || journal.OperationID == input.OperationID || !journal.After.UpdatedAt.After(journal.Before.UpdatedAt) {
				t.Fatal(journal, err)
			}
			if _, err := f.client.AppendActivity(context.Background(), "gh-1", input); err == nil || f.writes != 1 {
				t.Fatal("duplicate deterministic activity POST", err, f.writes)
			}
		})
	}
}

func TestJournaledActivityFailuresKeepPreparedUncertaintyWithoutRetries(t *testing.T) {
	for _, mode := range []string{"denied", "lost", "author", "membership", "readback-denied", "readback-changed", "target-changed", "duplicate", "receipt-denied", "receipt-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommentWriteFixture(t)
			if strings.HasPrefix(mode, "receipt-") {
				f.journalWriteFixture.mode = mode
			} else {
				f.mode = mode
			}
			result, err := f.client.AppendActivity(context.Background(), "gh-1", models.Activity{Kind: "comment", SessionID: "ses_actor", OperationID: "activity-op", Message: "Saved"})
			if err == nil || result != nil || f.writes != 1 || !strings.Contains(err.Error(), "unverified") {
				t.Fatal("uncertain activity succeeded or retried", result, err, f.writes)
			}
			journal, decodeErr := decodeActionJournal(*f.comment, "gh-9")
			if decodeErr != nil || (journal.Phase != "prepared" && mode != "receipt-uncertain") || journal.Phase == "undone" {
				t.Fatal("false applied activity", journal, decodeErr)
			}
			if mode == "denied" && !strings.Contains(err.Error(), "HTTP 403 native comment denied") {
				t.Fatal("raw error lost", err)
			}
		})
	}
}

func TestJournaledActivityValidatesBeforeBootstrapOrNativeWrites(t *testing.T) {
	f := newCommentWriteFixture(t)
	for _, input := range []models.Activity{
		{Kind: "comment", SessionID: "ses_other", Message: "Wrong actor"},
		{Kind: "comment", SessionID: "ses_actor", Message: " "},
		{Kind: "comment", SessionID: "ses_actor", Message: actionJournalStart + "{}"},
	} {
		if _, err := f.client.AppendActivity(context.Background(), "gh-1", input); err == nil {
			t.Fatal("invalid activity accepted")
		}
	}
	if f.intentWrites != 0 || f.writes != 0 || f.client.journal.Carrier.ID != "" {
		t.Fatal("invalid activity bootstrapped or wrote", f)
	}
}

func TestJournaledActivityIntentFailuresAndStaleTargetPreventCommentPOST(t *testing.T) {
	for _, mode := range []string{"intent-denied", "intent-response", "intent-uncertain", "intent-read", "target-conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := newCommentWriteFixture(t)
			f.journalWriteFixture.mode = mode
			result, err := f.client.AppendActivity(context.Background(), "gh-1", models.Activity{Kind: "comment", SessionID: "ses_actor", Message: "Saved"})
			if err == nil || result != nil || f.writes != 0 || f.receiptWrites != 0 {
				t.Fatal("unverified intent or stale target allowed POST", result, err, f)
			}
			if mode == "target-conflict" && f.issues[1].Body != "Concurrent edit" {
				t.Fatal("external edit overwritten")
			}
		})
	}
}

func TestJournaledActivityPostReceiptTargetChangeIsReportedAndPreserved(t *testing.T) {
	f := newCommentWriteFixture(t)
	f.journalWriteFixture.mode = "target-later"
	result, err := f.client.AppendActivity(context.Background(), "gh-1", models.Activity{Kind: "comment", SessionID: "ses_actor", Message: "Saved"})
	if err == nil || result != nil || f.writes != 1 || f.issues[1].Body != "Later edit" {
		t.Fatal("post-receipt external edit hidden or overwritten", result, err, f)
	}
}
