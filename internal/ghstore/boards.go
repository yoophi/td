package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/query"
	"slices"
	"strconv"
	"strings"
	"time"
)

// BoardDetails is stored only on entity_kind=board carrier issues. The carrier
// title and GitHub timestamps remain the native name/clock authorities. A
// native close does not delete its configuration; DeletedAt is explicit.
type BoardDetails struct {
	Version      int             `json:"version"`
	Query        string          `json:"query,omitempty"`
	ViewMode     string          `json:"view_mode"`
	Builtin      bool            `json:"builtin,omitempty"`
	LastViewedAt *time.Time      `json:"last_viewed_at,omitempty"`
	DeletedAt    *time.Time      `json:"deleted_at,omitempty"`
	Positions    []BoardPosition `json:"positions,omitempty"`
	History      []BoardEvent    `json:"history,omitempty"`
}
type BoardPosition struct {
	IssueID  string    `json:"issue_id"`
	Position int       `json:"position"`
	AddedAt  time.Time `json:"added_at"`
}
type BoardEvent struct {
	OperationID string    `json:"operation_id"`
	Action      string    `json:"action"`
	SessionID   string    `json:"session_id"`
	At          time.Time `json:"at"`
	Reason      string    `json:"reason,omitempty"`
}

func (d BoardDetails) validate() error {
	if d.Version != 1 {
		return fmt.Errorf("unsupported board metadata version %d", d.Version)
	}
	if !slices.Contains([]string{"swimlanes", "backlog"}, d.ViewMode) {
		return fmt.Errorf("invalid board view mode %q", d.ViewMode)
	}
	if d.Query != "" {
		parsed, err := query.Parse(d.Query)
		if err != nil {
			return fmt.Errorf("invalid board TDQ query: %w", err)
		}
		if problems := parsed.Validate(); len(problems) > 0 {
			return fmt.Errorf("invalid board TDQ query: %v", problems)
		}
	}
	if d.Builtin && (d.Query != "" || d.DeletedAt != nil) {
		return fmt.Errorf("builtin All Issues board cannot be filtered or deleted")
	}
	for _, stamp := range []*time.Time{d.LastViewedAt, d.DeletedAt} {
		if stamp != nil && stamp.IsZero() {
			return fmt.Errorf("board timestamps must not be zero")
		}
	}
	ids := map[string]bool{}
	slots := map[int]bool{}
	for _, p := range d.Positions {
		number, err := Number(p.IssueID)
		if err != nil || p.IssueID != fmt.Sprintf("gh-%d", number) || p.Position < 0 || p.AddedAt.IsZero() || ids[p.IssueID] || slots[p.Position] {
			return fmt.Errorf("invalid, duplicate or noncanonical board position for %q", p.IssueID)
		}
		ids[p.IssueID] = true
		slots[p.Position] = true
	}
	operations := map[string]bool{}
	for _, event := range d.History {
		if strings.TrimSpace(event.OperationID) == "" || operations[event.OperationID] || strings.TrimSpace(event.SessionID) == "" || event.At.IsZero() || !slices.Contains([]string{"create", "update", "delete", "restore", "view", "set_view_mode", "set_position", "remove_position", "move"}, event.Action) {
			return fmt.Errorf("invalid board history record")
		}
		operations[event.OperationID] = true
	}
	return nil
}

type BoardRecord struct {
	models.Board
	Number   int          `json:"number"`
	URL      string       `json:"url"`
	Details  BoardDetails `json:"details"`
	observed Record
}

func boardRecord(record *Record) (*BoardRecord, error) {
	if record.meta.EntityKind != "board" {
		return nil, fmt.Errorf("%s is not a board entity", record.ID)
	}
	if record.meta.Board == nil {
		return nil, fmt.Errorf("board carrier %s has no versioned board metadata; repair it explicitly before use", record.ID)
	}
	if strings.TrimSpace(record.Title) == "" {
		return nil, fmt.Errorf("board carrier %s has an empty name", record.ID)
	}
	data, err := json.Marshal(record.meta.Board)
	if err != nil {
		return nil, err
	}
	var details BoardDetails
	if err := json.Unmarshal(data, &details); err != nil {
		return nil, err
	}
	id := fmt.Sprintf("bd-gh-%d", record.Number)
	if details.Builtin {
		if record.Title != "All Issues" {
			return nil, fmt.Errorf("builtin board carrier must be named All Issues")
		}
		id = "bd-all-issues"
	}
	return &BoardRecord{Board: models.Board{ID: id, Name: record.Title, Query: details.Query, IsBuiltin: details.Builtin, ViewMode: details.ViewMode, LastViewedAt: details.LastViewedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}, Number: record.Number, URL: record.URL, Details: details, observed: *record}, nil
}
func virtualAllIssuesBoard() BoardRecord {
	return BoardRecord{Board: models.Board{ID: "bd-all-issues", Name: "All Issues", IsBuiltin: true, ViewMode: "swimlanes"}, Details: BoardDetails{Version: 1, Builtin: true, ViewMode: "swimlanes"}}
}

