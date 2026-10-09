package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func TestGitHubWorkSessionCLIStartTagEndAndIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fixture")
	}
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	scope, err := ghcontext.Resolve(context.Background(), dir, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(scope.Path) })
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = auth ];then exit 0;fi
printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'
if [ "$4" = repos/owner/repo ];then printf '{"full_name":"owner/repo","has_issues":true}';exit 0;fi
case "$6" in
 'repos/owner/repo/issues/1/comments?per_page=100') printf '[[]]';;
 repos/owner/repo/issues/1) printf '{"number":1,"title":"First","state":"open","body":""}';;
 *) echo 'unexpected request' >&2;exit 3;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	execute := func(original *cobra.Command, args ...string) string {
		t.Helper()
		cmd := doctorTestCommand(original)
		if original == wsTagCmd {
			cmd.Flags().Bool("no-start", false, "")
		}
		out, err := executeGitHubTest(cmd, append(args, "--json")...)
		if err != nil || !json.Valid([]byte(out)) {
			t.Fatalf("%s: %s %v", original.Name(), out, err)
		}
		return out
	}
	execute(wsCurrentCmd)
	execute(wsStartCmd, "work")
	if _, err := executeGitHubTest(doctorTestCommand(wsStartCmd), "duplicate", "--json"); err == nil {
		t.Fatal("duplicate start accepted")
	}
	execute(wsTagCmd, "1", "gh-1", "--no-start")
	out := execute(wsCurrentCmd)
	var current struct {
		WorkSession models.WorkSession `json:"work_session"`
		Issues      []string           `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &current); err != nil || len(current.Issues) != 1 || current.Issues[0] != "gh-1" {
		t.Fatal(out, err)
	}
	execute(wsUntagCmd, "1")
	execute(wsEndCmd)
	out = execute(wsListCmd)
	var list []wsListEntry
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 || list[0].ID != current.WorkSession.ID || list[0].Status != "completed" || len(list[0].Issues) != 0 {
		t.Fatal(out, err)
	}
	if _, err := executeGitHubTest(doctorTestCommand(wsEndCmd), "--json"); err == nil {
		t.Fatal("end without active accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite created")
	}
}
