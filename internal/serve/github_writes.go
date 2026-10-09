package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type githubIssueWriter interface {
	Get(context.Context, string) (*ghstore.Record, error)
	Create(context.Context, *models.Issue) (*ghstore.Record, error)
	UpdateObserved(context.Context, *ghstore.Record, ghstore.Changes) (*ghstore.Record, error)
}
type GitHubWriteStore struct {
	availabilityEnabled        func() bool
	open                       func(context.Context) (githubIssueWriter, error)
	baseDir, sessionID, branch string
}

func NewGitHubWriteStore(dir string, selected models.GitHubStoreConfig, scope ghcontext.Scope, sessionID string) *GitHubWriteStore {
	return &GitHubWriteStore{baseDir: dir, sessionID: sessionID, branch: scope.Branch, open: func(ctx context.Context) (githubIssueWriter, error) {
		client, err := openSelectedGitHub(ctx, dir, selected)
		if err != nil {
			return nil, err
		}
		current, err := ghcontext.ResolveWeb(ctx, dir, selected.Repo)
		if err != nil {
			return nil, err
		}
		if current.Path != scope.Path {
			return nil, fmt.Errorf("web worktree or branch changed; restart td serve")
		}
		state := &githubSessionStore{scope: scope, sessionID: sessionID}
		if err := state.update(ctx, nil); err != nil {
			return nil, err
		}
		return client, nil
	}}
}
func (s *Server) EnableGitHubWrites(store *GitHubWriteStore) {
	store.availabilityEnabled = func() bool { return slices.Contains(s.githubEndpoints, "POST /v1/issues/{id}/start") }
	s.githubCapabilities = append(s.githubCapabilities, "issue_revisions")
	s.githubEndpoints = append(s.githubEndpoints, "POST /v1/issues", "PATCH /v1/issues/{id}")
	s.mux.HandleFunc("POST /v1/issues", store.create)
	s.mux.HandleFunc("PATCH /v1/issues/{id}", store.update)
}
func decodeGitHubWrite(w http.ResponseWriter, r *http.Request, body any) bool {
	if r.URL.RawQuery != "" {
		WriteError(w, ErrValidation, "this operation does not support query parameters", 400)
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		WriteError(w, ErrValidation, "invalid JSON: "+err.Error(), 400)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		WriteError(w, ErrValidation, "expected one JSON object", 400)
		return false
	}
	return true
}
func canonicalParent(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	n, err := ghstore.Number(id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("gh-%d", n), nil
}
func githubWriteError(w http.ResponseWriter, err error) {
	if writeGitHubRateLimit(w, err) {
		return
	}
	var input *ghstore.WorkflowInputError
	if errors.As(err, &input) {
		WriteError(w, ErrValidation, err.Error(), 400)
		return
	}
	var state *ghstore.WorkflowStateError
	if errors.As(err, &state) {
		WriteError(w, ErrConflict, err.Error(), 409)
		return
	}
	var denied *ghstore.PolicyError
	if errors.As(err, &denied) {
		WriteError(w, ErrForbidden, err.Error(), 403)
		return
	}
	var conflict *ghstore.ConflictError
	if errors.As(err, &conflict) {
		WriteError(w, ErrConflict, err.Error(), 409)
		return
	}
	if errors.Is(err, errWebSessionChanged) {
		WriteError(w, ErrConflict, err.Error(), 409)
		return
	}
	if strings.Contains(err.Error(), "parent relationship creates or enters a cycle") {
		WriteError(w, ErrValidation, err.Error(), 400)
		return
	}
	readError(w, err)
}
func (s *GitHubWriteStore) create(w http.ResponseWriter, r *http.Request) {
	var body IssueCreateBody
	if !decodeGitHubWrite(w, r, &body) {
		return
	}
	min, max := titleLengthLimitsFor(HandlerContext{BaseDir: s.baseDir})
	if errs := ValidateIssueCreate(&body, min, max); len(errs) > 0 {
		WriteValidation(w, errs)
		return
	}
	parent, err := canonicalParent(body.ParentID)
	if err != nil {
		WriteError(w, ErrValidation, err.Error(), 400)
		return
	}
	kind, priority := models.TypeTask, models.PriorityP2
	if body.Type != "" {
		kind = models.NormalizeType(body.Type)
	}
	if body.Priority != "" {
		priority = models.NormalizePriority(body.Priority)
	}
	issue := &models.Issue{Title: body.Title, Description: body.Description, Type: kind, Priority: priority, Points: body.Points, Labels: body.Labels, ParentID: parent, Acceptance: body.Acceptance, Sprint: body.Sprint, Minor: body.Minor, CreatorSession: s.sessionID, CreatedBranch: s.branch}
	if body.DueDate != "" {
		issue.DueDate = &body.DueDate
	}
	if body.DeferUntil != "" {
		issue.DeferUntil = &body.DeferUntil
	}
	client, err := s.open(r.Context())
	if err != nil {
		githubWriteError(w, err)
		return
	}
	result, err := client.Create(r.Context(), issue)
	if err != nil {
		githubWriteError(w, err)
		return
	}
	s.writeIssueSuccess(w, r, client, result, 201, true)
}
func (s *GitHubWriteStore) update(w http.ResponseWriter, r *http.Request) {
	var body IssueUpdateBody
	if !decodeGitHubWrite(w, r, &body) {
		return
	}
	min, max := titleLengthLimitsFor(HandlerContext{BaseDir: s.baseDir})
	if errs := ValidateIssueUpdate(&body, min, max); len(errs) > 0 {
		WriteValidation(w, errs)
		return
	}
	if _, err := ghstore.Number(r.PathValue("id")); err != nil {
		WriteError(w, ErrValidation, err.Error(), 400)
		return
	}
	if body.ParentID != nil {
		parent, err := canonicalParent(*body.ParentID)
		if err != nil {
			WriteError(w, ErrValidation, err.Error(), 400)
			return
		}
		body.ParentID = &parent
	}
	client, err := s.open(r.Context())
	if err != nil {
		githubWriteError(w, err)
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
	changes := ghstore.Changes{Title: body.Title, Description: body.Description, Acceptance: body.Acceptance, Points: body.Points}
	if body.Type != nil && *body.Type != "" {
		kind := models.NormalizeType(*body.Type)
		changes.Type = &kind
	}
	if body.Priority != nil && *body.Priority != "" {
		priority := models.NormalizePriority(*body.Priority)
		changes.Priority = &priority
	}
	if body.Labels != nil {
		changes.Labels = &body.Labels
	}
	if body.ParentID != nil || body.Sprint != nil || body.Minor != nil || body.DueDate != nil || body.DeferUntil != nil {
		details, err := observed.CopyDetails()
		if err != nil {
			githubWriteError(w, err)
			return
		}
		if body.ParentID != nil {
			details.ParentID = *body.ParentID
		}
		if body.Sprint != nil {
			details.Sprint = *body.Sprint
		}
		if body.Minor != nil {
			details.Minor = *body.Minor
		}
		if body.DueDate != nil {
			details.DueDate = body.DueDate
			if *body.DueDate == "" {
				details.DueDate = nil
			}
		}
		if body.DeferUntil != nil {
			details.DeferUntil = body.DeferUntil
			if *body.DeferUntil == "" {
				details.DeferUntil = nil
			}
		}
		changes.Details = &details
	}
	if !changes.HasFields() {
		s.writeIssueSuccess(w, r, client, observed, 200, false)
		return
	}
	result, err := client.UpdateObserved(r.Context(), observed, changes)
	if err != nil {
		githubWriteError(w, err)
		return
	}
	s.writeIssueSuccess(w, r, client, result, 200, true)
}
