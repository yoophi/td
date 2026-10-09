package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/td/internal/models"
	"strings"
	"testing"
	"time"
)

func TestChangeTokenTracksIssueCommentAndDeletedEntityChanges(t *testing.T) {
	ctx := context.Background()
	c, issues, _ := hierarchyFixture(t)
	now := time.Now().UTC()
	body, err := encodeBody("", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{DeletedAt: &now}})
	if err != nil {
		t.Fatal(err)
	}
	issues[3]["body"] = body
	base := c.run
	comments := map[string][]apiComment{}
	c.run = func(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "/comments?") {
			page := comments[args[5]]
			if page == nil {
				page = []apiComment{}
			}
			return json.Marshal([][]apiComment{page})
		}
		return base(ctx, dir, input, args...)
	}
	token, err := c.ChangeToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.ChangeToken(ctx)
	if err != nil || again != token {
		t.Fatal("unstable unchanged token")
	}
	endpoint := "repos/owner/repo/issues/comments?per_page=100"
	comments[endpoint] = []apiComment{{ID: 90, IssueURL: "https://api.github.com/repos/owner/repo/issues/3", Body: "comment on deleted issue"}}
	changed, err := c.ChangeToken(ctx)
	if err != nil || changed == token {
		t.Fatalf("deleted comment ignored: %v", err)
	}
	comments[endpoint][0].Body = "edited"
	edited, err := c.ChangeToken(ctx)
	if err != nil || edited == changed {
		t.Fatal("comment edit ignored")
	}
	comments[endpoint] = nil
	restored, err := c.ChangeToken(ctx)
	if err != nil || restored != token {
		t.Fatal("comment deletion ignored")
	}
	board, err := encodeBody("board", metadata{Type: models.TypeTask, Priority: models.PriorityP2, EntityKind: "board"})
	if err != nil {
		t.Fatal(err)
	}
	issues[2]["body"] = board
	boardToken, err := c.ChangeToken(ctx)
	if err != nil || boardToken == token {
		t.Fatalf("auxiliary entity ignored: %v", err)
	}
	issues[2]["title"] = "renamed board"
	renamed, err := c.ChangeToken(ctx)
	if err != nil || renamed == boardToken {
		t.Fatal("board edit ignored")
	}
	comments[endpoint] = []apiComment{{ID: 90, IssueURL: "https://api.github.com/repos/owner/repo/issues/3", Body: activityPrefix + "invalid"}}
	if token, err := c.ChangeToken(ctx); err == nil || token != "" {
		t.Fatalf("corruption became token: %s %v", token, err)
	}
	c.run = func(context.Context, string, []byte, ...string) ([]byte, error) {
		return nil, errors.New("permission denied")
	}
	if token, err := c.ChangeToken(ctx); err == nil || token != "" {
		t.Fatalf("failure became token: %s %v", token, err)
	}
}

func TestChangeTokenStableOrderingAndRejectsDuplicatePages(t *testing.T) {
	reverse := false
	duplicate := false
	c := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "/comments?") {
			comments := []apiComment{{ID: 2, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: "two"}, {ID: 1, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: "one"}}
			if reverse {
				comments[0], comments[1] = comments[1], comments[0]
			}
			return json.Marshal([][]apiComment{comments})
		}
		issues := []apiIssue{{Number: 1, State: "open"}, {Number: 2, PullRequest: json.RawMessage(`{}`)}}
		if reverse {
			issues[0], issues[1] = issues[1], issues[0]
		}
		pages := [][]apiIssue{issues}
		if duplicate {
			pages = append(pages, issues)
		}
		return json.Marshal(pages)
	}}
	token, err := c.ChangeToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reverse = true
	again, err := c.ChangeToken(context.Background())
	if err != nil || again != token {
		t.Fatalf("order changed token: %v", err)
	}
	duplicate = true
	if token, err := c.ChangeToken(context.Background()); err == nil || token != "" {
		t.Fatal("duplicate pages accepted")
	}
}
