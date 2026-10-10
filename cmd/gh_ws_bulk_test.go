package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func editWorkSessionFixture(t *testing.T, change func(string) string) {
	t.Helper()
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(gh)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gh, []byte(change(string(script))), 0755); err != nil {
		t.Fatal(err)
	}
}

func traceWorkSessionReads(t *testing.T) string {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "calls")
	quoted, _ := json.Marshal(trace)
	editWorkSessionFixture(t, func(script string) string {
		needle := "f=pathlib.Path(os.environ['TD_WS_STATE']);statepath=f"
		if !strings.Contains(script, needle) {
			t.Fatal("work-session fixture seam missing")
		}
		replacement := "with pathlib.Path(" + string(quoted) + ").open('a') as traced:traced.write(method+' '+path+'\\n')\n" + needle
		return strings.Replace(script, needle, replacement, 1)
	})
	return trace
}

func assertWorkSessionBulkCalls(t *testing.T, trace string) {
	t.Helper()
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.Contains(line, "repos/owner/repo/issues") {
			calls = append(calls, line)
		}
	}
	want := []string{"GET repos/owner/repo/issues?state=all&per_page=100", "GET repos/owner/repo/issues/comments?per_page=100"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("work-session reads must use one issue/comment collection, got %v", calls)
	}
}

func TestGitHubWorkSessionReadUsesOneBulkSnapshot(t *testing.T) {
	for _, count := range []int{2, 145} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			_, scope := githubWSFixture(t)
			t.Setenv("TD_GH_CACHE", "off")
			editWorkSessionFixture(t, func(script string) string {
				script = strings.Replace(script, "for n in [1,2]]", fmt.Sprintf("for n in range(1,%d)]", count+1), 1)
				return strings.Replace(script, "data=[issues]", "data=[issues[:100],issues[100:]]", 1)
			})
			githubWSExecute(t, wsStartCmd, "Bulk read fixture")
			state, err := scope.Update(context.Background(), func(s *ghcontext.State) error {
				for i := 1; i <= count; i++ {
					if err := s.TagWorkSession(fmt.Sprintf("gh-%d", i)); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			trace := traceWorkSessionReads(t)
			for _, name := range []string{"current", "show"} {
				t.Run(name, func(t *testing.T) {
					if err := os.WriteFile(trace, nil, 0600); err != nil {
						t.Fatal(err)
					}
					var output string
					if name == "current" {
						output = githubWSExecute(t, wsCurrentCmd)
					} else {
						output = githubWSExecute(t, wsShowCmd, state.ActiveWorkSession)
					}
					var result struct {
						Issues       []string `json:"issues"`
						IssueDetails []any    `json:"issue_details"`
					}
					if json.Unmarshal([]byte(output), &result) != nil || len(result.Issues) != count || len(result.IssueDetails) != count {
						t.Fatal("bulk read lost tagged tasks", output)
					}
					assertWorkSessionBulkCalls(t, trace)
				})
			}
		})
	}
}

