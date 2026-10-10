package issuestore

import (
	"context"
	"errors"
	"testing"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

func TestSQLiteWriterPreservesObservationAndLoggedConflict(t *testing.T) {
	database, err := db.Initialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	w := NewSQLiteWriter(database, "writer-session")
	input := &models.Issue{Title: "Original title", CreatorSession: "writer-session"}
	created, err := w.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if input.ID != "" || created.ID == "" {
		t.Fatal("creation mutated the input or omitted assigned identity")
	}
	observed, err := w.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft := *observed
	draft.Title = "First field edit"
	if _, err := w.UpdateObserved(context.Background(), observed, draft); err != nil {
		t.Fatal(err)
	}
	if observed.Title != "Original title" {
		t.Fatal("original observation mutated")
	}
	draft.Title = "Stale field edit"
	if _, err := w.UpdateObserved(context.Background(), observed, draft); !errors.Is(err, db.ErrIssueChanged) {
		t.Fatalf("stale edit: %v", err)
	}
	current, err := w.Get(context.Background(), created.ID)
	if err != nil || current.Title != "First field edit" {
		t.Fatalf("stored issue=%v error=%v", current, err)
	}
	actions, err := database.GetRecentActions("writer-session", 10)
	if err != nil || len(actions) != 2 {
		t.Fatalf("expected only atomic create/update log entries: %v %v", actions, err)
	}
}

func TestSQLiteWriterRejectsCancellationIdentityAndWorkflowWithoutWrites(t *testing.T) {
	database, err := db.Initialize(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	w := NewSQLiteWriter(database, "writer-session")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.Create(ctx, &models.Issue{Title: "Canceled"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := w.Get(ctx, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	created, err := w.Create(context.Background(), &models.Issue{Title: "Normal issue"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := w.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.UpdateObserved(ctx, observed, *observed); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, mutate := range []func(*models.Issue){
		func(i *models.Issue) { i.ID = "another-issue" },
		func(i *models.Issue) { i.Status = models.StatusClosed },
		func(i *models.Issue) { i.ReviewerSession = "forged-reviewer" },
	} {
		draft := *observed
		mutate(&draft)
		if _, err := w.UpdateObserved(context.Background(), observed, draft); err == nil {
			t.Fatal("unsafe update accepted")
		}
	}
	if _, err := w.Create(context.Background(), &models.Issue{Title: "Unchecked close", Status: models.StatusClosed}); err == nil {
		t.Fatal("unchecked lifecycle creation accepted")
	}
	actions, err := database.GetRecentActions("writer-session", 10)
	if err != nil || len(actions) != 1 {
		t.Fatalf("rejected operations wrote history: %v %v", actions, err)
	}
}
