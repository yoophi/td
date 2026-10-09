package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGitHubMonitorReviewCountDoesNotAddIndividualRequests(t *testing.T) {
	t.Setenv("TD_GH_CACHE", "off")
	dir := t.TempDir()
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
			client, err := ghstore.Open(context.Background(), dir, &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"})
			if err != nil {
				t.Fatal(err)
			}
			msg, err := FetchGitHubData(context.Background(), client, "actor", reviewpolicy.ModeTrusted, nil, "", "auto", true, SortByPriority, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if !msg.TaskList.ReviewVerificationRequired || len(msg.TaskList.PendingOther) != count || len(msg.TaskList.Reviewable) != 0 || len(msg.TaskList.ReadyToClose) != 0 {
				t.Fatalf("unsafe queue: %+v", msg.TaskList)
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

type failedBulkReader struct{ GitHubMonitorReader }

func (*failedBulkReader) ReadSnapshot(context.Context, bool) (*ghstore.Snapshot, error) {
	return nil, errors.New("bulk observation failed")
}
func TestGitHubMonitorBulkFailureDoesNotFallBackToIndividualReads(t *testing.T) {
	_, err := FetchGitHubData(context.Background(), &failedBulkReader{}, "actor", reviewpolicy.ModeTrusted, nil, "", "auto", true, SortByPriority, time.Now())
	if err == nil || !strings.Contains(err.Error(), "bulk observation failed") {
		t.Fatalf("%v", err)
	}
	s := &GitHubDataSource{ctx: context.Background(), open: func(context.Context) (GitHubMonitorReader, error) { return &failedBulkReader{}, nil }}
	result := s.Handoffs()
	if result.Error == nil || !strings.Contains(result.Error.Error(), "bulk observation failed") {
		t.Fatalf("%v", result.Error)
	}
}
