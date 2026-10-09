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
)

// Exercise root dispatch while injecting only the local association backend;
// never redirect HOME or write the user's actual global association file.
func TestOfflineAssociationsHelperProcess(t *testing.T) {
	if os.Getenv("TD_OFFLINE_ASSOC_HELPER") != "1" {
		return
	}
	path := os.Getenv("TD_OFFLINE_ASSOC_FILE")
	loadDirectoryAssociations = func() (map[string]string, error) {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		if err != nil {
			return nil, err
		}
		var result map[string]string
		err = json.Unmarshal(data, &result)
		return result, err
	}
	saveDirectoryAssociations = func(result map[string]string) error {
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0600)
	}
	for i, arg := range os.Args {
		if arg == "--" {
			args := os.Args[i+1:]
			os.Args = append([]string{"td"}, args...)
			rootCmd.SetArgs(args)
			Execute()
			os.Exit(0)
		}
	}
	t.Fatal("missing helper command")
}

func TestGitHubOfflineAssociationJSONLifecycle(t *testing.T) {
	dir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	t.Setenv("TD_OFFLINE_ASSOC_HELPER", "1")
	state := filepath.Join(t.TempDir(), "associations.json")
	t.Setenv("TD_OFFLINE_ASSOC_FILE", state)
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	t.Setenv("TD_ANALYTICS", "false")
	// Any accidental gh invocation fails, rather than producing a plausible
	// network result. The script's call log must stay absent throughout.
	ghPath := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	if err := os.WriteFile(ghPath, []byte("#!/bin/sh\nprintf 'unexpected gh call\\n' >> \"$TD_SYNC_STATUS_CALLS\"\nexit 9\n"), 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sourceA, sourceB := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	for _, enabled := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", enabled)
		for _, tc := range []struct {
			args []string
			want string
			fail bool
		}{
			{[]string{"config", "assoc"}, "[]", false},
			{[]string{"config", "associate", sourceB, dir}, `"action":"associate"`, false},
			{[]string{"config", "associate", sourceA, dir}, `"project":`, false},
			{[]string{"config", "associations"}, `"directory":`, false},
			{[]string{"config", "dissociate", sourceA}, `"action":"dissociate"`, false},
			{[]string{"config", "dissociate", sourceB}, `"action":"dissociate"`, false},
			{[]string{"config", "assoc"}, "[]", false},
			{[]string{"config", "dissociate", sourceA}, `"error":`, true},
			{[]string{"config", "associate", sourceA, filepath.Join(dir, "missing")}, `"error":`, true},
			{[]string{"config", "assoc", "unexpected"}, `"error":`, true},
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			args := append([]string{"-test.run=^TestOfflineAssociationsHelperProcess$", "--", "--work-dir", dir, "--json"}, tc.args...)
			child := exec.CommandContext(ctx, executable, args...)
			var stdout, stderr bytes.Buffer
			child.Stdout = &stdout
			child.Stderr = &stderr
			err := child.Run()
			cancel()
			if (err != nil) != tc.fail || !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("%v %v %s stderr=%s", tc.args, err, stdout.String(), stderr.String())
			}
			if len(tc.args) == 2 && tc.args[1] == "associations" {
				var rows []map[string]string
				if err := json.Unmarshal(stdout.Bytes(), &rows); err != nil || len(rows) != 2 || rows[0]["directory"] > rows[1]["directory"] {
					t.Fatal("unstable association ordering", stdout.String(), err)
				}
			}
		}
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("offline command invoked gh", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("offline command opened SQLite", err)
	}
}
