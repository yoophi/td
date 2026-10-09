package ghstore

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestBoardViewsPositionedFirstAndQueryOrderRemainder(t *testing.T) {
	now := time.Now().UTC()
	b := virtualAllIssuesBoard()
	b.Details.Positions = []BoardPosition{{IssueID: "gh-2", Position: 2, AddedAt: now}, {IssueID: "gh-9", Position: 1, AddedAt: now}}
	candidates := []models.Issue{{ID: "gh-3"}, {ID: "gh-2"}, {ID: "gh-1"}, {ID: "gh-4", DeletedAt: &now}}
	views, err := ApplyBoardPositions(&b, candidates)
	if err != nil || len(views) != 3 || views[0].Issue.ID != "gh-2" || !views[0].HasPosition || views[1].Issue.ID != "gh-3" || views[1].HasPosition || views[2].Issue.ID != "gh-1" {
		t.Fatalf("%+v %v", views, err)
	}
	if _, err = ApplyBoardPositions(&b, append(candidates, candidates[0])); err == nil {
		t.Fatal("duplicates accepted")
	}
}

func TestBoardMoveBeforeIncludesUnpositionedPrefixAndKeepsHiddenKeys(t *testing.T) {
	ctx := context.Background()
	c, _, writes := hierarchyFixture(t)
	b, err := c.CreateBoard(ctx, "Sprint", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	b, err = c.SetBoardPositionObserved(ctx, b, "gh-3", 5, "actor")
	if err != nil {
		t.Fatal(err)
	}
	candidates := []models.Issue{{ID: "gh-1"}, {ID: "gh-2"}}
	before := *writes
	b, err = c.MoveBoardBeforeObserved(ctx, b, "gh-2", "", candidates, "actor")
	if err != nil {
		t.Fatal(err)
	}
	if *writes != before+1 || len(b.Details.Positions) != 3 {
		t.Fatalf("move %+v", b.Details.Positions)
	}
	hidden := slices.IndexFunc(b.Details.Positions, func(p BoardPosition) bool { return p.IssueID == "gh-3" })
	if hidden < 0 || b.Details.Positions[hidden].Position != 5 {
		t.Fatal("hidden position changed")
	}
	views, err := ApplyBoardPositions(b, candidates)
	if err != nil || views[0].Issue.ID != "gh-1" || views[1].Issue.ID != "gh-2" || !views[0].HasPosition {
		t.Fatalf("%+v %v", views, err)
	}
	b, err = c.MoveBoardBeforeObserved(ctx, b, "gh-2", "gh-1", candidates, "actor")
	if err != nil {
		t.Fatal(err)
	}
	views, err = ApplyBoardPositions(b, candidates)
	if err != nil || views[0].Issue.ID != "gh-2" || views[1].Issue.ID != "gh-1" {
		t.Fatalf("%+v %v", views, err)
	}
	before = *writes
	for _, ids := range [][2]string{{"gh-9", ""}, {"gh-1", "gh-9"}, {"gh-1", "gh-1"}, {"1", "gh-2"}} {
		if _, err = c.MoveBoardBeforeObserved(ctx, b, ids[0], ids[1], candidates, "actor"); err == nil {
			t.Fatalf("accepted %v", ids)
		}
	}
	if *writes != before {
		t.Fatal("invalid move wrote")
	}
}

func TestBoardMoveLeavesQueryOrderedTailAndRejectsStaleObservation(t *testing.T) {
	ctx := context.Background()
	c, _, writes := hierarchyFixture(t)
	b, err := c.CreateBoard(ctx, "Sprint", "status = open", "actor")
	if err != nil {
		t.Fatal(err)
	}
	candidates := []models.Issue{{ID: "gh-1"}, {ID: "gh-2"}, {ID: "gh-3"}}
	stale := b
	b, err = c.MoveBoardBeforeObserved(ctx, b, "gh-3", "gh-2", candidates, "actor")
	if err != nil {
		t.Fatal(err)
	}
	views, err := ApplyBoardPositions(b, candidates)
	if err != nil || views[0].Issue.ID != "gh-1" || views[1].Issue.ID != "gh-3" || views[2].Issue.ID != "gh-2" || views[2].HasPosition {
		t.Fatalf("%+v %v", views, err)
	}
	before := *writes
	var conflict *ConflictError
	if _, err = c.MoveBoardBeforeObserved(ctx, stale, "gh-2", "gh-1", candidates, "actor"); !errors.As(err, &conflict) || *writes != before {
		t.Fatalf("stale move %v", err)
	}
	b.Query = "status = closed"
	if q, err := c.BoardQueryObserved(b); err != nil || q != "status = open" {
		t.Fatalf("mutable query trusted %q %v", q, err)
	}
	other := *c
	other.repo = "other/repo"
	if _, err := other.BoardQueryObserved(b); err == nil {
		t.Fatal("foreign query observation accepted")
	}
}

func TestBoardMoveRejectsSortKeyOverflowWithoutWriting(t *testing.T) {
	ctx := context.Background()
	c, _, writes := hierarchyFixture(t)
	b, err := c.CreateBoard(ctx, "Sprint", "", "actor")
	if err != nil {
		t.Fatal(err)
	}
	b, err = c.SetBoardPositionObserved(ctx, b, "gh-3", int(^uint(0)>>1), "actor")
	if err != nil {
		t.Fatal(err)
	}
	before := *writes
	if _, err = c.MoveBoardBeforeObserved(ctx, b, "gh-1", "", []models.Issue{{ID: "gh-1"}}, "actor"); err == nil || *writes != before {
		t.Fatalf("overflow %v writes=%d", err, *writes)
	}
}
