package serve

import (
	"context"
	"errors"
	"github.com/marcus/td/internal/ghcontext"
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubMonitorReadsOnlyWebFocusAndPreservesFailedContext(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	web := ghcontext.Scope{Directory: dir, Path: filepath.Join(dir, "web.json")}
	cli := ghcontext.Scope{Directory: dir, Path: filepath.Join(dir, "cli.json")}
	original, err := web.Update(ctx, func(state *ghcontext.State) error { state.Focus = "7"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Update(ctx, func(state *ghcontext.State) error { state.Focus = "gh-99"; return nil }); err != nil {
		t.Fatal(err)
	}
	store := &githubSessionStore{scope: web, sessionID: original.Session.ID}
	focus, err := store.ReadFocus(ctx)
	if err != nil || focus == nil || *focus != "gh-7" {
		t.Fatalf("%v %v", focus, err)
	}
	state, err := cli.Update(ctx, nil)
	if err != nil || state.Focus != "gh-99" {
		t.Fatal("web read touched CLI focus")
	}
	if _, err := web.Update(ctx, func(state *ghcontext.State) error { state.Focus = "invalid"; return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(web.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadFocus(ctx); err == nil {
		t.Fatal("invalid focus accepted")
	}
	after, err := os.ReadFile(web.Path)
	if err != nil || string(after) != string(before) {
		t.Fatal("invalid focus was rewritten")
	}
	if _, err := web.Update(ctx, func(state *ghcontext.State) error { state.Focus = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	if focus, err := store.ReadFocus(ctx); err != nil || focus != nil {
		t.Fatal("empty focus is not nil")
	}
	if _, err := web.Update(ctx, func(state *ghcontext.State) error { web.NewSession(state); return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadFocus(ctx); !errors.Is(err, errWebSessionChanged) {
		t.Fatalf("identity error lost: %v", err)
	}
}
