package ghstore

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

// IssueDetails contains fields that GitHub Issues does not store natively.
// Native issue identity, title, body, labels and server timestamps stay outside
// this structure. Unknown fields/versions fail closed rather than being lost.
// Workflow writes must use UpdateObserved after validating the observed issue.
type IssueDetails struct {
	Status                   models.Status                `json:"status,omitempty"`
	Minor                    bool                         `json:"minor,omitempty"`
	Sprint                   string                       `json:"sprint,omitempty"`
	ParentID                 string                       `json:"parent_id,omitempty"`
	CreatorSession           string                       `json:"creator_session,omitempty"`
	ImplementerSession       string                       `json:"implementer_session,omitempty"`
	ReviewerSession          string                       `json:"reviewer_session,omitempty"`
	ReviewRequestedBySession string                       `json:"review_requested_by_session,omitempty"`
	ClosedBySession          string                       `json:"closed_by_session,omitempty"`
	CreatedBranch            string                       `json:"created_branch,omitempty"`
	ReviewedAt               *time.Time                   `json:"reviewed_at,omitempty"`
	DeletedAt                *time.Time                   `json:"deleted_at,omitempty"`
	DueDate                  *string                      `json:"due_date,omitempty"`
	DeferUntil               *string                      `json:"defer_until,omitempty"`
	DeferCount               int                          `json:"defer_count,omitempty"`
	Dependencies             []string                     `json:"dependencies,omitempty"`
	Files                    []models.IssueFile           `json:"files,omitempty"`
	Reviews                  []models.IssueReview         `json:"reviews,omitempty"`
	Sessions                 []models.IssueSessionHistory `json:"sessions,omitempty"`
}

func (d IssueDetails) validate() error {
	if d.Status != "" && !slices.Contains([]models.Status{models.StatusOpen, models.StatusInProgress, models.StatusBlocked, models.StatusInReview, models.StatusClosed}, d.Status) {
		return fmt.Errorf("invalid detailed issue status %q", d.Status)
	}
	if d.DeferCount < 0 {
		return fmt.Errorf("defer count must not be negative")
	}
	for _, date := range []*string{d.DueDate, d.DeferUntil} {
		if date != nil {
			if parsed, err := time.Parse("2006-01-02", *date); err != nil || parsed.Format("2006-01-02") != *date {
				return fmt.Errorf("invalid issue date %q (expected YYYY-MM-DD)", *date)
			}
		}
	}
	for _, id := range append([]string{d.ParentID}, d.Dependencies...) {
		if id != "" {
			n, err := Number(id)
			if err != nil || id != fmt.Sprintf("gh-%d", n) {
				return fmt.Errorf("relationship ID %q must be canonical gh-N", id)
			}
		}
	}
	seen := map[string]bool{}
	for _, id := range d.Dependencies {
		if id == "" || seen[id] {
			return fmt.Errorf("empty or duplicate dependency %q", id)
		}
		seen[id] = true
	}
	for _, file := range d.Files {
		if strings.TrimSpace(file.FilePath) == "" || !slices.Contains([]models.FileRole{models.FileRoleImplementation, models.FileRoleTest, models.FileRoleReference, models.FileRoleConfig}, file.Role) {
			return fmt.Errorf("invalid linked file")
		}
	}
	for _, review := range d.Reviews {
		if review.ID == "" || review.ReviewerSession == "" || review.CreatedAt.IsZero() || !slices.Contains([]string{reviewpolicy.DecisionApproved, reviewpolicy.DecisionChangesRequested, reviewpolicy.DecisionApprovedByParentCascade}, review.Decision) {
			return fmt.Errorf("invalid review record")
		}
	}
	for _, entry := range d.Sessions {
		if entry.SessionID == "" || entry.CreatedAt.IsZero() || !slices.Contains([]models.IssueSessionAction{models.ActionSessionCreated, models.ActionSessionStarted, models.ActionSessionUnstarted, models.ActionSessionReviewed, models.ActionSessionReviewApproved, models.ActionSessionReviewChangesRequested, models.ActionSessionClosed}, entry.Action) {
			return fmt.Errorf("invalid session history")
		}
	}
	return nil
}

func (d IssueDetails) apply(issue *models.Issue) {
	issue.Minor, issue.Sprint, issue.ParentID = d.Minor, d.Sprint, d.ParentID
	issue.CreatorSession, issue.ImplementerSession, issue.ReviewerSession = d.CreatorSession, d.ImplementerSession, d.ReviewerSession
	issue.ReviewRequestedBySession, issue.ClosedBySession = d.ReviewRequestedBySession, d.ClosedBySession
	issue.CreatedBranch, issue.ReviewedAt, issue.DeletedAt = d.CreatedBranch, d.ReviewedAt, d.DeletedAt
	issue.DueDate, issue.DeferUntil, issue.DeferCount = d.DueDate, d.DeferUntil, d.DeferCount
	// Native close/reopen wins over stale body metadata. A native state change
	// is never evidence of review approval; keep historical data separate.
	if d.Status != "" && nativeStatus(d.Status) == nativeStatus(issue.Status) {
		issue.Status = d.Status
	}
	if d.Status != "" && nativeStatus(d.Status) != nativeStatus(issue.Status) {
		issue.ImplementerSession = ""
		issue.ReviewerSession = ""
		issue.ReviewedAt = nil
		issue.ReviewRequestedBySession = ""
		issue.ClosedBySession = ""
	}
}

func nativeStatus(status models.Status) models.Status {
	if status == models.StatusClosed {
		return models.StatusClosed
	}
	return models.StatusOpen
}

func detailsFromIssue(issue *models.Issue) IssueDetails {
	return IssueDetails{Status: issue.Status, Minor: issue.Minor, Sprint: issue.Sprint, ParentID: issue.ParentID, CreatorSession: issue.CreatorSession, ImplementerSession: issue.ImplementerSession, ReviewerSession: issue.ReviewerSession, ReviewRequestedBySession: issue.ReviewRequestedBySession, ClosedBySession: issue.ClosedBySession, CreatedBranch: issue.CreatedBranch, ReviewedAt: issue.ReviewedAt, DeletedAt: issue.DeletedAt, DueDate: issue.DueDate, DeferUntil: issue.DeferUntil, DeferCount: issue.DeferCount}
}
