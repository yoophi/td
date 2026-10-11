package storetransfer

import (
	"fmt"
	"strings"

	"github.com/marcus/td/internal/ghstore"
)

type GitHubNoteInput struct {
	SourceKey string
	SourceID  string
	Title     string
	Content   string
	Options   ghstore.NoteCreateOptions
}

type GitHubBoardInput struct {
	SourceKey string
	SourceID  string
	Name      string
	Query     string
	Builtin   bool
	Options   ghstore.BoardCreateOptions
}

type GitHubEntityInputs struct {
	SourceSHA256 string
	Notes        []GitHubNoteInput
	Boards       []GitHubBoardInput
}

// BuildGitHubEntityInputs prepares notes/boards from immutable SQLite input.
// Task bindings use qualified keys and canonical destination IDs; they must be
// proven by the executor's receipts before publication. This is not authority.
// Reused qualified keys omit entities that the checked plan will not overwrite.
// Source authors/clocks and deleted position rows remain in the full capsule.
func BuildGitHubEntityInputs(source *Source, taskIDs map[string]string, reusedKeys map[string]bool) (*GitHubEntityInputs, error) {
	if source == nil {
		return nil, fmt.Errorf("SQLite transfer source required")
	}
	s, err := source.Envelope.decode()
	if err != nil {
		return nil, err
	}
	if s.SQLite == nil {
		return nil, fmt.Errorf("GitHub entity projection requires a SQLite source")
	}
	preview, err := s.Preview()
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	taskKeys := map[string]bool{}
	knownKeys := map[string]bool{}
	for _, i := range preview.Identities {
		keys[i.ID] = i.Key
		knownKeys[i.Key] = true
		if i.Kind == "task" {
			taskKeys[i.Key] = true
		}
	}
	for key := range reusedKeys {
		if !knownKeys[key] {
			return nil, fmt.Errorf("unknown reused source key %q", key)
		}
	}
	for key := range taskIDs {
		if !taskKeys[key] {
			return nil, fmt.Errorf("unknown or non-task destination binding %q", key)
		}
	}
	ids := map[string]string{}
	used := map[string]bool{}
	for _, task := range s.SQLite.Tasks {
		id := taskIDs[keys[task.Issue.ID]]
		n, err := ghstore.Number(id)
		if err != nil || id != fmt.Sprintf("gh-%d", n) || used[id] {
			return nil, fmt.Errorf("missing, noncanonical or duplicate GitHub task binding for %q", task.Issue.ID)
		}
		used[id] = true
		ids[task.Issue.ID] = id
	}
	result := &GitHubEntityInputs{SourceSHA256: s.Envelope.SHA256, Notes: []GitHubNoteInput{}, Boards: []GitHubBoardInput{}}
	for _, n := range s.SQLite.Notes {
		if reusedKeys[keys[n.ID]] {
			continue
		}
		if n.Title == "" || strings.TrimSpace(n.Title) != n.Title {
			return nil, fmt.Errorf("note %q title would be empty or normalized by GitHub creation; resolve explicitly before transfer", n.ID)
		}
		result.Notes = append(result.Notes, GitHubNoteInput{SourceKey: keys[n.ID], SourceID: n.ID, Title: n.Title, Content: n.Content, Options: ghstore.NoteCreateOptions{Pinned: n.Pinned, Archived: n.Archived, Deleted: n.DeletedAt != nil, DeletedAt: n.DeletedAt}})
	}
	names := []string{}
	for _, row := range s.SQLite.Boards {
		b := row.Board
		if reusedKeys[keys[b.ID]] {
			continue
		}
		duplicateName := false
		for _, name := range names {
			if strings.EqualFold(name, b.Name) {
				duplicateName = true
				break
			}
		}
		if b.Name == "" || strings.TrimSpace(b.Name) != b.Name || duplicateName {
			return nil, fmt.Errorf("board %q has empty/normalized/duplicate GitHub name; resolve explicitly before transfer", b.ID)
		}
		names = append(names, b.Name)
		if b.ViewMode != "swimlanes" && b.ViewMode != "backlog" {
			return nil, fmt.Errorf("unsupported GitHub view mode %q for board %q", b.ViewMode, b.ID)
		}
		q, err := RewriteQuery(b.Query, ids)
		if err != nil {
			return nil, err
		}
		if q.Blocked() {
			return nil, fmt.Errorf("board %q has unresolved or unsupported query references", b.ID)
		}
		input := GitHubBoardInput{SourceKey: keys[b.ID], SourceID: b.ID, Name: b.Name, Query: q.Query, Builtin: b.IsBuiltin, Options: ghstore.BoardCreateOptions{ViewMode: b.ViewMode}}
		for _, p := range row.Positions {
			if p.DeletedAt != nil {
				continue
			}
			input.Options.Positions = append(input.Options.Positions, ghstore.BoardPosition{IssueID: ids[p.IssueID], Position: p.Position, AddedAt: p.AddedAt})
		}
		probe := ghstore.BoardDetails{Version: 1, Query: input.Query, ViewMode: input.Options.ViewMode, Builtin: input.Builtin, Positions: input.Options.Positions}
		// Use the same pure metadata validation as the creation path.
		if err := ghstore.ValidateBoardCreateOptions(probe); err != nil {
			return nil, fmt.Errorf("board %q input: %w", b.ID, err)
		}
		result.Boards = append(result.Boards, input)
	}
	return result, nil
}
