package serve

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
)

type githubBoardReadClient interface {
	githubReadClient
	issuestore.GitHubBoardReader
	ListBoards(context.Context) ([]ghstore.BoardRecord, error)
	GetBoard(context.Context, string) (*ghstore.BoardRecord, error)
}

func (s *Server) EnableGitHubBoardReads(store *GitHubReadStore) {
	s.githubCapabilities = append(s.githubCapabilities, "board_reads")
	s.githubEndpoints = append(s.githubEndpoints, "GET /v1/boards", "GET /v1/boards/{id}")
	s.mux.HandleFunc("GET /v1/boards", func(w http.ResponseWriter, r *http.Request) { store.boards(w, r, false) })
	s.mux.HandleFunc("GET /v1/boards/{id}", func(w http.ResponseWriter, r *http.Request) { store.boards(w, r, true) })
}
func boardReadError(w http.ResponseWriter, err error) {
	// Only our explicit absence and the remote 404 are mapped to not-found.
	// Metadata, ambiguity and authentication failures must remain visible.
	if strings.HasPrefix(err.Error(), "board not found:") || strings.Contains(err.Error(), "HTTP 404") {
		WriteError(w, ErrNotFound, err.Error(), 404)
		return
	}
	githubWriteError(w, err)
}
func (s *GitHubReadStore) boards(w http.ResponseWriter, r *http.Request, detail bool) {
	for key, values := range r.URL.Query() {
		if !detail || key != "include_closed" || len(values) != 1 || !slices.Contains([]string{"true", "false"}, values[0]) {
			WriteError(w, ErrValidation, "unsupported, repeated or invalid board query parameter: "+key, 400)
			return
		}
	}
	raw, err := s.open(r.Context())
	if err != nil {
		readError(w, err)
		return
	}
	c, ok := raw.(githubBoardReadClient)
	if !ok {
		WriteError(w, "unsupported_operation", "board read store unavailable", 501)
		return
	}
	if !detail {
		boards, err := c.ListBoards(r.Context())
		if err != nil {
			boardReadError(w, err)
			return
		}
		dtos := make([]BoardDTO, 0, len(boards))
		for _, b := range boards {
			dtos = append(dtos, githubBoardDTO(&b))
		}
		WriteSuccess(w, map[string]any{"boards": dtos}, 200)
		return
	}
	board, err := c.GetBoard(r.Context(), r.PathValue("id"))
	if err != nil {
		boardReadError(w, err)
		return
	}
	snapshot, err := issuestore.ReadGitHubBoardSnapshot(r.Context(), c, board, r.URL.Query().Get("include_closed") == "true")
	if err != nil {
		boardReadError(w, err)
		return
	}
	views, err := ghstore.ApplyBoardPositions(board, snapshot.Candidates)
	if err != nil {
		boardReadError(w, err)
		return
	}
	// Build unresolved blocker summaries from a complete listing, rather than
	// from the filtered board, so dependencies outside the board remain visible.
	byID := map[string]ghstore.Record{}
	seen := map[string]bool{}
	for _, record := range snapshot.Records {
		if seen[record.ID] {
			boardReadError(w, fmt.Errorf("board dependency listing repeated %s", record.ID))
			return
		}
		seen[record.ID] = true
		if record.DeletedAt == nil {
			byID[record.ID] = record
		}
	}
	cards := make([]map[string]any, 0, len(views))
	for _, v := range views {
		dto := IssueToDTO(&v.Issue).slimForBoard()
		source, ok := byID[v.Issue.ID]
		if !ok {
			boardReadError(w, fmt.Errorf("board task %s disappeared during read; refresh", v.Issue.ID))
			return
		}
		blockers := []BlockerRefDTO{}
		if source.Details != nil {
			for _, id := range source.Details.Dependencies {
				target, ok := byID[id]
				if !ok {
					boardReadError(w, fmt.Errorf("board dependency %s of %s is missing or deleted; repair the dependency", id, source.ID))
					return
				}
				if target.Status != models.StatusClosed {
					blockers = append(blockers, BlockerRefDTO{DepID: db.DependencyID(source.ID, id, "depends_on"), IssueID: id, Title: target.Title, Status: string(target.Status), RelationType: "depends_on"})
				}
			}
		}
		if len(blockers) > 0 {
			dto.DependencySummary = &DependencySummaryDTO{Blockers: blockers}
		}
		cards = append(cards, map[string]any{"issue": dto, "board_id": v.BoardID, "position": v.Position, "has_position": v.HasPosition, "category": v.Category})
	}
	w.Header().Set("ETag", `"`+board.Revision()+`"`)
	WriteSuccess(w, map[string]any{"board": githubBoardDTO(board), "issues": cards, "revision": board.Revision()}, 200)
}

// Virtual boards have no GitHub clocks; do not serialize zero time as an
// apparent historical creation/update date.
func githubBoardDTO(board *ghstore.BoardRecord) BoardDTO {
	dto := BoardToDTO(&board.Board)
	if board.CreatedAt.IsZero() {
		dto.CreatedAt = ""
	}
	if board.UpdatedAt.IsZero() {
		dto.UpdatedAt = ""
	}
	return dto
}
