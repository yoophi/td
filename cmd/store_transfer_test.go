package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/storetransfer"
)

func runTransferTest(args ...string) (string, error) {
	cmd := newStoreTransferCommand()
	cmd.Flags().Bool("json", false, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestStoreTransferInvalidModesAndBindingsDoNotInitialize(t *testing.T) {
	dir := storeTestDir(t)
	for _, args := range [][]string{
		{}, {"--dry-run", "--execute"}, {"--execute"}, {"--execute", "--to", "gh-issue"}, {"--dry-run", "--to", "unknown"},
		{"--resume"}, {"--resume", "--execute"}, {"--resume", "--dry-run"}, {"--resume", "--to", "gh-issue"}, {"--resume", "--remote", "origin"}, {"--resume", "--map", "key=id"},
		{"--dry-run", "--to", "sqlite", "--destination", dir},
		{"--dry-run", "--to", "gh-issue", "--destination", dir},
		{"--dry-run", "--to", "gh-issue", "--destination", dir, "--remote", "origin", "--map", "invalid"},
		{"--dry-run", "--to", "gh-issue", "--destination", dir, "--remote", "origin", "--map", "key=id", "--map", "key=id"},
	} {
		out, err := runTransferTest(args...)
		if err == nil || out != "" {
			t.Fatalf("invalid options produced preview: %v %q %v", args, out, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos")); !os.IsNotExist(err) {
		t.Fatal("invalid transfer initialized data")
	}
	cmd, _, err := rootCmd.Find([]string{"store", "transfer"})
	if err != nil || !skipImplicitIssueAccess(cmd) {
		t.Fatal("transfer triggers implicit session/autosync access")
	}
}

func transferGHSourceFixture(t *testing.T) string {
	t.Helper()
	root := storeTestDir(t)
	for _, args := range [][]string{{"init", "--quiet", root}, {"-C", root, "remote", "add", "origin", "https://github.com/owner/repo.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %s %v", out, err)
		}
	}
	if err := config.Save(root, &models.Config{Store: config.StoreGitHub, GitHub: &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
 'auth token --hostname github.com') printf 'fixture-credential' ;;
 'api --hostname github.com repos/owner/repo --include') printf 'HTTP/2.0 200 OK\n\n{"full_name":"owner/repo","has_issues":true,"archived":false}' ;;
 *'/issues?state=all&per_page=100'*) printf '%s\n' 'HTTP/2.0 200 OK' '' '[[{"number":1,"title":"source task","body":"original body\n\n<!-- td:issue:v1\n{\"type\":\"task\",\"priority\":\"P2\",\"details\":{\"status\":\"open\",\"creator_session\":\"source-creator\"}}\n-->","state":"open","labels":[],"user":{"login":"original-author"},"created_at":"2026-10-12T00:00:00Z","updated_at":"2026-10-12T00:00:00Z"}]]' ;;
 *'/issues/comments?per_page=100'*) printf 'HTTP/2.0 200 OK\n\n[[]]' ;;
 *) echo 'unexpected or write API call' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	return root
}

