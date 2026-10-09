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

	"github.com/spf13/cobra"
)

func TestGitHubAuthenticationRoutesAndScope(t *testing.T) {
	dir := githubScheduleTestDir(t)
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	calls := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv("TD_AUTH_FIXTURE_CALLS", calls)
	script := `#!/usr/bin/env python3
import os,sys,json
args=sys.argv[1:]
with open(os.environ['TD_AUTH_FIXTURE_CALLS'],'a') as f:f.write(json.dumps(args)+'\n')
if args[0]=='api':print('HTTP/2.0 200 OK\n\n'+json.dumps({'login':'fixture-user'}))
elif args[:2]==['auth','logout']:sys.exit(0)
else:sys.exit(9)
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "false")
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		cmd := &cobra.Command{Use: "status", RunE: authStatusCmd.RunE}
		cmd.Flags().Bool("json", true, "")
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil || !json.Valid(out.Bytes()) || !strings.Contains(out.String(), `"account":"fixture-user"`) {
			t.Fatal(out.String(), err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		child := exec.CommandContext(ctx, executable, "-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "--json", "auth", "status")
		result, err := child.Output()
		cancel()
		if err != nil || !json.Valid(result) || !bytes.Contains(result, []byte(`"account":"fixture-user"`)) {
			t.Fatalf("auth registration feature=%s: %s %v", enabled, result, err)
		}
		for _, action := range []string{"login", "logout"} {
			cmd := &cobra.Command{Use: action}
			cmd.Flags().Bool("json", true, "")
			cmd.Flags().String("account", "", "")
			cmd.Flags().Bool("confirm-global", false, "")
			if handled, err := githubAuth(cmd, action); !handled || err == nil {
				t.Fatal("unattended credential mutation was allowed", action, handled, err)
			}
		}
	}
	before, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(before), "\n") != 4 || strings.Contains(string(before), `"auth"`) {
		t.Fatal("scope guards called gh", string(before))
	}
	cmd := &cobra.Command{Use: "logout", RunE: authLogoutCmd.RunE}
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().String("account", "fixture-user", "")
	cmd.Flags().Bool("confirm-global", true, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil || !json.Valid(out.Bytes()) {
		t.Fatal(out.String(), err)
	}
	after, err := os.ReadFile(calls)
	if err != nil || !strings.Contains(string(after), `["auth", "logout", "--hostname", "github.com", "--user", "fixture-user"]`) {
		t.Fatal(string(after), err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("authentication opened SQLite", err)
	}
	// Failed status must preserve diagnostics without producing a success payload.
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho 'fixture HTTP 401: credentials rejected' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	cmd = &cobra.Command{Use: "status", RunE: authStatusCmd.RunE, SilenceErrors: true, SilenceUsage: true}
	cmd.SetOut(&out)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "fixture HTTP 401") || out.Len() != 0 {
		t.Fatal("auth failure lost diagnostics", err, out.String())
	}
}

func TestSQLiteAuthenticationStillRequiresFeature(t *testing.T) {
	storeTestDir(t)
	t.Setenv("TD_FEATURE_SYNC_CLI", "false")
	for _, action := range []string{"login", "logout", "status"} {
		if handled, err := githubAuth(&cobra.Command{}, action); !handled || err == nil || !strings.Contains(err.Error(), "sync_cli") {
			t.Fatal(action, handled, err)
		}
	}
	t.Setenv("TD_FEATURE_SYNC_CLI", "true")
	if handled, err := githubAuth(&cobra.Command{}, "status"); handled || err != nil {
		t.Fatal("SQLite routing changed", handled, err)
	}
}
