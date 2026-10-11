package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/testutil"
	"github.com/spf13/cobra"
)

func TestGitHubJournalPortableCLIExportRestoreAndDestinationUndo(t *testing.T) {
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_ANALYTICS", "false")
	t.Setenv("TD_SESSION_ID", "portable-journal-cli")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func(f testutil.GitHubJournalFixture, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, executable, append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", f.Directory}, args...)...)
		child.Dir = f.Directory
		var stdout, stderr bytes.Buffer
		child.Stdout, child.Stderr = &stdout, &stderr
		if err := child.Run(); err != nil {
			t.Fatalf("%v: %v\n%s\n%s", args, err, stdout.String(), stderr.String())
		}
		return stdout.String()
	}
	source := testutil.NewGitHubJournalFixture(t)
	run(source, "update", "gh-1", "--title", "Source change", "--json")
	exported := run(source, "export", "--format", "journal")
	bundle, err := ghstore.ParsePortableJournal([]byte(exported))
	if err != nil || len(bundle.Sources) != 1 || len(bundle.Sources[0].Actions) != 1 {
		t.Fatal("source CLI export lost history", err)
	}
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte(exported), 0600); err != nil {
		t.Fatal(err)
	}
	destination := testutil.NewGitHubJournalFixture(t)
	mapping := filepath.Join(t.TempDir(), "mapping.json")
	if err := os.WriteFile(mapping, []byte(`{"github:owner/repo:gh-1":"gh-1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	preview := run(destination, "import", path, "--format", "journal", "--mapping", mapping, "--dry-run", "--json")
	var plan ghstore.JournalRestorePlan
	if err := json.Unmarshal([]byte(preview), &plan); err != nil || !plan.DryRun || plan.Actions != 1 || len(plan.Mappings) != 1 {
		t.Fatal(preview, err)
	}
	requests, err := os.ReadFile(destination.RequestsPath)
	if err != nil || strings.Contains(string(requests), `"method": "POST"`) || strings.Contains(string(requests), `"method": "PATCH"`) {
		t.Fatal("dry run wrote remotely", err, string(requests))
	}
	before, _ := os.ReadFile(destination.StatePath)
	out := run(destination, "import", path, "--format", "journal", "--mapping", mapping, "--json")
	var restored ghstore.JournalRestoreResult
	if err := json.Unmarshal([]byte(out), &restored); err != nil || restored.ArchiveID == "" || restored.WrittenParts < 1 || restored.AlreadyRestored {
		t.Fatal(out, err)
	}
	after, _ := os.ReadFile(destination.StatePath)
	var initial, actual struct {
		Issues   map[string]json.RawMessage
		Comments []json.RawMessage
	}
	if err := json.Unmarshal(before, &initial); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &actual); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(initial.Issues["1"], actual.Issues["1"]) || len(actual.Comments) != restored.WrittenParts+1 {
		t.Fatal("CLI restore executed source action or altered target")
	}
	if got := run(destination, "last", "--json"); strings.Contains(got, "Source change") || strings.Contains(got, `"action_type"`) {
		t.Fatal("archive became active action history", got)
	}
	out = run(destination, "import", path, "--format", "journal", "--mapping", mapping, "--json")
	var repeated ghstore.JournalRestoreResult
	if err := json.Unmarshal([]byte(out), &repeated); err != nil || !repeated.AlreadyRestored || repeated.ArchiveID != restored.ArchiveID {
		t.Fatal(out, err)
	}
	unchanged, _ := os.ReadFile(destination.StatePath)
	if !bytes.Equal(after, unchanged) {
		t.Fatal("confirmed repeat wrote again")
	}
	second := run(destination, "export", "--format", "journal")
	retained, err := ghstore.ParsePortableJournal([]byte(second))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range retained.Sources {
		if row.SHA256 == bundle.Sources[0].SHA256 {
			found = reflect.DeepEqual(row, bundle.Sources[0])
		}
	}
	if !found || len(retained.Bindings) != 1 {
		t.Fatal("CLI re-export lost source history/mapping")
	}
	// Imported history neither supplies authority nor interferes with a new
	// destination-native action's existing undo path.
	run(destination, "update", "gh-1", "--title", "Destination change", "--json")
	run(destination, "undo", "--json")
	final, _ := os.ReadFile(destination.StatePath)
	var state struct {
		Issues map[string]struct{ Title string }
	}
	if err := json.Unmarshal(final, &state); err != nil || state.Issues["1"].Title != "Original fixture" {
		t.Fatal("destination undo no longer works after archive restore", err)
	}
	for _, f := range []testutil.GitHubJournalFixture{source, destination} {
		if _, err := os.Stat(filepath.Join(f.Directory, ".todos", "issues.db")); !os.IsNotExist(err) {
			t.Fatal("portable journal opened SQLite", err)
		}
	}
}

func TestGitHubJournalPortableInvalidInputAndFlagsFailBeforeNetwork(t *testing.T) {
	for _, store := range []string{"sqlite", "gh-issue"} {
		t.Run(store, func(t *testing.T) {
			dir := storeTestDir(t)
			if store == "gh-issue" {
				if err := config.SetStore(dir, store, &models.GitHubStoreConfig{Repo: "owner/repo", Remote: "origin"}); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", t.TempDir())
			cases := [][]string{{"-", "--format", "journal"}, {"-", "--format", "journal", "--force"}, {"-", "--format", "journal", "--source", "legacy"}, {"-", "--mapping", "missing.json"}}
			for _, args := range cases {
				command := &cobra.Command{Use: importCmd.Use, Args: importCmd.Args, RunE: importCmd.RunE, SilenceErrors: true, SilenceUsage: true}
				command.Flags().String("format", "json", "")
				command.Flags().String("mapping", "", "")
				command.Flags().String("source", "", "")
				command.Flags().Bool("force", false, "")
				command.SetArgs(args)
				command.SetIn(strings.NewReader(`{"format":"td-journal","version":99}`))
				var out bytes.Buffer
				command.SetOut(&out)
				if err := command.Execute(); err == nil || strings.Contains(err.Error(), "gh CLI not found") || out.Len() != 0 {
					t.Fatal("validation opened GitHub or emitted partial success", args, err, out.String())
				}
			}
			command := exportCommand("journal", "")
			_ = command.Flags().Set("all", "true")
			if _, err := githubExport(command, dir); err == nil || strings.Contains(err.Error(), "gh CLI not found") {
				t.Fatal("invalid journal export reached network", err)
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("invalid journal command opened SQLite")
			}
		})
	}
}
