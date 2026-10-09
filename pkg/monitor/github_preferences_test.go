package monitor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/marcus/td/internal/ghcontext"
)

func testMonitorPreferences(t *testing.T) *localMonitorPreferences {
	t.Helper()
	return &localMonitorPreferences{ctx: context.Background(), path: filepath.Join(t.TempDir(), "monitor.json")}
}
func TestMonitorPreferencesScopeAndConcurrentUpdates(t *testing.T) {
	scope := ghcontext.Scope{Directory: t.TempDir(), Worktree: "/one"}
	first := monitorPreferencesForScope(context.Background(), scope)
	same := monitorPreferencesForScope(context.Background(), scope)
	scope.Worktree = "/two"
	other := monitorPreferencesForScope(context.Background(), scope)
	if first.path != same.path || first.path == other.path {
		t.Fatal("preference isolation failed")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs <- first.Update(func(p *monitorPreferences) { p.GettingStartedSeen = true })
		}()
		go func() {
			defer wg.Done()
			errs <- same.Update(func(p *monitorPreferences) { p.BoardViews["bd-gh-1"] = "backlog" })
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := first.Load()
	if err != nil || !p.GettingStartedSeen || p.BoardViews["bd-gh-1"] != "backlog" {
		t.Fatal(p, err)
	}
	p, err = other.Load()
	if err != nil || p.GettingStartedSeen || len(p.BoardViews) != 0 {
		t.Fatal("worktree preferences leaked", p, err)
	}
	info, err := os.Stat(first.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("preference file is not private", info, err)
	}
}
func TestMonitorPreferencesInvalidFileAndWritePreserveExistingState(t *testing.T) {
	p := testMonitorPreferences(t)
	if err := p.Update(func(v *monitorPreferences) { v.LastBoardID = "bd-gh-1" }); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p.path)
	if err := p.Update(func(v *monitorPreferences) { v.PaneHeights = [3]float64{0.2, 0.2, 0.2} }); err == nil {
		t.Fatal("invalid proportions saved")
	}
	after, _ := os.ReadFile(p.path)
	if string(before) != string(after) {
		t.Fatal("invalid write changed file")
	}
	for _, body := range []string{`{"version":2}`, `{"version":1,"unknown":true}`, string(before) + " {}", "invalid"} {
		if err := os.WriteFile(p.path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := p.Update(func(v *monitorPreferences) { v.GettingStartedSeen = true }); err == nil {
			t.Fatal("invalid file overwritten", body)
		}
		got, _ := os.ReadFile(p.path)
		if string(got) != body {
			t.Fatal("invalid file destroyed")
		}
	}
}

func TestGitHubFirstRunNeverStartsTDSyncPrompt(t *testing.T) {
	m := Model{DataSource: &dashboardOnlyFixture{}, IsFirstRunInit: true, GettingStartedOpen: true}
	result, cmd := m.handleGettingStartedAction("close")
	if cmd != nil || result.IsFirstRunInit || result.GettingStartedOpen {
		t.Fatal("GitHub onboarding invoked td-sync")
	}
}
