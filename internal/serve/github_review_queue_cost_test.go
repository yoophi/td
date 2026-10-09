package serve

import (
	"encoding/json"
	"fmt"
	"github.com/marcus/td/internal/models"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubHTTPMonitorReviewCountDoesNotAddIndividualRequests(t *testing.T) {
	t.Setenv("TD_GH_CACHE", "off")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".todos"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".todos", "config.json"), []byte(`{"store":"gh-issue","github":{"remote":"origin","repo":"owner/repo"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--quiet", dir}, {"-C", dir, "remote", "add", "origin", "https://github.com/owner/repo.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	bin := t.TempDir()
	fixture, log := filepath.Join(bin, "issues.json"), filepath.Join(bin, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TD_REVIEW_CALL_LOG"
case "$*" in
  *'auth token'*) printf 'fixture-token\n' ;;
  *'/issues/comments?'*) printf 'HTTP/2 200 OK\r\n\r\n[[]]\n' ;;
  *'/issues?state=all'*) printf 'HTTP/2 200 OK\r\n\r\n'; cat "$TD_REVIEW_ISSUES" ;;
  *'repos/owner/repo --include') printf 'HTTP/2 200 OK\r\n\r\n{"full_name":"owner/repo","has_issues":true}\n' ;;
  *) printf 'unexpected individual request\n' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TD_REVIEW_ISSUES", fixture)
	t.Setenv("TD_REVIEW_CALL_LOG", log)
	for _, count := range []int{1, 60} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			issues := []map[string]any{}
			for n := 1; n <= count; n++ {
				issues = append(issues, map[string]any{"number": n, "state": "open", "title": "Review", "body": "<!-- td:issue:v1\n{\"type\":\"task\",\"priority\":\"P2\",\"points\":0,\"details\":{\"status\":\"in_review\"}}\n-->"})
			}
			payload, _ := json.Marshal([]any{issues})
			if err := os.WriteFile(fixture, payload, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(log, nil, 0600); err != nil {
				t.Fatal(err)
			}
			srv := NewGitHubServer(dir, "actor", "owner/repo", ServeConfig{})
			srv.EnableGitHubMonitor(NewGitHubReadStore(dir, models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}), &githubMonitorFocusFixture{})
			w := httptest.NewRecorder()
			srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/monitor", nil))
			if w.Code != 200 {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			var envelope struct {
				Data struct {
					Monitor MonitorDTO `json:"monitor"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			list := envelope.Data.Monitor.TaskList
			if !list.ReviewVerificationRequired || len(list.PendingOther) != count || len(list.Reviewable) != 0 || len(list.ReadyToClose) != 0 {
				t.Fatalf("unsafe HTTP queue: %+v", list)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 4 {
				t.Fatalf("request cost grew: %s", calls)
			}
		})
	}
}
