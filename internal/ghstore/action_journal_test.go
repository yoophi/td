package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func journalStateFixture(t *testing.T, title string) ActionIssueState {
	t.Helper()
	s := ActionIssueState{Title: title, Body: "user body", State: "open", Labels: []string{"topic", "td:open"}, UpdatedAt: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}
	var err error
	s.Revision, err = s.checksum()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestActionUndoPlanRejectsConflictsUncertainOutcomesAndWrongActor(t *testing.T) {
	journal := journalFixture(t)
	current := &Record{nativeBody: journal.After.Body, nativeState: journal.After.State}
	current.ID, current.Title, current.Labels, current.UpdatedAt = journal.IssueID, journal.After.Title,
		append([]string{}, journal.After.Labels...), journal.After.UpdatedAt
	record := ActionJournalRecord{ActionJournal: journal, Author: "github-author"}
	plan, err := record.PlanIssueUndo(current, "ses_actor", "GitHub-Author")
	if err != nil || plan.AlreadyUndone || plan.Payload["title"] != journal.Before.Title || plan.Payload["body"] != journal.Before.Body {
		t.Fatal(plan, err)
	}
	for _, change := range []func(*Record){
		func(r *Record) { r.Title = "Other actor's edit" },
		func(r *Record) { r.nativeBody = "Changed metadata" },
		func(r *Record) { r.UpdatedAt = r.UpdatedAt.Add(time.Second) },
		func(r *Record) { r.Labels = []string{"different"} },
		func(r *Record) { r.nativeState = "closed" },
	} {
		changed := *current
		change(&changed)
		_, err := record.PlanIssueUndo(&changed, "ses_actor", "github-author")
		var conflict *ConflictError
		if !errors.As(err, &conflict) || conflict.AfterWrite {
			t.Fatal("later change was not rejected before writes", err)
		}
	}
	for _, phase := range []string{"prepared", "undo_prepared"} {
		pending := record
		pending.Phase = phase
		if phase == "undo_prepared" {
			pending.UndoOperationID = "td-op-UNDO"
		}
		if _, err := pending.PlanIssueUndo(current, "ses_actor", "github-author"); err == nil {
			t.Fatal("uncertain operation was replayed")
		}
	}
	for _, actor := range [][2]string{{"ses_other", "github-author"}, {"ses_actor", "other-author"}, {"", "github-author"}, {"ses_actor", ""}} {
		if _, err := record.PlanIssueUndo(current, actor[0], actor[1]); err == nil {
			t.Fatal("forged actor accepted", actor)
		}
	}
	creation := record
	creation.Action = "create"
	creation.Before = nil
	if _, err := creation.PlanIssueUndo(current, "ses_actor", "github-author"); err == nil || !strings.Contains(err.Error(), "logical delete") {
		t.Fatal("creation erased without a limit", err)
	}
	undone := record
	undone.Phase = "undone"
	undone.UndoOperationID = "td-op-UNDO"
	restored := journalStateFixture(t, "Restored")
	undone.Restored = &restored
	actual := *current
	actual.Title = restored.Title
	plan, err = undone.PlanIssueUndo(&actual, "ses_actor", "github-author")
	if err != nil || !plan.AlreadyUndone || plan.Payload != nil {
		t.Fatal("verified undo receipt was not idempotent", plan, err)
	}
	actual.Title = "Later change after undo"
	if _, err := undone.PlanIssueUndo(&actual, "ses_actor", "github-author"); err == nil {
		t.Fatal("old receipt overwrote later change")
	}
}

func journalFixture(t *testing.T) ActionJournal {
	before, after := journalStateFixture(t, "Before"), journalStateFixture(t, "After")
	return ActionJournal{OperationID: "td-op-ABC", SessionID: "ses_actor", IssueID: "gh-1", Action: "update", Phase: "applied", Before: &before, After: &after}
}

func journalCommentFixture(t *testing.T, a ActionJournal) apiComment {
	t.Helper()
	body, err := renderActionJournal(a)
	if err != nil {
		t.Fatal(err)
	}
	c := apiComment{ID: 12, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: body,
		URL: "https://github.com/owner/repo/issues/1#issuecomment-12", CreatedAt: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}
	c.User.Login = "github-author"
	return c
}

func TestActionJournalPhasesAndGitHubAttribution(t *testing.T) {
	for _, phase := range []string{"prepared", "applied", "undo_prepared", "undone"} {
		t.Run(phase, func(t *testing.T) {
			a := journalFixture(t)
			a.Phase = phase
			if phase == "prepared" {
				a.After = nil
			}
			if phase == "undo_prepared" || phase == "undone" {
				a.UndoOperationID = "td-op-UNDO"
			}
			if phase == "undone" {
				restored := journalStateFixture(t, "Before")
				restored.UpdatedAt = restored.UpdatedAt.Add(time.Minute)
				restored.Revision, _ = restored.checksum()
				a.Restored = &restored
			}
			comment := journalCommentFixture(t, a)
			comment.UpdatedAt = comment.CreatedAt.Add(time.Second)
			decoded, err := decodeActionJournal(comment, "gh-1")
			if err != nil || decoded.Phase != phase || decoded.Author != "github-author" || decoded.ID != "ghc-12" || !decoded.UpdatedAt.Equal(comment.UpdatedAt) {
				t.Fatal(decoded, err)
			}
			activity, err := decodeActivity(comment, "gh-1")
			if err != nil || activity.Kind != "action_journal" || activity.Native || strings.Contains(activity.Message, "user body") {
				t.Fatal("journal is not a native comment or a full-body feed", activity, err)
			}
		})
	}
	a := journalFixture(t)
	a.Action = "create"
	a.Before = nil
	if _, err := renderActionJournal(a); err != nil {
		t.Fatal(err)
	}
}

func TestActionJournalRejectsUnverifiedAndMalformedRecords(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ActionJournal)
	}{
		{"missing before", func(a *ActionJournal) { a.Before = nil }},
		{"missing after", func(a *ActionJournal) { a.After = nil }},
		{"unknown phase", func(a *ActionJournal) { a.Phase = "success" }},
		{"unknown action", func(a *ActionJournal) { a.Action = "shell" }},
		{"identity injection", func(a *ActionJournal) { a.OperationID = "bad\nmarker" }},
		{"noncanonical id", func(a *ActionJournal) { a.IssueID = "1" }},
		{"missing session", func(a *ActionJournal) { a.SessionID = "" }},
		{"before checksum", func(a *ActionJournal) { a.Before.Body = "other" }},
		{"native state", func(a *ActionJournal) { a.Before.State = "in_progress" }},
		{"duplicate labels", func(a *ActionJournal) { a.Before.Labels = []string{"a", "a"} }},
		{"nil labels", func(a *ActionJournal) { a.Before.Labels = nil }},
		{"missing observation clock", func(a *ActionJournal) { a.Before.UpdatedAt = time.Time{} }},
		{"undone without receipt", func(a *ActionJournal) { a.Phase = "undone"; a.UndoOperationID = "td-op-UNDO" }},
		{"undo without identity", func(a *ActionJournal) { a.Phase = "undo_prepared" }},
		{"same undo identity", func(a *ActionJournal) { a.Phase = "undo_prepared"; a.UndoOperationID = a.OperationID }},
		{"creation with before", func(a *ActionJournal) { a.Action = "create" }},
		{"restored before undo", func(a *ActionJournal) { a.Restored = a.Before }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := journalFixture(t)
			tc.edit(&a)
			if _, err := renderActionJournal(a); err == nil {
				t.Fatal("invalid journal accepted")
			}
		})
	}
	comment := journalCommentFixture(t, journalFixture(t))
	for _, tc := range []struct {
		name  string
		body  string
		issue string
	}{
		{"future version", strings.Replace(comment.Body, actionJournalStart, actionJournalPrefix+"v2\n", 1), "gh-1"},
		{"spoofed author", strings.Replace(comment.Body, `"phase":"applied"`, `"author":"spoofed","phase":"applied"`, 1), "gh-1"},
		{"visible edit", "Edited\n" + comment.Body, "gh-1"},
		{"duplicate marker", comment.Body + actionJournalStart, "gh-1"},
		{"trailing object", strings.TrimSuffix(comment.Body, markerEnd) + "{}" + markerEnd, "gh-1"},
		{"foreign issue", comment.Body, "gh-2"},
		{"mixed activity", comment.Body + activityStart, "gh-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := comment
			c.Body = tc.body
			if _, err := decodeActionJournal(c, tc.issue); err == nil {
				t.Fatal("invalid journal accepted")
			}
			if tc.name != "foreign issue" {
				if _, err := decodeActivity(c, tc.issue); err == nil {
					t.Fatal("invalid journal hidden in activity")
				}
			}
		})
	}
}

