package ghstore

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBoardCRUDPreservesCarrierAndPrivateObservation(t *testing.T) {
	ctx := context.Background()
	c, issues, writes := hierarchyFixture(t)
	board, err := c.CreateBoard(ctx, " Sprint ", "status != closed", "actual-web")
	if err != nil {
		t.Fatal(err)
	}
	if board.Name != "Sprint" || board.Details.History[0].SessionID != "actual-web" || *writes != 1 {
		t.Fatalf("%+v writes=%d", board, *writes)
	}
	now := time.Now().UTC()
	d := board.Details
	d.ViewMode = "backlog"
	d.LastViewedAt = &now
	d.Positions = []BoardPosition{{IssueID: "gh-1", Position: 4, AddedAt: now}}
	issues[4]["body"] = boardMetadataFixture(t, "human description", d)
	issues[4]["state"] = "closed"
	issues[4]["labels"] = []string{"native-label"}
	board, err = c.GetBoard(ctx, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Public fields must not become a replacement for the private observation.
	board.Details.Positions = nil
	board.Details.ViewMode = "swimlanes"
	query := "priority = P1"
	updated, err := c.UpdateBoardObserved(ctx, board, BoardChanges{Query: &query}, "actual-cli")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Details.Query != query || updated.Details.ViewMode != "backlog" || len(updated.Details.Positions) != 1 || !updated.Details.LastViewedAt.Equal(now) {
		t.Fatalf("configuration lost: %+v", updated.Details)
	}
	if issues[4]["state"] != "closed" || !strings.Contains(issues[4]["body"].(string), "human description") {
		t.Fatalf("carrier lost: %+v", issues[4])
	}
	last := updated.Details.History[len(updated.Details.History)-1]
	if last.Action != "update" || last.SessionID != "actual-cli" {
		t.Fatalf("history %+v", last)
	}
	deleted, err := c.DeleteBoardObserved(ctx, updated, "actual-web", " obsolete ")
	if err != nil {
		t.Fatal(err)
	}
	last = deleted.Details.History[len(deleted.Details.History)-1]
	if deleted.Details.DeletedAt == nil || last.Reason != "obsolete" || last.Action != "delete" || len(deleted.Details.Positions) != 1 || issues[4]["state"] != "closed" {
		t.Fatalf("delete %+v", deleted)
	}
	boards, err := c.ListBoards(ctx)
	if err != nil || len(boards) != 1 || boards[0].ID != "bd-all-issues" {
		t.Fatalf("visible %+v %v", boards, err)
	}
	if *writes != 3 {
		t.Fatalf("writes %d", *writes)
	}
}

func TestBoardWritesValidateBeforeMutation(t *testing.T) {
	ctx := context.Background()
	c, _, writes := hierarchyFixture(t)
	for _, args := range [][3]string{{"", "", "actor"}, {"Sprint", "future_field = x", "actor"}, {"Sprint", "", " "}, {"all issues", "", "actor"}} {
		if _, err := c.CreateBoard(ctx, args[0], args[1], args[2]); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if *writes != 0 {
		t.Fatal("invalid creation wrote")
	}
	b, err := c.CreateBoard(ctx, "Sprint", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	blank, bad := " ", "status ="
	for _, ch := range []BoardChanges{{}, {Name: &blank}, {Query: &bad}} {
		if _, err := c.UpdateBoardObserved(ctx, b, ch, "actor"); err == nil {
			t.Fatalf("accepted %+v", ch)
		}
	}
	if _, err := c.UpdateBoardObserved(ctx, b, BoardChanges{Name: &blank}, ""); err == nil {
		t.Fatal("blank actor accepted")
	}
	builtin, err := c.GetBoard(ctx, "bd-all-issues")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteBoardObserved(ctx, builtin, "actor", ""); err == nil {
		t.Fatal("virtual builtin deleted")
	}
	other := *c
	other.repo = "other/repo"
	if _, err := other.DeleteBoardObserved(ctx, b, "actor", ""); err == nil {
		t.Fatal("foreign observation accepted")
	}
	if *writes != 1 {
		t.Fatalf("invalid mutations wrote: %d", *writes)
	}
}

func TestBoardWriteConflictsBeforeAndAfterSave(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			ctx := context.Background()
			c, issues, writes := hierarchyFixture(t)
			b, err := c.CreateBoard(ctx, "Sprint", "", "actor")
			if err != nil {
				t.Fatal(err)
			}
			base := c.run
			if !after {
				issues[4]["title"] = "Peer edit"
			}
			c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				data, err := base(ctx, dir, payload, args...)
				if after && slices.Contains(args, "PATCH") {
					issues[4]["title"] = "Peer edit"
				}
				return data, err
			}
			q := "status = open"
			_, err = c.UpdateBoardObserved(ctx, b, BoardChanges{Query: &q}, "actor")
			var conflict *ConflictError
			if !errors.As(err, &conflict) || conflict.AfterWrite != after {
				t.Fatalf("conflict %v", err)
			}
			expected := 1
			if after {
				expected = 2
			}
			if *writes != expected {
				t.Fatalf("writes=%d", *writes)
			}
		})
	}
}

func TestBoardWriteTransportFailureDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	c, _, writes := hierarchyFixture(t)
	b, err := c.CreateBoard(ctx, "Sprint", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	base := c.run
	attempts := 0
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") {
			attempts++
			return nil, errors.New("HTTP 403 permission denied")
		}
		return base(ctx, dir, payload, args...)
	}
	_, err = c.DeleteBoardObserved(ctx, b, "actor", "obsolete")
	if err == nil || !strings.Contains(err.Error(), "permission denied") || attempts != 1 || *writes != 1 {
		t.Fatalf("%v attempts=%d writes=%d", err, attempts, *writes)
	}
}

func TestBoardCreationCatalogRaceReportsSavedCarrier(t *testing.T) {
	ctx := context.Background()
	c, issues, writes := hierarchyFixture(t)
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if slices.Contains(args, "POST") {
			issues[5] = map[string]any{"number": 5, "title": "Sprint", "state": "open", "body": boardMetadataFixture(t, "peer", BoardDetails{Version: 1, ViewMode: "swimlanes"})}
		}
		return data, err
	}
	_, err := c.CreateBoard(ctx, "Sprint", "", "actor")
	if err == nil || !strings.Contains(err.Error(), "bd-gh-4 was created") || !strings.Contains(err.Error(), "duplicate") || *writes != 1 {
		t.Fatalf("%v writes=%d", err, *writes)
	}
}

func TestBoardWriteIgnoredResponseCannotReportSuccess(t *testing.T) {
	ctx := context.Background()
	c, _, writes := hierarchyFixture(t)
	b, err := c.CreateBoard(ctx, "Sprint", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	base := c.run
	attempts := 0
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") {
			attempts++
			return base(ctx, dir, nil, "api", "--method", "GET", "--include", "unused", "repos/owner/repo/issues/4")
		}
		return base(ctx, dir, payload, args...)
	}
	q := "status = open"
	_, err = c.UpdateBoardObserved(ctx, b, BoardChanges{Query: &q}, "actor")
	if err == nil || !strings.Contains(err.Error(), "values differed") || attempts != 1 || *writes != 1 {
		t.Fatalf("%v attempts=%d writes=%d", err, attempts, *writes)
	}
}

func TestBoardRevisionUsesPrivateCarrierObservation(t *testing.T) {
	ctx := context.Background()
	c, _, _ := hierarchyFixture(t)
	b, err := c.CreateBoard(ctx, "Sprint", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	revision := b.Revision()
	if len(revision) != 64 || revision == strings.Repeat("0", 64) {
		t.Fatalf("invalid revision %q", revision)
	}
	b.Name = "Public edit"
	b.Details.Query = "status = closed"
	if b.Revision() != revision {
		t.Fatal("public fields changed original revision")
	}
	q := "status = open"
	updated, err := c.UpdateBoardObserved(ctx, b, BoardChanges{Query: &q}, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision() == revision {
		t.Fatal("saved carrier did not change revision")
	}
	virtual := virtualAllIssuesBoard()
	if virtual.Revision() != "virtual" {
		t.Fatal("virtual revision")
	}
}
