package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/models"
)

func TestGitHubInfoPaginatedCountsLocationAndCommandRouting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake gh")
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
if [ "$1" = auth ]; then exit 0; fi
if [ "$4" = repos/owner/repo ]; then
 printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n{"full_name":"owner/repo","has_issues":true}'
elif [ "$5" = GET ] && [ "$6" = 'repos/owner/repo/issues?state=all&per_page=100' ]; then
 case " $* " in *' --paginate --slurp '*) ;; *) exit 4 ;; esac
 if [ "$TD_INFO_FAIL" = yes ]; then echo 'rate limit (HTTP 429)' >&2; exit 1; fi
 printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n%s' "$TD_INFO_PAGES"
else
 echo 'unexpected mutation or request' >&2;exit 3
fi
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TD_INFO_PAGES", `[[{"number":1,"title":"First","state":"open","body":""}],[{"number":2,"title":"Second","state":"closed","body":""},{"number":3,"pull_request":{},"state":"open"}]]`)
	out, err := executeGitHubTest(doctorTestCommand(infoCmd), "--json")
	var v struct {
		Store, Repository, LocalContext, Database string
		Issues                                    map[string]int
		ByPriority                                map[string]int `json:"by_priority"`
	}
	if err != nil || json.Unmarshal([]byte(out), &v) != nil || v.Store != "gh-issue" || v.Repository != "owner/repo" || v.Database != "" || v.Issues["total"] != 2 || v.Issues["closed"] != 1 || v.ByPriority["P2"] != 2 {
		t.Fatalf("%s %v", out, err)
	}
	var raw map[string]any
	if err = json.Unmarshal([]byte(out), &raw); err != nil || raw["local_context"] != scope.Path || raw["database"] != nil {
		t.Fatal(raw, err)
	}
	out, err = executeGitHubTest(doctorTestCommand(infoCmd))
	if err != nil || !strings.Contains(out, "Issues: 2 total") || strings.Contains(out, "issues.db") {
		t.Fatalf("%s %v", out, err)
	}
	t.Setenv("TD_INFO_PAGES", "[]")
	out, err = executeGitHubTest(doctorTestCommand(infoCmd), "--json")
	if err != nil || !strings.Contains(out, `"total":0`) {
		t.Fatalf("%s %v", out, err)
	}
	t.Setenv("TD_INFO_FAIL", "yes")
	out, err = executeGitHubTest(doctorTestCommand(infoCmd), "--json")
	if err == nil || out != "" || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("partial success %s %v", out, err)
	}
	if _, err = os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite DB created")
	}
	found, _, err := rootCmd.Find([]string{"stats"})
	if err != nil || found != statsCmd {
		t.Fatal("stats collision", found, err)
	}
	if len(infoCmd.Aliases) != 0 {
		t.Fatal(infoCmd.Aliases)
	}
	if err = config.SetStore(dir, "sqlite", nil); err != nil {
		t.Fatal(err)
	}
	if handled, err := githubInfo(doctorTestCommand(infoCmd), dir); handled || err != nil {
		t.Fatalf("SQLite dispatch: %v %v", handled, err)
	}
}
