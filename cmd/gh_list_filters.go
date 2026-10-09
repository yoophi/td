package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

type gitHubListFilters struct {
	points                   listPointsRange
	created, updated, closed listDateRange
	implementer, reviewer    string
	mine                     bool
}

func readGitHubListFilters(cmd *cobra.Command) (gitHubListFilters, error) {
	var f gitHubListFilters
	if raw, _ := cmd.Flags().GetString("points"); raw != "" {
		r, err := parseListPointsRange(raw)
		if err != nil {
			return f, err
		}
		f.points = r
	}
	for name, target := range map[string]*listDateRange{"created": &f.created, "updated": &f.updated, "closed": &f.closed} {
		if raw, _ := cmd.Flags().GetString(name); raw != "" {
			r, err := parseListDateRange(raw)
			if err != nil {
				return f, fmt.Errorf("--%s: %w", name, err)
			}
			*target = r
		}
	}
	f.implementer, _ = cmd.Flags().GetString("implementer")
	f.reviewer, _ = cmd.Flags().GetString("reviewer")
	f.mine, _ = cmd.Flags().GetBool("mine")
	return f, nil
}

func resolveGitHubListState(cmd *cobra.Command, cfg *models.Config) (*ghcontext.State, error) {
	dir, err := gitHubContextDirectory()
	if err != nil {
		return nil, err
	}
	scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
	if err != nil {
		return nil, err
	}
	return scope.Update(cmd.Context(), nil)
}

func (f gitHubListFilters) matches(issue models.Issue) bool {
	return f.points.matches(issue.Points) && f.created.matches(issue.CreatedAt) && f.updated.matches(issue.UpdatedAt) && f.closed.matchesOptional(issue.ClosedAt) && (f.implementer == "" || issue.ImplementerSession == f.implementer) && (f.reviewer == "" || issue.ReviewerSession == f.reviewer)
}

// Queue membership is informational. Freshness and revision are checked by
// the client, and approving/closing later must recheck policy independently.
func gitHubListReviewBuckets(cmd *cobra.Command, client *ghstore.Client, records []ghstore.Record, actor string) (map[string]bool, map[string]bool, error) {
	mode, err := resolveReviewPolicyMode(getBaseDir())
	if err != nil {
		return nil, nil, err
	}
	return readGitHubReviewBuckets(cmd.Context(), client, records, actor, mode)
}

func gitHubListLabelMatches(labels []string, label string) bool {
	csv := strings.Join(labels, ",")
	for _, pattern := range []string{label + ",%", "%," + label + ",%", "%," + label, label} {
		if issuestore.SQLiteLIKE(pattern)(csv) {
			return true
		}
	}
	return false
}

func compareGitHubOptionalTime(a, b *time.Time) int {
	if a == nil {
		if b == nil {
			return 0
		}
		return -1
	}
	if b == nil {
		return 1
	}
	return a.Compare(*b)
}

func optionalString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func printGitHubReviewList(cmd *cobra.Command, rows []issuestore.Record, awaiting, ready map[string]bool, includeApproved bool) error {
	for _, bucket := range []struct {
		ids   map[string]bool
		title string
	}{{awaiting, "AWAITING YOUR REVIEW"}, {ready, "READY TO CLOSE"}} {
		if bucket.title == "READY TO CLOSE" && !includeApproved {
			continue
		}
		selected := []issuestore.Record{}
		for _, r := range rows {
			if bucket.ids[r.ID] {
				selected = append(selected, r)
			}
		}
		if len(selected) == 0 {
			continue
		}
		cmd.Printf("%s (%d):\n", bucket.title, len(selected))
		for _, r := range selected {
			cmd.Printf("  %s\n", output.FormatIssueShort(&r.Issue))
		}
	}
	if len(rows) == 0 {
		cmd.Println("No issues awaiting your review or ready to close")
	}
	return nil
}
