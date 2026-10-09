package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCommentDeletionMembershipProtectionAndOutcome(t *testing.T) {
	for _, mode := range []string{"native", "td-comment", "log", "handoff", "wrong-issue", "missing", "edited", "issue-edited", "denied", "verification-error", "still-visible", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			deletes, reads := 0, 0
			deleted := false
			comment := apiComment{ID: 12, IssueURL: "https://api.github.com/repos/owner/repo/issues/9", Body: "Native fixture", CreatedAt: time.Now().UTC()}
			switch mode {
			case "td-comment":
				comment.Body, _ = renderActivity(activityData{Kind: "comment", OperationID: "fixture-op", SessionID: "fixture-author", Message: "Fixture message"})
			case "log":
				comment.Body, _ = renderActivity(activityData{Kind: "log", OperationID: "fixture-op", SessionID: "fixture-author", Message: "Fixture log", LogType: "progress"})
			case "handoff":
				comment.Body, _ = renderActivity(activityData{Kind: "handoff", OperationID: "fixture-op", SessionID: "fixture-author", Done: []string{"Fixture done"}})
			case "wrong-issue":
				comment.IssueURL = "https://api.github.com/repos/owner/repo/issues/10"
			case "malformed":
				comment.Body = "<!-- td:activity:v999\n{}\n-->"
			}
			c := &Client{repo: "owner/repo", run: func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				switch {
				case slices.Contains(args, "repos/owner/repo/issues/9"):
					title := "Fixture issue"
					if mode == "issue-edited" && reads > 0 {
						title = "External edit"
					}
					return json.Marshal(map[string]any{"number": 9, "state": "open", "title": title})
				case slices.Contains(args, "repos/owner/repo/issues/9/comments?per_page=100"):
					if deleted && mode == "verification-error" {
						return nil, errors.New("fixture read denied")
					}
					comments := []apiComment{comment}
					if mode == "missing" || (deleted && mode != "still-visible") {
						comments = []apiComment{}
					}
					return json.Marshal([][]apiComment{comments})
				case slices.Contains(args, "repos/owner/repo/issues/comments/12"):
					if slices.Contains(args, "DELETE") {
						deletes++
						if mode == "denied" {
							return nil, errors.New("fixture delete denied")
						}
						deleted = true
						return nil, nil
					}
					reads++
					result := comment
					if mode == "edited" && reads == 2 {
						result.Body = "External edit"
					}
					return json.Marshal(result)
				default:
					t.Fatalf("unexpected request: %v", args)
					return nil, nil
				}
			}}
			err := c.DeleteComment(ctx, "9", "ghc-12")
			if mode == "native" || mode == "td-comment" {
				if err != nil || deletes != 1 {
					t.Fatalf("delete: %v count %d", err, deletes)
				}
				return
			}
			if err == nil {
				t.Fatal("expected refusal or uncertain outcome")
			}
			want := 0
			if mode == "denied" || mode == "verification-error" || mode == "still-visible" {
				want = 1
				if !strings.Contains(err.Error(), "inspect") {
					t.Fatalf("missing recovery instruction: %v", err)
				}
			}
			if deletes != want {
				t.Fatalf("deletes=%d want %d: %v", deletes, want, err)
			}
			if mode == "missing" || mode == "wrong-issue" {
				var missing *CommentNotFoundError
				if !errors.As(err, &missing) {
					t.Fatalf("wrong missing type: %T", err)
				}
			}
			if mode == "log" || mode == "handoff" {
				var denied *PolicyError
				if !errors.As(err, &denied) {
					t.Fatalf("wrong protection type: %T", err)
				}
			}
		})
	}
}

func TestCommentNumberRejectsNonLocalIdentity(t *testing.T) {
	for _, s := range []string{"", "ghc-0", "-1", "gh-1", "ghc-1/../../2", "https://github.com/owner/repo/issues/1", "ghc-9999999999999999999999999"} {
		if _, err := CommentNumber(s); err == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{"ghc-12", "12"} {
		n, err := CommentNumber(s)
		if err != nil || n != 12 {
			t.Fatalf("rejected %q: %v", s, err)
		}
	}
}
