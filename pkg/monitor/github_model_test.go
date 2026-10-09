package monitor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
)

func TestGitHubModelActualActorCancellationNoSQLiteOrSync(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/repo.git"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if data, err := c.CombinedOutput(); err != nil {
			t.Fatal(string(data), err)
		}
	}
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TD_CONTEXT_ID", "real-monitor-constructor")
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = auth ]; then exit 0; fi
printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n{"full_name":"owner/repo","has_issues":true}'
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	m, err := NewGitHubModel(context.Background(), dir, time.Minute, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if m.DB != nil || m.SessionID == "" || m.DataSource == nil || m.BoardSource == nil || m.syncRuntime == nil || m.syncRuntime.service != nil {
		t.Fatal("remote construction initialized SQLite/sync or lost actor")
	}
	source := m.DataSource.(*GitHubDataSource)
	if source.actor != m.SessionID {
		t.Fatal("actor mismatch")
	}
	if _, err := source.focus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if msg := source.Fetch("", false, SortByPriority); !errors.Is(msg.Error, context.Canceled) {
		t.Fatal("close did not cancel requests", msg.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created", err)
	}
}

func TestGitHubModelMissingGHIsExplicitAndNeverCreatesSQLite(t *testing.T) {
	dir := t.TempDir()
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := NewGitHubModel(context.Background(), dir, time.Minute, "test"); err == nil {
		t.Fatal("missing gh silently accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite fallback", err)
	}
}
