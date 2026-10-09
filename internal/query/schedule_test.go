package query

import (
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestScheduleCalendarFieldsAndSQLAliases(t *testing.T) {
	today, future := "2026-10-10", "2026-10-11"
	src := &dateSource{issues: []models.Issue{{ID: "a", DueDate: &today, DeferUntil: &future, DeferCount: 2}, {ID: "b", DueDate: &future}, {ID: "c"}}}
	for _, tc := range []struct {
		expression string
		want       int
		column     string
	}{{"due <= 2026-10-10", 1, "due_date"}, {"due_date >= 2026-10-10", 2, "due_date"}, {"defer > 2026-10-10", 1, "defer_until"}, {"defer_until = NULL", 2, "defer_until"}, {"defer_count > 1", 1, "defer_count"}} {
		q, err := Parse(tc.expression)
		if err != nil || len(q.Validate()) > 0 {
			t.Fatal(tc, err)
		}
		e := NewEvaluator(&EvalContext{Now: time.Date(2026, 10, 10, 0, 1, 0, 0, time.FixedZone("KST", 9*60*60))}, q)
		matcher, err := e.ToMatcher()
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, issue := range src.issues {
			if matcher(issue) {
				count++
			}
		}
		if count != tc.want {
			t.Fatal(tc, count)
		}
		conditions, err := e.ToSQLConditions()
		if err != nil || len(conditions) != 1 || !strings.Contains(conditions[0].Clause, tc.column) {
			t.Fatal(tc, conditions, err)
		}
	}
}
