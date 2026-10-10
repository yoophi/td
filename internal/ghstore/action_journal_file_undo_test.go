package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

// All review identities in this fixture are synthetic test data.
func fileUndoFixture(t *testing.T, reviewed, closed bool) (*journalWriteFixture, func(string)) {
	t.Helper()
	f := newJournalWriteFixture(t, "")
	f.client.stateLabels = fixtureStateLabels()
	ctx := context.Background()
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	clock := f.issues[1].UpdatedAt.Add(-time.Minute)
	d := IssueDetails{Status: models.StatusOpen, Files: []models.IssueFile{{ID: "file-original", IssueID: "gh-1", FilePath: "original.go", Role: models.FileRoleImplementation, LinkedAt: clock, LinkedSHA: "original-sha"}}}
	handoffBody, err := renderActivity(activityData{Kind: "handoff", SessionID: "ses_fixture_worker", OperationID: "fixture-handoff", Done: []string{"Synthetic fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	comment := apiComment{ID: 77, Body: handoffBody, UpdatedAt: clock, CreatedAt: clock, IssueURL: "https://api.github.com/repos/owner/repo/issues/1"}
	comment.User.Login = "github-author"
	events := []nativeStateEvent{}
	if reviewed {
		d.Status = models.StatusInReview
		d.ImplementerSession = "ses_fixture_worker"
		d.ReviewerSession = "ses_fixture_reviewer"
		d.ReviewedAt = &clock
		d.Reviews = []models.IssueReview{{ID: "rv-fixture", IssueID: "gh-1", ReviewerSession: d.ReviewerSession, Decision: reviewpolicy.DecisionApproved, CreatedAt: clock}}
		d.ReviewEvents = stateEventDigest(events)
		d.ReviewHandoffID, d.ReviewHandoffUpdatedAt = "ghc-77", clock
	}
	issue := f.issues[1]
	label := "td:open"
	if reviewed {
		label = "td:in_review"
	}
	if closed {
		issue.State, d.Status = "closed", models.StatusClosed
		label = "td:closed"
		events = []nativeStateEvent{{ID: 1, Event: "closed"}}
	}
	issue.Labels = []struct {
		Name string `json:"name"`
	}{{Name: label}}
	issue.Body, err = encodeBody("Before", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &d})
	if err != nil {
		t.Fatal(err)
	}
	if reviewed {
		data, _ := json.Marshal(issue)
		r, err := decodeIssue(data)
		if err != nil {
			t.Fatal(err)
		}
		d.ReviewBasis = reviewBasis(r, d)
		issue.Body, err = encodeBody("Before", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &d})
		if err != nil {
			t.Fatal(err)
		}
	}
	f.issues[1] = issue
	mode := ""
	base := f.client.run
	eventReads, commentReads := 0, 0
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "repos/owner/repo/issues/1/events?per_page=100") {
			eventReads++
			if mode == "target-after-final-review" && eventReads == 4 {
				target := f.issues[1]
				target.Title = "Concurrent title during final review check"
				f.issues[1] = target
			}
			if mode == "events-denied" {
				return nil, errors.New("HTTP 403 native event permission denied")
			}
			if mode == "events" || mode == "events-after-intent" && eventReads >= 2 || mode == "events-after-patch" && eventReads >= 3 || mode == "events-after-receipt" && eventReads >= 4 {
				return json.Marshal([][]nativeStateEvent{append(slices.Clone(events), nativeStateEvent{ID: 100, Event: "reopened"})})
			}
			return json.Marshal([][]nativeStateEvent{events})
		}
		if slices.Contains(args, "repos/owner/repo/issues/1/comments?per_page=100") {
			commentReads++
			if mode == "handoff-deleted" {
				return json.Marshal([][]apiComment{{}})
			}
			copy := comment
			if mode == "handoff" || mode == "handoff-after-intent" && commentReads >= 2 || mode == "handoff-after-patch" && commentReads >= 3 || mode == "handoff-after-receipt" && commentReads >= 4 {
				copy.UpdatedAt = clock.Add(time.Second)
			}
			return json.Marshal([][]apiComment{{copy}})
		}
		return base(ctx, dir, payload, args...)
	}
	r, err := f.client.Get(ctx, "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	_, noop, err := f.client.ReplaceLinkedFilesObserved(ctx, r, []models.IssueFile{{ID: "file-new", FilePath: "new.go", Role: models.FileRoleTest, LinkedAt: clock, LinkedSHA: "new-sha"}})
	if err != nil || noop {
		t.Fatal(noop, err)
	}
	return f, func(value string) { mode = value }
}

