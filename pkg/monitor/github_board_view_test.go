package monitor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type viewBoardFixture struct {
	boardSourceFixture
	actor           string
	writes, creates int
	failure         error
	beforeName      string
}

func (f *viewBoardFixture) SetBoardViewModeObserved(_ context.Context, b *ghstore.BoardRecord, mode, actor string) (*ghstore.BoardRecord, error) {
	f.beforeName = b.Name
	f.actor = actor
	if b.Name != f.boards[0].Name {
		return nil, &ghstore.ConflictError{ID: b.ID}
	}
	f.writes++
	if f.failure != nil {
		return nil, f.failure
	}
	saved := *b
	saved.ViewMode = mode
	saved.Details.ViewMode = mode
	f.boards[0] = saved
	return &saved, nil
}
func (f *viewBoardFixture) MaterializeBuiltinBoardObserved(_ context.Context, b *ghstore.BoardRecord, actor string) (*ghstore.BoardRecord, error) {
	f.creates++
	saved := *b
	saved.Number = 9
	f.boards[0] = saved
	return &saved, nil
}

func TestGitHubBoardViewUsesOriginalObservationAndPreservesFailure(t *testing.T) {
	f := &viewBoardFixture{boardSourceFixture: boardSourceFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-1", Name: "Original", ViewMode: "swimlanes"}, Number: 1, Details: ghstore.BoardDetails{Version: 1, ViewMode: "swimlanes"}}}}}
	source := &GitHubBoardSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (githubBoardClient, error) { return f, nil }}
	msg := source.LoadBoard("bd-gh-1", DefaultBoardStatusFilter())
	if msg.Error != nil {
		t.Fatal(msg.Error)
	}
	f.boards[0].Name = "Peer"
	var conflict *ghstore.ConflictError
	if _, _, err := msg.ViewStore.SetViewMode("backlog"); !errors.As(err, &conflict) || f.writes != 0 || f.beforeName != "Original" {
		t.Fatalf("stale %v", err)
	}
	msg = source.LoadBoard("bd-gh-1", DefaultBoardStatusFilter())
	saved, next, err := msg.ViewStore.SetViewMode("backlog")
	if err != nil || saved.ViewMode != "backlog" || next == nil || f.actor != "actual-monitor" {
		t.Fatalf("%+v %v", saved, err)
	}
	saved, _, err = next.SetViewMode("swimlanes")
	if err != nil || saved.ViewMode != "swimlanes" {
		t.Fatalf("new observation %+v %v", saved, err)
	}
	f.boards[0].Number = 0
	f.boards[0].ID = "bd-all-issues"
	f.boards[0].IsBuiltin = true
	msg = source.LoadBoard("bd-all-issues", DefaultBoardStatusFilter())
	f.failure = errors.New("permission denied")
	_, _, err = msg.ViewStore.SetViewMode("backlog")
	if err == nil || f.creates != 1 || !strings.Contains(err.Error(), "carrier was created") {
		t.Fatalf("partial %v", err)
	}
}

func TestBoardViewModelWaitsForSaveAndRejectsStaleReplies(t *testing.T) {
	f := &viewBoardFixture{boardSourceFixture: boardSourceFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-1", Name: "Board", ViewMode: "swimlanes"}, Number: 1, Details: ghstore.BoardDetails{Version: 1, ViewMode: "swimlanes"}}}}}
	source := &GitHubBoardSource{ctx: context.Background(), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (githubBoardClient, error) { return f, nil }}
	m := Model{BoardSource: source, TaskListMode: TaskListModeBoard, BoardMode: BoardMode{Board: &f.boards[0].Board}}
	loaded := source.LoadBoard("bd-gh-1", DefaultBoardStatusFilter())
	result, _ := m.Update(loaded)
	m = result.(Model)
	m.BoardMode.Issues = []models.BoardIssueView{{Issue: models.Issue{ID: "gh-1"}}, {Issue: models.Issue{ID: "gh-2"}}}
	m.BoardMode.SwimlaneRows = []TaskListRow{{Issue: models.Issue{ID: "gh-1"}}, {Issue: models.Issue{ID: "gh-2"}}}
	m.BoardMode.SwimlaneCursor = 1
	m, cmd := m.toggleBoardView()
	if cmd == nil || !m.BoardViewPending || m.BoardMode.ViewMode != BoardViewSwimlanes {
		t.Fatal("changed before save")
	}
	_, duplicate := m.toggleBoardView()
	if duplicate != nil {
		t.Fatal("duplicate write started")
	}
	saved := cmd().(BoardViewSavedMsg)
	result, _ = m.Update(saved)
	m = result.(Model)
	if m.BoardViewPending || m.BoardMode.ViewMode != BoardViewBacklog || m.BoardMode.Cursor != 1 {
		t.Fatal("successful save not applied")
	}
	f.failure = errors.New("write uncertain; inspect before retrying")
	m, cmd = m.toggleBoardView()
	saved = cmd().(BoardViewSavedMsg)
	result, _ = m.Update(saved)
	m = result.(Model)
	if !m.StatusIsError || m.BoardMode.ViewMode != BoardViewBacklog || m.BoardViewPending {
		t.Fatal("failed save changed view")
	}
	f.failure = nil
	m, cmd = m.toggleBoardView()
	saved = cmd().(BoardViewSavedMsg)
	// An intervening refresh must not be overwritten by the older reply.
	refreshed := source.LoadBoard("bd-gh-1", DefaultBoardStatusFilter())
	result, _ = m.Update(refreshed)
	m = result.(Model)
	generation := m.BoardMode.ViewGeneration
	result, refresh := m.Update(saved)
	m = result.(Model)
	if refresh == nil || m.BoardMode.ViewGeneration != generation {
		t.Fatal("stale reply replaced fresh observation")
	}
	old := saved
	old.Request = 0
	old.Error = errors.New("old failure")
	result, _ = m.Update(old)
	if result.(Model).StatusIsError {
		t.Fatal("unrelated reply overrode status")
	}
}
