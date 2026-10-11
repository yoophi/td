package storetransfer

import (
	"fmt"
	"sort"
	"time"
)

// These records describe intended handling, not implemented destination
// writes. Source keys survive pending creation; target IDs are resolved only
// by the later checked executor/checkpoint controller.
type RelationPlan struct {
	Kind     string `json:"kind"`
	FromKey  string `json:"from_key"`
	ToKey    string `json:"to_key"`
	Handling string `json:"handling"`
}
type PositionPlan struct {
	BoardKey  string     `json:"board_key"`
	IssueKey  string     `json:"issue_key"`
	Position  int        `json:"position"`
	AddedAt   time.Time  `json:"added_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	Handling  string     `json:"handling"`
}
type BoardQueryPlan struct {
	BoardKey         string       `json:"board_key"`
	Original         string       `json:"original"`
	Rewrite          QueryRewrite `json:"rewrite"`
	PendingKeys      []string     `json:"pending_keys"`
	MissingSourceIDs []string     `json:"missing_source_ids"`
}
type Disclosure struct {
	Code     string `json:"code"`
	Handling string `json:"handling"`
	Count    int    `json:"count"`
	Reason   string `json:"reason"`
}
type DataPlan struct {
	Identities   IdentityPlan     `json:"identities"`
	Relations    []RelationPlan   `json:"relations"`
	Positions    []PositionPlan   `json:"positions"`
	BoardQueries []BoardQueryPlan `json:"board_queries"`
	Disclosures  []Disclosure     `json:"disclosures"`
}

func BuildDataPlan(source, target *Source, bindings map[string]string) (*DataPlan, error) {
	identity, err := PlanIdentities(source, target, bindings)
	if err != nil {
		return nil, err
	}
	s, err := source.Envelope.decode()
	if err != nil {
		return nil, err
	}
	plan := &DataPlan{Identities: *identity, Relations: []RelationPlan{}, Positions: []PositionPlan{}, BoardQueries: []BoardQueryPlan{}, Disclosures: []Disclosure{}}
	keys := map[string]string{}
	taskKeys := map[string]string{}
	for _, a := range identity.Assignments {
		keys[a.SourceID] = a.SourceKey
		if a.Kind == "task" {
			taskKeys[a.SourceID] = a.SourceKey
		}
	}
	relation := func(kind, from, to string) {
		handling := "apply"
		if kind == "blocks" && target.Envelope.Store == "gh-issue" {
			handling = "passive_archive"
		}
		plan.Relations = append(plan.Relations, RelationPlan{kind, keys[from], keys[to], handling})
	}
	position := func(board, issue string, index int, added time.Time, deleted *time.Time) {
		handling := "apply"
		if deleted != nil && target.Envelope.Store == "gh-issue" {
			handling = "passive_archive"
		}
		plan.Positions = append(plan.Positions, PositionPlan{keys[board], keys[issue], index, added, deleted, handling})
	}
	boardQuery := func(board, text string) error {
		r, err := RewriteQuery(text, identity.KnownTaskMappings())
		if err != nil {
			return err
		}
		row := BoardQueryPlan{BoardKey: keys[board], Original: text, Rewrite: *r, PendingKeys: []string{}, MissingSourceIDs: []string{}}
		for _, id := range r.Unmapped {
			if key, exists := taskKeys[id]; exists {
				row.PendingKeys = append(row.PendingKeys, key)
			} else {
				row.MissingSourceIDs = append(row.MissingSourceIDs, id)
			}
		}
		plan.BoardQueries = append(plan.BoardQueries, row)
		return nil
	}
	if s.SQLite != nil {
		for _, task := range s.SQLite.Tasks {
			if task.Issue.ParentID != "" {
				relation("parent", task.Issue.ID, task.Issue.ParentID)
			}
			for _, dep := range task.Dependencies {
				relation(dep.RelationType, dep.IssueID, dep.DependsOnID)
			}
		}
		for _, board := range s.SQLite.Boards {
			for _, pos := range board.Positions {
				position(board.Board.ID, pos.IssueID, pos.Position, pos.AddedAt, pos.DeletedAt)
			}
			if err := boardQuery(board.Board.ID, board.Board.Query); err != nil {
				return nil, err
			}
		}
	} else {
		for _, task := range s.GitHub.Tasks {
			if task.Record.ParentID != "" {
				relation("parent", task.Record.ID, task.Record.ParentID)
			}
			if d := task.Record.Details; d != nil {
				for _, dep := range d.Dependencies {
					relation("depends_on", task.Record.ID, dep)
				}
			}
		}
		for _, board := range s.GitHub.Boards {
			for _, pos := range board.Details.Positions {
				position(board.ID, pos.IssueID, pos.Position, pos.AddedAt, nil)
			}
			if err := boardQuery(board.ID, board.Query); err != nil {
				return nil, err
			}
		}
	}
	plan.Disclosures = preservationReport(s, target, plan)
	return plan, nil
}

type ResolvedRelation struct {
	Kind   string `json:"kind"`
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
}
type ResolvedPosition struct {
	BoardID   string     `json:"board_id"`
	IssueID   string     `json:"issue_id"`
	Position  int        `json:"position"`
	AddedAt   time.Time  `json:"added_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

// ReadyRelations and ReadyPositions resolve only known mappings. They do not
// grant write authority or validate creation receipts; the later executor must
// do that. Any unresolved active reference rejects the entire returned batch.
func (p *DataPlan) ReadyRelations() ([]ResolvedRelation, error) {
	if p == nil {
		return nil, fmt.Errorf("data plan is required")
	}
	ids := map[string]string{}
	for _, a := range p.Identities.Assignments {
		ids[a.SourceKey] = a.TargetID
	}
	result := []ResolvedRelation{}
	for _, r := range p.Relations {
		if r.Handling == "passive_archive" {
			continue
		}
		if r.Handling != "apply" {
			return nil, fmt.Errorf("unknown relationship handling %q", r.Handling)
		}
		if ids[r.FromKey] == "" || ids[r.ToKey] == "" {
			return nil, fmt.Errorf("relationship %s has unresolved creation mappings", r.Kind)
		}
		result = append(result, ResolvedRelation{r.Kind, ids[r.FromKey], ids[r.ToKey]})
	}
	return result, nil
}
func (p *DataPlan) ReadyPositions() ([]ResolvedPosition, error) {
	if p == nil {
		return nil, fmt.Errorf("data plan is required")
	}
	ids := map[string]string{}
	for _, a := range p.Identities.Assignments {
		ids[a.SourceKey] = a.TargetID
	}
	result := []ResolvedPosition{}
	for _, r := range p.Positions {
		if r.Handling == "passive_archive" {
			continue
		}
		if r.Handling != "apply" {
			return nil, fmt.Errorf("unknown position handling %q", r.Handling)
		}
		if ids[r.BoardKey] == "" || ids[r.IssueKey] == "" {
			return nil, fmt.Errorf("board position has unresolved creation mappings")
		}
		result = append(result, ResolvedPosition{ids[r.BoardKey], ids[r.IssueKey], r.Position, r.AddedAt, r.DeletedAt})
	}
	return result, nil
}

func preservationReport(source, target *Source, plan *DataPlan) []Disclosure {
	activeRelations, activePositions := 0, 0
	for _, r := range plan.Relations {
		if r.Handling == "apply" {
			activeRelations++
		}
	}
	for _, p := range plan.Positions {
		if p.Handling == "apply" {
			activePositions++
		}
	}
	tasks, notes, boards, history, nativeComments, journalActions := 0, 0, 0, 0, 0, 0
	if source.SQLite != nil {
		tasks, notes, boards = len(source.SQLite.Tasks), len(source.SQLite.Notes), len(source.SQLite.Boards)
		history = len(source.SQLite.Actions)
		for _, t := range source.SQLite.Tasks {
			history += len(t.Logs) + len(t.Handoffs) + len(t.Comments) + len(t.Reviews) + len(t.SessionHistory) + len(t.GitSnapshots)
		}
	} else {
		tasks, notes, boards = len(source.GitHub.Tasks), len(source.GitHub.Notes), len(source.GitHub.Boards)
		for _, t := range source.GitHub.Tasks {
			history += len(t.Activity)
			if d := t.Record.Details; d != nil {
				history += len(d.Reviews) + len(d.Sessions) + len(d.Transitions)
			}
		}
		for _, b := range source.GitHub.Boards {
			history += len(b.Details.History)
		}
		for _, n := range source.GitHub.Native {
			nativeComments += len(n.Comments)
		}
		if j := source.GitHub.Journal; j != nil {
			for _, s := range j.Sources {
				journalActions += len(s.Actions)
			}
		}
	}
	result := []Disclosure{
		{"task_content_state_schedule_deletion", "preserve", tasks, "Plan task content, status/type/priority/acceptance/labels/sprint/files/schedule/logical deletion with mapped IDs; executor is not implemented by this report."},
		{"notes_content_flags", "preserve", notes, "Plan note content and pinned/archived/deleted flags; original authors and clocks remain source provenance."},
		{"boards_configuration_queries", "preserve", boards, "Plan board configuration and mapped queries; blocked queries require explicit policy or confirmed mappings before board writes. Device last-view state is excluded."},
		{"relationships", "preserve", activeRelations, "Plan current parents and dependencies using qualified source keys; archival legacy relations are listed separately and never activated."},
		{"board_positions", "preserve", activePositions, "Plan board membership/order with mapped IDs; archival position tombstones are listed separately and never activated."},
		{"original_actors_reviews_history", "passive_archive", history, "Count collected typed history records (projections can overlap raw native evidence). Original authors, sessions, reviews, transitions, logs, handoffs, snapshots and comments are provenance, not destination actors, active approvals or executable undo history."},
		{"device_state_credentials", "excluded", 0, "Device session registries, credentials, focus/UI settings and local work bundles are not transfer input."},
		{"text_search_identity_semantics", "limitation", 0, "Text remains unchanged; bare searches also match IDs, so matching behavior involving renamed IDs may differ."},
		{"destination_access_and_receipts", "unverified", 0, "Observation does not prove future write permission, unchanged target state or completion receipt provenance."},
	}
	if target.Envelope.Store == "gh-issue" {
		blocks, deletedPositions := 0, 0
		for _, r := range plan.Relations {
			if r.Handling == "passive_archive" {
				blocks++
			}
		}
		for _, p := range plan.Positions {
			if p.Handling == "passive_archive" {
				deletedPositions++
			}
		}
		result = append(result, Disclosure{"native_author_server_clocks", "passive_archive", tasks + notes + boards, "GitHub assigns native authors and server timestamps; preserve original attribution/clocks as history, never impersonate them."})
		result = append(result, Disclosure{"sqlite_legacy_blocks", "passive_archive", blocks, "SQLite effective blocker queries read depends_on only. Raw blocks rows must not silently become newly active GitHub dependencies."})
		result = append(result, Disclosure{"deleted_position_rows", "passive_archive", deletedPositions, "GitHub board positions have no per-position tombstone field. Deleted SQLite position rows remain historical evidence and are not active membership."})
	}
	if source.GitHub != nil {
		result = append(result, Disclosure{"native_comment_evidence", "passive_archive", nativeComments, "Raw native comments include hidden journal transports; retain original bodies, IDs, authors and clocks as evidence, not replayable destination operations."}, Disclosure{"portable_journal_actions", "passive_archive", journalActions, "Retain collected journal phases including uncertainty; imported records confer no undo or approval authority."})
	}
	if source.GitHub != nil && target.Envelope.Store == "sqlite" {
		result = append(result, Disclosure{"github_extensions_native_history", "passive_archive", len(source.GitHub.Native), "Native carrier/comment identity, author/clocks and GitHub-specific data lack SQLite shared columns; retain the source archive rather than claim lossless column restoration."})
	}
	if source.SQLite != nil {
		kinds := []string{}
		for kind := range source.SQLite.ExcludedActions {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			result = append(result, Disclosure{"excluded_action_kind:" + kind, "excluded", source.SQLite.ExcludedActions[kind], "Unknown/local action payloads were not collected; only their count is available."})
		}
		result = append(result, Disclosure{"hard_deleted_board_data", "limitation", 0, "SQLite cascade-deleted board/position rows cannot be reconstructed from absent data; available logged before-images remain passive provenance."})
	}
	for _, row := range plan.BoardQueries {
		if len(row.PendingKeys) > 0 {
			result = append(result, Disclosure{"query_pending_creation", "deferred", len(row.PendingKeys), "Exact query references await confirmed destination IDs."})
		}
		if len(row.MissingSourceIDs) > 0 || len(row.Rewrite.Unsupported) > 0 {
			result = append(result, Disclosure{"query_identity_selector", "blocked", len(row.MissingSourceIDs) + len(row.Rewrite.Unsupported), "Absent source IDs and identity pattern/order selectors cannot be automatically remapped; original query remains source evidence."})
		}
	}
	return result
}
