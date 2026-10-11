package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

func runExportOutputCLI(t *testing.T, dir string, fail bool, args ...string) []byte {
	t.Helper()
	return runExportOutputCLIFrom(t, dir, dir, fail, args...)
}

func runExportOutputCLIFrom(t *testing.T, dir, cwd string, fail bool, args ...string) []byte {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	argv := append([]string{"-test.run=^TestGitHubSyncCLIHelperProcess$", "--", "--work-dir", dir, "export"}, args...)
	child := exec.CommandContext(ctx, executable, argv...)
	child.Dir = cwd
	var stdout, stderr bytes.Buffer
	child.Stdout, child.Stderr = &stdout, &stderr
	err = child.Run()
	if (err != nil) != fail {
		t.Fatalf("%v: %v stdout=%s stderr=%s", args, err, stdout.String(), stderr.String())
	}
	return stdout.Bytes()
}

func TestExportCLIStoreSelectionWorkDirAndRefresh(t *testing.T) {
	githubDir := githubScheduleTestDir(t)
	calls := recordScheduleGHCalls(t)
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_ANALYTICS", "false")
	t.Setenv("TD_GH_CACHE", "off") // Cache freshness itself is checked in ghstore.
	t.Setenv("TD_GH_CONTEXT_ROOT", t.TempDir())
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "true")
	sqliteDir, cwd := t.TempDir(), t.TempDir()
	for _, dir := range []string{sqliteDir, cwd} {
		database, err := db.Initialize(dir)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 4; i++ {
			issue := &models.Issue{Title: fmt.Sprintf("Schedule fixture %d", i), Status: models.StatusOpen}
			if dir == cwd {
				issue.Title = "Wrong current directory"
			} else if i == 3 {
				issue.Status = models.StatusClosed
			}
			if err := database.CreateIssue(issue); err != nil {
				t.Fatal(err)
			}
			if dir == sqliteDir && i == 4 {
				if err := database.DeleteIssue(issue.ID); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
	callerDB := filepath.Join(cwd, ".todos", "issues.db")
	callerBefore, err := os.ReadFile(callerDB)
	if err != nil {
		t.Fatal(err)
	}
	readCalls := func() []byte {
		data, err := os.ReadFile(calls)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return data
	}
	for _, mode := range []string{"false", "true"} {
		t.Setenv("TD_FEATURE_SYNC_CLI", mode)
		for _, all := range []bool{false, true} {
			want := []string{"Schedule fixture 1", "Schedule fixture 2", "Schedule fixture 3"}
			if all {
				want = append(want, "Schedule fixture 4")
			}
			for _, format := range []string{"json", "md"} {
				for _, dir := range []string{sqliteDir, githubDir} {
					args := []string{"--format", format, "--refresh", "--json"}
					if all {
						args = append(args, "--all")
					}
					beforeCalls := readCalls()
					out := runExportOutputCLIFrom(t, dir, cwd, false, args...)
					if dir == sqliteDir && !bytes.Equal(beforeCalls, readCalls()) {
						t.Fatal("SQLite export accessed GitHub")
					}
					if format == "json" {
						var rows []exportedItem
						if err := json.Unmarshal(out, &rows); err != nil {
							t.Fatal(err)
						}
						got := []string{}
						for _, row := range rows {
							got = append(got, row.Issue.Title)
						}
						sort.Strings(got)
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("mode=%s dir=%s all=%t: %v != %v", mode, dir, all, got, want)
						}
					} else {
						var document struct{ Document string }
						if err := json.Unmarshal(out, &document); err != nil {
							t.Fatal(err)
						}
						if strings.Contains(document.Document, "Wrong current directory") || strings.Contains(document.Document, "Schedule fixture 5") {
							t.Fatal("work-dir ignored or PR exported")
						}
						for _, title := range want {
							if !strings.Contains(document.Document, title) {
								t.Fatal("Markdown lost task", title)
							}
						}
						if !all && strings.Contains(document.Document, "Schedule fixture 4") {
							t.Fatal("deleted task exported without --all")
						}
					}
				}
			}
		}
	}
	callerAfter, err := os.ReadFile(callerDB)
	if err != nil || !bytes.Equal(callerBefore, callerAfter) {
		t.Fatal("export changed the caller's unrelated database", err)
	}
	if _, err := os.Stat(filepath.Join(githubDir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("GitHub export opened SQLite", err)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var args []string
		if json.Unmarshal([]byte(line), &args) != nil {
			t.Fatal("invalid call log")
		}
		if strings.Contains(strings.Join(args, " "), "--method") && !strings.Contains(strings.Join(args, " "), "--method GET") {
			t.Fatal("export attempted remote write", args)
		}
	}
}

func TestExportCLIOutputAndOptionContracts(t *testing.T) {
	dir := githubScheduleTestDir(t)
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_ANALYTICS", "false")
	for _, store := range []string{config.StoreGitHub, config.StoreSQLite} {
		if store == config.StoreSQLite {
			if err := config.SetStore(dir, store, nil); err != nil {
				t.Fatal(err)
			}
			database, err := db.Initialize(dir)
			if err != nil {
				t.Fatal(err)
			}
			issue := &models.Issue{Title: "Export fixture 한글", Description: "**body**", Status: models.StatusOpen}
			if err := database.CreateIssue(issue); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
		}
		for _, mode := range []string{"false", "true"} {
			t.Setenv("TD_FEATURE_SYNC_CLI", mode)
			for _, format := range []string{"json", "md"} {
				path := filepath.Join(t.TempDir(), "archive."+format)
				out := runExportOutputCLI(t, dir, false, "--format", format, "--all", "--output", path, "--json")
				var receipt map[string]any
				if json.Unmarshal(out, &receipt) != nil || receipt["action"] != "exported" || receipt["path"] != path || receipt["format"] != format {
					t.Fatalf("%s/%s/%s: invalid export receipt %s", store, mode, format, out)
				}
				data, err := os.ReadFile(path)
				if err != nil || len(data) == 0 || (format == "json" && !json.Valid(data)) || (format == "md" && !bytes.HasPrefix(data, []byte("# Issues Export"))) {
					t.Fatal("artifact format changed", err)
				}
				plain := runExportOutputCLI(t, dir, false, "--format", format, "--all")
				if !bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace(plain)) {
					t.Fatal("file/stdout artifact differs", format)
				}
			}
			var markdown struct {
				Format   string `json:"format"`
				Document string `json:"document"`
				Rendered bool   `json:"rendered"`
			}
			out := runExportOutputCLI(t, dir, false, "--format", "md", "--json")
			if json.Unmarshal(out, &markdown) != nil || markdown.Format != "md" || markdown.Rendered || !strings.HasPrefix(markdown.Document, "# Issues Export") {
				t.Fatal("missing Markdown JSON document")
			}
			out = runExportOutputCLI(t, dir, false, "--format", "md", "--render-markdown", "--json")
			if json.Unmarshal(out, &markdown) != nil || !markdown.Rendered || markdown.Document == "" {
				t.Fatal("rendered Markdown option was lost")
			}
			for _, args := range [][]string{{"--format", "xml"}, {"--format", "json", "--render-markdown"}, {"--format", "journal", "--render-markdown"}, {"unexpected"}, {"--output", filepath.Join(dir, "missing", "file.json")}} {
				out = runExportOutputCLI(t, dir, true, append(args, "--json")...)
				if !json.Valid(out) || !bytes.Contains(out, []byte(`"error":`)) {
					t.Fatal("invalid option/file failure lacks JSON error", args)
				}
			}
		}
	}
}

func TestSQLiteExportSourceFailureProtectsOutput(t *testing.T) {
	t.Setenv("TD_GH_SYNC_HELPER", "1")
	t.Setenv("TD_ANALYTICS", "false")
	t.Setenv("TD_FEATURE_SYNC_CLI", "false")
	t.Setenv("TD_FEATURE_SYNC_AUTOSYNC", "false")
	for _, table := range []string{"logs", "handoffs", "issue_dependencies", "issue_files"} {
		t.Run(table, func(t *testing.T) {
			dir := t.TempDir()
			database, err := db.Initialize(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.CreateIssue(&models.Issue{Title: "Incomplete export must fail", Status: models.StatusOpen}); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			execTestSQL(t, dir, "DROP TABLE "+table)
			path := filepath.Join(t.TempDir(), "backup.json")
			if err := os.WriteFile(path, []byte("original backup"), 0600); err != nil {
				t.Fatal(err)
			}
			out := runExportOutputCLI(t, dir, true, "--format", "json", "--output", path, "--json")
			if !json.Valid(out) || !bytes.Contains(out, []byte(`"error":`)) {
				t.Fatal("read failure lacks JSON error")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "original backup" {
				t.Fatal("failed read replaced backup", err)
			}
		})
	}
}
