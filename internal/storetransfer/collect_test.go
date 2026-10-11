package storetransfer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

func TestCollectSQLiteValidatesBeforeReturningWithoutSourceMutation(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	issue := &models.Issue{Title: "Source task", Description: strings.Repeat("original", 2000)}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".todos", "issues.db")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Collect(context.Background(), dir, "source-stable", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Source.SQLite.Tasks) != 1 || len(result.Preview.Identities) != 2 {
		t.Fatalf("incomplete preview: %+v", result.Preview)
	}
	if result.Source.Envelope.Namespace != "source-stable" {
		t.Fatal("lost explicit source identity")
	}
	if result, err := Collect(context.Background(), dir, "source-stable", 1000); !errors.Is(err, ErrCapacity) || result != nil {
		t.Fatalf("capacity returned partial source: %v", err)
	}
	if result, err := Collect(context.Background(), dir, "", 0); err == nil || result != nil {
		t.Fatal("empty source identity accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("source mutated")
	}
}

func TestCollectRejectsMissingSourceCancellationAndGHWithoutSQLiteFallback(t *testing.T) {
	dir := t.TempDir()
	if result, err := Collect(context.Background(), dir, "source", 0); err == nil || result != nil {
		t.Fatal("missing SQLite accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("missing source initialized")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := Collect(ctx, dir, "source", 0); !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("lost cancellation: %v", err)
	}
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	// An absent gh executable must remain an explicit GitHub-store failure.
	t.Setenv("PATH", t.TempDir())
	if result, err := Collect(context.Background(), dir, "", 0); err == nil || result != nil || !strings.Contains(err.Error(), "gh") {
		t.Fatalf("missing gh was hidden: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("GitHub source fell back to SQLite")
	}
}
