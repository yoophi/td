package ghcontext

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestPersistentContextIsolationAndHistory(t *testing.T) {
	dir := t.TempDir()
	first := Scope{Directory: dir, Path: filepath.Join(dir, "one.json"), Branch: "main", Worktree: "one"}
	second := Scope{Directory: dir, Path: filepath.Join(dir, "two.json"), Branch: "other", Worktree: "two"}
	a, err := first.Update(context.Background(), func(s *State) error { s.Focus = "gh-7"; s.Session.Name = "First"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := first.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Session.ID != again.Session.ID || again.Focus != "gh-7" || b.Session.ID == a.Session.ID || b.Focus != "" {
		t.Fatalf("isolation: %+v %+v %+v", a, b, again)
	}
	fresh, err := first.Update(context.Background(), func(s *State) error { first.NewSession(s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Focus != "" || fresh.Session.PreviousSessionID != a.Session.ID || len(fresh.History) != 1 {
		t.Fatalf("lost history: %+v", fresh)
	}
	list, err := first.List()
	if err != nil || len(list) != 3 {
		t.Fatalf("list=%v err=%v", list, err)
	}
}

func TestContextUnknownFormatIsPreserved(t *testing.T) {
	dir := t.TempDir()
	scope := Scope{Directory: dir, Path: filepath.Join(dir, "test.json")}
	for _, data := range []string{`{"version":99}`, `broken`} {
		if err := os.WriteFile(scope.Path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := scope.Update(context.Background(), nil); err == nil {
			t.Fatal("accepted corrupt state")
		}
		unchanged, err := os.ReadFile(scope.Path)
		if err != nil || string(unchanged) != data {
			t.Fatal("destroyed corrupt state")
		}
	}
}

func TestContextConcurrentCreation(t *testing.T) {
	dir := t.TempDir()
	scope := Scope{Directory: dir, Path: filepath.Join(dir, "test.json")}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := scope.Update(context.Background(), nil)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- state.Session.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent creation lost identity")
		}
	}
}

func TestScopeUsesFullExplicitIdentityAndContext(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	t.Setenv("TD_SESSION_ID", "same-prefix-with-more-than-thirty-two-characters-a")
	first, err := Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_SESSION_ID", "same-prefix-with-more-than-thirty-two-characters-b")
	second, err := Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == second.Path {
		t.Fatal("truncated explicit IDs collided")
	}
	t.Setenv("TD_CONTEXT_ID", "new-context")
	third, err := Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if second.Path == third.Path {
		t.Fatal("contexts collided")
	}
}

func TestWebScopeIndependentOfLauncher(t *testing.T) {
	dir := t.TempDir()
	if data, err := exec.Command("git", "init", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	t.Setenv("TD_CONTEXT_ID", "fixture-terminal-one")
	first, err := ResolveWeb(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	cli, err := Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_CONTEXT_ID", "fixture-terminal-two")
	second, err := ResolveWeb(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != second.Path || first.Path == cli.Path {
		t.Fatal("web identity depends on launcher or shares CLI identity")
	}
	other, err := ResolveWeb(context.Background(), dir, "owner/other")
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == other.Path {
		t.Fatal("repository web identities collide")
	}
}
