package issuestore

import (
	"context"
	"reflect"
	"testing"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/query"
)

func TestGitHubSQLiteCalendarAndSprintSortParity(t *testing.T) {
	database, err := db.Initialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	early, late := "2020-01-01", "2099-01-01"
	issues := []models.Issue{{Title: "no dates", Sprint: "b"}, {Title: "late due", DueDate: &late, DeferUntil: &early, DeferCount: 2, Sprint: "a"}, {Title: "early due", DueDate: &early, DeferUntil: &late, DeferCount: 1, Sprint: "c"}}
	rows := []ghstore.Record{}
	for _, issue := range issues {
		if err := database.CreateIssue(&issue); err != nil {
			t.Fatal(err)
		}
		if err := database.UpdateIssue(&issue); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, ghstore.Record{Issue: issue})
	}
	snapshot, err := NewGitHubQuerySnapshot(context.Background(), rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, expression := range []string{"type=task sort:due", "type=task sort:-due_date", "type=task sort:defer", "type=task sort:-defer_until", "type=task sort:defer_count", "type=task sort:sprint"} {
		a, err := query.Execute(database, expression, "", query.ExecuteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		b, err := query.Execute(snapshot, expression, "", query.ExecuteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ids := func(rows []models.Issue) []string {
			out := []string{}
			for _, r := range rows {
				out = append(out, r.ID)
			}
			return out
		}
		if !reflect.DeepEqual(ids(a), ids(b)) {
			t.Fatal(expression, ids(a), ids(b))
		}
	}
}
