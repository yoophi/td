package ghstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

type multipartWriteFixture struct {
	*journalWriteFixture
	parts             map[int64]apiComment
	partWrites        int
	targetFenceChecks int
	mode              string
}

func multipartText(seed int64) string {
	data := make([]byte, 28000)
	_, _ = rand.New(rand.NewSource(seed)).Read(data)
	return base64.StdEncoding.EncodeToString(data)
}

func newMultipartWriteFixture(t *testing.T, mode string) *multipartWriteFixture {
	t.Helper()
	base := newJournalWriteFixture(t, "")
	f := &multipartWriteFixture{journalWriteFixture: base, parts: map[int64]apiComment{}, mode: mode}
	before := base.issues[1]
	before.Body = multipartText(67)
	base.issues[1] = before
	run := base.client.run
	base.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		method, path := args[4], strings.TrimPrefix(args[5], "repos/owner/repo")
		if method == "PATCH" && path == "/issues/1" {
			if base.comment == nil {
				return nil, fmt.Errorf("target mutation preceded intent publication")
			}
			comments := []apiComment{*base.comment}
			for _, part := range f.parts {
				comments = append(comments, part)
			}
			journal, err := decodeMultipartActionJournal(*base.comment, "gh-9", comments)
			if err != nil || journal == nil || (journal.Phase != "prepared" && journal.Phase != "undo_prepared") {
				return nil, fmt.Errorf("target mutation preceded complete verified intent: %v", err)
			}
			f.targetFenceChecks++
		}
		if path == "/issues/comments?per_page=100" || path == "/issues/9/comments?per_page=100" {
			page := []apiComment{}
			for _, part := range f.parts {
				page = append(page, part)
			}
			if base.comment != nil {
				page = append(page, *base.comment)
			}
			if f.mode == "part-readback-edited" && len(page) > 0 {
				page[0].UpdatedAt = page[0].UpdatedAt.Add(time.Second)
			}
			return json.Marshal([][]apiComment{page})
		}
		if method == "POST" && path == "/issues/9/comments" {
			var input struct{ Body string }
			if err := json.Unmarshal(payload, &input); err != nil {
				return nil, err
			}
			if len(input.Body) > actionCommentBudget {
				return nil, fmt.Errorf("HTTP 422 comment exceeds budget")
			}
			if strings.Contains(input.Body, actionJournalPartPrefix) {
				f.partWrites++
				if f.mode == "part-denied" {
					return nil, errors.New("HTTP 403 part denied (original diagnostic)")
				}
				clock := base.issues[9].UpdatedAt
				part := apiComment{ID: int64(400 + f.partWrites), Body: input.Body, IssueURL: "https://api.github.com/repos/owner/repo/issues/9", CreatedAt: clock, UpdatedAt: clock}
				part.User.Login = "github-author"
				if f.mode == "part-other-author" {
					part.User.Login = "other-author"
				}
				f.parts[part.ID] = part
				if f.mode == "target-conflict" {
					changed := base.issues[1]
					changed.Title = "Concurrent edit"
					changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
					base.issues[1] = changed
				}
				if f.mode == "root-concurrent" && base.comment != nil {
					base.comment.Body = "Concurrent journal edit"
				}
				if f.mode == "part-uncertain" {
					return nil, errors.New("connection lost after part POST")
				}
				return json.Marshal(part)
			}
			if f.mode == "manifest-denied" {
				return nil, errors.New("HTTP 403 manifest denied")
			}
		}
		return run(ctx, dir, payload, args...)
	}
	if err := base.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestMultipartJournalUpdateAndUndo(t *testing.T) {
	f := newMultipartWriteFixture(t, "")
	before := f.issues[1]
	description := multipartText(68)
	result, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &description})
	if err != nil {
		t.Fatalf("large valid journal must publish before target update: %v", err)
	}
	if result.Description != description || f.targetWrites != 1 || f.partWrites < 2 || !strings.Contains(f.comment.Body, actionMultipartJournalStart) {
		t.Fatal("multipart update incomplete")
	}
	history, err := f.client.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := history.ActionHistory("")
	if err != nil || len(entries) != 1 || entries[0].Phase != "applied" {
		t.Fatal("multipart action is not readable", err)
	}
	undo, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err != nil || undo.Journal.Phase != "undone" || f.targetWrites != 2 || f.targetFenceChecks != 2 || f.issues[1].Body != before.Body {
		t.Fatal("multipart inverse failed", err)
	}
	state, receipts, parts := f.issues[1], f.receiptWrites, f.partWrites
	undo, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err != nil || !undo.AlreadyUndone || !reflect.DeepEqual(state, f.issues[1]) || receipts != f.receiptWrites || parts != f.partWrites {
		t.Fatal("repeat undo wrote again", err)
	}
}

