package storetransfer

import (
	"testing"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

func TestIdentityPlanUsesQualifiedExplicitBindingsAndLeavesCreationUnresolved(t *testing.T) {
	source, err := NewSQLite("original", sqliteReferenceFixture(), 0)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := NewSQLite("destination", &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion, Tasks: []db.SQLiteExportTask{{Issue: models.Issue{ID: "destination-parent"}}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := source.Preview()
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]string{}
	for _, identity := range preview.Identities {
		if identity.ID == "parent" {
			bindings[identity.Key] = "destination-parent"
		}
	}
	plan, err := PlanIdentities(source, destination, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Assignments) != 4 || plan.KnownTaskMappings()["parent"] != "destination-parent" || plan.KnownTaskMappings()["child"] != "" {
		t.Fatal("guessed ID or dropped entity")
	}
	for _, assignment := range plan.Assignments {
		if assignment.SourceID == "child" && (assignment.Action != "create" || assignment.TargetID != "") {
			t.Fatal("creation was prematurely confirmed")
		}
	}
	for _, test := range []map[string]string{{"parent": "destination-parent"}, {"unknown": "destination-parent"}} {
		if _, err := PlanIdentities(source, destination, test); err == nil {
			t.Fatal("unqualified/unknown binding accepted")
		}
	}
	if _, err := PlanIdentities(source, source, nil); err == nil {
		t.Fatal("self transfer accepted")
	}
}

func TestIdentityPlanRejectsWrongKindDuplicateAndBuiltinReplacement(t *testing.T) {
	d := sqliteReferenceFixture()
	d.Boards = append(d.Boards, db.SQLiteExportBoard{Board: models.Board{ID: "bd-all-issues", IsBuiltin: true}})
	s, err := NewSQLite("source", d, 0)
	if err != nil {
		t.Fatal(err)
	}
	target, err := NewSQLite("destination", &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion, Tasks: []db.SQLiteExportTask{{Issue: models.Issue{ID: "target-task"}}}, Notes: []models.Note{{ID: "target-note"}}, Boards: []db.SQLiteExportBoard{{Board: models.Board{ID: "target-board"}}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Preview()
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, identity := range preview.Identities {
		keys[identity.ID] = identity.Key
	}
	plan, err := PlanIdentities(s, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, assignment := range plan.Assignments {
		if assignment.SourceID == "bd-all-issues" {
			found = assignment.Action == "builtin" && assignment.TargetID == "bd-all-issues"
		}
	}
	if !found {
		t.Fatal("builtin was duplicated or assigned a guessed carrier")
	}
	for _, bindings := range []map[string]string{
		{keys["parent"]: "target-note"}, {keys["parent"]: "missing"},
		{keys["parent"]: "target-task", keys["child"]: "target-task"},
		{keys["bd-all-issues"]: "target-board"}, {keys["board"]: "bd-all-issues"},
	} {
		if result, err := PlanIdentities(s, target, bindings); err == nil || result != nil {
			t.Fatalf("invalid mapping accepted: %+v", bindings)
		}
	}
	badTarget, err := NewSQLite("bad-target", &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion, Notes: []models.Note{{ID: "bd-all-issues"}}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PlanIdentities(s, badTarget, nil); err == nil {
		t.Fatal("reserved target ID collision ignored")
	}
}
