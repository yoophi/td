package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

// The command must inspect every selected task and input before its first
// remote write, including when fields are combined with comments or relations.
func TestGitHubMixedUpdatePreflightsAllInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing member", []string{"gh-6", "gh-9995", "--comment", "Do not publish"}, "gh-9995"},
		{"deleted member", []string{"gh-6", "gh-4", "--comment", "Do not publish"}, "deleted"},
		{"duplicate member", []string{"gh-6", "6", "--comment", "Do not publish"}, "repeats gh-6"},
		{"invalid later transition", []string{"gh-6", "gh-3", "--status", "in_progress"}, "invalid status transition"},
		{"later self parent", []string{"gh-6", "gh-7", "--parent", "gh-7", "--comment", "Do not publish"}, "parent"},
		{"later self dependency", []string{"gh-6", "gh-7", "--depends-on", "gh-7"}, "circular dependency"},
		{"later self block", []string{"gh-6", "gh-7", "--blocks", "gh-7"}, "cannot block the same issue"},
		{"oversized comment", []string{"gh-6", "gh-7", "--comment", strings.Repeat("x", 64*1024)}, "native comment publication budget"},
		{"single oversized comment", []string{"gh-6", "--comment", strings.Repeat("x", 64*1024)}, "native comment publication budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, run := journalCLI(t)
			for _, title := range []string{"First original", "Second original"} {
				if out, stderr, err := run("create", title); err != nil {
					t.Fatal(out, stderr, err)
				}
			}
			read := func(name string) []byte {
				t.Helper()
				data, err := os.ReadFile(os.Getenv(name))
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			before, journal := read("TD_SCHEDULE_STATE"), read("TD_GH_FIXTURE_JOURNAL")
			args := append([]string{"update"}, tc.args...)
			args = append(args, "--title", "Must never be saved")
			out, stderr, err := run(args...)
			if err == nil || !strings.Contains(out+stderr, tc.want) {
				t.Fatalf("expected %q, got stdout=%s stderr=%s error=%v", tc.want, out, stderr, err)
			}
			if !bytes.Equal(before, read("TD_SCHEDULE_STATE")) {
				t.Fatal("invalid later input changed an earlier task or published a comment")
			}
			if !bytes.Equal(journal, read("TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("invalid input wrote action history")
			}
		})
	}
}

func TestGitHubMixedUpdatePreservesValidCombinedEffects(t *testing.T) {
	_, session, run := journalCLI(t)
	useHandoffReviewFixture(t)
	execute := func(args ...string) string {
		t.Helper()
		out, stderr, err := run(args...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
		return out
	}
	show := func(id string) ghstore.Record {
		t.Helper()
		var r ghstore.Record
		if err := json.Unmarshal([]byte(execute("show", id, "--json")), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, title := range []string{"First", "Second", "Shared blocked task"} {
		execute("create", title, "--description", title)
	}
	execute("update", "gh-6", "gh-7", "--title", "Combined", "--description", "Appended", "--append",
		"--parent", "gh-2", "--depends-on", "gh-1", "--blocks", "gh-8", "--status", "in_progress", "--comment", "Started together")
	for i, id := range []string{"gh-6", "gh-7"} {
		r := show(id)
		if r.Title != "Combined" || r.Status != models.StatusInProgress || r.ImplementerSession != session || r.ParentID != "gh-2" ||
			r.Description != []string{"First\n\nAppended", "Second\n\nAppended"}[i] || r.Details == nil || !slices.Equal(r.Details.Dependencies, []string{"gh-1"}) {
			t.Fatal("combined effects lost", r)
		}
	}
	blocked := show("gh-8")
	if blocked.Details == nil || !slices.Equal(blocked.Details.Dependencies, []string{"gh-6", "gh-7"}) {
		t.Fatal("later source overwrote the earlier reverse relationship", blocked)
	}
	// Parent-only batches must use relationship handling, not the scalar-only
	// journal contract, which intentionally rejects parent changes.
	execute("update", "gh-6", "gh-7", "--parent", "")
	for _, id := range []string{"gh-6", "gh-7"} {
		if r := show(id); r.ParentID != "" {
			t.Fatal("parent-only batch did not clear parent", r)
		}
	}
	execute("update", "gh-6", "gh-7", "--status", "in_review", "--comment", "Ready for review")
	for _, id := range []string{"gh-6", "gh-7"} {
		if r := show(id); r.Status != models.StatusInReview {
			t.Fatal("review transition was lost", r)
		}
	}
	data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	var remote struct{ Comments []struct{ Body string } }
	if err := json.Unmarshal(data, &remote); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"Started together", "Ready for review"} {
		count := 0
		for _, comment := range remote.Comments {
			if strings.Contains(comment.Body, message) {
				count++
			}
		}
		if count != 2 {
			t.Fatalf("expected one %q comment per selected task, got %d", message, count)
		}
	}
}

func TestGitHubMixedUpdatePreflightsLaterClaimConstraints(t *testing.T) {
	for _, mode := range []string{"blocked", "other-owner"} {
		t.Run(mode, func(t *testing.T) {
			_, _, run := journalCLI(t)
			execute := func(args ...string) {
				t.Helper()
				if out, stderr, err := run(args...); err != nil {
					t.Fatal(out, stderr, err)
				}
			}
			execute("create", "First task")
			execute("create", "Constrained later task")
			want := "without --force"
			if mode == "blocked" {
				execute("block", "gh-7")
			} else {
				original := os.Getenv("TD_CONTEXT_ID")
				t.Setenv("TD_CONTEXT_ID", "other-claim-owner")
				execute("start", "gh-7")
				t.Setenv("TD_CONTEXT_ID", original)
				want = "release the claim explicitly"
			}
			before, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if err != nil {
				t.Fatal(err)
			}
			journal, err := os.ReadFile(os.Getenv("TD_GH_FIXTURE_JOURNAL"))
			if err != nil {
				t.Fatal(err)
			}
			out, stderr, err := run("update", "gh-6", "gh-7", "--status", "in_progress", "--title", "Must never be saved")
			if err == nil || !strings.Contains(out+stderr, want) {
				t.Fatal("expected claim constraint", want, out, stderr, err)
			}
			after, readErr := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("later claim constraint allowed earlier changes", readErr)
			}
			after, readErr = os.ReadFile(os.Getenv("TD_GH_FIXTURE_JOURNAL"))
			if readErr != nil || !bytes.Equal(journal, after) {
				t.Fatal("later claim constraint wrote action history", readErr)
			}
		})
	}
}
