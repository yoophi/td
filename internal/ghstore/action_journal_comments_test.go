package ghstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func commentJournalFixture(t *testing.T) ActionJournal {
	t.Helper()
	a := journalFixture(t)
	a.Action, a.CarrierID = "comment_create", "gh-9"
	body, err := renderActivity(activityData{Kind: "comment", OperationID: a.OperationID, SessionID: a.SessionID, Message: "Native comment result"})
	if err != nil {
		t.Fatal(err)
	}
	a.CommentInput = &ActionCommentInput{Body: body}
	after := *a.Before
	after.UpdatedAt = after.UpdatedAt.Add(time.Second)
	after.Revision, _ = after.checksum()
	a.After = &after
	a.CommentAfter = &ActionCommentState{ID: "ghc-21", IssueID: a.IssueID, Body: body,
		Author: "github-author", CreatedAt: after.UpdatedAt, UpdatedAt: after.UpdatedAt}
	a.CommentAfter.Revision, _ = a.CommentAfter.checksum()
	return a
}

func TestCommentJournalVersionedRoundTripAndSafeIssueUndoBoundary(t *testing.T) {
	for _, phase := range []string{"prepared", "applied", "undo_prepared", "undone"} {
		t.Run(phase, func(t *testing.T) {
			a := commentJournalFixture(t)
			a.Phase = phase
			if phase == "prepared" {
				a.After, a.CommentAfter = nil, nil
			}
			if phase == "undo_prepared" || phase == "undone" {
				a.UndoOperationID = "td-op-UNDO"
			}
			if phase == "undone" {
				restored := *a.Before
				restored.UpdatedAt = a.After.UpdatedAt.Add(time.Second)
				restored.Revision, _ = restored.checksum()
				a.Restored = &restored
			}
			c := journalCommentFixture(t, a)
			if !strings.Contains(c.Body, actionCommentJournalStart) {
				t.Fatal("comment result used legacy issue schema")
			}
			decoded, err := decodeActionJournal(c, a.CarrierID)
			if err != nil || decoded.CommentInput.Body != a.CommentInput.Body || decoded.Phase != phase {
				t.Fatal(decoded, err)
			}
			feed, err := decodeActivity(c, a.CarrierID)
			if err != nil || feed.Kind != "action_journal" || strings.Contains(feed.Message, "Native comment result") {
				t.Fatal("nested activity body misclassified as outer metadata", feed, err)
			}
			_, err = decoded.PlanIssueUndo(nil, a.SessionID, "github-author")
			var scope *UndoScopeError
			if !errors.As(err, &scope) {
				t.Fatal("comment action could become an issue PATCH", err)
			}
		})
	}
}

func TestCommentJournalRejectsForgedAndInconsistentStates(t *testing.T) {
	for name, edit := range map[string]func(*ActionJournal){
		"input absent":         func(a *ActionJournal) { a.CommentInput = nil },
		"result absent":        func(a *ActionJournal) { a.CommentAfter = nil },
		"carrier absent":       func(a *ActionJournal) { a.CarrierID = "" },
		"issue creation input": func(a *ActionJournal) { a.CreateInput = &ActionCreateInput{} },
		"canonical comment id": func(a *ActionJournal) { a.CommentAfter.ID = "21" },
		"wrong issue": func(a *ActionJournal) {
			a.CommentAfter.IssueID = "gh-2"
			a.CommentAfter.Revision, _ = a.CommentAfter.checksum()
		},
		"body tampered": func(a *ActionJournal) {
			a.CommentAfter.Body = "Other body"
			a.CommentAfter.Revision, _ = a.CommentAfter.checksum()
		},
		"author missing":   func(a *ActionJournal) { a.CommentAfter.Author = "" },
		"clock missing":    func(a *ActionJournal) { a.CommentAfter.CreatedAt = time.Time{} },
		"clock reversed":   func(a *ActionJournal) { a.CommentAfter.UpdatedAt = a.CommentAfter.CreatedAt.Add(-time.Second) },
		"checksum changed": func(a *ActionJournal) { a.CommentAfter.Revision = "forged" },
		"target changed":   func(a *ActionJournal) { a.After.Title = "Concurrent title"; a.After.Revision, _ = a.After.checksum() },
		"target clock reversed": func(a *ActionJournal) {
			a.After.UpdatedAt = a.Before.UpdatedAt.Add(-time.Second)
			a.After.Revision, _ = a.After.checksum()
		},
		"native input":              func(a *ActionJournal) { a.CommentInput.Body = "Unmanaged native text" },
		"wrong operation":           func(a *ActionJournal) { a.OperationID = "td-op-DIFFERENT" },
		"nested journal":            func(a *ActionJournal) { a.CommentInput.Body = actionJournalStart + "{}" },
		"issue action with comment": func(a *ActionJournal) { a.Action = "update" },
		"prepared result":           func(a *ActionJournal) { a.Phase = "prepared"; a.CommentAfter = nil },
	} {
		t.Run(name, func(t *testing.T) {
			a := commentJournalFixture(t)
			edit(&a)
			if _, err := renderActionJournal(a); err == nil {
				t.Fatal("invalid comment state accepted", a)
			}
		})
	}
	a := commentJournalFixture(t)
	c := journalCommentFixture(t, a)
	c.User.Login = "forged-native-author"
	if _, err := decodeActionJournal(c, a.CarrierID); err == nil {
		t.Fatal("comment result impersonated journal author")
	}
	c = journalCommentFixture(t, a)
	c.Body = strings.Replace(c.Body, actionCommentJournalStart, actionJournalStart, 1)
	if _, err := decodeActionJournal(c, a.CarrierID); err == nil {
		t.Fatal("v2 comment fields smuggled into v1")
	}
	c = journalCommentFixture(t, journalFixture(t))
	c.Body = strings.Replace(c.Body, actionJournalStart, actionCommentJournalStart, 1)
	if _, err := decodeActionJournal(c, "gh-1"); err == nil {
		t.Fatal("v1 payload mislabeled as v2")
	}
}

