package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/spf13/cobra"
)

func doctorTestCommand(original *cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: original.Use, Args: original.Args, RunE: original.RunE, SilenceUsage: true, SilenceErrors: true}
	c.Flags().Bool("json", false, "")
	return c
}

func TestGitHubDoctorJSONFailuresAndForeignKeysWithoutSQLite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake gh")
	}
	dir := storeTestDir(t)
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	if err := config.SetStore(dir, "gh-issue", &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = auth ]; then exit 0; fi
if [ "$1" != api ]; then exit 2; fi
printf 'HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n'
case "$4" in
  repos/owner/repo) printf '%s' "$TD_DOCTOR_REPO" ;;
  'repos/owner/repo/issues?state=all&per_page=1') printf '[]' ;;
  *) exit 3 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		response string
		ok       bool
	}{{`{"full_name":"owner/repo","has_issues":true}`, true}, {`{"full_name":"owner/changed","has_issues":true}`, false}, {`{"full_name":"owner/repo","has_issues":false}`, false}} {
		t.Setenv("TD_DOCTOR_REPO", tc.response)
		out, err := executeGitHubTest(doctorTestCommand(doctorCmd), "--json")
		var report ghstore.DiagnosticReport
		if json.Unmarshal([]byte(out), &report) != nil || report.OK != tc.ok || (err == nil) != tc.ok {
			t.Fatalf("%s %v", out, err)
		}
		if !tc.ok && !errors.Is(err, errSilentExit) {
			t.Fatalf("double error envelope: %v", err)
		}
	}
	out, err := executeGitHubTest(doctorTestCommand(doctorCmd))
	if !errors.Is(err, errSilentExit) || !strings.Contains(out, "Issues enabled: FAIL") {
		t.Fatalf("%s %v", out, err)
	}
	_, err = executeGitHubTest(doctorTestCommand(doctorFkCmd))
	if err == nil || !strings.Contains(err.Error(), "not applicable to gh-issue") {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite DB created")
	}
}
