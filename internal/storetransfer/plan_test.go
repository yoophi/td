package storetransfer

import (
	"testing"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestDataPlanPendingReferencesAndResolvedDestinationIDs(t *testing.T) {
	data := sqliteReferenceFixture()
	data.Boards[0].Board.Query = `any(id, "parent", "note", "future")`
	source, err := NewSQLite("source", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewSQLite("target", &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion,
		Tasks:  []db.SQLiteExportTask{{Issue: models.Issue{ID: "new-parent"}}, {Issue: models.Issue{ID: "new-child"}}},
		Boards: []db.SQLiteExportBoard{{Board: models.Board{ID: "new-board"}}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildDataPlan(source, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := plan.BoardQueries[0]
	if len(q.PendingKeys) != 1 || len(q.MissingSourceIDs) != 2 {
		t.Fatalf("non-task reference incorrectly deferred: %+v", q)
	}
	if rows, err := plan.ReadyRelations(); err == nil || rows != nil {
		t.Fatal("unresolved relationships returned")
	}
	if rows, err := plan.ReadyPositions(); err == nil || rows != nil {
		t.Fatal("unresolved positions returned")
	}
	bindings := map[string]string{}
	for _, a := range plan.Identities.Assignments {
		switch a.SourceID {
		case "parent":
			bindings[a.SourceKey] = "new-parent"
		case "child":
			bindings[a.SourceKey] = "new-child"
		case "board":
			bindings[a.SourceKey] = "new-board"
		}
	}
	plan, err = BuildDataPlan(source, target, bindings)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := plan.ReadyRelations()
	if err != nil {
		t.Fatal(err)
	}
	if len(relations) != 2 || relations[0].FromID != "new-child" || relations[0].ToID != "new-parent" {
		t.Fatalf("source IDs leaked into destination relations: %+v", relations)
	}
	positions, err := plan.ReadyPositions()
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 || positions[0].BoardID != "new-board" || positions[0].IssueID != "new-child" || positions[0].Position != 1 {
		t.Fatalf("incorrect position mapping: %+v", positions)
	}
	if len(plan.BoardQueries[0].PendingKeys) != 0 || len(plan.BoardQueries[0].MissingSourceIDs) != 2 {
		t.Fatal("missing references silently rewritten")
	}
	// One unresolved late reference must invalidate the entire batch.
	plan.Relations = append(plan.Relations, RelationPlan{Kind: "depends_on", FromKey: "missing", ToKey: "missing", Handling: "apply"})
	if rows, err := plan.ReadyRelations(); err == nil || rows != nil {
		t.Fatal("partial ready batch returned")
	}
}

func TestDataPlanGitHubArchivesLegacyBlocksAndPositionTombstones(t *testing.T) {
	data := sqliteReferenceFixture()
	data.Tasks[1].Dependencies = append(data.Tasks[1].Dependencies, models.IssueDependency{IssueID: "child", DependsOnID: "parent", RelationType: "blocks"})
	deleted := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	data.Boards[0].Positions[0].DeletedAt = &deleted
	source, err := NewSQLite("source", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewGitHub(&ghstore.WorkspaceExport{Repository: "owner/target"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildDataPlan(source, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Relations) != 3 || plan.Relations[2].Handling != "passive_archive" || plan.Positions[0].Handling != "passive_archive" {
		t.Fatalf("historical rows activated: %+v", plan)
	}
	positions, err := plan.ReadyPositions()
	if err != nil || len(positions) != 0 {
		t.Fatal("archival position required active mapping")
	}
	counts := map[string]int{}
	for _, d := range plan.Disclosures {
		counts[d.Code] = d.Count
	}
	if counts["task_content_state_schedule_deletion"] != 2 || counts["notes_content_flags"] != 1 || counts["original_actors_reviews_history"] != 1 || counts["sqlite_legacy_blocks"] != 1 || counts["deleted_position_rows"] != 1 || counts["relationships"] != 2 || counts["board_positions"] != 0 {
		t.Fatalf("incorrect preservation counts: %+v", counts)
	}
	// The same tombstone remains representable in a SQLite destination.
	sqlTarget, err := NewSQLite("sql-target", &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion}, 0)
	if err != nil {
		t.Fatal(err)
	}
	sqlPlan, err := BuildDataPlan(source, sqlTarget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sqlPlan.Positions[0].Handling != "apply" || sqlPlan.Relations[2].Handling != "apply" {
		t.Fatal("representable SQLite data discarded")
	}
	// Typed fields are display data: the immutable capsule remains authoritative.
	source.SQLite.Tasks = nil
	immutable, err := BuildDataPlan(source, sqlTarget, nil)
	if err != nil || len(immutable.Relations) != 3 {
		t.Fatal("mutable display fields used as input")
	}
}

func TestDataPlanResolversRejectUnknownHandlingAndNil(t *testing.T) {
	var absent *DataPlan
	if _, err := absent.ReadyRelations(); err == nil {
		t.Fatal("nil relation plan accepted")
	}
	if _, err := absent.ReadyPositions(); err == nil {
		t.Fatal("nil position plan accepted")
	}
	p := &DataPlan{Relations: []RelationPlan{{Handling: "unknown"}}, Positions: []PositionPlan{{Handling: "unknown"}}}
	if _, err := p.ReadyRelations(); err == nil {
		t.Fatal("unknown handling silently activated")
	}
	if _, err := p.ReadyPositions(); err == nil {
		t.Fatal("unknown handling silently activated")
	}
}

func TestPreservationReportIncludesBoardHistoryAsPassiveEvidence(t *testing.T) {
	source := &Source{GitHub: &ghstore.WorkspaceExport{Boards: []ghstore.BoardRecord{{Details: ghstore.BoardDetails{History: []ghstore.BoardEvent{{Action: "move"}, {Action: "delete"}}}}}}}
	target := &Source{Envelope: Envelope{Store: "sqlite"}}
	for _, d := range preservationReport(source, target, &DataPlan{}) {
		if d.Code == "original_actors_reviews_history" {
			if d.Count != 2 || d.Handling != "passive_archive" {
				t.Fatalf("board history omitted or activated: %+v", d)
			}
			return
		}
	}
	t.Fatal("missing history disclosure")
}
