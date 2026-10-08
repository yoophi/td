package ghstore

import (
	"context"
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
