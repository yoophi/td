package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func relationUndoFixture(t *testing.T, kind string, reviewed bool) (*journalWriteFixture, func(string), string) {
	t.Helper()
	f, reviewMode := fileUndoFixture(t, reviewed, false)
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{2, 3, 4} {
		f.issues[n] = apiIssue{Number: n, Title: fmt.Sprintf("Target %d", n), Body: "Target", State: "open", UpdatedAt: f.issues[1].UpdatedAt}
	}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		for _, n := range []int{3, 4} {
			if slices.Contains(args, fmt.Sprintf("repos/owner/repo/issues/%d", n)) {
				return json.Marshal(f.issues[n])
			}
		}
		return base(ctx, dir, payload, args...)
	}
	setSource := func(deps []string, parent string) {
		issue := f.issues[1]
		desc, meta, _, err := decodeBody(issue.Body)
		if err != nil {
			t.Fatal(err)
		}
		meta.Details.Dependencies, meta.Details.ParentID = deps, parent
		// The synthetic original review must include the original relations.
		if reviewed {
			encoded, _ := json.Marshal(issue)
			r, err := decodeIssue(encoded)
			if err != nil {
				t.Fatal(err)
			}
			meta.Details.ReviewBasis = reviewBasis(r, *meta.Details)
		}
		issue.Body, err = encodeBody(desc, meta)
		if err != nil {
			t.Fatal(err)
		}
		f.issues[1] = issue
	}
	switch kind {
	case "remove", "replace", "reorder":
		setSource([]string{"gh-2", "gh-3"}, "")
	case "parent-clear", "parent-replace":
		setSource(nil, "gh-2")
	}
	original := f.issues[1].Body
	r, err := f.client.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "add":
		_, err = f.client.ChangeDependencyObserved(context.Background(), r, "gh-2", true, "ses_actor")
	case "remove":
		_, err = f.client.ChangeDependencyObserved(context.Background(), r, "gh-2", false, "ses_actor")
	case "replace":
		_, _, err = f.client.ReplaceDependenciesObserved(context.Background(), r, []string{"gh-4"}, "ses_actor")
	case "reorder":
		_, _, err = f.client.ReplaceDependenciesObserved(context.Background(), r, []string{"gh-3", "gh-2"}, "ses_actor")
	case "parent-add":
		parent := "gh-2"
		_, err = f.client.UpdateObserved(context.Background(), r, Changes{ParentID: &parent})
	case "parent-clear":
		parent := ""
		_, err = f.client.UpdateObserved(context.Background(), r, Changes{ParentID: &parent})
	case "parent-replace":
		parent := "gh-3"
		_, err = f.client.UpdateObserved(context.Background(), r, Changes{ParentID: &parent})
	default:
		t.Fatal("unknown fixture", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	reviewMode("")
	return f, reviewMode, original
}

func TestRelationUndoRestoresSingleSourceAndOriginalReview(t *testing.T) {
	for _, kind := range []string{"add", "remove", "replace", "reorder", "parent-add", "parent-clear", "parent-replace"} {
		for _, reviewed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reviewed=%t", kind, reviewed), func(t *testing.T) {
				f, _, original := relationUndoFixture(t, kind, reviewed)
				writes := f.targetWrites
				journal, _ := decodeActionJournal(*f.comment, "gh-9")
				before, after := *journal.Before, *journal.After
				result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
				if err != nil || result.Journal.Phase != "undone" || result.Issue.nativeBody != original || f.targetWrites != writes+1 {
					t.Fatal(result, err, f.targetWrites)
				}
				if reviewed && (result.Issue.ReviewerSession != "ses_fixture_reviewer" || activeApproval(result.Issue.Details) == nil) {
					t.Fatal("original review not restored", result)
				}
				if !reflect.DeepEqual(before, *result.Journal.Before) || !reflect.DeepEqual(after, *result.Journal.After) {
					t.Fatal("original snapshots changed")
				}
				writes, receipts := f.targetWrites, f.receiptWrites
				result, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
				if err != nil || !result.AlreadyUndone || f.targetWrites != writes || f.receiptWrites != receipts {
					t.Fatal("repeat inverse wrote", result, err)
				}
			})
		}
	}
}

func changeRelationTarget(t *testing.T, f *journalWriteFixture, n int, mutate func(*IssueDetails)) {
	t.Helper()
	issue := f.issues[n]
	desc, meta, _, err := decodeBody(issue.Body)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Details == nil {
		meta.Details = &IssueDetails{}
	}
	mutate(meta.Details)
	issue.Body, err = encodeBody(desc, meta)
	if err != nil {
		t.Fatal(err)
	}
	f.issues[n] = issue
}

