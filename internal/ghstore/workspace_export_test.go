package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestWorkspaceExportUsesOneObservationAndRetainsDeletedEntitiesAndNativeHistory(t *testing.T) {
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	encode := func(meta metadata) string {
		t.Helper()
		body, err := encodeBody("Source content 한글", meta)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	noteBody := encode(metadata{Type: models.TypeTask, Priority: models.PriorityP2, EntityKind: "note",
		Note: &NoteDetails{Version: 1, Pinned: true, Archived: true, DeletedAt: &now}})
	boardBody := encode(metadata{Type: models.TypeTask, Priority: models.PriorityP2, EntityKind: "board",
		Board: &BoardDetails{Version: 1, Query: `id = "gh-1"`, ViewMode: "backlog", DeletedAt: &now,
			LastViewedAt: &now, Positions: []BoardPosition{{IssueID: "gh-1", Position: 7, AddedAt: now}}}})
	taskBody := encode(metadata{Type: models.TypeTask, Priority: models.PriorityP2,
		Details: &IssueDetails{DeletedAt: &now}})
	issues := [][]apiIssue{{{Number: 5, Title: "Deleted note", State: "open", Body: noteBody},
		{Number: 4, PullRequest: json.RawMessage(`{}`)}}, {{Number: 3, Title: "Deleted board", State: "closed", Body: boardBody},
		{Number: 2, Title: "Deleted task", State: "open", Body: taskBody}}, {{Number: 1, Title: "Closed task", State: "closed"}}}
	for i := range issues {
		for j := range issues[i] {
			issues[i][j].User.Login = "original-author"
			issues[i][j].CreatedAt, issues[i][j].UpdatedAt = now, now
		}
	}
	action := journalFixture(t)
	action.Phase, action.After = "prepared", nil
	root := journalCommentFixture(t, action)
	root.IssueURL = "https://api.github.com/repos/owner/repo/issues/1"
	root.UpdatedAt = root.CreatedAt
	comment := apiComment{ID: 42, IssueURL: "https://api.github.com/repos/owner/repo/issues/5", Body: "Original native note comment", CreatedAt: now, UpdatedAt: now}
	comment.User.Login = "note-comment-author"
	client, calls := snapshotFixture(issues, [][]apiComment{{comment}, {root}}, nil)
	workspace, err := client.ExportWorkspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || len(workspace.Tasks) != 2 || len(workspace.Notes) != 1 || len(workspace.Boards) != 1 || len(workspace.Native) != 4 {
		t.Fatal("partial collection or N+1", workspace, *calls)
	}
	if workspace.Tasks[0].Record.Number != 1 || workspace.Tasks[0].NativeState != "closed" || workspace.Tasks[1].Record.DeletedAt == nil {
		t.Fatal("task scope lost closed/deleted data")
	}
	note, board := workspace.Notes[0], workspace.Boards[0]
	if note.ID != "nt-gh-5" || !note.Pinned || !note.Archived || note.DeletedAt == nil || note.Content != "Source content 한글" || board.ID != "bd-gh-3" || board.Details.DeletedAt == nil || board.Query != `id = "gh-1"` || board.Details.Positions[0].Position != 7 {
		t.Fatal("note/board carrier mismatched across reversed pages", note, board)
	}
	if board.LastViewedAt != nil || board.Details.LastViewedAt != nil {
		t.Fatal("device-local view clock included as transferable content")
	}
	for _, native := range workspace.Native {
		if native.Author != "original-author" || !native.CreatedAt.Equal(now) {
			t.Fatal("native attribution lost", native)
		}
		if native.Number == 3 && native.Body != boardBody {
			t.Fatal("original public body was rewritten")
		}
		if native.Number == 1 && (len(native.Comments) != 1 || native.Comments[0].Body != root.Body || native.Comments[0].ID != root.ID) {
			t.Fatal("raw action history was summarized or omitted")
		}
		if native.Number == 5 && (len(native.Comments) != 1 || native.Comments[0].Author != "note-comment-author" || native.Comments[0].Body != comment.Body) {
			t.Fatal("raw native note history lost")
		}
	}
	if len(workspace.Journal.Sources) != 1 || len(workspace.Journal.Sources[0].Actions) != 1 || workspace.Journal.Sources[0].Actions[0].Phase != "prepared" || workspace.Journal.Sources[0].Actions[0].After != nil {
		t.Fatal("uncertain history became completed", workspace.Journal)
	}
	encoded, err := json.Marshal(workspace)
	if err != nil {
		t.Fatal(err)
	}
	var decoded WorkspaceExport
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded.Native, workspace.Native) || decoded.Notes[0].ID != note.ID || decoded.Boards[0].Details.Positions[0].IssueID != "gh-1" {
		t.Fatal("transport round-trip lost source data", err)
	}
}

func TestWorkspaceExportFailureCancellationAndEmptyDoNotReturnPartialData(t *testing.T) {
	cause := errors.New("HTTP 403: original API rate-limit diagnostic")
	client, calls := snapshotFixture([][]apiIssue{{{Number: 1, State: "open"}}}, [][]apiComment{{}}, cause)
	if result, err := client.ExportWorkspace(context.Background()); !errors.Is(err, cause) || result != nil {
		t.Fatal("failed activity collection exposed partial source", result, err)
	}
	*calls = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := client.ExportWorkspace(ctx); !errors.Is(err, context.Canceled) || result != nil || len(*calls) != 0 {
		t.Fatal("cancelled export read or returned data", result, err, *calls)
	}
	client, calls = snapshotFixture([][]apiIssue{{}}, [][]apiComment{{}}, nil)
	result, err := client.ExportWorkspace(context.Background())
	if err != nil || len(*calls) != 1 || result.Tasks == nil || result.Notes == nil || result.Boards == nil || result.Native == nil || result.Journal == nil || len(result.Native) != 0 {
		t.Fatal("empty source contract", result, err, *calls)
	}
}