func TestMultipartUploadFailuresNeverMutateTarget(t *testing.T) {
	for _, mode := range []string{"part-denied", "part-uncertain", "part-other-author", "part-readback-edited", "manifest-denied", "target-conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := newMultipartWriteFixture(t, mode)
			description := multipartText(68)
			_, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &description})
			if err == nil || f.targetWrites != 0 || f.targetFenceChecks != 0 {
				t.Fatal("upload failure allowed target mutation", err)
			}
			if mode != "target-conflict" && f.comment != nil {
				t.Fatal("unverified parts published an intent")
			}
			if mode == "part-uncertain" && (f.partWrites != 1 || len(f.parts) != 1) {
				t.Fatal("uncertain part POST was retried")
			}
			if mode == "part-denied" && !strings.Contains(err.Error(), "HTTP 403 part denied (original diagnostic)") {
				t.Fatal("original error lost", err)
			}
		})
	}
}

func TestMultipartPublicationExplicitRecoveryReusesVerifiedPartialUpload(t *testing.T) {
	f := newMultipartWriteFixture(t, "part-uncertain")
	a := multipartJournalFixture(t)
	a.CarrierID = "gh-9"
	a.Phase = "prepared"
	a.After = nil
	plan, err := planActionJournalPublication(a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.client.publishJournal(context.Background(), "/issues/9/comments", plan, nil, nil)
	if err == nil || f.partWrites != 1 || f.comment != nil || f.targetWrites != 0 {
		t.Fatal("uncertain upload not retained", err)
	}
	f.mode = ""
	root, err := f.client.publishJournal(context.Background(), "/issues/9/comments", plan, nil, nil)
	if err != nil || root.Phase != "prepared" || f.partWrites != len(plan.partBodies) || f.targetWrites != 0 {
		t.Fatal("explicit upload recovery replayed a part or target", err)
	}
	writes := f.intentWrites
	if _, err := f.client.publishJournal(context.Background(), "/issues/9/comments", plan, nil, nil); err == nil || f.intentWrites != writes || f.partWrites != len(plan.partBodies) {
		t.Fatal("existing manifest was POSTed again", err)
	}
}

func TestMultipartForwardRecoveryDoesNotReplayTarget(t *testing.T) {
	f := newMultipartWriteFixture(t, "")
	f.journalWriteFixture.mode = "receipt-denied"
	description := multipartText(68)
	_, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &description})
	if err == nil || f.targetWrites != 1 {
		t.Fatal("missing completion failure", err)
	}
	before := f.issues[1]
	f.journalWriteFixture.mode = ""
	recovered, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !recovered.Applied || f.targetWrites != 1 || !reflect.DeepEqual(before, f.issues[1]) || recovered.ForwardOutcome.TargetWriteVerified {
		t.Fatal("multipart recovery claimed or replayed target write", err)
	}
	parts, receipts, root := f.partWrites, f.receiptWrites, *f.comment
	recovered, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !recovered.AlreadyReconciled || f.partWrites != parts || f.receiptWrites != receipts || !reflect.DeepEqual(root, *f.comment) {
		t.Fatal("completed recovery was not read-only", err)
	}
}

