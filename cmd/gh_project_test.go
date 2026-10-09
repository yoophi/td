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
		{syncProjectInviteCmd, []string{"someone@example.com"}, "not implemented yet"},
		{syncProjectKickCmd, []string{"someone@example.com"}, "GitHub login"},
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

func TestGitHubProjectMembersPagesEmptyAndPermissionFailure(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script, err := os.ReadFile(ghPath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "elif '/labels?' in path:"
	route := `elif '/collaborators?' in path:
 if os.environ.get('TD_MEMBERS_DENIED'):sys.stderr.write('fixture HTTP 403 collaborators denied');sys.exit(1)
 data=json.loads(os.environ['TD_MEMBERS_RESPONSE'])
`
	if !strings.Contains(string(script), anchor) {
		t.Fatal("fixture routing changed")
	}
	if err := os.WriteFile(ghPath, []byte(strings.Replace(string(script), anchor, route+anchor, 1)), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_MEMBERS_RESPONSE", `[[{"id":1,"login":"one","role_name":"custom-role","permissions":{"pull":true}}],[{"id":2,"login":"two","role_name":"maintain","permissions":{"maintain":true}}]]`)
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		out, err := executeGitHubProjectTest(syncProjectMembersCmd)
		if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"role_name":"custom-role"`) || !strings.Contains(out, `"scope":"visible-collaborators"`) {
			t.Fatal(out, err)
		}
	}
	t.Setenv("TD_MEMBERS_RESPONSE", `[[]]`)
	out, err := executeGitHubProjectTest(syncProjectMembersCmd)
	if err != nil || !strings.Contains(out, `"members":[]`) {
		t.Fatal(out, err)
	}
	t.Setenv("TD_MEMBERS_DENIED", "1")
	out, err = executeGitHubProjectTest(syncProjectMembersCmd)
	if err == nil || !strings.Contains(err.Error(), "fixture HTTP 403") || out != "" {
		t.Fatal(out, err)
	}
	invocations, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	collections := 0
	for _, line := range strings.Split(strings.TrimSpace(string(invocations)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		for _, arg := range args {
			if strings.Contains(arg, "/collaborators?") {
				collections++
				if !strings.Contains(strings.Join(args, " "), "--paginate --slurp") {
					t.Fatal("collection not paginated", args)
				}
			}
			if strings.HasPrefix(arg, "repos/") && arg != "repos/owner/repo" && arg != "repos/owner/repo/collaborators?affiliation=all&per_page=100" {
				t.Fatal("unexpected per-user/task API", args)
			}
		}
	}
	if collections != 4 {
		t.Fatal("unexpected collection count", collections)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("members opened SQLite", err)
	}
	if _, err := os.Stat(os.Getenv("TD_SCHEDULE_STATE")); !os.IsNotExist(err) {
		t.Fatal("members changed remote state", err)
	}
}

func TestGitHubProjectKickTargetsLoginAndDoesNotRetry(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	writes := filepath.Join(t.TempDir(), "writes.jsonl")
	t.Setenv("TD_KICK_WRITES", writes)
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script, err := os.ReadFile(ghPath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "elif '/labels?' in path:"
	route := `elif '/collaborators/' in path:
 if method!='DELETE':sys.stderr.write('unexpected method');sys.exit(1)
 if os.environ.get('TD_KICK_FAIL')=='denied':sys.stderr.write('fixture HTTP 403 delete denied');sys.exit(1)
 with open(os.environ['TD_KICK_WRITES'],'a') as log:log.write(json.dumps({'path':path,'method':method})+'\n')
 if os.environ.get('TD_KICK_FAIL')=='uncertain':sys.stderr.write('fixture connection lost after DELETE');sys.exit(1)
 sys.stdout.write('HTTP/2.0 204 No Content\r\n\r\n');sys.exit(0)
`
	if !strings.Contains(string(script), anchor) {
		t.Fatal("fixture routing changed")
	}
	if err := os.WriteFile(ghPath, []byte(strings.Replace(string(script), anchor, route+anchor, 1)), 0755); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		out, err := executeGitHubProjectTest(syncProjectKickCmd, "some-user")
		if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"action":"remove-repository-collaborator"`) || !strings.Contains(out, `"effective_access_verified":false`) {
			t.Fatal(out, err)
		}
	}
	before, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	out, err := executeGitHubProjectTest(syncProjectKickCmd, "some-user@example.com")
	if err == nil || out != "" {
		t.Fatal("email treated as login", out, err)
	}
	after, err := os.ReadFile(calls)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid identity called gh", err)
	}
	for _, failure := range []string{"denied", "uncertain"} {
		t.Setenv("TD_KICK_FAIL", failure)
		out, err = executeGitHubProjectTest(syncProjectKickCmd, "some-user")
		if err == nil || out != "" || !strings.Contains(err.Error(), "inspect repository/access state") {
			t.Fatal(failure, out, err)
		}
	}
	invocations, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	deletes := 0
	for _, line := range strings.Split(strings.TrimSpace(string(invocations)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		for _, arg := range args {
			if strings.Contains(arg, "/collaborators/") {
				deletes++
				if arg != "repos/owner/repo/collaborators/some-user" || !strings.Contains(strings.Join(args, " "), "--method DELETE") {
					t.Fatal(args)
				}
			}
		}
	}
	if deletes != 4 {
		t.Fatal("unexpected retry or write count", deletes)
	}
	applied, err := os.ReadFile(writes)
	if err != nil || strings.Count(string(applied), "\n") != 3 {
		t.Fatal("fixture write receipt count", string(applied), err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("kick opened SQLite", err)
	}
}
