package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

func TestGitHubWorkSessionPreparedHandoffReview(t *testing.T) {
	for _, failure := range []string{"", "comment-denied", "review-denied", "oversized-later-handoff"} {
		t.Run(failure, func(t *testing.T) {
			dir, _, run := journalCLI(t)
			useNativeCommentOrderFixture(t)
			useHandoffReviewFixture(t)
			execute := func(args ...string) string {
				t.Helper()
				out, stderr, err := run(append(args, "--json")...)
				if err != nil {
					t.Fatal(args, out, stderr, err)
				}
				return out
			}
			execute("create", "Review target")
			execute("create", "Handoff only target")
			execute("ws", "start", "Prepared handoff fixture")
			execute("ws", "tag", "gh-6", "gh-7", "--no-start")
			execute("start", "gh-6")
			scope, err := ghcontext.Resolve(context.Background(), dir, "owner/repo")
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			journalsBefore := len(fixtureAllJournals(t))
			args := []string{"ws", "handoff", "--review", "--done", "Finished implementation", "--json"}
			switch failure {
			case "comment-denied":
				t.Setenv("TD_SCHEDULE_FAIL_LOG", "1")
			case "review-denied":
				t.Setenv("TD_SCHEDULE_FAIL_WRITE", "1")
			case "oversized-later-handoff":
				args = append(args, "--remaining", "(gh-7) "+strings.Repeat("x", 64000))
			}
			out, stderr, commandErr := run(args...)
			if (commandErr != nil) != (failure != "") {
				t.Fatal(out, stderr, commandErr)
			}
			t.Setenv("TD_SCHEDULE_FAIL_LOG", "")
			t.Setenv("TD_SCHEDULE_FAIL_WRITE", "")
			state, err := scope.Update(context.Background(), nil)
			if err != nil || (state.ActiveWorkSession == "") != (failure == "") {
				t.Fatalf("work-session end differs from actual completion: %+v %v", state, err)
			}
			after, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			var remote struct {
				Comments []struct {
					ID       int64
					IssueURL string `json:"issue_url"`
				}
			}
			if err := json.Unmarshal(after, &remote); err != nil {
				t.Fatal(err)
			}
			if failure == "oversized-later-handoff" {
				if !strings.Contains(out+stderr, "publication budget") || !bytes.Equal(before, after) || len(fixtureAllJournals(t)) != journalsBefore {
					t.Fatal("invalid later handoff caused earlier target/journal writes", out, stderr)
				}
				return
			}
			var first, second ghstore.Record
			if json.Unmarshal([]byte(execute("show", "gh-6")), &first) != nil || json.Unmarshal([]byte(execute("show", "gh-7")), &second) != nil {
				t.Fatal("invalid issue JSON")
			}
			wantStatus := models.StatusInProgress
			if failure == "" {
				wantStatus = models.StatusInReview
				var result struct {
					ReviewRequests []string `json:"review_requests"`
					Ended          bool     `json:"ended"`
				}
				if json.Unmarshal([]byte(out), &result) != nil || len(result.ReviewRequests) != 1 || result.ReviewRequests[0] != "gh-6" || !result.Ended {
					t.Fatal("review selection or output changed", out)
				}
				matches := 0
				for _, comment := range remote.Comments {
					if strings.HasSuffix(comment.IssueURL, "/6") && first.Details.ReviewHandoffID == fmt.Sprintf("ghc-%d", comment.ID) {
						matches++
					}
				}
				if matches != 1 {
					t.Fatal("review did not reference its actual created handoff", first.Details)
				}
			}
			if first.Status != wantStatus || second.Status != models.StatusOpen {
				t.Fatal("request changed frozen review selection", first.Status, second.Status)
			}
			if failure == "review-denied" && (!strings.Contains(out+stderr, "handoffs recorded") || !strings.Contains(out+stderr, "no automatic retry")) {
				t.Fatal("partial completion or original error was hidden", out, stderr)
			}
			wantComments := 2
			if failure == "comment-denied" {
				wantComments = 0
			}
			if len(remote.Comments) != wantComments {
				t.Fatal("lost or repeated native handoff", len(remote.Comments), wantComments)
			}
			if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
				t.Fatal("GitHub handoff created task SQLite")
			}
		})
	}
}

func useHandoffReviewFixture(t *testing.T) {
	t.Helper()
	// The shared fixture historically returned all activity from the
	// issue-specific endpoint. Fanout reuses an operation ID on distinct
	// tasks, so this fixture must honor the real endpoint's membership.
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(gh)
	if err != nil {
		t.Fatal(err)
	}
	old := "elif '/comments?' in path:data=[state['comments']]"
	if !strings.Contains(string(body), old) {
		t.Fatal("fixture comment listing changed; inspect its target scope")
	}
	updated := strings.Replace(string(body), old, "elif '/comments?' in path:\n n=path.split('/issues/')[1].split('/')[0];data=[state['comments'] if n.startswith('comments?') else [c for c in state['comments'] if c['issue_url'].endswith('/'+n)]]", 1)

	for needle, extra := range map[string]string{
		"issues[n]['updated_at']=clock;f.write_text(json.dumps(state))": "\n if os.environ.get('TD_GH_JOURNAL_FAIL')=='second-comment-lost' and n=='7':sys.stderr.write('connection lost after second comment');sys.exit(1)",
		"f.write_text(json.dumps(state));data=None":                     "\n if os.environ.get('TD_GH_JOURNAL_FAIL')=='delete-lost':sys.stderr.write('connection lost after DELETE');sys.exit(1)",
	} {
		if !strings.Contains(updated, needle) {
			t.Fatal("handoff fixture failure seam missing", needle)
		}
		updated = strings.Replace(updated, needle, needle+extra, 1)
	}
	if err := os.WriteFile(gh, []byte(updated), 0755); err != nil {
		t.Fatal(err)
	}

}