func TestRelationUndoRejectsInvalidRestoredGraphBeforeIntent(t *testing.T) {
	for _, mode := range []string{"dep-cycle-source", "dep-existing-cycle", "dep-missing", "dep-deleted", "dep-auxiliary", "parent-cycle-source", "parent-existing-cycle", "parent-missing", "parent-deleted"} {
		t.Run(mode, func(t *testing.T) {
			kind := "remove"
			if strings.HasPrefix(mode, "parent") {
				kind = "parent-clear"
			}
			f, _, _ := relationUndoFixture(t, kind, false)
			now := time.Now().UTC()
			switch mode {
			case "dep-cycle-source":
				changeRelationTarget(t, f, 2, func(d *IssueDetails) { d.Dependencies = []string{"gh-1"} })
			case "dep-existing-cycle":
				changeRelationTarget(t, f, 2, func(d *IssueDetails) { d.Dependencies = []string{"gh-3"} })
				changeRelationTarget(t, f, 3, func(d *IssueDetails) { d.Dependencies = []string{"gh-2"} })
			case "parent-cycle-source":
				changeRelationTarget(t, f, 2, func(d *IssueDetails) { d.ParentID = "gh-1" })
			case "parent-existing-cycle":
				changeRelationTarget(t, f, 2, func(d *IssueDetails) { d.ParentID = "gh-3" })
				changeRelationTarget(t, f, 3, func(d *IssueDetails) { d.ParentID = "gh-2" })
			case "dep-deleted", "parent-deleted":
				changeRelationTarget(t, f, 2, func(d *IssueDetails) { d.DeletedAt = &now })
			case "dep-missing", "parent-missing":
				delete(f.issues, 2)
			case "dep-auxiliary":
				issue := f.issues[2]
				issue.Body, _ = encodeBody("Not a task", metadata{EntityKind: "note", Type: models.TypeTask, Priority: models.PriorityP2})
				f.issues[2] = issue
			}
			writes, receipts := f.targetWrites, f.receiptWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			var conflict *ConflictError
			if result != nil || !errors.As(err, &conflict) || conflict.AfterWrite || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal("invalid graph wrote intent/target", result, err)
			}
		})
	}
}

func TestRelationUndoDetectsReachableGraphChangeAtEveryFence(t *testing.T) {
	for _, at := range []int{2, 3, 4} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			f, _, _ := relationUndoFixture(t, "remove", false)
			base, reads := f.client.run, 0
			f.client.run = func(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "repos/owner/repo/issues?state=all&per_page=100") {
					reads++
					if reads == at {
						target := f.issues[2]
						target.UpdatedAt = target.UpdatedAt.Add(time.Second)
						f.issues[2] = target
					}
				}
				return base(ctx, dir, input, args...)
			}
			writes := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			var conflict *ConflictError
			if result != nil || !errors.As(err, &conflict) || conflict.AfterWrite != (at > 2) || f.targetWrites != writes+boolToInt(at > 2) {
				t.Fatal("changed graph was accepted/replayed", result, err, f.targetWrites)
			}
		})
	}
}

func TestRelationUndoRevalidatesOriginalReviewAndPreservesTransportError(t *testing.T) {
	for _, mode := range []string{"events", "handoff", "events-after-intent", "handoff-after-patch", "events-after-receipt", "events-denied"} {
		t.Run(mode, func(t *testing.T) {
			f, setMode, _ := relationUndoFixture(t, "remove", true)
			setMode(mode)
			writes := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if result != nil || err == nil || f.targetWrites > writes+1 {
				t.Fatal(result, err)
			}
			if mode == "events-denied" && !strings.Contains(err.Error(), "HTTP 403 native event permission denied") {
				t.Fatal("original cause lost", err)
			}
		})
	}
}

func TestRelationUndoGraphReadCostDoesNotGrowPerTarget(t *testing.T) {
	for _, size := range []int{1, 45, 101} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f, _, _ := relationUndoFixture(t, "remove", false)
			for n := 10; n < size+10; n++ {
				f.issues[n] = apiIssue{Number: n, Title: "Unrelated", Body: "Unrelated", State: "open", UpdatedAt: f.issues[1].UpdatedAt}
			}
			// Expand the proposed reachable graph into a chain.
			for n := 10; n < size+9; n++ {
				changeRelationTarget(t, f, n, func(d *IssueDetails) { d.Dependencies = []string{fmt.Sprintf("gh-%d", n+1)} })
			}
			changeRelationTarget(t, f, 2, func(d *IssueDetails) { d.Dependencies = []string{"gh-10"} })
			base, collectionReads, foreignPointReads := f.client.run, 0, 0
			f.client.run = func(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
				path := args[5]
				if path == "repos/owner/repo/issues?state=all&per_page=100" {
					collectionReads++
				}
				if args[4] == "GET" && strings.HasPrefix(path, "repos/owner/repo/issues/") && path != "repos/owner/repo/issues/1" && path != "repos/owner/repo/issues/9" && !strings.Contains(path, "/comments") {
					foreignPointReads++
				}
				data, err := base(ctx, dir, input, args...)
				if path == "repos/owner/repo/issues?state=all&per_page=100" && err == nil {
					var pages [][]apiIssue
					if err := json.Unmarshal(data, &pages); err != nil {
						return nil, err
					}
					all := pages[0]
					pages = nil
					for len(all) > 0 {
						n := min(len(all), 100)
						pages = append(pages, all[:n])
						all = all[n:]
					}
					return json.Marshal(pages)
				}
				return data, err
			}
			if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
				t.Fatal(err)
			}
			if collectionReads != 4 || foreignPointReads != 0 {
				t.Fatal("graph undo introduced N+1", collectionReads, foreignPointReads)
			}
		})
	}
}

