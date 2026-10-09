package ghstore

import (
	"context"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestScheduleFieldsPreserveDetailsClearAndCount(t *testing.T) {
	f := newReviewFixture(t)
	ctx := context.Background()
	due, deferDate := "2026-10-10", "2026-10-11"
	r, err := f.client.Update(ctx, "1", Changes{DueDate: &due, DeferUntil: &deferDate})
	if err != nil || *r.DueDate != due || *r.DeferUntil != deferDate || r.DeferCount != 0 {
		t.Fatal(r, err)
	}
	for _, value := range []string{"2026-10-11", "2026-10-10", "2026-10-12"} {
		r, err = f.client.Update(ctx, "1", Changes{DeferUntil: &value})
		if err != nil {
			t.Fatal(err)
		}
	}
	if r.DeferCount != 1 || *r.DueDate != due {
		t.Fatal(r)
	}
	clearDate := ""
	r, err = f.client.Update(ctx, "1", Changes{DueDate: &clearDate, DeferUntil: &clearDate})
	if err != nil || r.DueDate != nil || r.DeferUntil != nil || r.DeferCount != 1 {
		t.Fatal(r, err)
	}
	before := f.writes
	invalid := "2026-02-30"
	if _, err := f.client.Update(ctx, "1", Changes{DueDate: &invalid}); err == nil || f.writes != before {
		t.Fatal("invalid date written", err)
	}
	if _, err := f.client.Update(ctx, "1", Changes{Details: &IssueDetails{Status: models.StatusOpen}, DeferUntil: &deferDate}); err == nil || f.writes != before {
		t.Fatal("ambiguous details written", err)
	}
	observed, err := f.client.Get(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	f.issue["title"] = "external edit"
	if _, err := f.client.UpdateObserved(ctx, observed, Changes{DueDate: &due}); err == nil || f.writes != before {
		t.Fatal("stale date edit written", err)
	}
}
