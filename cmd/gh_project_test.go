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
	cmd.Flags().Bool("custom-role", false, "")
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().String("visibility", "", "")
	cmd.Flags().String("description", "", "")
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
		{syncProjectCreateCmd, []string{"name"}, "explicit GitHub owner/repository"},
		{syncProjectInviteCmd, []string{"someone@example.com"}, "explicit login and permission"},
		{syncProjectKickCmd, []string{"someone@example.com"}, "GitHub login"},
		{syncProjectRoleCmd, []string{"user", "writer"}, "td-sync roles are not converted"},
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
	cmd.Flags().Bool("custom-role", false, "")
	if err := cmd.Flags().Set("custom-role", "true"); err != nil {
		t.Fatal(err)
	}
	if handled, err := githubSyncProject(cmd, nil); !handled || err == nil || !strings.Contains(err.Error(), "only to GitHub") {
		t.Fatal("SQLite ignored GitHub permission flag", handled, err)
	}
	create := &cobra.Command{Use: "create"}
	create.Flags().String("visibility", "", "")
	if err := create.Flags().Set("visibility", "private"); err != nil {
		t.Fatal(err)
	}
	if handled, err := githubSyncProject(create, []string{"name"}); !handled || err == nil || !strings.Contains(err.Error(), "only to GitHub") {
		t.Fatal("SQLite ignored GitHub visibility", handled, err)
	}
	list := &cobra.Command{Use: "list"}
	list.Flags().Bool("all", false, "")
	if err := list.Flags().Set("all", "true"); err != nil {
		t.Fatal(err)
	}
	if handled, err := githubSyncProject(list, nil); !handled || err == nil || !strings.Contains(err.Error(), "only to GitHub") {
		t.Fatal("SQLite ignored all-repo scope", handled, err)
	}
}