// ListBoards returns auxiliary configurations plus the virtual builtin until
// a later explicit write materializes it. Reads never create carrier issues.
func (c *Client) ListBoards(ctx context.Context) ([]BoardRecord, error) {
	data, err := c.request(ctx, "GET", "/issues?state=all&per_page=100", nil, true)
	if err != nil {
		return nil, err
	}
	var pages [][]apiIssue
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid board issue list: %w", err)
	}
	result := []BoardRecord{}
	seen := map[int]bool{}
	builtin := false
	for _, page := range pages {
		for _, item := range page {
			if len(item.PullRequest) > 0 && string(item.PullRequest) != "null" {
				continue
			}
			record, err := item.record()
			if err != nil {
				return nil, err
			}
			if seen[item.Number] {
				return nil, fmt.Errorf("board listing repeated carrier %d", item.Number)
			}
			seen[item.Number] = true
			if record.meta.EntityKind != "board" {
				continue
			}
			record.repository = c.repo
			board, err := boardRecord(record)
			if err != nil {
				return nil, err
			}
			if board.Details.DeletedAt != nil {
				continue
			}
			if board.IsBuiltin {
				if builtin {
					return nil, fmt.Errorf("multiple All Issues board carriers; reconcile them explicitly")
				}
				builtin = true
			}
			result = append(result, *board)
		}
	}
	if !builtin {
		virtual := virtualAllIssuesBoard()
		virtual.observed.repository = c.repo
		result = append(result, virtual)
	}
	slices.SortFunc(result, func(a, b BoardRecord) int {
		if a.IsBuiltin != b.IsBuiltin {
			if a.IsBuiltin {
				return -1
			}
			return 1
		}
		if cmp := a.CreatedAt.Compare(b.CreatedAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.ID, b.ID)
	})
	return result, nil
}
func (c *Client) GetBoard(ctx context.Context, ref string) (*BoardRecord, error) {
	if strings.HasPrefix(ref, "bd-gh-") {
		suffix := strings.TrimPrefix(ref, "bd-gh-")
		number, err := strconv.Atoi(suffix)
		if err != nil || number <= 0 || ref != fmt.Sprintf("bd-gh-%d", number) {
			return nil, fmt.Errorf("invalid GitHub board ID %q", ref)
		}
		data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/%d", number), nil, false)
		if err != nil {
			return nil, err
		}
		record, err := decodeIssue(data)
		if err != nil {
			return nil, err
		}
		if record.Number != number {
			return nil, fmt.Errorf("board carrier identity mismatch")
		}
		record.repository = c.repo
		board, err := boardRecord(record)
		if err != nil {
			return nil, err
		}
		if board.ID != ref || board.Details.DeletedAt != nil {
			return nil, fmt.Errorf("board not found: %s", ref)
		}
		return board, nil
	}
	boards, err := c.ListBoards(ctx)
	if err != nil {
		return nil, err
	}
	for _, board := range boards {
		if ref == board.ID {
			copy := board
			return &copy, nil
		}
	}
	var found *BoardRecord
	for _, board := range boards {
		if strings.EqualFold(ref, board.Name) {
			if found != nil {
				return nil, fmt.Errorf("ambiguous board name %q; use bd-gh-N", ref)
			}
			copy := board
			found = &copy
		}
	}
	if found == nil {
		return nil, fmt.Errorf("board not found: %s", ref)
	}
	return found, nil
}

// Revision is the opaque original carrier observation used by HTTP If-Match.
// Virtual builtin observations have no carrier and use a distinct token.
func (b *BoardRecord) Revision() string {
	if b.Number == 0 {
		return "virtual"
	}
	return fmt.Sprintf("%x", b.observed.revision)
}
