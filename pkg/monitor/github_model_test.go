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
	if m.RefreshInterval != time.Minute {
		t.Fatalf("one-minute interval changed to %s", m.RefreshInterval)
	}
	for _, interval := range []time.Duration{time.Second, 30 * time.Second, 10 * time.Minute} {
		other, err := NewGitHubModel(context.Background(), dir, interval, "test")
		if err != nil {
			t.Fatal(err)
		}
		if other.RefreshInterval != max(interval, MinRefreshInterval) {
			t.Fatalf("requested %s, got %s", interval, other.RefreshInterval)
		}
		_ = other.Close()
	}
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
	// UI writes must not alter shared project configuration.
	originalPreferencesPath := m.preferences.path
	m.preferences.path = filepath.Join(t.TempDir(), "preferences.json")
	before, err := os.ReadFile(filepath.Join(dir, ".todos", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.SearchQuery = "device-only search"
	m.SortMode = SortByUpdatedDesc
	if msg := m.saveFilterState()(); msg != nil {
		t.Fatal(msg)
	}
	m.PaneHeights = [3]float64{0.2, 0.4, 0.4}
	if msg := m.savePaneHeightsAsync()().(PaneHeightsSavedMsg); msg.Error != nil {
		t.Fatal(msg.Error)
	}
	if msg := m.markGettingStartedSeen()(); msg != nil {
		t.Fatal(msg)
	}
	prefs, err := m.preferences.Load()
	if err != nil || prefs.Filter.SearchQuery != m.SearchQuery || !prefs.GettingStartedSeen || prefs.PaneHeights != m.PaneHeights {
		t.Fatal(prefs, err)
	}
	after, err := os.ReadFile(filepath.Join(dir, ".todos", "config.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("shared config changed", err)
	}
	if msg := m.restoreFilterState()().(RestoreFilterMsg); msg.SearchQuery != m.SearchQuery {
		t.Fatal("local filter not restored")
	}
	for _, construct := range []func() (*Model, error){
		func() (*Model, error) { return NewEmbedded(dir, time.Minute, "test") },
		func() (*Model, error) {
			return NewEmbeddedWithOptions(EmbeddedOptions{BaseDir: dir, Interval: time.Minute, Version: "test", Theme: DefaultTheme()})
		},
	} {
		embedded, err := construct()
		if err != nil {
			t.Fatal(err)
		}
		if embedded.DB != nil || !embedded.Embedded || embedded.SessionID != m.SessionID || embedded.syncRuntime.service != nil {
			t.Fatal("embedded constructor used SQLite/sync or changed actor")
		}
		if err := embedded.Close(); err != nil {
			t.Fatal(err)
		}
		if msg := embedded.DataSource.Fetch("", false, SortByPriority); !errors.Is(msg.Error, context.Canceled) {
			t.Fatal("embedded close lost cancellation", msg.Error)
		}
	}
	// Linked worktrees use their own real branch/actor and UI preferences.
	for _, args := range [][]string{{"-c", "user.name=Fixture", "-c", "user.email=yoophi@gmail.com", "commit", "--allow-empty", "-m", "fixture"}, {"worktree", "add", "-b", "monitor-other", filepath.Join(t.TempDir(), "linked")}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if data, err := c.CombinedOutput(); err != nil {
			t.Fatal(string(data), err)
		}
		if args[0] == "worktree" {
			linked, err := NewGitHubModelForWorktree(context.Background(), dir, args[4], time.Minute, "test")
			if err != nil {
				t.Fatal(err)
			}
			if linked.SessionID == m.SessionID || linked.DataSource.(*GitHubDataSource).branch != "monitor-other" || linked.preferences.path == originalPreferencesPath {
				t.Fatal("linked worktree actor/preferences not isolated")
			}
			_ = linked.Close()
		}
	}
	if err := config.SetStore(dir, "sqlite", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NotesFactory(context.Background()); err == nil {
		t.Fatal("GitHub notes fell back to SQLite after store change")
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
