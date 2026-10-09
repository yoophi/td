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
)

func TestGitHubBootstrapCLIPreflightFailureAndSetupCancellation(t *testing.T) {
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_ANALYTICS", "false") // Failure diagnostics stay local if a project exists.
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	contextRoot := t.TempDir()
	t.Setenv("TD_GH_CONTEXT_ROOT", contextRoot)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("TD_BOOTSTRAP_CALLS", calls)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TD_BOOTSTRAP_CALLS"
if [ "$1" = auth ]; then
 if [ "$TD_BOOTSTRAP_FAILURE" = auth ]; then echo 'fixture auth denied' >&2; exit 1; fi
 printf 'fixture-only-token\n'; exit 0
fi
if [ "$1" = api ]; then
 if [ "$TD_BOOTSTRAP_FAILURE" = api ]; then echo 'HTTP 403 fixture access denied' >&2; exit 1; fi
 printf 'HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'
 printf '%s' "$TD_BOOTSTRAP_RESPONSE"; exit 0
fi
echo 'unexpected gh command' >&2; exit 9
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		for _, tc := range []struct {
			name, remote, response, failure, input, want string
			setup, success, noGit, noGH                  bool
		}{
			{name: "missing gh", noGH: true, want: "gh CLI not found"},
			{name: "not git", noGit: true, want: "Git working tree"},
			{name: "missing remote", want: "missing or unreadable"},
			{name: "other host", remote: "https://gitlab.com/owner/repo.git", want: "github.com"},
			{name: "auth", remote: "https://github.com/owner/repo.git", failure: "auth", want: "fixture auth denied"},
			{name: "access", remote: "https://github.com/owner/repo.git", failure: "api", want: "fixture access denied"},
			{name: "disabled", remote: "https://github.com/owner/repo.git", response: `{"full_name":"owner/repo","has_issues":false}`, want: "Issues is disabled"},
			{name: "missing field", remote: "https://github.com/owner/repo.git", response: `{"full_name":"owner/repo"}`, want: "missing has_issues"},
			{name: "malformed", remote: "https://github.com/owner/repo.git", response: `broken`, want: "invalid gh repository response"},
			{name: "init success", remote: "https://github.com/owner/repo.git", response: `{"full_name":"owner/repo","has_issues":true}`, success: true},
			{name: "setup cancelled", setup: true, input: "2\n", want: "setup cancelled"},
			{name: "setup success", setup: true, input: "2\n\n", remote: "https://github.com/owner/repo.git", response: `{"full_name":"owner/repo","has_issues":true}`, success: true},
		} {
			t.Run(enabled+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				if !tc.noGit {
					runGit(t, dir, "init")
				}
				if tc.remote != "" {
					runGit(t, dir, "remote", "add", "origin", tc.remote)
				}
				t.Setenv("TD_BOOTSTRAP_FAILURE", tc.failure)
				t.Setenv("TD_BOOTSTRAP_RESPONSE", tc.response)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				args := []string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir}
				if tc.setup {
					args = append(args, "config", "setup")
				} else {
					args = append(args, "--json", "init", "--store", "gh-issue")
				}
				child := exec.CommandContext(ctx, executable, args...)
				child.Dir = dir
				if tc.noGH {
					child.Env = append(os.Environ(), "PATH="+t.TempDir())
				}
				child.Stdin = strings.NewReader(tc.input)
				var stdout, stderr bytes.Buffer
				child.Stdout, child.Stderr = &stdout, &stderr
				err := child.Run()
				if (err == nil) != tc.success || (tc.want != "" && !strings.Contains(stdout.String()+stderr.String(), tc.want)) {
					t.Fatalf("%v stdout=%s stderr=%s", err, stdout.String(), stderr.String())
				}
				if !tc.setup && !json.Valid(stdout.Bytes()) {
					t.Fatal("invalid JSON receipt/error", stdout.String())
				}
				if strings.Contains(stdout.String()+stderr.String(), "fixture-only-token") {
					t.Fatal("credential exposed")
				}
				if tc.success {
					cfg, err := config.Load(dir)
					if err != nil || cfg.Store != config.StoreGitHub || cfg.GitHub == nil || cfg.GitHub.Repo != "owner/repo" {
						t.Fatal(cfg, err)
					}
				} else if _, err := os.Stat(filepath.Join(dir, ".todos")); !os.IsNotExist(err) {
					t.Fatal("bootstrap failure persisted project data", err)
				}
				if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
					t.Fatal("bootstrap created SQLite", err)
				}
			})
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil || strings.Contains(string(data), "/issues") || strings.Contains(string(data), "/comments") {
		t.Fatal("bootstrap accessed shared issue data", string(data), err)
	}
	entries, err := os.ReadDir(contextRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("bootstrap created issue-session context", entries, err)
	}
}