func TestGitHubWorkSessionBulkReadPreservesSelectedHistory(t *testing.T) {
	dir, scope := githubWSFixture(t)
	t.Setenv("TD_GH_CACHE", "off")
	githubWSExecute(t, wsStartCmd, "History selection")
	githubWSExecute(t, wsTagCmd, "1", "2", "--no-start")
	githubWSExecute(t, wsUntagCmd, "1")
	state, err := scope.Update(context.Background(), func(s *ghcontext.State) error {
		ws, err := s.CurrentWorkSession()
		if err != nil {
			return err
		}
		ws.LocalActivities = []models.Activity{{Kind: "log", SessionID: s.Session.ID, WorkSessionID: ws.ID, Message: "Local history", LogType: models.LogTypeProgress, CreatedAt: time.Date(2026, 10, 9, 0, 0, 1, 0, time.UTC)}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := state.CurrentWorkSession()
	if err != nil {
		t.Fatal(err)
	}
	comment := func(id, issue int, session, message, clock string) map[string]any {
		body := fmt.Sprintf("%s\n\n<!-- td:activity:v1\n{\"kind\":\"log\",\"operation_id\":\"operation-%d\",\"session_id\":\"ses_fixture\",\"work_session_id\":%q,\"message\":%q,\"log_type\":\"progress\"}\n-->", message, id, session, message)
		return map[string]any{"id": id, "body": body, "issue_url": fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", issue), "created_at": clock, "updated_at": clock, "user": map[string]string{"login": "fixture-author"}}
	}
	native := map[string][]map[string]any{
		"1": {comment(100, 1, ws.ID, "Deleted untagged history", "2026-10-09T00:00:02Z")},
		"2": {comment(101, 2, "ws_foreign", "Foreign history", "2026-10-09T00:00:00Z"), comment(102, 2, ws.ID, "Current history", "2026-10-09T00:00:03Z")},
		"3": {comment(103, 3, ws.ID, "Unselected history", "2026-10-09T00:00:04Z")},
	}
	data, err := json.Marshal(native)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("TD_WS_STATE"), data, 0600); err != nil {
		t.Fatal(err)
	}
	editWorkSessionFixture(t, func(script string) string { return strings.Replace(script, "for n in [1,2]]", "for n in [1,2,3]]", 1) })
	t.Setenv("TD_WS_DELETED", "yes")
	output := githubWSExecute(t, wsCurrentCmd)
	var result struct {
		Issues     []string          `json:"issues"`
		Activities []models.Activity `json:"activities"`
		Details    []ghstore.Record  `json:"issue_details"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	messages := func(rows []models.Activity) []string {
		v := []string{}
		for _, a := range rows {
			v = append(v, a.Message)
		}
		return v
	}
	want := []string{"Local history", "Deleted untagged history", "Current history"}
	if !reflect.DeepEqual(messages(result.Activities), want) || !reflect.DeepEqual(result.Issues, []string{"gh-2"}) || len(result.Details) != 1 || result.Details[0].ID != "gh-2" {
		t.Fatal("lost history selection or chronological order", output)
	}
	output, err = executeGitHubTest(githubWSCommand(wsShowCmd), ws.ID, "--full")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range want {
		if !strings.Contains(output, message) {
			t.Fatal("human output lost history", output)
		}
	}
	if strings.Contains(output, "Foreign history") || strings.Contains(output, "Unselected history") {
		t.Fatal("human output leaked unrelated history", output)
	}
	client, err := ghstore.Open(context.Background(), dir, &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"})
	if err != nil {
		t.Fatal(err)
	}
	trace := traceWorkSessionReads(t)
	activities, err := readGitHubWorkSessionActivity(context.Background(), client, ws)
	if err != nil || !reflect.DeepEqual(messages(activities), want) {
		t.Fatal("automatic handoff input differs", activities, err)
	}
	assertWorkSessionBulkCalls(t, trace)
	if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
		t.Fatal("SQLite opened")
	}
}

func TestGitHubWorkSessionBulkReadErrorsDoNotReturnPartialResults(t *testing.T) {
	for _, mode := range []string{"tagged-missing", "history-missing", "remote-error"} {
		t.Run(mode, func(t *testing.T) {
			_, scope := githubWSFixture(t)
			t.Setenv("TD_GH_CACHE", "off")
			githubWSExecute(t, wsStartCmd, "Read failure")
			githubWSExecute(t, wsTagCmd, "1", "2", "--no-start")
			expected := "gh-404"
			if mode == "remote-error" {
				expected = "HTTP 403 original work-session read diagnostic"
				editWorkSessionFixture(t, func(script string) string {
					needle := "elif path=='repos/owner/repo/issues/comments?per_page=100':"
					return strings.Replace(script, needle, needle+"\n sys.stderr.write('HTTP 403 original work-session read diagnostic');sys.exit(1)", 1)
				})
			} else {
				_, err := scope.Update(context.Background(), func(s *ghcontext.State) error {
					ws, err := s.CurrentWorkSession()
					if err != nil {
						return err
					}
					if mode == "tagged-missing" {
						ws.Issues = append(ws.Issues, "gh-404")
					} else {
						ws.HistoryIssues = append(ws.HistoryIssues, "gh-404")
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			output, err := executeGitHubTest(githubWSCommand(wsCurrentCmd), "--json")
			if err == nil || !strings.Contains(err.Error(), expected) || output != "" {
				t.Fatal("missing/error read became a partial successful view", output, err)
			}
		})
	}
}

type noWorkSessionSnapshot struct{ calls int }

func (s *noWorkSessionSnapshot) ReadSnapshot(context.Context, bool) (*ghstore.Snapshot, error) {
	s.calls++
	return nil, fmt.Errorf("unexpected remote read")
}
func TestGitHubWorkSessionLocalOnlyDoesNotReadRemote(t *testing.T) {
	reader := &noWorkSessionSnapshot{}
	ws := &ghcontext.WorkSession{LocalActivities: []models.Activity{{Message: "Local only"}}}
	issues, activities, err := readGitHubWorkSession(context.Background(), reader, ws)
	if err != nil || reader.calls != 0 || len(issues) != 0 || len(activities) != 1 || activities[0].Message != "Local only" {
		t.Fatal(issues, activities, err, reader.calls)
	}
}
