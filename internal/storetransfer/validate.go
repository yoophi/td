package storetransfer

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/query"
)

// Preview describes source identities, not destination IDs or permission to
// execute. Historical actors/actions remain passive. Query selectors may
// intentionally match absent IDs; report those instead of dropping them.
type Preview struct {
	Identities      []Identity       `json:"identities"`
	QueryReferences []QueryReference `json:"query_references"`
}
type Identity struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Key  string `json:"key"`
}
type QueryReference struct {
	BoardID string `json:"board_id"`
	Field   string `json:"field"`
	ID      string `json:"id"`
	Present bool   `json:"present"`
}

// Preview always revalidates the immutable envelope, not mutable display data.
func (s *Source) Preview() (*Preview, error) {
	if s == nil {
		return nil, fmt.Errorf("nil transfer source")
	}
	decoded, err := s.Envelope.decode()
	if err != nil {
		return nil, err
	}
	return decoded.validateReferences()
}

func (s *Source) validateReferences() (*Preview, error) {
	result := &Preview{Identities: []Identity{}, QueryReferences: []QueryReference{}}
	ids := map[string]string{}
	tasks := map[string]models.Issue{}
	add := func(kind, id string) error {
		if id == "" || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\x00") {
			return fmt.Errorf("invalid %s source ID %q", kind, id)
		}
		if prior, exists := ids[id]; exists {
			return fmt.Errorf("duplicate source ID %q (%s/%s)", id, prior, kind)
		}
		ids[id] = kind
		key := strings.Join([]string{s.Envelope.Store, url.QueryEscape(s.Envelope.Namespace), kind, url.QueryEscape(id)}, ":")
		result.Identities = append(result.Identities, Identity{kind, id, key})
		return nil
	}
	if s.SQLite != nil {
		for _, task := range s.SQLite.Tasks {
			if err := add("task", task.Issue.ID); err != nil {
				return nil, err
			}
			tasks[task.Issue.ID] = task.Issue
		}
		for _, note := range s.SQLite.Notes {
			if err := add("note", note.ID); err != nil {
				return nil, err
			}
		}
		for _, board := range s.SQLite.Boards {
			if err := add("board", board.Board.ID); err != nil {
				return nil, err
			}
		}
	} else {
		for _, task := range s.GitHub.Tasks {
			r := task.Record
			if task.Repository != s.Envelope.Namespace || task.Version != 1 || r.Number <= 0 || r.ID != fmt.Sprintf("gh-%d", r.Number) {
				return nil, fmt.Errorf("invalid task source binding %q", r.ID)
			}
			if err := add("task", r.ID); err != nil {
				return nil, err
			}
			tasks[r.ID] = r.Issue
		}
		for _, note := range s.GitHub.Notes {
			if note.Number <= 0 || note.ID != fmt.Sprintf("nt-gh-%d", note.Number) {
				return nil, fmt.Errorf("invalid note source binding %q", note.ID)
			}
			if err := add("note", note.ID); err != nil {
				return nil, err
			}
		}
		for _, board := range s.GitHub.Boards {
			wantID := fmt.Sprintf("bd-gh-%d", board.Number)
			if board.IsBuiltin && board.Details.Builtin {
				wantID = "bd-all-issues"
			}
			if board.Number <= 0 || board.ID != wantID || board.IsBuiltin != board.Details.Builtin {
				return nil, fmt.Errorf("invalid board source binding %q", board.ID)
			}
			if err := add("board", board.ID); err != nil {
				return nil, err
			}
		}
		if err := s.validateNative(); err != nil {
			return nil, err
		}
		if s.GitHub.Journal != nil {
			if err := s.GitHub.Journal.Validate(); err != nil {
				return nil, fmt.Errorf("invalid passive journal source: %w", err)
			}
		}
	}
	ref := func(owner, field, id string) error {
		if _, exists := tasks[id]; !exists {
			return fmt.Errorf("source %s %s refers to missing task %q", owner, field, id)
		}
		if owner == id && (field == "parent" || field == "dependency") {
			return fmt.Errorf("source %s has a self %s reference", owner, field)
		}
		return nil
	}
	for id, task := range tasks {
		if task.ParentID != "" {
			if err := ref(id, "parent", task.ParentID); err != nil {
				return nil, err
			}
		}
	}
	if s.SQLite != nil {
		for _, task := range s.SQLite.Tasks {
			id := task.Issue.ID
			edges := map[string]bool{}
			for _, dep := range task.Dependencies {
				if dep.IssueID != id || (dep.RelationType != "blocks" && dep.RelationType != "depends_on") {
					return nil, fmt.Errorf("invalid dependency owner/type for %s", id)
				}
				key := dep.RelationType + ":" + dep.DependsOnID
				if edges[key] {
					return nil, fmt.Errorf("duplicate dependency for %s", id)
				}
				edges[key] = true
				if err := ref(id, "dependency", dep.DependsOnID); err != nil {
					return nil, err
				}
			}
			owned := map[string]bool{}
			check := func(kind, childID, owner string) error {
				key := kind + ":" + childID
				if childID == "" || owner != id || owned[key] {
					return fmt.Errorf("invalid or duplicate %s identity/owner in task %s", kind, id)
				}
				owned[key] = true
				return nil
			}
			for _, row := range task.Logs {
				if err := check("log", row.ID, row.IssueID); err != nil {
					return nil, err
				}
			}
			for _, row := range task.Handoffs {
				if err := check("handoff", row.ID, row.IssueID); err != nil {
					return nil, err
				}
			}
			for _, row := range task.Files {
				if err := check("file", row.ID, row.IssueID); err != nil {
					return nil, err
				}
			}
			for _, row := range task.Comments {
				if err := check("comment", row.ID, row.IssueID); err != nil {
					return nil, err
				}
			}
			for _, row := range task.Reviews {
				if row == nil {
					return nil, fmt.Errorf("nil review in task %s", id)
				}
				if err := check("review", row.ID, row.IssueID); err != nil {
					return nil, err
				}
			}
			for _, row := range task.SessionHistory {
				if err := check("session_history", row.ID, row.IssueID); err != nil {
					return nil, err
				}
			}
			for _, row := range task.GitSnapshots {
				if err := check("git_snapshot", row.ID, row.IssueID); err != nil {
					return nil, err
				}
			}
		}
		for _, board := range s.SQLite.Boards {
			positions := map[string]bool{}
			for _, pos := range board.Positions {
				if pos.BoardID != board.Board.ID || positions[pos.IssueID] || pos.Position < 0 {
					return nil, fmt.Errorf("invalid position owner/identity for board %s", board.Board.ID)
				}
				positions[pos.IssueID] = true
				if err := ref(board.Board.ID, "position", pos.IssueID); err != nil {
					return nil, err
				}
			}
			if err := validateQuery(board.Board.ID, board.Board.Query, tasks, result); err != nil {
				return nil, err
			}
		}
	} else {
		for _, task := range s.GitHub.Tasks {
			owned := map[string]bool{}
			check := func(kind, childID, owner string) error {
				key := kind + ":" + childID
				if childID == "" || owner != task.Record.ID || owned[key] {
					return fmt.Errorf("invalid or duplicate %s identity/owner in task %s", kind, task.Record.ID)
				}
				owned[key] = true
				return nil
			}
			for _, row := range task.Activity {
				if err := check("activity", row.ID, row.IssueID); err != nil {
					return nil, err
				}
			}
			if details := task.Record.Details; details != nil {
				if details.ParentID != task.Record.ParentID {
					return nil, fmt.Errorf("conflicting parent projections for %s", task.Record.ID)
				}
				seen := map[string]bool{}
				for _, dep := range details.Dependencies {
					if seen[dep] {
						return nil, fmt.Errorf("duplicate dependency for %s", task.Record.ID)
					}
					seen[dep] = true
					if err := ref(task.Record.ID, "dependency", dep); err != nil {
						return nil, err
					}
				}
				filePaths := map[string]bool{}
				for _, row := range details.Files {
					// GitHub metadata permits omitted file row IDs/owners. The
					// carrier provides ownership; do not invent an original ID.
					if row.FilePath == "" || filePaths[row.FilePath] || (row.IssueID != "" && row.IssueID != task.Record.ID) {
						return nil, fmt.Errorf("invalid or duplicate file path/owner in task %s", task.Record.ID)
					}
					filePaths[row.FilePath] = true
					if row.ID != "" {
						if err := check("file", row.ID, task.Record.ID); err != nil {
							return nil, err
						}
					}
				}
				for _, row := range details.Reviews {
					if err := check("review", row.ID, row.IssueID); err != nil {
						return nil, err
					}
				}
				for _, row := range details.Sessions {
					if err := check("session_history", row.ID, row.IssueID); err != nil {
						return nil, err
					}
				}
			}
		}
		for _, board := range s.GitHub.Boards {
			if board.Query != board.Details.Query {
				return nil, fmt.Errorf("conflicting query projections for board %s", board.ID)
			}
			positions := map[string]bool{}
			for _, pos := range board.Details.Positions {
				if positions[pos.IssueID] || pos.Position < 0 {
					return nil, fmt.Errorf("invalid position identity for board %s", board.ID)
				}
				positions[pos.IssueID] = true
				if err := ref(board.ID, "position", pos.IssueID); err != nil {
					return nil, err
				}
			}
			if err := validateQuery(board.ID, board.Query, tasks, result); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(result.Identities, func(i, j int) bool { return result.Identities[i].Key < result.Identities[j].Key })
	return result, nil
}

func (s *Source) validateNative() error {
	native := map[int]ghstore.NativeExport{}
	comments := map[int64]bool{}
	for _, item := range s.GitHub.Native {
		if _, duplicate := native[item.Number]; duplicate || item.Number <= 0 {
			return fmt.Errorf("invalid or duplicate native carrier %d", item.Number)
		}
		if item.Kind != "issue" && item.Kind != "note" && item.Kind != "board" && item.Kind != "journal" {
			return fmt.Errorf("unknown native entity kind %q", item.Kind)
		}
		native[item.Number] = item
		for _, comment := range item.Comments {
			if comment.ID <= 0 || comments[comment.ID] {
				return fmt.Errorf("invalid or duplicate native comment %d", comment.ID)
			}
			comments[comment.ID] = true
		}
	}
	used := map[int]bool{}
	bind := func(number int, kind, title string) error {
		item, ok := native[number]
		if !ok || item.Kind != kind || item.Title != title || used[number] {
			return fmt.Errorf("conflicting or missing native %s binding %d", kind, number)
		}
		used[number] = true
		return nil
	}
	for _, task := range s.GitHub.Tasks {
		if err := bind(task.Record.Number, "issue", task.Record.Title); err != nil {
			return err
		}
		n := native[task.Record.Number]
		if n.Body != task.Body || n.State != task.NativeState || n.Author != task.Author {
			return fmt.Errorf("conflicting native task content %s", task.Record.ID)
		}
	}
	for _, note := range s.GitHub.Notes {
		if err := bind(note.Number, "note", note.Title); err != nil {
			return err
		}
	}
	for _, board := range s.GitHub.Boards {
		if err := bind(board.Number, "board", board.Name); err != nil {
			return err
		}
	}
	for number, item := range native {
		if item.Kind != "journal" && !used[number] {
			return fmt.Errorf("unbound native source carrier %d", number)
		}
	}
	return nil
}

func validateQuery(board, text string, tasks map[string]models.Issue, result *Preview) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	parsed, err := query.Parse(text)
	if err != nil {
		return fmt.Errorf("source board %s query: %w", board, err)
	}
	if problems := parsed.Validate(); len(problems) > 0 {
		return fmt.Errorf("source board %s query: %v", board, problems)
	}
	add := func(field string, value any) {
		if id, ok := value.(string); ok {
			_, exists := tasks[id]
			result.QueryReferences = append(result.QueryReferences, QueryReference{board, field, id, exists})
		}
	}
	var visit func(query.Node)
	visit = func(node query.Node) {
		switch n := node.(type) {
		case *query.BinaryExpr:
			visit(n.Left)
			visit(n.Right)
		case *query.UnaryExpr:
			visit(n.Expr)
		case *query.FieldExpr:
			if n.Field == "id" || n.Field == "parent" || n.Field == "epic" || n.Field == "dep.blocks" || n.Field == "dep.depends_on" {
				if n.Operator == query.OpEq || n.Operator == query.OpNeq {
					add(n.Field, n.Value)
				}
				if list, ok := n.Value.(*query.ListValue); ok {
					for _, value := range list.Values {
						add(n.Field, value)
					}
				}
			}
		case *query.FunctionCall:
			if n.Name == "blocks" || n.Name == "blocked_by" || n.Name == "child_of" || n.Name == "descendant_of" {
				for _, arg := range n.Args {
					add(n.Name, arg)
				}
			}
			if (n.Name == "any" || n.Name == "all" || n.Name == "none") && len(n.Args) > 1 {
				if field, ok := n.Args[0].(string); ok && (field == "id" || field == "parent" || field == "epic" || field == "dep.blocks" || field == "dep.depends_on") {
					for _, arg := range n.Args[1:] {
						add(field, arg)
					}
				}
			}
		}
	}
	visit(parsed.Root)
	return nil
}