func TestFileUndoRestoresFilesAndVerifiedOriginalReview(t *testing.T) {
	for _, tc := range []struct {
		name             string
		reviewed, closed bool
	}{{"open", false, false}, {"reviewed", true, false}, {"closed-approved", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := fileUndoFixture(t, tc.reviewed, tc.closed)
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			original := *a.Before
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || result.Journal.Phase != "undone" || f.targetWrites != 2 {
				t.Fatal(result, err, f.targetWrites)
			}
			if result.Issue.nativeBody != original.Body || result.Issue.Details.Files[0].LinkedSHA != "original-sha" || result.Issue.Details.Files[0].ID != "file-original" {
				t.Fatal("original file data was not restored", result)
			}
			if tc.reviewed && (result.Issue.ReviewerSession != "ses_fixture_reviewer" || activeApproval(result.Issue.Details) == nil) {
				t.Fatal("verified original approval not restored", result)
			}
			writes, receipts := f.targetWrites, f.receiptWrites
			result, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || !result.AlreadyUndone || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal("undo replay wrote", result, err)
			}
		})
	}
}

func TestFileUndoRejectsChangedReviewEvidenceAtEveryFence(t *testing.T) {
	for _, mode := range []string{"events", "events-denied", "handoff", "handoff-deleted", "events-after-intent", "handoff-after-intent", "events-after-patch", "handoff-after-patch", "events-after-receipt", "handoff-after-receipt"} {
		t.Run(mode, func(t *testing.T) {
			f, setMode := fileUndoFixture(t, true, false)
			setMode(mode)
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			var conflict *ConflictError
			if mode == "events-denied" {
				if result != nil || err == nil || errors.As(err, &conflict) || f.targetWrites != 1 || !strings.Contains(err.Error(), "HTTP 403 native event permission denied") {
					t.Fatal("transport error lost or incorrectly changed into conflict", result, err)
				}
				return
			}
			if result != nil || !errors.As(err, &conflict) {
				t.Fatal("stale evidence succeeded", result, err)
			}
			afterPatch := strings.Contains(mode, "after-patch") || strings.Contains(mode, "after-receipt")
			if conflict.AfterWrite != afterPatch || f.targetWrites != 1+boolToInt(afterPatch) {
				t.Fatal("wrong write boundary", conflict, f.targetWrites)
			}
		})
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestFileUndoCannotRestoreChangedHistoricalReviewOrGraph(t *testing.T) {
	for _, mode := range []string{"review-author", "new-review", "dependency", "parent", "claim", "state", "file-path", "file-owner"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := fileUndoFixture(t, true, false)
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			desc, meta, _, err := decodeBody(a.After.Body)
			if err != nil {
				t.Fatal(err)
			}
			d := meta.Details
			switch mode {
			case "review-author":
				d.Reviews[0].ReviewerSession = "forged-reviewer"
			case "new-review":
				d.Reviews = append(d.Reviews, d.Reviews[0])
			case "dependency":
				d.Dependencies = []string{"gh-2"}
			case "parent":
				d.ParentID = "gh-2"
			case "claim":
				d.ImplementerSession = "other-worker"
			case "state":
				d.Status = models.StatusInProgress
			case "file-path":
				d.Files[0].FilePath = "../unsafe"
			case "file-owner":
				d.Files[0].IssueID = "gh-2"
			}
			a.After.Body, err = encodeBody(desc, meta)
			if err != nil {
				t.Fatal(err)
			}
			if err := scalarUndoScope(a); err == nil {
				t.Fatal("unrelated effect accepted", mode)
			}
		})
	}
}

