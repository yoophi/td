package ghstore

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func TestLinkedFileChangePreservesDetailsAndRevokesApproval(t *testing.T) {
	f := newReviewFixture(t)
	o := TransitionOptions{SessionID: "synthetic-worker", Mode: reviewpolicy.ModeTrusted, Minor: true}
	f.transition(t, "start", o)
	f.transition(t, "review", o)
	// Use a non-minor fixture review for the record-only approval.
	observed, err := f.client.Get(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	details, _ := observed.CopyDetails()
	details.Minor = false
	if _, err = f.client.UpdateObserved(context.Background(), observed, Changes{Details: &details}); err != nil {
		t.Fatal(err)
	}
	// Minor/content change invalidates the old review basis, so create a new cycle.
	f.transition(t, "start", o)
	o.Minor = false
	f.transition(t, "review", o)
	o.RecordOnly = true
	o.SelfReview = true
	o.Reason = "Synthetic test fixture review"
	f.transition(t, "approve", o)
	observed, err = f.client.Get(context.Background(), "1")
	if err != nil {
		t.Fatal(err)
	}
	files := []models.IssueFile{{ID: "file-fixture", IssueID: "gh-1", FilePath: "src/main.go", Role: models.FileRoleImplementation, LinkedSHA: "abc", LinkedAt: time.Now().UTC()}}
	result, noop, err := f.client.ReplaceLinkedFilesObserved(context.Background(), observed, files)
	if err != nil || noop || result.Details.Files[0].FilePath != "src/main.go" {
		t.Fatal(result, noop, err)
	}
	if result.Status != models.StatusInReview || result.ImplementerSession != observed.ImplementerSession || len(result.Details.Sessions) != len(observed.Details.Sessions) {
		t.Fatal("lost workflow/participation details")
	}
	if result.ReviewerSession != "" || result.ReviewedAt != nil || result.Details.ReviewBasis != "" || result.Details.Reviews[len(result.Details.Reviews)-1].SupersededAt == nil {
		t.Fatal("active approval survived file edit")
	}
	if _, _, err := f.client.Transition(context.Background(), "1", "approve", TransitionOptions{SessionID: o.SessionID, Mode: o.Mode, SelfReview: true, Reason: "Fixture stale approval"}); err == nil {
		t.Fatal("stale review approved")
	}
	writes := f.writes
	if _, noop, err := f.client.ReplaceLinkedFilesObserved(context.Background(), result, files); err != nil || !noop || f.writes != writes {
		t.Fatal("repeat file set wrote", noop, err)
	}
	result, noop, err = f.client.ReplaceLinkedFilesObserved(context.Background(), result, nil)
	if err != nil || noop || len(result.Details.Files) != 0 {
		t.Fatal(result, noop, err)
	}
}

func TestLinkedFileValidationAndObservedConflicts(t *testing.T) {
	for _, path := range []string{"../outside", "/absolute", "src/../file", "a\\b", ".", ""} {
		t.Run(path, func(t *testing.T) {
			f := newReviewFixture(t)
			r, _ := f.client.Get(context.Background(), "1")
			_, _, err := f.client.ReplaceLinkedFilesObserved(context.Background(), r, []models.IssueFile{{FilePath: path, Role: models.FileRoleImplementation}})
			if err == nil || f.writes != 0 {
				t.Fatal("unsafe path accepted", err)
			}
		})
	}
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			f := newReviewFixture(t)
			oldRun := f.client.run
			reads := 0
			f.client.run = func(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") {
					reads++
					if (!after && reads == 2) || (after && reads == 3) {
						f.issue["body"] = "Concurrent body edit"
					}
				}
				return oldRun(ctx, dir, input, args...)
			}
			r, err := f.client.Get(context.Background(), "1")
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = f.client.ReplaceLinkedFilesObserved(context.Background(), r, []models.IssueFile{{FilePath: "main.go", Role: models.FileRoleTest}})
			var conflict *ConflictError
			if !errors.As(err, &conflict) || conflict.AfterWrite != after {
				t.Fatal("missing observed conflict", err)
			}
			if (!after && f.writes != 0) || (after && f.writes != 1) {
				t.Fatal("unexpected writes", f.writes)
			}
			if f.issue["body"] != "Concurrent body edit" {
				t.Fatal("lost concurrent body")
			}
		})
	}
}