func TestRelationUndoUnrelatedChangesDoNotConflict(t *testing.T) {
	f, _, _ := relationUndoFixture(t, "remove", false)
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "repos/owner/repo/issues?state=all&per_page=100") {
			issue := f.issues[4]
			issue.Title += "x"
			f.issues[4] = issue
		}
		return base(ctx, dir, input, args...)
	}
	if _, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); err != nil {
		t.Fatal("unrelated issue blocked graph inverse", err)
	}
}

func TestRelationUndoRejectsForgedTransitionOrWorkflowEffects(t *testing.T) {
	for _, mode := range []string{"actor", "prefix", "incomplete", "reviewer", "participation", "status"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := relationUndoFixture(t, "remove", true)
			a, _ := decodeActionJournal(*f.comment, "gh-9")
			desc, meta, _, _ := decodeBody(a.After.Body)
			switch mode {
			case "actor":
				meta.Details.Transitions[0].SessionID = "other"
			case "prefix":
				bdesc, bm, _, _ := decodeBody(a.Before.Body)
				bm.Details.Transitions = []TransitionRecord{{OperationID: "old-history", Action: "start", From: models.StatusOpen, To: models.StatusInReview, SessionID: "ses_actor", At: time.Now().UTC()}}
				a.Before.Body, _ = encodeBody(bdesc, bm)
			case "incomplete":
				meta.Details.Dependencies = []string{"gh-4"}
			case "reviewer":
				meta.Details.Reviews[0].ReviewerSession = "other"
			case "participation":
				meta.Details.ImplementerSession = "other"
			case "status":
				meta.Details.Status = models.StatusInProgress
			}
			a.After.Body, _ = encodeBody(desc, meta)
			if supported, err := relationUndoScope(a); supported || err != nil {
				t.Fatal("unrelated effect allowed", mode, supported, err)
			}
		})
	}
}

func TestRelationUndoGraphTransportFailurePreservesCauseAndNeverRetries(t *testing.T) {
	for _, at := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			f, _, _ := relationUndoFixture(t, "remove", false)
			base, reads := f.client.run, 0
			cause := errors.New("HTTP 403 API rate limit exceeded; x-ratelimit-reset=1791616785")
			f.client.run = func(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "repos/owner/repo/issues?state=all&per_page=100") {
					reads++
					if reads == at {
						return nil, cause
					}
				}
				return base(ctx, dir, input, args...)
			}
			writes := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			var conflict *ConflictError
			if result != nil || !errors.Is(err, cause) || errors.As(err, &conflict) || f.targetWrites != writes+boolToInt(at > 2) || !strings.Contains(err.Error(), "API rate limit exceeded") {
				t.Fatal("raw cause lost/reclassified or target replayed", result, err, f.targetWrites)
			}
		})
	}
}

func TestRelationUndoTargetAndReceiptFailuresNeverReplaySource(t *testing.T) {
	for _, mode := range []string{"target-denied", "target-uncertain", "undo-receipt-denied", "undo-receipt-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := relationUndoFixture(t, "remove", false)
			f.receiptWrites = 1
			f.mode = mode
			if result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor"); result != nil || err == nil {
				t.Fatal("uncertain graph inverse reported success", result, err)
			}
			writes := f.targetWrites
			f.mode = ""
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if f.targetWrites != writes {
				t.Fatal("uncertain source PATCH replayed")
			}
			if mode == "undo-receipt-uncertain" {
				if err != nil || !result.AlreadyUndone {
					t.Fatal("verified saved receipt not rehydrated", result, err)
				}
			} else if err == nil {
				t.Fatal("unverified result marked successful")
			}
		})
	}
}

func TestRelationUndoFinalPointCheckPreservesLaterSourceEdit(t *testing.T) {
	f, _, _ := relationUndoFixture(t, "remove", false)
	base, reads := f.client.run, 0
	f.client.run = func(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "repos/owner/repo/issues?state=all&per_page=100") {
			reads++
		}
		if reads == 4 && slices.Contains(args, "repos/owner/repo/issues/1") && slices.Contains(args, "GET") {
			target := f.issues[1]
			target.Title = "Later source edit after final graph observation"
			f.issues[1] = target
		}
		return base(ctx, dir, input, args...)
	}
	writes := f.targetWrites
	result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	var conflict *ConflictError
	if result != nil || !errors.As(err, &conflict) || !conflict.AfterWrite || f.targetWrites != writes+1 || f.issues[1].Title != "Later source edit after final graph observation" {
		t.Fatal("later source edit accepted or overwritten", result, err)
	}
}
