package backendstore

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

type Board struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Query        string     `json:"query"`
	IsBuiltin    bool       `json:"is_builtin"`
	ViewMode     string     `json:"view_mode"`
	Revision     string     `json:"revision"`
	LastViewedAt *time.Time `json:"last_viewed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type BoardCard struct {
	BoardID     string `json:"board_id"`
	Position    int    `json:"position"`
	HasPosition bool   `json:"has_position"`
	Category    string `json:"category"`
	Issue       Issue  `json:"issue"`
}

type BoardDetail struct {
	Board  Board       `json:"board"`
	Issues []BoardCard `json:"issues"`
}

func validateBoard(b Board) error {
	if b.ID == "" || strings.ContainsAny(b.ID, "/?#") || b.Name == "" || !revisionPattern.MatchString(b.Revision) || b.CreatedAt.IsZero() || b.UpdatedAt.IsZero() {
		return fmt.Errorf("invalid backend board")
	}
	return nil
}

func (c *Connection) Boards(ctx context.Context) ([]Board, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return nil, e
	}
	var reply struct {
		Boards []Board `json:"boards"`
	}
	if e = c.Client.request(ctx, "GET", path+"/boards", c.SessionID, "", nil, &reply); e != nil {
		return nil, e
	}
	if reply.Boards == nil {
		return nil, fmt.Errorf("invalid backend board list")
	}
	seen := map[string]bool{}
	for _, b := range reply.Boards {
		if e = validateBoard(b); e != nil || seen[b.ID] {
			return nil, fmt.Errorf("invalid backend board list")
		}
		seen[b.ID] = true
	}
	return reply.Boards, nil
}

func ResolveBoard(rows []Board, raw string) (Board, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Board{}, fmt.Errorf("board reference required")
	}
	var matches []Board
	for _, b := range rows {
		if b.ID == raw || strings.EqualFold(b.Name, raw) {
			return b, nil
		}
		if strings.HasPrefix(b.ID, raw) {
			matches = append(matches, b)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Board{}, fmt.Errorf("ambiguous board reference %q", raw)
	}
	return Board{}, fmt.Errorf("board %q not found", raw)
}

func (c *Connection) BoardDetail(ctx context.Context, id string, includeClosed bool) (BoardDetail, error) {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return BoardDetail{}, e
	}
	if id == "" || strings.ContainsAny(id, "/?#") {
		return BoardDetail{}, fmt.Errorf("invalid backend board ID")
	}
	path += "/boards/" + url.PathEscape(id)
	if includeClosed {
		path += "?include_closed=true"
	}
	var detail BoardDetail
	if e = c.Client.request(ctx, "GET", path, c.SessionID, "", nil, &detail); e != nil {
		return BoardDetail{}, e
	}
	if e = validateBoard(detail.Board); e != nil || detail.Board.ID != id || detail.Issues == nil {
		return BoardDetail{}, fmt.Errorf("invalid backend board detail")
	}
	for _, card := range detail.Issues {
		if card.BoardID != id || ValidateIssue(card.Issue) != nil || card.HasPosition && card.Position < 1 {
			return BoardDetail{}, fmt.Errorf("invalid backend board card")
		}
	}
	return detail, nil
}

func (v BoardCard) Model() models.BoardIssueView {
	return models.BoardIssueView{BoardID: v.BoardID, Position: v.Position, HasPosition: v.HasPosition, Issue: v.Issue.Issue, Category: v.Category}
}