func TestGitHubProjectAccessibleListNoPerRepositoryQueries(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script, err := os.ReadFile(ghPath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "path=next(a for a in args if a.startswith('repos/owner/repo') or a=='user');"
	collection := `if any(a.startswith('user/repos?') for a in args):
 if os.environ.get('TD_REPO_LIST_DENIED'):sys.stderr.write('fixture HTTP 403 repo list denied');sys.exit(1)
 sys.stdout.write('HTTP/2.0 200 OK\r\n\r\n'+os.environ['TD_REPO_LIST_RESPONSE']);sys.exit(0)
`
	if !strings.Contains(string(script), anchor) {
		t.Fatal("fixture routing changed")
	}
	if err := os.WriteFile(ghPath, []byte(strings.Replace(string(script), anchor, collection+anchor, 1)), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_REPO_LIST_RESPONSE", `[[{"id":1,"full_name":"owner/one","visibility":"private","has_issues":true,"archived":false,"disabled":false,"temp_clone_token":"fixture-secret"}],[{"id":2,"full_name":"org/two","visibility":"internal","has_issues":false,"archived":true,"disabled":false}]]`)
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		out, err := executeGitHubProjectTest(syncProjectListCmd, "--all")
		if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"scope":"credential-accessible-repositories"`) || !strings.Contains(out, `"full_name":"org/two"`) || strings.Contains(out, "fixture-secret") {
			t.Fatal(out, err)
		}
	}
	t.Setenv("TD_REPO_LIST_RESPONSE", `[[]]`)
	out, err := executeGitHubProjectTest(syncProjectListCmd, "--all")
	if err != nil || !strings.Contains(out, `"projects":[]`) {
		t.Fatal(out, err)
	}
	t.Setenv("TD_REPO_LIST_DENIED", "1")
	out, err = executeGitHubProjectTest(syncProjectListCmd, "--all")
	if err == nil || out != "" || !strings.Contains(err.Error(), "fixture HTTP 403") {
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
			if strings.HasPrefix(arg, "user/repos?") {
				collections++
				if !strings.Contains(strings.Join(args, " "), "--paginate --slurp") {
					t.Fatal(args)
				}
			}
			if strings.HasPrefix(arg, "repos/") && arg != "repos/owner/repo" {
				t.Fatal("per-repo or task lookup", args)
			}
		}
	}
	if collections != 4 {
		t.Fatal("retry or per-item collection", collections)
	}
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(cfg)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("listing changed selection", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("listing opened SQLite", err)
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

func TestGitHubProjectInviteAndRoleReceiptsAndRace(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	writes := filepath.Join(t.TempDir(), "writes.jsonl")
	t.Setenv("TD_GRANT_WRITES", writes)
	t.Setenv("TD_GRANT_MODE", "invitation")
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script, err := os.ReadFile(ghPath)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Replace(string(script), "'has_issues':True}", "'has_issues':True,'owner':{'type':os.environ.get('TD_GRANT_OWNER','Organization')}}", 1)
	anchor := "elif '/labels?' in path:"
	route := `elif '/collaborators/' in path:
 if method=='GET':
  if os.environ.get('TD_GRANT_MODE')=='absent':sys.stderr.write('fixture HTTP 404 no collaborator');sys.exit(1)
  sys.stdout.write('HTTP/2.0 204 No Content\r\n\r\n');sys.exit(0)
 if method!='PUT':sys.stderr.write('unexpected grant method');sys.exit(1)
 patch=json.load(sys.stdin)
 with open(os.environ['TD_GRANT_WRITES'],'a') as log:log.write(json.dumps({'path':path,'permission':patch['permission']})+'\n')
 if os.environ.get('TD_GRANT_MODE')=='uncertain':sys.stderr.write('fixture connection lost after PUT');sys.exit(1)
 if os.environ.get('TD_GRANT_MODE')=='assigned':sys.stdout.write('HTTP/2.0 204 No Content\r\n\r\n');sys.exit(0)
 data={'id':17,'invitee':{'login':'some-user'},'repository':{'full_name':'owner/repo'}}
`
	if !strings.Contains(s, anchor) {
		t.Fatal("fixture routing changed")
	}
	if err := os.WriteFile(ghPath, []byte(strings.Replace(s, anchor, route+anchor, 1)), 0755); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		t.Setenv("TD_GRANT_MODE", "invitation")
		out, err := executeGitHubProjectTest(syncProjectInviteCmd, "some-user", "triage")
		if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"outcome":"invitation-created"`) || !strings.Contains(out, `"invitation_id":17`) {
			t.Fatal(out, err)
		}
		t.Setenv("TD_GRANT_MODE", "assigned")
		out, err = executeGitHubProjectTest(syncProjectRoleCmd, "some-user", "maintain")
		if err != nil || !strings.Contains(out, `"outcome":"assignment-accepted"`) {
			t.Fatal(out, err)
		}
	}
	before, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"some-user"}, {"some-user@example.com", "push"}, {"some-user", "writer"}} {
		if out, err := executeGitHubProjectTest(syncProjectInviteCmd, args...); err == nil || out != "" {
			t.Fatal(args, out, err)
		}
	}
	after, err := os.ReadFile(calls)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid invite called gh", err)
	}
	out, err := executeGitHubProjectTest(syncProjectInviteCmd, "some-user", "Security Reader", "--custom-role")
	if err != nil || !strings.Contains(out, `"requested_permission":"Security Reader"`) {
		t.Fatal(out, err)
	}
	t.Setenv("TD_GRANT_MODE", "invitation")
	out, err = executeGitHubProjectTest(syncProjectRoleCmd, "some-user", "push")
	if err == nil || out != "" || !strings.Contains(err.Error(), "invitation 17") || !strings.Contains(err.Error(), "conflict detection is best-effort") {
		t.Fatal(out, err)
	}
	t.Setenv("TD_GRANT_MODE", "uncertain")
	out, err = executeGitHubProjectTest(syncProjectInviteCmd, "some-user", "push")
	if err == nil || out != "" || !strings.Contains(err.Error(), "fixture connection lost") || !strings.Contains(err.Error(), "inspect repository/access state") {
		t.Fatal(out, err)
	}
	t.Setenv("TD_GRANT_MODE", "absent")
	out, err = executeGitHubProjectTest(syncProjectRoleCmd, "some-user", "push")
	if err == nil || out != "" || !strings.Contains(err.Error(), "no permission write attempted") {
		t.Fatal(out, err)
	}
	t.Setenv("TD_GRANT_OWNER", "User")
	out, err = executeGitHubProjectTest(syncProjectInviteCmd, "some-user", "pull")
	if err == nil || out != "" || !strings.Contains(err.Error(), "organization-only") {
		t.Fatal(out, err)
	}
	applied, err := os.ReadFile(writes)
	if err != nil || strings.Count(string(applied), "\n") != 7 {
		t.Fatal("retry or failed validation wrote permission", string(applied), err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("grant opened SQLite", err)
	}
}