func TestCommentJournalBulkHistoryMixesVersionsWithoutExtraQueries(t *testing.T) {
	body, err := encodeBody("Journal", metadata{EntityKind: "journal", Type: "task", Priority: "P2"})
	if err != nil {
		t.Fatal(err)
	}
	v1 := journalFixture(t)
	v1.CarrierID = "gh-9"
	legacy := journalCommentFixture(t, v1)
	legacy.IssueURL = "https://api.github.com/repos/owner/repo/issues/9"
	v2 := commentJournalFixture(t)
	v2.OperationID = "td-op-COMMENT"
	v2.CommentInput.Body = strings.ReplaceAll(v2.CommentInput.Body, "td-op-ABC", v2.OperationID)
	v2.CommentAfter.Body = v2.CommentInput.Body
	v2.CommentAfter.Revision, _ = v2.CommentAfter.checksum()
	comment := journalCommentFixture(t, v2)
	comment.ID, comment.IssueURL = 13, legacy.IssueURL
	comment.CreatedAt = legacy.CreatedAt.Add(time.Second)
	client, calls := snapshotFixture([][]apiIssue{{{Number: 9, Title: "Journal", Body: body, State: "open"}}}, [][]apiComment{{legacy, comment}}, nil)
	snapshot, err := client.ReadSnapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	actions, err := snapshot.ActionHistory(v1.SessionID)
	if err != nil || len(actions) != 2 || actions[0].CommentAfter == nil || actions[1].Action != "update" || len(*calls) != 2 {
		t.Fatal(actions, err, *calls)
	}
}

func TestActionJournalCanonicalJSONSeparatesNestedMarkersFromOuterBlocks(t *testing.T) {
	a := journalFixture(t)
	a.Before.Body = "Preserved user text " + activityStart + "{} " + actionJournalStart + "{}"
	a.Before.Revision, _ = a.Before.checksum()
	c := journalCommentFixture(t, a)
	if _, err := decodeActivity(c, a.IssueID); err != nil {
		t.Fatal("raw snapshot marker mistaken for a second outer block", err)
	}
	for _, extra := range []string{activityStart + "{}" + markerEnd, actionJournalStart + "{}" + markerEnd} {
		mixed := c
		mixed.Body += "\n" + extra
		if _, err := decodeActivity(mixed, a.IssueID); err == nil {
			t.Fatal("noncanonical second outer block accepted")
		}
	}
}

func TestCommentJournalUndoReceiptCannotRewriteOriginalComment(t *testing.T) {
	old := &ActionJournalRecord{ActionJournal: commentJournalFixture(t)}
	next := commentJournalFixture(t)
	next.Phase, next.UndoOperationID = "undo_prepared", "td-op-UNDO"
	next.CommentInput.Body = strings.ReplaceAll(next.CommentInput.Body, "Native comment result", "Changed original comment")
	next.CommentAfter.Body = next.CommentInput.Body
	next.CommentAfter.Revision, _ = next.CommentAfter.checksum()
	if _, err := renderActionJournal(next); err != nil {
		t.Fatal("test mutation must otherwise be canonical", err)
	}
	// No runner: the immutable payload guard must reject before any API call.
	_, err := (&Client{}).rewriteActionJournal(context.Background(), old, next)
	if err == nil || !strings.Contains(err.Error(), "cannot rewrite original") {
		t.Fatal("comment snapshot replacement accepted", err)
	}
}
