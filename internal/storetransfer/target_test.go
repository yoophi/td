package storetransfer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

func TestInspectNewSQLiteTargetDoesNotInitializeOrSelectStore(t *testing.T) {
	dir := t.TempDir()
	spec := TargetSpec{ProjectDir: dir, Store: "sqlite", Namespace: "destination-stable"}
	result, err := InspectTarget(context.Background(), spec, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsSQLiteInitialization || len(result.Observation.SQLite.Tasks) != 0 {
		t.Fatal("new target not reported accurately")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos")); !os.IsNotExist(err) {
		t.Fatal("preview initialized a directory")
	}
	if err := os.Symlink(filepath.Join(dir, "absent"), filepath.Join(dir, ".todos")); err != nil {
		t.Fatal(err)
	}
	if result, err := InspectTarget(context.Background(), spec, 0); err == nil || result != nil {
		t.Fatal("dangling destination directory accepted")
	}
}

func TestInspectExplicitSQLiteTargetUnderGHConfigIsNotSourceFallback(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	issue := &models.Issue{Title: "existing destination"}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".todos", "issues.db")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := InspectTarget(context.Background(), TargetSpec{ProjectDir: dir, Store: "sqlite", Namespace: "target"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.NeedsSQLiteInitialization || len(result.Observation.SQLite.Tasks) != 1 {
		t.Fatal("existing target not read")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("target mutated")
	}
	cfg, err := config.Load(dir)
	if err != nil || cfg.Store != "gh-issue" {
		t.Fatal("current store selection mutated")
	}
	if source, err := Collect(context.Background(), dir, "target", 0); err == nil || source != nil {
		t.Fatal("source bypassed its selected GH store")
	}
	if source, err := db.ReadSQLiteWorkspace(context.Background(), dir); err == nil || source != nil {
		t.Fatal("destination opt-in weakened source fallback guard")
	}
}

func TestInspectTargetRejectsInvalidOptionsAndPreservesCancellation(t *testing.T) {
	dir := t.TempDir()
	for _, spec := range []TargetSpec{{ProjectDir: dir}, {Store: "sqlite", Namespace: "target"}, {ProjectDir: dir, Store: "sqlite"}, {ProjectDir: dir, Store: "sqlite", Namespace: "target", Remote: "origin"}, {ProjectDir: dir, Store: "gh-issue"}} {
		if result, err := InspectTarget(context.Background(), spec, 0); err == nil || result != nil {
			t.Fatalf("invalid target accepted: %+v", spec)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := InspectTarget(ctx, TargetSpec{}, 0); !errors.Is(err, context.Canceled) || result != nil {
		t.Fatal("cancellation hidden")
	}
	t.Setenv("PATH", t.TempDir())
	if result, err := InspectTarget(context.Background(), TargetSpec{ProjectDir: dir, Store: "gh-issue", Remote: "origin"}, 0); err == nil || result != nil || !strings.Contains(err.Error(), "gh CLI") {
		t.Fatal("missing gh was hidden", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos")); !os.IsNotExist(err) {
		t.Fatal("GitHub failure fell back to SQLite")
	}
}

func TestInspectGHRemoteAndIssuesPreflightIsReadOnlyAndNotDuplicated(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", dir}, {"-C", dir, "remote", "add", "origin", "https://github.com/owner/repo.git"}} {
		if out, err := exec.Command(git, args...).CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %s %v", out, err)
		}
	}
	bin := t.TempDir()
	trace := filepath.Join(bin, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TD_TARGET_TEST_TRACE"
case "$*" in
 'auth token --hostname github.com') printf 'fixture-credential' ;;
 'api --hostname github.com repos/owner/repo --include') printf 'HTTP/2.0 200 OK\n\n{"full_name":"owner/repo","has_issues":true,"archived":false}' ;;
 *'/issues?state=all&per_page=100'*) printf 'HTTP/2.0 200 OK\n\n[[]]' ;;
 *) echo 'unexpected or write API call' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_TARGET_TEST_TRACE", trace)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	result, err := InspectTarget(context.Background(), TargetSpec{ProjectDir: dir, Store: "gh-issue", Remote: "origin"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Spec.Namespace != "owner/repo" || result.Observation.SQLite != nil || result.NeedsSQLiteInitialization {
		t.Fatal("wrong destination scope")
	}
	calls, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "auth token") != 1 || strings.Count(string(calls), "api --hostname github.com repos/owner/repo --include\n") != 1 {
		t.Fatalf("duplicated auth/repository checks: %s", calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos")); !os.IsNotExist(err) {
		t.Fatal("GH inspection initialized local store")
	}
}