func TestActionIssueStatePreservesNativeMetadataAndDetectsLaterEdits(t *testing.T) {
	body, err := encodeBody("Description", metadata{Type: "task", Priority: "P1", Details: &IssueDetails{Status: "in_progress"}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(apiIssue{Number: 1, Title: "Issue", Body: body, State: "open", UpdatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	r, err := decodeIssue(data)
	if err != nil {
		t.Fatal(err)
	}
	s, err := actionIssueState(r)
	if err != nil || s.Body != body || s.State != "open" || r.Description == s.Body || string(r.Status) == s.State {
		t.Fatal("lost native body/state", s, r, err)
	}
	before := journalStateFixture(t, "Title")
	reordered := before
	reordered.Labels = []string{"td:open", "topic"}
	if digest, _ := reordered.checksum(); digest != before.Revision {
		t.Fatal("label order changed revision")
	}
	changed := before
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
	if digest, _ := changed.checksum(); digest == before.Revision {
		t.Fatal("later same-content edit was ignored")
	}
	changed = before
	changed.Body = "Concurrent metadata"
	if digest, _ := changed.checksum(); digest == before.Revision {
		t.Fatal("body edit was ignored")
	}
}

func TestActionHistoryUsesTwoBulkCollectionsForAllCarriers(t *testing.T) {
	issues := [][]apiIssue{{}}
	comments := [][]apiComment{{}}
	for n := 1; n <= 60; n++ {
		meta := metadata{Type: "task", Priority: "P2"}
		switch n % 4 {
		case 1:
			meta.EntityKind = "note"
			meta.Note = &NoteDetails{Version: 1}
		case 2:
			meta.EntityKind = "board"
			meta.Board = &BoardDetails{Version: 1, ViewMode: "backlog"}
		case 3:
			deleted := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
			meta.Details = &IssueDetails{Status: "open", DeletedAt: &deleted}
		}
		body, err := encodeBody("Carrier", meta)
		if err != nil {
			t.Fatal(err)
		}
		issues[0] = append(issues[0], apiIssue{Number: n, Title: "Carrier", Body: body, State: "open"})
		a := journalFixture(t)
		a.OperationID = fmt.Sprintf("td-op-%d", n)
		a.IssueID = fmt.Sprintf("gh-%d", n)
		if n%2 == 0 {
			a.SessionID = "ses_other"
		}
		c := journalCommentFixture(t, a)
		c.ID = int64(n)
		c.IssueURL = fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", n)
		c.CreatedAt = c.CreatedAt.Add(time.Duration(n) * time.Second)
		comments[0] = append(comments[0], c)
	}
	client, calls := snapshotFixture(issues, comments, nil)
	s, err := client.ReadSnapshot(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.ActionHistory("")
	if err != nil || len(all) != 60 || all[0].IssueID != "gh-60" {
		t.Fatal(all, err)
	}
	own, err := s.ActionHistory("ses_actor")
	if err != nil || len(own) != 30 || own[0].IssueID != "gh-59" || len(*calls) != 2 {
		t.Fatal(own, err, *calls)
	}
	if _, err := (&Snapshot{}).ActionHistory(""); err == nil {
		t.Fatal("incomplete history accepted")
	}
	duplicate := journalCommentFixture(t, journalFixture(t))
	s.entries[0].Comments = append(s.entries[0].Comments, duplicate, duplicate)
	if _, err := s.ActionHistory("ses_nonexistent"); err == nil {
		t.Fatal("session filter hid duplicate operations")
	}
}
