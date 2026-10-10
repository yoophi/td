package monitor

import (
	"context"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/testutil"
)

func journalMonitor(t *testing.T) (testutil.GitHubJournalFixture, *GitHubDataSource) {
	t.Helper()
	f := testutil.NewGitHubJournalFixture(t)
	s := NewGitHubDataSource(context.Background(), f.Directory, models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}, "actual-monitor", "main", func(context.Context) (*string, error) { return nil, nil })
	return f, s
}
func TestGitHubMonitorProductionFactoryJournalsCreateEditAndDelete(t *testing.T) {
	f, s := journalMonitor(t)
	msg := s.Fetch("", false, SortByPriority)
	if msg.Error != nil {
		t.Fatal(msg.Error)
	}
	if len(f.Journals(t)) != 0 {
		t.Fatal("read created journals")
	}
	id, err := s.CreateIssue(models.Issue{Title: "Monitor created journal fixture", Type: models.TypeTask, Priority: models.PriorityP2}, nil)
	if err != nil || id != "gh-2" {
		t.Fatal(id, err)
	}
	msg = s.Fetch("", false, SortByPriority)
	if msg.Error != nil {
		t.Fatal(msg.Error)
	}
	handle := msg.Transitions[id].(*githubIssueTransition)
	issue := handle.observed.Issue
	issue.Title = "Monitor updated journal fixture"
	if err := handle.SaveEdit(issue, nil); err != nil {
		t.Fatal(err)
	}
	msg = s.Fetch("", false, SortByPriority)
	if msg.Error != nil {
		t.Fatal(msg.Error)
	}
	handle = msg.Transitions[id].(*githubIssueTransition)
	if err := handle.applyTransition("start", ghstore.TransitionOptions{}); err != nil {
		t.Fatal(err)
	}
	msg = s.Fetch("", false, SortByPriority)
	if msg.Error != nil {
		t.Fatal(msg.Error)
	}
	handle = msg.Transitions[id].(*githubIssueTransition)
	if err := handle.Transition("review"); err != nil {
		t.Fatal(err)
	}
	msg = s.Fetch("", false, SortByPriority)
	if msg.Error != nil {
		t.Fatal(msg.Error)
	}
	handle = msg.Transitions[id].(*githubIssueTransition)
	if err := handle.Approve("", "Fixture implementation self-review", true); err != nil {
		t.Fatal(err)
	}
	_, deleter, err := s.PrepareDelete(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := deleter.Delete(); err != nil {
		t.Fatal(err)
	}
	actions := f.Journals(t)
	if len(actions) != 7 {
		t.Fatal(actions)
	}
	for _, a := range actions {
		if a.Phase != "applied" || a.SessionID != "actual-monitor" {
			t.Fatal(a)
		}
	}
}
func TestGitHubMonitorJournalFailuresPreserveNativeUncertainty(t *testing.T) {
	for _, mode := range []string{"intent-denied", "receipt-denied", "target-denied"} {
		t.Run(mode, func(t *testing.T) {
			f, s := journalMonitor(t)
			t.Setenv("TD_JOURNAL_FAIL", mode)
			id, err := s.CreateIssue(models.Issue{Title: "Monitor attempted journal fixture", Type: models.TypeTask, Priority: models.PriorityP2}, nil)
			if err == nil || id != "" || !strings.Contains(err.Error(), "403") {
				t.Fatal(id, err)
			}
			actions := f.Journals(t)
			want := 1
			if mode == "intent-denied" {
				want = 0
			}
			if len(actions) != want {
				t.Fatal("missing prepared outcome", actions)
			}
			for _, a := range f.Journals(t) {
				if a.Phase != "prepared" {
					t.Fatal(a)
				}
			}
		})
	}
}

func TestGitHubMonitorModelRejectsRetiredContextBeforeJournalWrites(t *testing.T) {
	for _, mode := range []string{"session", "branch", "store"} {
		t.Run(mode, func(t *testing.T) {
			f := testutil.NewGitHubJournalFixture(t)
			m, err := NewGitHubModel(context.Background(), f.Directory, time.Minute, "test")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = m.Close() }()
			source := m.DataSource.(*GitHubDataSource)
			scope, err := ghcontext.Resolve(context.Background(), f.Directory, "owner/repo")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "session":
				_, err = scope.Update(context.Background(), func(state *ghcontext.State) error { scope.NewSession(state); return nil })
			case "branch":
				err = exec.Command("git", "-C", f.Directory, "symbolic-ref", "HEAD", "refs/heads/replaced-branch").Run()
			case "store":
				err = os.WriteFile(filepath.Join(f.Directory, ".todos", "config.json"), []byte(`{"store":"sqlite"}`), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(f.RequestsPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.CreateIssue(models.Issue{Title: "Retired context must not write"}, nil); err == nil || !strings.Contains(err.Error(), "restart") {
				t.Fatal(err)
			}
			boards := m.BoardSource.(*GitHubBoardSource)
			if _, err := boards.open(context.Background()); err == nil {
				t.Fatal("board source accepted retired context")
			}
			if _, err := m.NotesFactory(context.Background()); err == nil {
				t.Fatal("notes accepted retired context")
			}
			after, err := os.ReadFile(f.RequestsPath)
			if err != nil || string(before) != string(after) {
				t.Fatal("retired context reached GitHub", err)
			}
		})
	}
}

func TestGitHubMonitorModelJournalsBoardAndNotesWriters(t *testing.T) {
	f := testutil.NewGitHubJournalFixture(t)
	m, err := NewGitHubModel(context.Background(), f.Directory, time.Minute, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	source := m.BoardSource.(*GitHubBoardSource)
	board, err := source.CreateBoard("Monitor journal board", "status = open")
	if err != nil {
		t.Fatal(err)
	}
	_, editors, err := source.ListBoardsForEditing()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := editors[board.ID].Save("Monitor renamed board", "status = open"); err != nil {
		t.Fatal(err)
	}
	_, editors, err = source.ListBoardsForEditing()
	if err != nil {
		t.Fatal(err)
	}
	if err := editors[board.ID].Delete(); err != nil {
		t.Fatal(err)
	}
	raw, err := m.NotesFactory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	note, err := raw.Create("Monitor journal note", "Original body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.DeleteObserved(note); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	actions := f.Journals(t)
	if len(actions) != 5 {
		t.Fatal(actions)
	}
	for _, a := range actions {
		if a.Phase != "applied" || a.SessionID != m.SessionID {
			t.Fatal(a)
		}
	}
	if actions[0].Action != "create" || actions[1].Action != "board" || actions[2].Action != "board" || actions[3].Action != "create" || actions[4].Action != "note" {
		t.Fatal(actions)
	}
}
