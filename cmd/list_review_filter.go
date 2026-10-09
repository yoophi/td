package cmd

import (
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
)

// Classify approval state before the output limit, consistently for JSON and
// human list output. A recorded approval belongs to the close bucket.
func listSQLiteReviewableIssues(database *db.DB, opts db.ListIssuesOptions, includeApproved bool) ([]models.Issue, error) {
	all := opts
	all.Limit = 0
	candidates, err := database.ListIssues(all)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, issue := range candidates {
		approval, err := database.GetActiveApprovalReview(issue.ID)
		if err != nil {
			return nil, err
		}
		if approval == nil {
			ids = append(ids, issue.ID)
			seen[issue.ID] = true
		}
	}
	if includeApproved {
		ready := all
		ready.ReviewableBy = ""
		ready.ReadyToCloseBy = opts.ReviewableBy
		rows, err := database.ListIssues(ready)
		if err != nil {
			return nil, err
		}
		for _, issue := range rows {
			if !seen[issue.ID] {
				ids = append(ids, issue.ID)
				seen[issue.ID] = true
			}
		}
	}
	if len(ids) == 0 {
		return []models.Issue{}, nil
	}
	selected := opts
	selected.IDs = ids
	selected.ReviewableBy = ""
	selected.ReadyToCloseBy = ""
	return database.ListIssues(selected)
}
