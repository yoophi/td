package issuestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

func TestSQLiteReaderStatesAndCancellation(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []models.Status{models.StatusOpen, models.StatusInProgress, models.StatusBlocked, models.StatusInReview, models.StatusClosed} {
		issue := &models.Issue{Title: "Reader fixture", Type: models.TypeTask, Priority: models.PriorityP2, Status: status}
		if err := database.CreateIssue(issue); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReader(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	for _, tc := range []struct {
		all   bool
		count int
	}{{false, 4}, {true, 5}} {
		records, err := reader.List(context.Background(), tc.all)
		if err != nil || len(records) != tc.count {
			t.Fatalf("all=%v records=%v err=%v", tc.all, records, err)
		}
		got, err := reader.Get(context.Background(), records[0].ID)
		if err != nil || got.ID != records[0].ID {
			t.Fatalf("get=%v err=%v", got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.List(ctx, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := reader.Get(ctx, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestGitHubReaderDoesNotFallback(t *testing.T) {
	dir := t.TempDir()
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := OpenReader(context.Background(), dir); err == nil {
		t.Fatal("missing gh accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("unexpected SQLite fallback")
	}
}
