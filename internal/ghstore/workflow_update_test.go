package ghstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func TestWorkflowUpdateCannotBypassReviewAndRetainsClaims(t *testing.T) {
	f := newReviewFixture(t)
	worker := TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeStrict}
	for _, status := range []models.Status{models.StatusInProgress, models.StatusBlocked, models.StatusOpen, models.StatusInProgress, models.StatusInReview} {
		r, err := f.client.UpdateWorkflow(context.Background(), "1", Changes{Status: &status}, worker)
		if err != nil || r.Status != status {
			t.Fatalf("status %s: %+v %v", status, r, err)
		}
		if status == models.StatusInProgress && r.ImplementerSession != worker.SessionID {
			t.Fatal("missing claim attribution")
		}
	}
	before := f.writes
	closed := models.StatusClosed
	if _, err := f.client.UpdateWorkflow(context.Background(), "1", Changes{Status: &closed}, worker); err == nil || f.writes != before {
		t.Fatalf("self approval through update: %v", err)
	}
	reviewer := TransitionOptions{SessionID: "fixture-reviewer", Mode: reviewpolicy.ModeStrict}
	r, err := f.client.UpdateWorkflow(context.Background(), "1", Changes{Status: &closed}, reviewer)
	if err != nil || r.Status != closed || r.ReviewerSession != reviewer.SessionID {
		t.Fatalf("approval: %+v %v", r, err)
	}
	open := models.StatusOpen
	r, err = f.client.UpdateWorkflow(context.Background(), "1", Changes{Status: &open}, worker)
	if err != nil || r.Status != open || r.ReviewerSession != "" || r.ImplementerSession != "" {
		t.Fatalf("reopen: %+v %v", r, err)
	}
}

func TestWorkflowUpdateEditsInvalidateOldReviewAndReportPartialResult(t *testing.T) {
	f := newReviewFixture(t)
	f.transition(t, "review", TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeStrict})
	title := "Changed after review submission"
	closed := models.StatusClosed
	before := f.writes
	_, err := f.client.UpdateWorkflow(context.Background(), "1", Changes{Title: &title, Status: &closed}, TransitionOptions{SessionID: "fixture-reviewer", Mode: reviewpolicy.ModeStrict})
	if err == nil || !strings.Contains(err.Error(), "field changes were saved") || !strings.Contains(err.Error(), "review is stale") || f.writes != before+1 {
		t.Fatalf("writes=%d %v", f.writes, err)
	}
	r, err := f.client.Get(context.Background(), "1")
	if err != nil || r.Title != title || r.Status != models.StatusInReview || r.ReviewerSession != "" {
		t.Fatalf("partial state: %+v %v", r, err)
	}
}

func TestWorkflowUpdateRejectsInvalidTransitionsBeforeFieldWrites(t *testing.T) {
	f := newReviewFixture(t)
	f.nativeState("closed")
	status := models.StatusInProgress
	title := "Should not be saved"
	_, err := f.client.UpdateWorkflow(context.Background(), "1", Changes{Title: &title, Status: &status}, TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted})
	if err == nil || f.writes != 0 || f.issue["title"] == title {
		t.Fatalf("invalid transition changed issue: %v", err)
	}
}

func TestWorkflowUpdateCombinesEditsWithNewReviewBasis(t *testing.T) {
	f := newReviewFixture(t)
	status := models.StatusInReview
	title := "New content submitted for review"
	worker := TransitionOptions{SessionID: "fixture-worker", Mode: reviewpolicy.ModeTrusted}
	r, err := f.client.UpdateWorkflow(context.Background(), "1", Changes{Title: &title, Status: &status}, worker)
	if err != nil || r.Status != status || r.Title != title {
		t.Fatalf("%+v %v", r, err)
	}
	worker.SelfReview = true
	worker.Reason = "Honest test fixture self-review"
	r = f.transition(t, "approve", worker)
	if r.Status != models.StatusClosed {
		t.Fatal("new basis was not reviewable")
	}
}

func TestWorkflowUpdateObservedRejectsStaleFormBeforeAnyWrite(t *testing.T) {
	f := newReviewFixture(t)
	original, err := f.client.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	f.issue["title"] = "Peer edit"
	title := "My form edit"
	target := models.StatusInProgress
	var conflict *ConflictError
	_, err = f.client.UpdateWorkflowObserved(context.Background(), original, Changes{Title: &title, Status: &target}, TransitionOptions{SessionID: "actual", Mode: reviewpolicy.ModeTrusted})
	if !errors.As(err, &conflict) || f.writes != 0 || f.posts != 0 || f.issue["title"] != "Peer edit" {
		t.Fatalf("stale form wrote: %v", err)
	}
}

func TestWorkflowUpdateObservedRetainsWrittenRevisionForStatus(t *testing.T) {
	f := newReviewFixture(t)
	observed, err := f.client.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	run := f.client.run
	readsAfterEdit := 0
	f.client.run = func(ctx context.Context, stdin string, payload []byte, args ...string) ([]byte, error) {
		if f.writes == 1 && slices.Contains(args, "repos/owner/repo/issues/1") && !slices.Contains(args, "PATCH") {
			readsAfterEdit++
			if readsAfterEdit == 2 {
				f.issue["title"] = "Peer changed after field save"
			}
		}
		return run(ctx, stdin, payload, args...)
	}
	title := "Saved field edit"
	target := models.StatusInProgress
	_, err = f.client.UpdateWorkflowObserved(context.Background(), observed, Changes{Title: &title, Status: &target}, TransitionOptions{SessionID: "actual", Mode: reviewpolicy.ModeTrusted})
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !strings.Contains(err.Error(), "field changes were saved") || f.writes != 1 || f.posts != 0 {
		t.Fatalf("status adopted a newer revision: writes=%d %v", f.writes, err)
	}
	current, readErr := f.client.Get(context.Background(), "gh-1")
	if readErr != nil || current.Status != models.StatusOpen || current.Title != "Peer changed after field save" {
		t.Fatalf("partial result %+v %v", current, readErr)
	}
}

func TestWorkflowUpdateObservedSuccessfulEditAndUntrustedObservation(t *testing.T) {
	f := newReviewFixture(t)
	observed, err := f.client.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	title := "Form edited title"
	target := models.StatusInProgress
	options := TransitionOptions{SessionID: "actual-monitor", Mode: reviewpolicy.ModeTrusted}
	current, err := f.client.UpdateWorkflowObserved(context.Background(), observed, Changes{Title: &title, Status: &target}, options)
	if err != nil || current.Title != title || current.Status != target || current.ImplementerSession != options.SessionID {
		t.Fatalf("%+v %v", current, err)
	}
	before := f.writes
	for _, invalid := range []*Record{nil, {Issue: models.Issue{ID: "gh-1"}}} {
		if _, err := f.client.UpdateWorkflowObserved(context.Background(), invalid, Changes{Title: &title}, options); err == nil || f.writes != before {
			t.Fatal("untrusted observation accepted")
		}
	}
}