func TestFileUndoFailuresDoNotReplayOrClaimRestoration(t *testing.T) {
	for _, mode := range []string{"target-denied", "target-uncertain", "undo-receipt-denied", "undo-receipt-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := fileUndoFixture(t, false, false)
			f.mode = mode
			if r, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); r != nil || err == nil {
				t.Fatal(r, err)
			}
			writes := f.targetWrites
			f.mode = ""
			r, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if f.targetWrites != writes {
				t.Fatal("uncertain target replayed")
			}
			if mode == "undo-receipt-uncertain" {
				if err != nil || !r.AlreadyUndone {
					t.Fatal(r, err)
				}
			} else if err == nil {
				t.Fatal("unverified result marked done")
			}
		})
	}
}

func TestFileUndoDoesNotMutateOriginalSnapshots(t *testing.T) {
	f, _ := fileUndoFixture(t, true, false)
	a, _ := decodeActionJournal(*f.comment, "gh-9")
	before, after := *a.Before, *a.After
	if _, err := fileUndoScope(a); err != nil {
		t.Fatal(err)
	}
	r, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, *r.Journal.Before) || !reflect.DeepEqual(after, *r.Journal.After) {
		t.Fatal("original journal snapshots changed")
	}
}

func TestFileUndoLinksAndUnlinksLegacyIssue(t *testing.T) {
	for _, unlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "link", true: "unlink"}[unlink], func(t *testing.T) {
			f := newJournalWriteFixture(t, "")
			f.client.stateLabels = fixtureStateLabels()
			if err := f.client.TrackActions("ses_actor"); err != nil {
				t.Fatal(err)
			}
			files := []models.IssueFile{{ID: "file-fixture", FilePath: "test.go", Role: models.FileRoleTest}}
			if unlink {
				issue := f.issues[1]
				body, err := encodeBody("Before", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Files: files}})
				if err != nil {
					t.Fatal(err)
				}
				issue.Body = body
				f.issues[1] = issue
				files = nil
			}
			original := f.issues[1].Body
			r, err := f.client.Get(context.Background(), "gh-1")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.client.ReplaceLinkedFilesObserved(context.Background(), r, files); err != nil {
				t.Fatal(err)
			}
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || result.Issue.nativeBody != original || len(result.Issue.Labels) != 0 {
				t.Fatal("legacy body/labels not restored", result, err)
			}
		})
	}
}

func TestFileUndoAlreadyUndoneStillChecksReviewEvidence(t *testing.T) {
	f, setMode := fileUndoFixture(t, true, false)
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	writes, receipts := f.targetWrites, f.receiptWrites
	setMode("events")
	if result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); result != nil || err == nil || f.targetWrites != writes || f.receiptWrites != receipts {
		t.Fatal("stale restored approval was accepted", result, err)
	}
	witness, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.client.recheckUndoWitnesses(context.Background(), []ActionJournalRecord{*witness}); err == nil {
		t.Fatal("stale file review was used as a sequential undo witness")
	}
}

func TestFileUndoRechecksTargetAfterFinalReviewObservation(t *testing.T) {
	f, setMode := fileUndoFixture(t, true, false)
	setMode("target-after-final-review")
	result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	var conflict *ConflictError
	if result != nil || !errors.As(err, &conflict) || !conflict.AfterWrite {
		t.Fatal("later target edit observed during final review check was reported as success", result, err)
	}
	if f.issues[1].Title != "Concurrent title during final review check" || f.targetWrites != 2 {
		t.Fatal("concurrent edit overwritten or inverse replayed")
	}
}
