package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func storeTestDir(t *testing.T) string {
	t.Helper()
	saveAndRestoreGlobals(t)
	dir := t.TempDir()
	baseDirOverride = &dir
	return dir
}

// Exercise command parsing without changing the process-global Cobra flags.
func executeStoreTest(t *testing.T, original *cobra.Command, input string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{Use: original.Use, Args: original.Args, RunE: original.RunE, SilenceErrors: true, SilenceUsage: true}
	cmd.Flags().String("store", "sqlite", "")
	cmd.Flags().String("remote", "origin", "")
	cmd.Flags().Bool("json", false, "")
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func TestStoreConfigLocalAndDefault(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("TD_FEATURE_SYNC_CLI", "false")
	out, err := executeStoreTest(t, configGetCmd, "", "store")
	if err != nil || out != "sqlite\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos")); !os.IsNotExist(err) {
		t.Fatal("reading defaults created project data")
	}
	if err := config.Save(dir, &models.Config{TitleMinLength: 23, FeatureFlags: map[string]bool{"custom": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := executeStoreTest(t, configSetCmd, "", "store", "sqlite"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil || cfg.Store != "sqlite" || cfg.TitleMinLength != 23 || !cfg.FeatureFlags["custom"] {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	out, err = executeStoreTest(t, configListCmd, "", "--json")
	var values map[string]any
	if err != nil || json.Unmarshal([]byte(out), &values) != nil || values["store"] != "sqlite" || values["sync"] != nil {
		t.Fatalf("out=%q err=%v", out, err)
	}
	for _, args := range [][]string{{"store", "invalid"}, {"store", "sqlite", "--remote", "origin"}} {
		if _, err := executeStoreTest(t, configSetCmd, "", args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if cmd, _, err := rootCmd.Find([]string{"config", "setup"}); err != nil || cmd != configSetupCmd {
		t.Fatalf("config setup unavailable: %v", err)
	}
}

func TestStoreSetupCancellationAndSQLiteInit(t *testing.T) {
	dir := storeTestDir(t)
	for _, input := range []string{"", "2\n"} {
		if _, err := executeStoreTest(t, configSetupCmd, input); err == nil {
			t.Fatal("EOF must cancel setup")
		}
		if _, err := os.Stat(filepath.Join(dir, ".todos")); !os.IsNotExist(err) {
			t.Fatal("cancelled setup saved config")
		}
	}
	if _, err := executeStoreTest(t, configSetupCmd, "1\n"); err != nil {
		t.Fatal(err)
	}
	// Config setup creates .todos before a database exists. Init must still work.
	if _, err := executeStoreTest(t, initCmd, "", "--store", "sqlite", "--json"); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
}

func TestStoreGitHubValidationAndSwitchPreservesIssues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh uses a POSIX shell")
	}
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/fork.git")
	runGit(t, dir, "remote", "add", "upstream", "git@github.com:owner/source.git")
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = auth ]; then exit 0; fi\nif [ \"$1\" = api ]; then printf '%s' \"$TD_TEST_GH_RESPONSE\"; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TD_TEST_GH_RESPONSE", `{"full_name":"owner/source","has_issues":true}`)
	database, err := db.Initialize(dir)
	if err != nil {
		t.Fatal(err)
	}
	issue := &models.Issue{Title: "Keep this local issue"}
	if err := database.CreateIssue(issue); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	if _, err := executeStoreTest(t, configSetCmd, "", "store", "gh-issue", "--remote", "upstream"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil || cfg.GitHub == nil || cfg.GitHub.Remote != "upstream" || cfg.GitHub.Repo != "owner/source" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	if database, err := db.Open(dir); err == nil {
		_ = database.Close()
		t.Fatal("gh-issue selection silently opened SQLite")
	}
	before, err := os.ReadFile(filepath.Join(dir, ".todos", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_TEST_GH_RESPONSE", `{"full_name":"owner/fork","has_issues":false}`)
	if _, err := executeStoreTest(t, configSetCmd, "", "store", "gh-issue"); err == nil {
		t.Fatal("accepted disabled Issues")
	}
	after, err := os.ReadFile(filepath.Join(dir, ".todos", "config.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed validation changed config")
	}
	if _, err := executeStoreTest(t, configSetCmd, "", "store", "sqlite"); err != nil {
		t.Fatal(err)
	}
	database, err = db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	got, err := database.GetIssue(issue.ID)
	if err != nil || got.Title != issue.Title {
		t.Fatalf("issue lost after switching stores: %v", err)
	}
}

func TestStoreInitGitHubFailureDoesNotInitialize(t *testing.T) {
	dir := storeTestDir(t)
	if _, err := executeStoreTest(t, initCmd, "", "--store", "gh-issue"); err == nil {
		t.Fatal("accepted non-Git project")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos")); !os.IsNotExist(err) {
		t.Fatal("failed initialization left project data")
	}
}

func TestStoreGitHubMissingGH(t *testing.T) {
	dir := storeTestDir(t)
	t.Setenv("PATH", t.TempDir())
	out, err := executeStoreTest(t, configSetCmd, "", "store", "gh-issue")
	if err == nil || !strings.Contains(err.Error(), "gh CLI not found") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos")); !os.IsNotExist(err) {
		t.Fatal("missing gh changed project data")
	}
}
