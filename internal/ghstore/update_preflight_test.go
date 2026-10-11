package ghstore

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestUpdatePreflightSelectionUsesOneBulkReadWithoutWrites(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	ids := []string{}
	for n := 10; n < 42; n++ {
		r := f.issues[1]
		r.Number, r.Title = n, fmt.Sprintf("Task %d", n)
		f.issues[n] = r
		ids = append(ids, fmt.Sprintf("gh-%d", n))
	}
	base := f.client.run
	calls := 0
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		calls++
		if args[4] != "GET" || args[5] != "repos/owner/repo/issues?state=all&per_page=100" {
			t.Fatal("preflight performed a write or per-task read", args)
		}
		return base(ctx, dir, payload, args...)
	}
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	title, status := "Changed", models.StatusInProgress
	activity := models.Activity{Kind: "comment", SessionID: "ses_actor", Message: "Prepared comment"}
	original := activity
	selected, err := f.client.PreflightUpdate(context.Background(), ids, Changes{Title: &title, Status: &status}, TransitionOptions{SessionID: "ses_actor"}, &activity)
	if err != nil || len(selected) != len(ids) || calls != 1 || !reflect.DeepEqual(original, activity) {
		t.Fatal("invalid preflight result or cost", err, len(selected), calls, activity)
	}
	for i, r := range selected {
		if r.ID != ids[i] || r.Title != fmt.Sprintf("Task %d", i+10) || r.Status != models.StatusOpen {
			t.Fatal("selection was reordered or projected as a future result", r)
		}
	}
}