func TestGitHubProjectCreationPreservesSelectionAndNeverRetries(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	writes := filepath.Join(t.TempDir(), "creates.jsonl")
	t.Setenv("TD_CREATE_WRITES", writes)
	t.Setenv("GH_HOST", "unexpected.example")
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	script, err := os.ReadFile(ghPath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "if args[0]=='auth':sys.exit(0)"
	create := `if args[:2]==['repo','create']:
 with open(os.environ['TD_CREATE_WRITES'],'a') as f:f.write(json.dumps({'args':args,'host':os.environ.get('GH_HOST'),'prompt':os.environ.get('GH_PROMPT_DISABLED')})+'\n')
 if os.environ.get('TD_CREATE_FAILURE')=='uncertain':sys.stderr.write('fixture connection lost after repository creation');sys.exit(1)
 print('https://github.com/new-owner/new-repo');sys.exit(0)
`
	s := strings.Replace(string(script), anchor, create+anchor, 1)
	s = strings.Replace(s, "a.startswith('repos/owner/repo')", "a.startswith('repos/')", 1)
	anchor = "elif '/labels?' in path:"
	verify := `elif path=='repos/new-owner/new-repo':
 if os.environ.get('TD_CREATE_FAILURE')=='verify':sys.stderr.write('fixture HTTP 403 verification denied');sys.exit(1)
 data={'full_name':'new-owner/new-repo','visibility':'private','has_issues':os.environ.get('TD_CREATE_FAILURE')!='issues-disabled'}
`
	if err := os.WriteFile(ghPath, []byte(strings.Replace(s, anchor, verify+anchor, 1)), 0755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	gitConfigBefore, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		out, err := executeGitHubProjectTest(syncProjectCreateCmd, "new-owner/new-repo", "--visibility", "private", "--description", "literal $(not-run)")
		if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"linked":false`) || !strings.Contains(out, `"visibility":"private"`) {
			t.Fatal(out, err)
		}
	}
	invocations, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	out, err := executeGitHubProjectTest(syncProjectCreateCmd, "new-owner/new-repo")
	if err == nil || out != "" {
		t.Fatal("implicit visibility allowed", out, err)
	}
	afterCalls, err := os.ReadFile(calls)
	if err != nil || !bytes.Equal(invocations, afterCalls) {
		t.Fatal("invalid create called gh", err)
	}
	for _, failure := range []string{"uncertain", "verify"} {
		t.Setenv("TD_CREATE_FAILURE", failure)
		out, err = executeGitHubProjectTest(syncProjectCreateCmd, "new-owner/new-repo", "--visibility", "private")
		if err == nil || out != "" || !strings.Contains(err.Error(), "fixture") {
			t.Fatal(failure, out, err)
		}
	}
	t.Setenv("TD_CREATE_FAILURE", "issues-disabled")
	out, err = executeGitHubProjectTest(syncProjectCreateCmd, "new-owner/new-repo", "--visibility", "private")
	if err != nil || !strings.Contains(out, `"has_issues":false`) || !strings.Contains(out, "Issues are disabled") {
		t.Fatal(out, err)
	}
	created, err := os.ReadFile(writes)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(created)), "\n")
	if len(lines) != 5 {
		t.Fatal("unexpected repository creation retry", string(created))
	}
	for _, line := range lines {
		var receipt struct {
			Args   []string `json:"args"`
			Host   string   `json:"host"`
			Prompt string   `json:"prompt"`
		}
		if err := json.Unmarshal([]byte(line), &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Host != "github.com" || receipt.Prompt != "true" || receipt.Args[2] != "new-owner/new-repo" || strings.Contains(strings.Join(receipt.Args, " "), "--source") {
			t.Fatal(receipt)
		}
	}
	cfg, err = config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(cfg)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("creation changed store config", err)
	}
	gitConfigAfter, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil || !bytes.Equal(gitConfigBefore, gitConfigAfter) {
		t.Fatal("creation changed Git remotes", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("creation opened SQLite", err)
	}
}
