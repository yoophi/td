package monitor

import (
	"context"
	"fmt"
	"testing"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/testutil"
)

func TestGitHubDataSourceBulkPageCostMatrix(t *testing.T) {
	for _, counts := range [][2]int{{0, 0}, {1, 0}, {1, 101}, {45, 0}, {45, 101}, {101, 0}, {101, 101}} {
		t.Run(fmt.Sprintf("issues%d/comments%d", counts[0], counts[1]), func(t *testing.T) {
			fixture := testutil.NewGitHubCostFixture(t, counts[0], counts[1])
			t.Setenv("TD_GH_DEBUG", "0")
			ctx, cost := ghstore.WithAPICost(context.Background())
			source := NewGitHubDataSource(ctx, fixture.Directory, models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}, "fixture-actual-actor", "fixture-branch", func(context.Context) (*string, error) { return nil, nil })
			msg := source.Fetch("", true, SortByPriority)
			if msg.Error != nil || len(msg.TaskList.PendingOther) != counts[0] || len(msg.TaskList.ReadyToClose) != 0 || len(msg.TaskList.Reviewable) != 0 {
				t.Fatalf("unsafe or failed aggregate: %+v", msg)
			}
			got := cost.Snapshot()
			if got.Total.HTTPResponses != 1+fixture.IssuePages+fixture.CommentPages || got.Categories["preflight"].HTTPResponses != 1 || got.Categories["issue_pages"].HTTPResponses != fixture.IssuePages || got.Categories["comment_pages"].HTTPResponses != fixture.CommentPages {
				t.Fatalf("cost=%+v", got)
			}
			for _, category := range []string{"issue_detail", "issue_comments", "review_validation"} {
				if got.Categories[category].Invocations != 0 {
					t.Fatalf("N+1 in TUI: %+v", got)
				}
			}
			// Each Fetch still opens/revalidates the repository; record rather than
			// silently attributing preflight repetition to bulk collection cost.
			second, cost2 := ghstore.WithAPICost(context.Background())
			source.ctx = second
			if msg := source.Fetch("", true, SortByPriority); msg.Error != nil {
				t.Fatal(msg.Error)
			}
			if cost2.Snapshot().Categories["preflight"].HTTPResponses != 1 {
				t.Fatal("preflight repetition accounting lost")
			}
		})
	}
}
