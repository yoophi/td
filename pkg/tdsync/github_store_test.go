package tdsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

func TestGitHubStoreGateSkipsSQLiteAndServerWithAutosyncEnabled(t *testing.T) {
	dir := t.TempDir()
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	t.Setenv("TD_AUTH_KEY", "fixture-auth-must-not-enable-github-autosync")
	for _, supplied := range []*db.DB{nil, {}} {
		// The zero DB would panic if the runtime tried to read its sync_state.
		s, err := New(Options{BaseDir: dir, DB: supplied})
		if err != nil {
			t.Fatal(err)
		}
		gate := s.Gate()
		if gate.Open || gate.Source != "store=gh-issue" || gate.Authenticated || gate.Configured {
			t.Fatal("GitHub entered SQLite autosync", gate)
		}
		result, err := s.Once(context.Background())
		if err != nil || result != (Result{}) {
			t.Fatal("GitHub replay was attempted", result, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("autosync opened SQLite", err)
	}
}
