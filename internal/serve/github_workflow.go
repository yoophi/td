package serve

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
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
	for _, action := range []string{"start", "review", "approve", "reviews", "reject", "block", "unblock", "close", "reopen"} {
		route := "POST /v1/issues/{id}/" + action
		s.githubEndpoints = append(s.githubEndpoints, route)
		s.mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) { store.transition(w, r, action) })
	}
}

// Until the shared cascade implementation is connected, fail before changing
// related issues rather than silently omitting required parent/dependent work.
func standaloneWorkflow(ctx context.Context, client githubWorkflowClient, root *ghstore.Record, action string, o ghstore.TransitionOptions) error {
	if action != "review" && action != "close" && action != "approve" {
		return nil
	}
	if action == "approve" && o.RecordOnly {
		return nil
	}
	if root.ParentID != "" {
		return fmt.Errorf("workflow cascades for parent-linked issues are not yet supported by HTTP")
	}
	records, err := client.List(ctx, true)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, record := range records {
		if seen[record.ID] {
			return fmt.Errorf("GitHub listing repeated %s; retry the read", record.ID)
		}
		seen[record.ID] = true

		if (action == "close" || action == "approve") && record.Details != nil && slices.Contains(record.Details.Dependencies, root.ID) {
			return fmt.Errorf("workflow cascades for issues with dependents are not yet supported by HTTP")
		}
	}
	return nil
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
	options := ghstore.TransitionOptions{SessionID: s.sessionID, Mode: mode, Reason: body.Reason, ReviewedBy: body.ReviewedBy, SelfReview: body.SelfReview, Decision: body.Decision, RecordOnly: body.RecordOnly, Minor: body.Minor, Force: body.Force, AdminReason: body.Admin, SelfCloseException: body.SelfCloseException}
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
	if err := standaloneWorkflow(r.Context(), client, observed, action, options); err != nil {
		if strings.Contains(err.Error(), "not yet supported by HTTP") {
			WriteError(w, "unsupported_operation", err.Error()+"; no transition write attempted", 501)
		} else {
			githubWriteError(w, err)
		}
		return
	}
	result, noop, err := client.TransitionObservedWithCascades(r.Context(), observed, action, options)
	if err != nil {
		githubWriteError(w, err)
		return
	}
	// Relationship membership can change while the individual issue is saved.
	// Report that partial result rather than suggesting the root was unchanged.
	if err := standaloneWorkflow(r.Context(), client, result, action, options); err != nil {
		WriteError(w, ErrConflict, fmt.Sprintf("%s transitioned to %s, but cascade verification failed; inspect current state before retrying: %v", result.ID, result.Status, err), 409)
		return
	}
	reviewed := []IssueDTO{}
	for _, child := range result.CascadedReviews {
		reviewed = append(reviewed, IssueToDTO(&child.Issue))
	}
	WriteSuccess(w, map[string]any{"reviewed_descendants": reviewed, "issue": IssueToDTO(&result.Issue), "noop": noop, "cascades": transitionCascadeResult{ParentStatusUpdates: []IssueDTO{}, AutoUnblocked: []IssueDTO{}}}, 200)
}
