package serve

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/marcus/td/internal/features"
	"github.com/marcus/td/internal/ghstore"
)

type githubWorkflowClient interface {
	githubIssueWriter
	List(context.Context, bool) ([]ghstore.Record, error)
	TransitionObservedWithCascades(context.Context, *ghstore.Record, string, ghstore.TransitionOptions) (*ghstore.Record, bool, error)
}

type githubWorkflowBody struct {
	Reason             string `json:"reason"`
	ReviewedBy         string `json:"reviewed_by"`
	SelfReview         bool   `json:"self_review"`
	Decision           string `json:"decision"`
	Summary            string `json:"summary"`
	RecordOnly         bool   `json:"record_only"`
	Minor              bool   `json:"minor"`
	Force              bool   `json:"force"`
	Admin              string `json:"admin"`
	SelfCloseException string `json:"self_close_exception"`
}

func (s *Server) EnableGitHubWorkflow(store *GitHubWriteStore) {
	s.githubCapabilities = append(s.githubCapabilities, "workflow_transitions")
	for _, action := range []string{"start", "review", "approve", "reviews", "reject", "block", "unblock", "close", "reopen"} {
		route := "POST /v1/issues/{id}/" + action
		s.githubEndpoints = append(s.githubEndpoints, route)
		s.mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) { store.transition(w, r, action) })
	}
}

func (s *GitHubWriteStore) transition(w http.ResponseWriter, r *http.Request, endpoint string) {
	if r.Body == nil || r.ContentLength == 0 {
		r.Body = io.NopCloser(strings.NewReader("{}"))
	}
	var body githubWorkflowBody
	if !decodeGitHubWrite(w, r, &body) {
		return
	}
	if _, err := ghstore.Number(r.PathValue("id")); err != nil {
		WriteError(w, ErrValidation, err.Error(), 400)
		return
	}
	action := endpoint
	if endpoint == "reviews" {
		action = "approve"
		body.RecordOnly = true
		if body.Reason != "" && body.Summary != "" && body.Reason != body.Summary {
			WriteError(w, ErrValidation, "reason and summary disagree", 400)
			return
		}
		if body.Summary != "" {
			body.Reason = body.Summary
		}
	} else if body.Summary != "" {
		WriteError(w, ErrValidation, "summary requires the reviews endpoint", 400)
		return
	}
	if body.Minor && action != "review" {
		WriteError(w, ErrValidation, "minor requires review", 400)
		return
	}
	if body.Force && action != "start" {
		WriteError(w, ErrValidation, "force requires start", 400)
		return
	}
	mode, err := features.ResolveReviewPolicyMode(s.baseDir)
	if err != nil {
		githubWriteError(w, err)
		return
	}
	options := ghstore.TransitionOptions{SessionID: s.sessionID, AgentType: "web", Mode: mode, Reason: body.Reason, ReviewedBy: body.ReviewedBy, SelfReview: body.SelfReview, Decision: body.Decision, RecordOnly: body.RecordOnly, Minor: body.Minor, Force: body.Force, AdminReason: body.Admin, SelfCloseException: body.SelfCloseException}
	if err := ghstore.ValidateReviewOptions(action, options); err != nil {
		WriteError(w, ErrValidation, err.Error(), 400)
		return
	}
	writer, err := s.open(r.Context())
	if err != nil {
		githubWriteError(w, err)
		return
	}
	client, ok := writer.(githubWorkflowClient)
	if !ok {
		WriteError(w, "unsupported_operation", "workflow store is unavailable", 501)
		return
	}
	observed, err := client.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		githubWriteError(w, err)
		return
	}
	if !checkIssueRevision(w, r, &observed.Issue) {
		return
	}
	result, noop, err := client.TransitionObservedWithCascades(r.Context(), observed, action, options)
	if err != nil {
		githubWriteError(w, err)
		return
	}
	dto, err := githubIssueDTO(r.Context(), client, result, s.baseDir, s.sessionID, true)
	if err != nil {
		githubWriteError(w, fmt.Errorf("%s transitioned to %s, but availability could not be read (inspect current state before retrying): %w", result.ID, result.Status, err))
		return
	}
	reviewed := []IssueDTO{}
	for _, child := range result.CascadedReviews {
		reviewed = append(reviewed, IssueToDTO(&child.Issue))
	}
	parents := []IssueDTO{}
	for _, parent := range result.ParentStatusUpdates {
		parents = append(parents, IssueToDTO(&parent.Issue))
	}
	unblocked := []IssueDTO{}
	for _, dependent := range result.AutoUnblocked {
		unblocked = append(unblocked, IssueToDTO(&dependent.Issue))
	}
	payload := map[string]any{"reviewed_descendants": reviewed, "issue": dto, "noop": noop, "cascades": transitionCascadeResult{ParentStatusUpdates: parents, AutoUnblocked: unblocked}}
	status := http.StatusOK
	if endpoint == "reviews" {
		status = http.StatusCreated
		if result.Details != nil && len(result.Details.Reviews) > 0 {
			review := result.Details.Reviews[len(result.Details.Reviews)-1]
			payload["review"] = IssueReviewToDTO(&review)
			if review.SupersededAt == nil && review.Decision == "approved" && result.ReviewedAt != nil && result.ReviewerSession == review.ReviewerSession {
				payload["active_review"] = &IssueReviewSummary{ID: review.ID, Decision: review.Decision, ReviewerSession: review.ReviewerSession, RequestedBySession: review.RequestedBySession, Summary: review.Summary, CreatedAt: review.CreatedAt, SelfReview: review.SelfReview, ReviewedBy: review.ReviewedBy}
			}
		}
	}
	WriteSuccess(w, payload, status)
}
