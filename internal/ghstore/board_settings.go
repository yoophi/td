package ghstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

type boardConfigMutation struct {
	action string
	apply  func(*BoardDetails) error
}

// UpdateBoardConfigObserved saves an explicit CLI configuration edit in one
// carrier write, including combined name/query/view-mode changes.
func (c *Client) UpdateBoardConfigObserved(ctx context.Context, board *BoardRecord, changes BoardChanges, mode *string, actor string) (*BoardRecord, error) {
	if mode == nil {
		return c.UpdateBoardObserved(ctx, board, changes, actor)
	}
	if *mode != "swimlanes" && *mode != "backlog" {
		return nil, &WorkflowInputError{Reason: "board view mode must be swimlanes or backlog"}
	}
	return c.writeBoardObserved(ctx, board, changes, false, actor, "", &boardConfigMutation{action: "set_view_mode", apply: func(d *BoardDetails) error { d.ViewMode = *mode; return nil }})
}

func (c *Client) SetBoardViewModeObserved(ctx context.Context, board *BoardRecord, mode, actor string) (*BoardRecord, error) {
	return c.UpdateBoardConfigObserved(ctx, board, BoardChanges{}, &mode, actor)
}

func (c *Client) MarkBoardViewedObserved(ctx context.Context, board *BoardRecord, actor string) (*BoardRecord, error) {
	return c.writeBoardObserved(ctx, board, BoardChanges{}, false, actor, "", &boardConfigMutation{action: "view", apply: func(d *BoardDetails) error { now := time.Now().UTC(); d.LastViewedAt = &now; return nil }})
}

func (c *Client) SetBoardPositionObserved(ctx context.Context, board *BoardRecord, issueID string, position int, actor string) (*BoardRecord, error) {
	number, err := Number(issueID)
	if err != nil || issueID != fmt.Sprintf("gh-%d", number) || position < 0 {
		return nil, &WorkflowInputError{Reason: "board position requires a canonical gh-N issue and nonnegative sort key"}
	}
	return c.writeBoardObserved(ctx, board, BoardChanges{}, false, actor, "", &boardConfigMutation{action: "set_position", apply: func(d *BoardDetails) error {
		if _, err := c.Get(ctx, issueID); err != nil {
			return fmt.Errorf("verify board position target (no write attempted): %w", err)
		}
		for _, p := range d.Positions {
			if p.IssueID != issueID && p.Position == position {
				return workflowStateError("board sort key %d is occupied by %s", position, p.IssueID)
			}
		}
		p := BoardPosition{IssueID: issueID, Position: position, AddedAt: time.Now().UTC()}
		for i, old := range d.Positions {
			if old.IssueID == issueID {
				d.Positions[i] = p
				return nil
			}
		}
		d.Positions = append(d.Positions, p)
		return nil
	}})
}

// Removing a saved position deliberately permits a missing/deleted target so
// stale configuration can be cleaned up without resurrecting the task.
func (c *Client) RemoveBoardPositionObserved(ctx context.Context, board *BoardRecord, issueID, actor string) (*BoardRecord, error) {
	number, err := Number(issueID)
	if err != nil || issueID != fmt.Sprintf("gh-%d", number) {
		return nil, &WorkflowInputError{Reason: "board position requires a canonical gh-N issue"}
	}
	return c.writeBoardObserved(ctx, board, BoardChanges{}, false, actor, "", &boardConfigMutation{action: "remove_position", apply: func(d *BoardDetails) error {
		i := slices.IndexFunc(d.Positions, func(p BoardPosition) bool { return p.IssueID == issueID })
		if i < 0 {
			return workflowStateError("issue %s has no saved board position", issueID)
		}
		d.Positions = slices.Delete(d.Positions, i, i+1)
		return nil
	}})
}

// MoveBoardPositionObserved inserts among saved positions at a one-based slot.
// It assigns positive sparse keys in one carrier write, avoiding partial respace
// writes and integer overflow. Query-ordered unpositioned tasks are handled by
// the adapter, not by this saved-position operation.
func (c *Client) MoveBoardPositionObserved(ctx context.Context, board *BoardRecord, issueID string, slot int, actor string) (*BoardRecord, error) {
	number, err := Number(issueID)
	if err != nil || issueID != fmt.Sprintf("gh-%d", number) || slot < 1 {
		return nil, &WorkflowInputError{Reason: "board move requires a canonical gh-N issue and a positive slot"}
	}
	return c.writeBoardObserved(ctx, board, BoardChanges{}, false, actor, "", &boardConfigMutation{action: "move", apply: func(d *BoardDetails) error {
		if _, err := c.Get(ctx, issueID); err != nil {
			return fmt.Errorf("verify board move target (no write attempted): %w", err)
		}
		positions := slices.DeleteFunc(d.Positions, func(p BoardPosition) bool { return p.IssueID == issueID })
		slices.SortFunc(positions, func(a, b BoardPosition) int {
			if a.Position < b.Position {
				return -1
			}
			if a.Position > b.Position {
				return 1
			}
			return 0
		})
		index := min(slot-1, len(positions))
		positions = slices.Insert(positions, index, BoardPosition{IssueID: issueID, AddedAt: time.Now().UTC()})
		const gap = 65536
		if len(positions) > int(^uint(0)>>1)/gap {
			return &WorkflowInputError{Reason: "board position capacity exceeded"}
		}
		for i := range positions {
			positions[i].Position = (i + 1) * gap
		}
		d.Positions = positions
		return nil
	}})
}

// EnsureBuiltinBoard materializes the virtual builtin only on an explicit write
// path. A racing creation remains possible; post-write catalog validation must
// report duplicates instead of silently choosing one.
func (c *Client) EnsureBuiltinBoard(ctx context.Context, actor string) (*BoardRecord, error) {
	if strings.TrimSpace(actor) == "" {
		return nil, &WorkflowInputError{Reason: "builtin persistence requires actual actor"}
	}
	boards, err := c.ListBoards(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range boards {
		if b.IsBuiltin && b.Number > 0 {
			return &b, nil
		}
	}
	return c.createBoard(ctx, "All Issues", "", actor, true)
}

// MaterializeBuiltinBoardObserved refuses to adopt a carrier that appeared
// after a virtual observation. Callers must refresh and obtain its revision.
func (c *Client) MaterializeBuiltinBoardObserved(ctx context.Context, observed *BoardRecord, actor string) (*BoardRecord, error) {
	if observed == nil || observed.Number != 0 || observed.ID != "bd-all-issues" || !observed.IsBuiltin || observed.observed.repository != c.repo {
		return nil, &WorkflowInputError{Reason: "builtin materialization requires a virtual observation"}
	}
	if strings.TrimSpace(actor) == "" {
		return nil, &WorkflowInputError{Reason: "builtin materialization requires actual actor"}
	}
	boards, err := c.ListBoards(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range boards {
		if b.IsBuiltin && b.Number > 0 {
			return nil, &ConflictError{ID: "bd-all-issues"}
		}
	}
	return c.createBoard(ctx, "All Issues", "", actor, true)
}
