// Package testutil provides deterministic external-command fixtures for tests.
package testutil

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GitHubCostFixture supplies real gh --include/--slurp response framing. It
// rejects individual endpoints, so aggregate regressions cannot hide N+1 reads.
type GitHubCostFixture struct {
	Directory                string
	IssuePages, CommentPages int
}

func NewGitHubCostFixture(t *testing.T, issueCount, commentCount int) GitHubCostFixture {
	t.Helper()
	if issueCount == 0 && commentCount > 0 {
		t.Fatal("comments require a parent issue")
	}
	dir, bin := t.TempDir(), t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", dir}, {"-C", dir, "remote", "add", "origin", "https://github.com/owner/repo.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, ".todos"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".todos", "config.json"), []byte(`{"store":"gh-issue","github":{"remote":"origin","repo":"owner/repo"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	issues := []any{}
	for n := 1; n <= issueCount; n++ {
		issues = append(issues, map[string]any{"number": n, "state": "open", "title": "private-fixture-title", "body": "private-fixture-description\n\n<!-- td:issue:v1\n{\"type\":\"task\",\"priority\":\"P2\",\"points\":0,\"details\":{\"status\":\"in_review\"}}\n-->"})
	}
	comments := []any{}
	for n := 1; n <= commentCount; n++ {
		comments = append(comments, map[string]any{"id": n, "issue_url": fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", 1+(n-1)%issueCount), "body": "private-fixture-comment"})
	}
	issueData, issuePages := paginatedResponse(issues)
	commentData, commentPages := paginatedResponse(comments)
	for name, data := range map[string]string{"issues": issueData, "comments": commentData} {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TD_COST_FIXTURE_"+strings.ToUpper(name), path)
	}
	script := `#!/bin/sh
case "$*" in
 *'auth token'*) printf 'private-fixture-token\n' ;;
 *'/issues/comments?'*) cat "$TD_COST_FIXTURE_COMMENTS" ;;
 *'/issues?state='*) cat "$TD_COST_FIXTURE_ISSUES" ;;
 *'repos/owner/repo --include') printf 'HTTP/2 200 OK\r\n\r\n{"full_name":"owner/repo","has_issues":true}\n' ;;
 *) printf 'unexpected individual API endpoint\n' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TD_GH_CACHE", "off")
	if issueCount == 0 {
		commentPages = 0
	}
	return GitHubCostFixture{Directory: dir, IssuePages: issuePages, CommentPages: commentPages}
}

func paginatedResponse(items []any) (string, int) {
	var response strings.Builder
	response.WriteString("[")
	pages := max(1, (len(items)+99)/100)
	for n := 0; n < pages; n++ {
		if n > 0 {
			response.WriteString(",")
		}
		response.WriteString("HTTP/2 200 OK\r\nX-Ratelimit-Remaining: 4999\r\nSet-Cookie: private-fixture-cookie\r\n\r\n")
		body, _ := json.Marshal(items[min(n*100, len(items)):min((n+1)*100, len(items))])
		response.Write(body)
		response.WriteString("\n")
	}
	response.WriteString("]")
	return response.String(), pages
}
