package monitor

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type boardSourceFixture struct {
	boards  []ghstore.BoardRecord
	records []ghstore.Record
	fail    error
	fresh   bool
}

func (f *boardSourceFixture) ListBoards(ctx context.Context) ([]ghstore.BoardRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.boards, f.fail
}
func (f *boardSourceFixture) GetBoard(context.Context, string) (*ghstore.BoardRecord, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	return &f.boards[0], nil
}
func (f *boardSourceFixture) List(ctx context.Context, _ bool) ([]ghstore.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.records, f.fail
}
func (f *boardSourceFixture) BoardQueryObserved(*ghstore.BoardRecord) (string, error) { return "", nil }
func (f *boardSourceFixture) ListActivity(context.Context, string) ([]models.Activity, error) {
	return nil, nil
}
func (f *boardSourceFixture) AppendActivity(context.Context, string, models.Activity) (*models.Activity, error) {
	panic("board read wrote activity")
}
func (f *boardSourceFixture) ObserveMonitorReview(context.Context, *ghstore.Record, string) (*ghstore.MonitorReviewFacts, error) {
	return &ghstore.MonitorReviewFacts{Fresh: f.fresh}, f.fail
}

func TestGitHubBoardSourceClassificationWithoutSQLite(t *testing.T) {
	now := time.Now().UTC()
	f := &boardSourceFixture{fresh: true, boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-9", Name: "Sprint", LastViewedAt: &now}, Details: ghstore.BoardDetails{Version: 1, ViewMode: "backlog", Positions: []ghstore.BoardPosition{{IssueID: "gh-2", Position: 1, AddedAt: now}}}}}, records: []ghstore.Record{
		{Issue: models.Issue{ID: "gh-1", Priority: models.PriorityP0, Status: models.StatusOpen}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-3"}}},
		{Issue: models.Issue{ID: "gh-2", Priority: models.PriorityP2, Status: models.StatusInProgress}, Details: &ghstore.IssueDetails{Transitions: []ghstore.TransitionRecord{{Action: "reject", At: now}}}},
		{Issue: models.Issue{ID: "gh-3", Priority: models.PriorityP1, Status: models.StatusInReview, ImplementerSession: "other"}},
		{Issue: models.Issue{ID: "gh-4", Priority: models.PriorityP2, Status: models.StatusClosed}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &GitHubBoardSource{ctx: ctx, baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (githubBoardClient, error) { return f, nil }}
	msg := source.LoadBoard("bd-gh-9", DefaultBoardStatusFilter())
	if msg.Error != nil || len(msg.Issues) != 3 || msg.Issues[0].Issue.ID != "gh-2" {
		t.Fatalf("%+v", msg)
	}
	categories := map[string]string{}
	for _, v := range msg.Issues {
		categories[v.Issue.ID] = v.Category
	}
	if categories["gh-1"] != string(CategoryBlocked) || categories["gh-2"] != string(CategoryNeedsRework) || categories["gh-3"] != string(CategoryReviewable) {
		t.Fatal(categories)
	}
	data := GroupBoardIssues(msg.Issues, SortByPriority)
	if len(data.Blocked) != 1 || len(data.NeedsRework) != 1 || len(data.Reviewable) != 1 {
		t.Fatalf("%+v", data)
	}
	f.fresh = false
	msg = source.LoadBoard("bd-gh-9", DefaultBoardStatusFilter())
	found := false
	for _, v := range msg.Issues {
		if v.Issue.ID == "gh-3" && v.Category == string(CategoryPendingOther) {
			found = true
		}
	}
	if msg.Error != nil || !found {
		t.Fatalf("stale %+v", msg)
	}
	last, err := source.LastViewedBoard()
	if err != nil || last != nil {
		t.Fatalf("%+v %v", last, err)
	}
	f.records[0].Details.Dependencies = []string{"gh-99"}
	if msg = source.LoadBoard("bd-gh-9", DefaultBoardStatusFilter()); msg.Error == nil || msg.Issues != nil {
		t.Fatal("missing dependency hidden")
	}
	cancel()
	if _, err = source.ListBoards(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
}

func TestBoardSourceModelReadsAndErrorsDoNotTouchSQLite(t *testing.T) {
	f := &boardSourceFixture{boards: []ghstore.BoardRecord{{Board: models.Board{ID: "bd-gh-9"}, Details: ghstore.BoardDetails{Version: 1, ViewMode: "backlog"}}}, records: []ghstore.Record{{Issue: models.Issue{ID: "gh-1", Status: models.StatusOpen, Priority: models.PriorityP2}}}}
	source := &GitHubBoardSource{ctx: context.Background(), preferences: testMonitorPreferences(t), baseDir: t.TempDir(), actor: "actual-monitor", open: func(context.Context) (githubBoardClient, error) { return f, nil }}
	if err := source.preferences.Update(func(p *monitorPreferences) { p.LastBoardID = "bd-gh-9" }); err != nil {
		t.Fatal(err)
	}
	m := Model{SessionID: "actual-monitor", BoardSource: source, BoardMode: BoardMode{Board: &models.Board{ID: "bd-gh-9"}, StatusFilter: DefaultBoardStatusFilter()}}
	if msg := m.fetchBoards()().(BoardsDataMsg); msg.Error != nil || len(msg.Boards) != 1 {
		t.Fatalf("%+v", msg)
	}
	msg := m.fetchBoardIssues("bd-gh-9")().(BoardIssuesMsg)
	result, _ := m.Update(msg)
	m = result.(Model)
	if len(m.BoardMode.Issues) != 1 || len(m.BoardMode.SwimlaneData.Ready) != 1 {
		t.Fatalf("%+v", m.BoardMode)
	}
	f.fail = errors.New("gh unavailable")
	failed := m.fetchBoardIssues("bd-gh-9")().(BoardIssuesMsg)
	result, _ = m.Update(failed)
	m = result.(Model)
	if !m.StatusIsError || len(m.BoardMode.Issues) != 1 {
		t.Fatal("failed refresh cleared saved cards")
	}
	restored := m.restoreLastViewedBoard()().(BoardsDataMsg)
	if restored.Error == nil {
		t.Fatal("restore failure hidden")
	}
}

func TestBoardReadSourceRejectsUnconnectedTUIWrites(t *testing.T) {
	source := &GitHubBoardSource{}
	m := Model{TaskListMode: TaskListModeBoard, BoardSource: source, BoardMode: BoardMode{Board: &models.Board{ID: "bd-gh-9"}}}
	for _, action := range []func() (Model, tea.Cmd){func() (Model, tea.Cmd) { return m.moveIssueInBoard(1) }, func() (Model, tea.Cmd) { return m.moveIssueInBacklog(1) }, func() (Model, tea.Cmd) { return m.moveIssueInSwimlane(1) }, m.moveIssueToTop, m.moveIssueToBottom} {
		next, cmd := action()
		if !next.StatusIsError || cmd != nil {
			t.Fatal("unconnected write did not fail explicitly")
		}
	}
	preview := m.boardEditorQueryPreview("status = open")().(BoardEditorQueryPreviewMsg)
	if preview.Error == nil {
		t.Fatal("unconnected preview used SQLite")
	}
}
