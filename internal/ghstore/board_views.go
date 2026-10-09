package ghstore

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/marcus/td/internal/models"
)

// ApplyBoardPositions preserves query order for unpositioned tasks. Positions
// outside the supplied candidates remain configuration, not visible tasks.
func ApplyBoardPositions(board *BoardRecord, candidates []models.Issue) ([]models.BoardIssueView, error) {
	if board == nil {
		return nil, &WorkflowInputError{Reason: "board is required"}
	}
	if err := board.Details.validate(); err != nil {
		return nil, err
	}
	positions := map[string]int{}
	for _, p := range board.Details.Positions {
		positions[p.IssueID] = p.Position
	}
	positioned := []models.BoardIssueView{}
	unpositioned := []models.BoardIssueView{}
	seen := map[string]bool{}
	for _, i := range candidates {
		if seen[i.ID] {
			return nil, fmt.Errorf("board candidates repeat %s", i.ID)
		}
		seen[i.ID] = true
		if i.DeletedAt != nil {
			continue
		}
		v := models.BoardIssueView{BoardID: board.ID, Issue: i}
		if p, ok := positions[i.ID]; ok {
			v.Position = p
			v.HasPosition = true
			positioned = append(positioned, v)
		} else {
			unpositioned = append(unpositioned, v)
		}
	}
	slices.SortFunc(positioned, func(a, b models.BoardIssueView) int {
		if a.Position < b.Position {
			return -1
		}
		if a.Position > b.Position {
			return 1
		}
		return 0
	})
	return append(positioned, unpositioned...), nil
}

// MoveBoardBeforeObserved accepts the adapter's current query candidates. The
// adapter must load these from this repository using the board's original query.
// Empty beforeID means the end; candidates below the positioned prefix retain
// query order. Hidden positions are retained without changing their sort keys.
func (c *Client) MoveBoardBeforeObserved(ctx context.Context, board *BoardRecord, issueID, beforeID string, candidates []models.Issue, actor string) (*BoardRecord, error) {
	for index, id := range []string{issueID, beforeID} {
		if index == 1 && id == "" {
			continue
		}
		n, err := Number(id)
		if err != nil || id != fmt.Sprintf("gh-%d", n) {
			return nil, &WorkflowInputError{Reason: "board move requires canonical gh-N issue IDs"}
		}
	}
	if issueID == beforeID {
		return nil, &WorkflowInputError{Reason: "cannot move a task before itself"}
	}
	return c.writeBoardObserved(ctx, board, BoardChanges{}, false, actor, "", &boardConfigMutation{action: "move", apply: func(d *BoardDetails) error {
		views, err := ApplyBoardPositions(&BoardRecord{Board: board.Board, Details: *d}, candidates)
		if err != nil {
			return err
		}
		order := []string{}
		found := false
		existing := map[string]BoardPosition{}
		maxKey := 0
		for _, p := range d.Positions {
			existing[p.IssueID] = p
			maxKey = max(maxKey, p.Position)
		}
		for _, v := range views {
			if v.Issue.ID == issueID {
				found = true
			} else {
				order = append(order, v.Issue.ID)
			}
		}
		index := len(order)
		if beforeID != "" {
			index = slices.Index(order, beforeID)
		}
		if !found || index < 0 {
			return workflowStateError("board changed: moved task or anchor no longer matches; refresh before retrying")
		}
		order = slices.Insert(order, index, issueID)
		// Position all tasks above the insertion and all previously positioned
		// candidates. Leave the remainder following the TDQ order.
		chosen := []string{}
		for i, id := range order {
			_, saved := existing[id]
			if i <= index || saved {
				chosen = append(chosen, id)
			}
		}
		const gap = 65536
		if len(chosen) > (int(^uint(0)>>1)-maxKey)/gap {
			return &WorkflowInputError{Reason: "board sort keys exhausted; refresh and repair positions"}
		}
		now := time.Now().UTC()
		for _, id := range chosen {
			if _, err := c.Get(ctx, id); err != nil {
				return fmt.Errorf("verify moved board prefix target %s (no write attempted): %w", id, err)
			}
			maxKey += gap
			p, ok := existing[id]
			if !ok {
				p = BoardPosition{IssueID: id, AddedAt: now}
			}
			p.Position = maxKey
			existing[id] = p
		}
		d.Positions = d.Positions[:0]
		for _, p := range existing {
			d.Positions = append(d.Positions, p)
		}
		slices.SortFunc(d.Positions, func(a, b BoardPosition) int {
			if a.Position < b.Position {
				return -1
			}
			if a.Position > b.Position {
				return 1
			}
			return 0
		})
		return nil
	}})
}

// BoardQueryObserved uses the authoritative private query, not mutable exported
// fields. Virtual builtin reads have only the canonical empty query.
func (c *Client) BoardQueryObserved(board *BoardRecord) (string, error) {
	if board == nil {
		return "", &WorkflowInputError{Reason: "board observation is required"}
	}
	if board.Number == 0 && board.ID == "bd-all-issues" && board.IsBuiltin && board.observed.repository == c.repo {
		return "", nil
	}
	original := board.observed
	if original.repository != c.repo || original.revision == ([32]byte{}) || original.Number != board.Number {
		return "", &WorkflowInputError{Reason: "board observation is not from this repository"}
	}
	before, err := boardRecord(&original)
	if err != nil {
		return "", err
	}
	if before.ID != board.ID || before.Details.DeletedAt != nil {
		return "", workflowStateError("invalid or deleted board observation")
	}
	return before.Query, nil
}