func TestStoreTransferExecuteSQLiteCommitsAndRejectsFreshReplay(t *testing.T) {
	sourceRoot := transferGHSourceFixture(t)
	before, err := os.ReadFile(filepath.Join(sourceRoot, ".todos", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	targetRoot := t.TempDir()
	args := []string{"--execute", "--to", "sqlite", "--destination", targetRoot, "--destination-namespace", "sql-target", "--json"}
	out, err := runTransferTest(args...)
	var result storetransfer.SQLiteTransferResult
	if err != nil || json.Unmarshal([]byte(out), &result) != nil || !result.Committed || result.DryRun || !result.Initialized || result.Tasks != 1 || len(result.SourceSHA256) != 64 || result.TargetStore != "sqlite" || len(result.Preservation) == 0 {
		t.Fatalf("bad execute: %q %v", out, err)
	}
	actual, err := db.ReadSQLiteTransferDestination(context.Background(), targetRoot, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual.Tasks) != 1 || len(actual.TransferArchives) != 1 || actual.Tasks[0].Issue.CreatorSession != "" || actual.Tasks[0].Issue.Title != "source task" {
		t.Fatal("source actors activated or data/capsule missing")
	}
	if actual.TransferArchives[0].SourceSHA256 != result.SourceSHA256 {
		t.Fatal("reported capsule differs from committed archive")
	}
	if out, err := runTransferTest(args...); err == nil || out != "" || !strings.Contains(err.Error(), "previously transferred") {
		t.Fatal("fresh collection repeated inserts", out, err)
	}
	after, err := os.ReadFile(filepath.Join(sourceRoot, ".todos", "config.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("source config changed")
	}
}

type transferBrokenOutput struct{}

func (transferBrokenOutput) Write([]byte) (int, error) { return 0, errors.New("broken stdout") }

func TestStoreTransferExecuteSQLiteReportsOutputFailureAfterCommit(t *testing.T) {
	transferGHSourceFixture(t)
	targetRoot := t.TempDir()
	cmd := newStoreTransferCommand()
	cmd.Flags().Bool("json", false, "")
	cmd.SetOut(transferBrokenOutput{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--execute", "--to", "sqlite", "--destination", targetRoot, "--destination-namespace", "target", "--json"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "committed") || !strings.Contains(err.Error(), "broken stdout") {
		t.Fatal("lost committed outcome or original output error", err)
	}
	actual, err := db.ReadSQLiteTransferDestination(context.Background(), targetRoot, 0)
	if err != nil || len(actual.Tasks) != 1 || len(actual.TransferArchives) != 1 {
		t.Fatal("output failure undid committed rows", err)
	}
}

func TestStoreTransferDryRunBothDirectionsWithReadOnlyGH(t *testing.T) {
	sqlRoot := storeTestDir(t)
	database, err := db.Initialize(sqlRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(sqlRoot, ".todos", "issues.db"))
	if err != nil {
		t.Fatal(err)
	}
	ghRoot := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", ghRoot}, {"-C", ghRoot, "remote", "add", "origin", "https://github.com/owner/repo.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %s %v", out, err)
		}
	}
	bin := t.TempDir()
	script := `#!/bin/sh
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
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	out, err := runTransferTest("--dry-run", "--to", "gh-issue", "--destination", ghRoot, "--source-namespace", "sql-source", "--remote", "origin", "--json")
	var result struct {
		DryRun                    bool            `json:"dry_run"`
		Writes                    int             `json:"writes"`
		SourceStore               string          `json:"source_store"`
		TargetStore               string          `json:"target_store"`
		NeedsSQLiteInitialization bool            `json:"needs_sqlite_initialization"`
		Plan                      json.RawMessage `json:"plan"`
	}
	if err != nil || json.Unmarshal([]byte(out), &result) != nil || !result.DryRun || result.Writes != 0 || result.SourceStore != "sqlite" || result.TargetStore != "gh-issue" || len(result.Plan) == 0 {
		t.Fatalf("bad SQLite→GH preview: %q %v", out, err)
	}
	after, err := os.ReadFile(filepath.Join(sqlRoot, ".todos", "issues.db"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("SQLite source changed")
	}
	if _, err := os.Stat(filepath.Join(ghRoot, ".todos")); !os.IsNotExist(err) {
		t.Fatal("GH destination initialized SQLite")
	}
	if err := config.Save(ghRoot, &models.Config{Store: config.StoreGitHub, GitHub: &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}}); err != nil {
		t.Fatal(err)
	}
	baseDirOverride = &ghRoot
	newSQL := t.TempDir()
	out, err = runTransferTest("--dry-run", "--to", "sqlite", "--destination", newSQL, "--destination-namespace", "sql-target", "--json")
	if err != nil || json.Unmarshal([]byte(out), &result) != nil || !result.DryRun || !result.NeedsSQLiteInitialization || result.SourceStore != "gh-issue" || result.TargetStore != "sqlite" {
		t.Fatalf("bad GH→SQLite preview: %q %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(newSQL, ".todos")); !os.IsNotExist(err) {
		t.Fatal("dry-run initialized destination")
	}
	baseDirOverride = &sqlRoot
	out, err = runTransferTest("--dry-run", "--to", "gh-issue", "--destination", ghRoot, "--source-namespace", "sql-source", "--remote", "origin")
	if err != nil || !strings.Contains(out, "Writes: 0") || !strings.Contains(out, "passive_archive") {
		t.Fatalf("bad human preview: %q %v", out, err)
	}
}