func TestMultipartPublicationPreservesConcurrentRootChange(t *testing.T) {
	f := newMultipartWriteFixture(t, "root-concurrent")
	description := multipartText(68)
	_, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &description})
	if err == nil || f.targetWrites != 1 || f.comment.Body != "Concurrent journal edit" || f.receiptWrites != 0 {
		t.Fatal("multipart completion overwrote changed original intent", err)
	}
}

func TestMultipartStorageMarkerCannotBePostedAsActivity(t *testing.T) {
	f := newMultipartWriteFixture(t, "")
	_, err := f.client.AppendActivity(context.Background(), "gh-1", models.Activity{Kind: "comment", Message: actionJournalPartStart + "fake", SessionID: "ses_actor"})
	if err == nil || f.intentWrites != 0 || f.partWrites != 0 || f.targetWrites != 0 {
		t.Fatal("reserved part marker reached native publication", err)
	}
}

func TestMultipartInversePreparationFailureDoesNotMutateTarget(t *testing.T) {
	f := newMultipartWriteFixture(t, "")
	description := multipartText(68)
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &description}); err != nil {
		t.Fatal(err)
	}
	root, target, receipts := *f.comment, f.issues[1], f.receiptWrites
	f.mode = "part-denied"
	_, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err == nil || f.targetWrites != 1 || f.receiptWrites != receipts || !reflect.DeepEqual(root, *f.comment) || !reflect.DeepEqual(target, f.issues[1]) {
		t.Fatal("incomplete inverse archive authorized a target write", err)
	}
}

func TestMultipartInverseRecoveryDoesNotReplayTarget(t *testing.T) {
	f := newMultipartWriteFixture(t, "")
	description := multipartText(68)
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &description}); err != nil {
		t.Fatal(err)
	}
	f.journalWriteFixture.mode = "undo-receipt-denied"
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err == nil || f.targetWrites != 2 {
		t.Fatal("missing inverse completion failure", err)
	}
	target := f.issues[1]
	f.journalWriteFixture.mode = ""
	result, err := f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.Applied || result.Journal.Phase != "undone" || f.targetWrites != 2 || !reflect.DeepEqual(target, f.issues[1]) {
		t.Fatal("inverse recovery replayed target", err)
	}
	parts, receipts, root := f.partWrites, f.receiptWrites, *f.comment
	result, err = f.client.ReconcileAction(context.Background(), "ghc-101", "ses_recovery", true)
	if err != nil || !result.AlreadyReconciled || parts != f.partWrites || receipts != f.receiptWrites || !reflect.DeepEqual(root, *f.comment) {
		t.Fatal("completed inverse recovery was not read-only", err)
	}
}

func TestMultipartPublicationRepeatsGuardAfterUpload(t *testing.T) {
	f := newMultipartWriteFixture(t, "")
	description := multipartText(68)
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &description}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.client.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err != nil {
		t.Fatal(err)
	}
	history, err := snapshot.ActionHistory("")
	if err != nil || len(history) != 1 {
		t.Fatal(err)
	}
	old := history[0]
	next := old.ActionJournal
	next.Phase = "undo_prepared"
	next.UndoOperationID = "td-op-next"
	plan, err := planActionJournalPublication(next)
	if err != nil {
		t.Fatal(err)
	}
	parts, root, receipts := f.partWrites, *f.comment, f.receiptWrites
	cause := errors.New("watched graph changed while parts uploaded")
	_, err = f.client.publishJournal(context.Background(), "/issues/comments/101", plan, &old, func() error {
		if f.partWrites <= parts {
			t.Fatal("guard did not run after upload")
		}
		return cause
	})
	if !errors.Is(err, cause) || receipts != f.receiptWrites || !reflect.DeepEqual(root, *f.comment) || f.targetWrites != 1 {
		t.Fatal("post-upload guard did not fence manifest rewrite", err)
	}
}
