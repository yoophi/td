package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/td/internal/models"
	"slices"
	"strings"
	"testing"
	"time"
)

func boardMetadataFixture(t *testing.T, name string, details BoardDetails) string {
	t.Helper()
	body, err := encodeBody(name, metadata{EntityKind: "board", Type: models.TypeTask, Priority: models.PriorityP2, Board: &details})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
func TestBoardMetadataRoundTripAndInvalidBoundaries(t *testing.T) {
	now := time.Now().UTC()
	valid := BoardDetails{Version: 1, Query: "status != closed", ViewMode: "backlog", LastViewedAt: &now, Positions: []BoardPosition{{IssueID: "gh-7", Position: 3, AddedAt: now}}, History: []BoardEvent{{OperationID: "op1", Action: "create", SessionID: "web", At: now}}}
	body := boardMetadataFixture(t, "Sprint", valid)
	_, meta, managed, err := decodeBody(body)
	if err != nil || !managed || meta.Board == nil || meta.Board.Query != valid.Query || meta.Board.Positions[0].IssueID != "gh-7" || !meta.Board.LastViewedAt.Equal(now) {
		t.Fatalf("%+v %v", meta, err)
	}
	for _, change := range []func(*BoardDetails){
		func(d *BoardDetails) { d.Version = 2 }, func(d *BoardDetails) { d.ViewMode = "unknown" }, func(d *BoardDetails) { d.Query = "status =" }, func(d *BoardDetails) { d.Query = "future_field = x" },
		func(d *BoardDetails) { d.Builtin = true }, func(d *BoardDetails) { d.Positions[0].IssueID = "#7" }, func(d *BoardDetails) { d.Positions[0].Position = -1 },
		func(d *BoardDetails) { d.Positions = append(d.Positions, d.Positions[0]) }, func(d *BoardDetails) {
			d.Positions = append(d.Positions, BoardPosition{IssueID: "gh-8", Position: 3, AddedAt: now})
		},
		func(d *BoardDetails) { d.History[0].SessionID = " " }, func(d *BoardDetails) { d.History[0].Action = "unknown" }, func(d *BoardDetails) { d.History = append(d.History, d.History[0]) },
	} {
		raw, err := json.Marshal(valid)
		if err != nil {
			t.Fatal(err)
		}
		var d BoardDetails
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		change(&d)
		if _, err := encodeBody("", metadata{EntityKind: "board", Type: models.TypeTask, Priority: models.PriorityP2, Board: &d}); err == nil {
			t.Fatalf("accepted %+v", d)
		}
	}
	for _, meta := range []metadata{
		{Type: models.TypeTask, Priority: models.PriorityP2, Board: &valid},
		{EntityKind: "note", Type: models.TypeTask, Priority: models.PriorityP2, Board: &valid},
		{EntityKind: "board", Type: models.TypeTask, Priority: models.PriorityP2, Board: &valid, Details: &IssueDetails{}},
	} {
		if _, err := encodeBody("", meta); err == nil {
			t.Fatal("mixed entity payload accepted")
		}
	}
	invalid := strings.Replace(body, `"view_mode":`, `"future":true,"view_mode":`, 1)
	if _, _, _, err := decodeBody(invalid); err == nil {
		t.Fatal("unknown nested board field accepted")
	}
}
func TestBoardReadsSeparateEntitiesPreserveNativeNameAndNeverWrite(t *testing.T) {
	ctx := context.Background()
	c, issues, writes := hierarchyFixture(t)
	now := time.Now().UTC()
	issues[1]["title"] = "Sprint"
	issues[1]["body"] = boardMetadataFixture(t, "Config", BoardDetails{Version: 1, ViewMode: "backlog", Query: "status != closed", Positions: []BoardPosition{{IssueID: "gh-3", Position: 2, AddedAt: now}}})
	issues[1]["state"] = "closed" // Carrier archive state does not erase config.
	issues[2]["title"] = "Deleted"
	issues[2]["body"] = boardMetadataFixture(t, "Config", BoardDetails{Version: 1, ViewMode: "swimlanes", DeletedAt: &now})
	boards, err := c.ListBoards(ctx)
	if err != nil || len(boards) != 2 || boards[0].ID != "bd-all-issues" || boards[0].Number != 0 || boards[1].ID != "bd-gh-1" || boards[1].Name != "Sprint" || *writes != 0 {
		t.Fatalf("%+v %v writes=%d", boards, err, *writes)
	}
	board, err := c.GetBoard(ctx, "sPrInT")
	if err != nil || board.ID != "bd-gh-1" || board.ViewMode != "backlog" {
		t.Fatalf("%+v %v", board, err)
	}
	board.Details.Positions[0].IssueID = "gh-99"
	if board.observed.meta.Board.Positions[0].IssueID != "gh-3" {
		t.Fatal("public details mutated private observation")
	}
	if _, err := c.GetBoard(ctx, "bd-gh-1"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bd-gh-2", "bd-gh-3", "bd-gh-01"} {
		if _, err := c.GetBoard(ctx, id); err == nil {
			t.Fatalf("invalid/deleted/non-board %s accepted", id)
		}
	}
	listed, err := c.List(ctx, true)
	if err != nil || len(listed) != 1 || listed[0].ID != "gh-3" {
		t.Fatalf("board leaked into tasks: %+v %v", listed, err)
	}
	if _, err := c.Get(ctx, "1"); err == nil || !strings.Contains(err.Error(), "board entity") {
		t.Fatal("board allowed as task")
	}
	if *writes != 0 {
		t.Fatal("read created builtin carrier")
	}
}
func TestBoardReadsRejectDuplicateBuiltinLegacyAndAmbiguousNames(t *testing.T) {
	for _, mode := range []string{"builtin", "duplicate-builtin", "ambiguous", "legacy", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			c, issues, _ := hierarchyFixture(t)
			d := BoardDetails{Version: 1, ViewMode: "swimlanes"}
			if mode == "builtin" || mode == "duplicate-builtin" {
				d.Builtin = true
				issues[1]["title"] = "All Issues"
			} else {
				issues[1]["title"] = "Sprint"
			}
			issues[1]["body"] = boardMetadataFixture(t, "", d)
			switch mode {
			case "duplicate-builtin":
				issues[2]["title"] = "All Issues"
				issues[2]["body"] = issues[1]["body"]
			case "ambiguous":
				issues[2]["title"] = "sprint"
				issues[2]["body"] = issues[1]["body"]
			case "legacy":
				body, err := encodeBody("", metadata{EntityKind: "board", Type: models.TypeTask, Priority: models.PriorityP2})
				if err != nil {
					t.Fatal(err)
				}
				issues[1]["body"] = body
			case "malformed":
				issues[1]["body"] = strings.Replace(issues[1]["body"].(string), `"version":1`, `"version":2`, 1)
			}
			if mode == "builtin" {
				boards, err := c.ListBoards(context.Background())
				if err != nil || len(boards) != 1 || boards[0].Number != 1 || boards[0].ID != "bd-all-issues" {
					t.Fatalf("%+v %v", boards, err)
				}
				return
			}
			if mode == "ambiguous" {
				if _, err := c.GetBoard(context.Background(), "Sprint"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
					t.Fatal("ambiguous name silently selected")
				}
				return
			}
			if boards, err := c.ListBoards(context.Background()); err == nil || boards != nil {
				t.Fatalf("invalid config hidden: %+v %v", boards, err)
			}
		})
	}
	c, _, _ := hierarchyFixture(t)
	c.run = func(context.Context, string, []byte, ...string) ([]byte, error) {
		return nil, errors.New("permission denied")
	}
	if boards, err := c.ListBoards(context.Background()); err == nil || boards != nil {
		t.Fatal("permission failure became virtual success")
	}
}
func TestBoardListRejectsRepeatedPagesAndSkipsPullRequests(t *testing.T) {
	body := boardMetadataFixture(t, "", BoardDetails{Version: 1, ViewMode: "swimlanes"})
	duplicate := false
	c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		if !slices.Contains(args, "--paginate") {
			t.Fatal("board read did not paginate")
		}
		items := []apiIssue{{Number: 1, Title: "Board", State: "open", Body: body}, {Number: 2, PullRequest: json.RawMessage(`{}`), Body: "malformed ignored PR"}}
		pages := [][]apiIssue{items}
		if duplicate {
			pages = append(pages, items)
		}
		return json.Marshal(pages)
	}}
	if boards, err := c.ListBoards(context.Background()); err != nil || len(boards) != 2 {
		t.Fatalf("%+v %v", boards, err)
	}
	duplicate = true
	if _, err := c.ListBoards(context.Background()); err == nil {
		t.Fatal("repeated carrier pages accepted")
	}
}

func TestBoardKnownIDPrecedesAConflictingName(t *testing.T) {
	c, issues, _ := hierarchyFixture(t)
	issues[1]["title"] = "bd-all-issues"
	issues[1]["body"] = boardMetadataFixture(t, "", BoardDetails{Version: 1, ViewMode: "swimlanes"})
	board, err := c.GetBoard(context.Background(), "bd-all-issues")
	if err != nil || !board.IsBuiltin || board.Number != 0 {
		t.Fatalf("name shadowed builtin ID: %+v %v", board, err)
	}
}
