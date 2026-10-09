package monitor

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type moveBoardFixture struct {
	viewBoardFixture
	moved, anchor string
	ids           []string
	gets          int
}

func (f *moveBoardFixture) Get(_ context.Context, id string) (*ghstore.Record, error) {
	f.gets++
	for _, r := range f.records {
		if r.ID == id {
			return &r, nil
		}
	}
	return nil, errors.New("task unavailable")
}
func (f *moveBoardFixture) MoveBoardBeforeObserved(_ context.Context, b *ghstore.BoardRecord, id, before string, candidates []models.Issue, actor string) (*ghstore.BoardRecord, error) {
	f.beforeName = b.Name
	f.actor = actor
	if b.Name != f.boards[0].Name {
		return nil, &ghstore.ConflictError{ID: b.ID}
	}
	f.writes++
	f.moved = id
	f.anchor = before
	f.ids = nil
	for _, i := range candidates {
		f.ids = append(f.ids, i.ID)
	}
	return b, f.failure
}
func TestGitHubBoardMoveFrozenObservationVisibleSubsetAndPartialCreation(t *testing.T) {
	f := &moveBoardFixture{viewBoardFixture: viewBoardFixture{boardSourceFixture: boardSourceFixture{
		boards:  []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-1", Name: "Original", ViewMode: "backlog"}, Number: 1, Details: ghstore.BoardDetails{Version: 1, ViewMode: "backlog"}}},
		records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Status: models.StatusOpen}}, {Issue: models.Issue{ID: "gh-2", Status: models.StatusOpen}}, {Issue: models.Issue{ID: "gh-3", Status: models.StatusOpen}}},
	}}}
	opens := 0
	source := &GitHubBoardSource{ctx: context.Background(), actor: "actual-monitor", baseDir: t.TempDir(), open: func(context.Context) (githubBoardClient, error) { opens++; return f, nil }}
	loaded := source.LoadBoard("bd-gh-1", DefaultBoardStatusFilter())
	if loaded.Error != nil {
		t.Fatal(loaded.Error)
	}
	store := loaded.MoveStore
	initial := opens
	for _, ids := range [][]string{{"gh-1", "gh-99"}, {"gh-1", "gh-1"}} {
		if err := store.MoveBefore("gh-1", "", ids); err == nil {
			t.Fatal("invalid observation accepted")
		}
	}
	if opens != initial {
		t.Fatal("invalid move opened writer")
	}
	f.boards[0].Name = "Peer"
	var conflict *ghstore.ConflictError
	if err := store.MoveBefore("gh-1", "gh-3", []string{"gh-1", "gh-3"}); !errors.As(err, &conflict) || f.writes != 0 || f.beforeName != "Original" {
		t.Fatalf("stale move %v", err)
	}
	loaded = source.LoadBoard("bd-gh-1", DefaultBoardStatusFilter())
	if err := loaded.MoveStore.MoveBefore("gh-3", "gh-1", []string{"gh-1", "gh-3"}); err != nil {
		t.Fatal(err)
	}
	if f.actor != "actual-monitor" || !reflect.DeepEqual(f.ids, []string{"gh-1", "gh-3"}) || f.anchor != "gh-1" {
		t.Fatalf("wrong subset %+v", f)
	}
	f.boards[0].Number = 0
	f.boards[0].ID = "bd-all-issues"
	f.boards[0].IsBuiltin = true
	loaded = source.LoadBoard("bd-all-issues", DefaultBoardStatusFilter())
	f.failure = errors.New("write outcome unknown")
	if err := loaded.MoveStore.MoveBefore("gh-1", "", []string{"gh-1", "gh-3"}); err == nil || !strings.Contains(err.Error(), "carrier was created") || f.creates != 1 {
		t.Fatalf("partial %v", err)
	}
}

type modelMoveFixture struct {
	id, anchor string
	ids        []string
	failure    error
}

func (f *modelMoveFixture) MoveBefore(id, anchor string, ids []string) error {
	f.id = id
	f.anchor = anchor
	f.ids = ids
	return f.failure
}
func TestBoardMoveModelAnchorsWaitAndFailure(t *testing.T) {
	for _, tt := range []struct {
		name         string
		direction    int
		edge, anchor string
	}{{"up", -1, "", "gh-1"}, {"down", 1, "", ""}, {"top", 0, "top", "gh-1"}, {"bottom", 0, "bottom", ""}} {
		t.Run(tt.name, func(t *testing.T) {
			f := &modelMoveFixture{}
			m := Model{BoardSource: &GitHubBoardSource{}, TaskListMode: TaskListModeBoard, BoardMode: BoardMode{Board: &models.Board{ID: "bd-gh-9"}, ViewMode: BoardViewBacklog, Cursor: 1, MoveStore: f, Issues: []models.BoardIssueView{{Issue: models.Issue{ID: "gh-1"}}, {Issue: models.Issue{ID: "gh-2"}}, {Issue: models.Issue{ID: "gh-3"}}}}}
			m, cmd := m.moveRemoteBoard(tt.direction, tt.edge)
			if cmd == nil || !m.BoardMovePending || m.BoardMode.Issues[1].Issue.ID != "gh-2" {
				t.Fatal("optimistic mutation or no request")
			}
			if _, duplicate := m.moveRemoteBoard(1, ""); duplicate != nil {
				t.Fatal("duplicate write")
			}
			msg := cmd().(BoardMovedMsg)
			if f.id != "gh-2" || f.anchor != tt.anchor {
				t.Fatalf("%+v", f)
			}
			result, refresh := m.Update(msg)
			m = result.(Model)
			if refresh == nil || m.BoardMovePending || m.BoardMode.MoveStore != nil || m.BoardMode.PendingSelectionID != "gh-2" {
				t.Fatal("missing refresh")
			}
			if _, duplicate := m.Update(msg); duplicate != nil {
				t.Fatal("duplicate reply")
			}
			m.BoardMovePending = true
			m.BoardMode.MoveStore = f
			msg.Error = errors.New("conflict")
			result, refresh = m.Update(msg)
			m = result.(Model)
			if refresh != nil || !m.StatusIsError || m.BoardMode.Issues[1].Issue.ID != "gh-2" {
				t.Fatal("failure lost cards")
			}
		})
	}
}
func TestBoardMoveSwimlaneUsesOnlyCurrentCategory(t *testing.T) {
	f := &modelMoveFixture{}
	m := Model{TaskListMode: TaskListModeBoard, BoardMode: BoardMode{Board: &models.Board{ID: "bd-gh-1"}, ViewMode: BoardViewSwimlanes, MoveStore: f, SwimlaneCursor: 2, SwimlaneRows: []TaskListRow{{Issue: models.Issue{ID: "gh-1"}, Category: "ready"}, {Issue: models.Issue{ID: "gh-2"}, Category: "blocked"}, {Issue: models.Issue{ID: "gh-3"}, Category: "ready"}}}}
	_, cmd := m.moveRemoteBoard(-1, "")
	if cmd == nil {
		t.Fatal("missing move")
	}
	msg := cmd().(BoardMovedMsg)
	if msg.Error != nil || f.anchor != "gh-1" || !reflect.DeepEqual(f.ids, []string{"gh-1", "gh-3"}) {
		t.Fatalf("category crossed %+v", f)
	}
}
