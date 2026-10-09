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
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/spf13/cobra"
)

func executeGitHubProjectTest(original *cobra.Command, args ...string) (string, error) {
	cmd := &cobra.Command{Use: original.Use, Args: original.Args, RunE: original.RunE, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().String("remote", "", "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().Bool("json", true, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestGitHubProjectSelectionAndDiagnostics(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	runGit(t, dir, "remote", "add", "mirror", "git@github.com:owner/repo.git")
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		for _, command := range []*cobra.Command{syncProjectLinkCmd, syncProjectJoinCmd} {
			out, err := executeGitHubProjectTest(command, "owner/repo", "--remote", "mirror")
			if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"remote_changed":false`) {
				t.Fatal(out, err)
			}
			cfg, err := config.Load(dir)
			if err != nil || cfg.GitHub.Remote != "mirror" || cfg.GitHub.Repo != "owner/repo" {
				t.Fatal(cfg, err)
			}
		}
		out, err := executeGitHubProjectTest(syncProjectListCmd)
		if err != nil || !strings.Contains(out, `"scope":"configured-repository"`) {
			t.Fatal(out, err)
		}
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cmd  *cobra.Command
		args []string
		want string
	}{
		{syncProjectLinkCmd, []string{"other/repo"}, "does not match"},
		{syncProjectLinkCmd, []string{"owner/repo", "--remote", "missing"}, "missing"},
		{syncProjectLinkCmd, []string{"owner/repo", "--force"}, "cannot bypass"},
		{syncProjectJoinCmd, nil, "explicit owner/repository"},
		{syncProjectUnlinkCmd, nil, "requires a repository"},
		{syncProjectCreateCmd, []string{"name"}, "not implemented yet"},
		{syncProjectMembersCmd, nil, "not implemented yet"},
		{syncProjectInviteCmd, []string{"someone@example.com"}, "not implemented yet"},
		{syncProjectKickCmd, []string{"user"}, "not implemented yet"},
		{syncProjectRoleCmd, []string{"user", "writer"}, "not implemented yet"},
	} {
		out, err := executeGitHubProjectTest(tc.cmd, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) || out != "" {
			t.Fatal(tc.cmd.Name(), out, err)
		}
	}
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(cfg)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failure changed config", string(after), err)
	}
	invocations, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(invocations)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "repos/") && arg != "repos/owner/repo" {
				t.Fatal("selection fetched tasks or modified repository", args)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("selection opened SQLite", err)
	}
	if _, err := os.Stat(os.Getenv("TD_SCHEDULE_STATE")); !os.IsNotExist(err) {
		t.Fatal("selection wrote remote data", err)
	}
}

func TestGitHubProjectAliasRegisteredAcrossFeatureModes(t *testing.T) {
	dir := githubScheduleTestDir(t)
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "false")
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		for _, name := range []string{"sync-project", "sp"} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			child := exec.CommandContext(ctx, executable, "-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "--json", name, "list")
			result, err := child.Output()
			cancel()
			if err != nil || !json.Valid(result) || !bytes.Contains(result, []byte(`"scope":"configured-repository"`)) {
				t.Fatal(name, enabled, string(result), err)
			}
		}
	}
}

func TestSQLiteProjectRouteStillRequiresFeature(t *testing.T) {
	storeTestDir(t)
	cmd := &cobra.Command{Use: "list"}
	t.Setenv("TD_FEATURE_SYNC_CLI", "false")
	if handled, err := githubSyncProject(cmd, nil); !handled || err == nil {
		t.Fatal(handled, err)
	}
	t.Setenv("TD_FEATURE_SYNC_CLI", "true")
	if handled, err := githubSyncProject(cmd, nil); handled || err != nil {
		t.Fatal(handled, err)
	}
}
