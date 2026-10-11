package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
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
		{}, {"--dry-run", "--execute"}, {"--execute"}, {"--dry-run", "--to", "unknown"},
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
