package serve

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/issuestore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/query"
)

type githubReadClient interface {
	issuestore.ActivityStore
	Get(context.Context, string) (*ghstore.Record, error)
	List(context.Context, bool) ([]ghstore.Record, error)
}
type GitHubReadStore struct {
	open func(context.Context) (githubReadClient, error)
}

func NewGitHubReadStore(dir string, selected models.GitHubStoreConfig) *GitHubReadStore {
	return &GitHubReadStore{open: func(ctx context.Context) (githubReadClient, error) { return openSelectedGitHub(ctx, dir, selected) }}
}
func (s *Server) EnableGitHubReads(store *GitHubReadStore) {
	s.githubEndpoints = append(s.githubEndpoints, "GET /v1/issues", "GET /v1/issues/{id}")
	s.mux.HandleFunc("GET /v1/issues", func(w http.ResponseWriter, r *http.Request) { store.list(s.sessionID, w, r) })
	s.mux.HandleFunc("GET /v1/issues/{id}", func(w http.ResponseWriter, r *http.Request) {
		store.detail(s.baseDir, s.sessionID, slices.Contains(s.githubEndpoints, "POST /v1/issues/{id}/start"), w, r)
	})
}
func readError(w http.ResponseWriter, err error) {
	code, status := "store_error", http.StatusBadGateway
	if strings.Contains(err.Error(), "HTTP 404") || strings.Contains(err.Error(), " is deleted; restore it before use") {
		code, status = ErrNotFound, http.StatusNotFound
	}
	WriteError(w, code, err.Error(), status)
}
func (s *GitHubReadStore) snapshot(ctx context.Context) ([]ghstore.Record, *issuestore.GitHubQuerySnapshot, error) {
	c, err := s.open(ctx)
	if err != nil {
		return nil, nil, err
	}
	records, err := c.List(ctx, true)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := issuestore.NewGitHubQuerySnapshot(ctx, records, c)
	return records, snapshot, err
}
func (s *GitHubReadStore) list(sessionID string, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for key := range q {
		if !slices.Contains([]string{"limit", "offset", "status", "type", "priority", "labels", "label", "epic", "epic_id", "search", "search_mode", "include_closed", "sort", "order"}, key) {
			WriteError(w, ErrValidation, "unsupported query parameter: "+key, http.StatusBadRequest)
			return
		}
	}

	for _, v := range parseStringParams(q["status"]) {
		if !models.IsValidStatus(models.NormalizeStatus(v)) {
			WriteError(w, ErrValidation, "invalid status: "+v, 400)
			return
		}
	}
	for _, v := range parseStringParams(q["type"]) {
		if !models.IsValidType(models.NormalizeType(v)) {
			WriteError(w, ErrValidation, "invalid type: "+v, 400)
			return
		}
	}
	for _, v := range q["priority"] {
		if !models.IsValidPriority(models.Priority(v)) {
			WriteError(w, ErrValidation, "invalid priority: "+v, 400)
			return
		}
	}
	if q.Has("include_closed") && !slices.Contains([]string{"true", "false"}, q.Get("include_closed")) {
		WriteError(w, ErrValidation, "include_closed must be true or false", 400)
		return
	}
	if q.Has("sort") && !slices.Contains([]string{"priority", "created", "updated", "id", "title", "status", "type", "points"}, q.Get("sort")) {
		WriteError(w, ErrValidation, "unsupported sort", 400)
		return
	}
	if q.Has("order") && !slices.Contains([]string{"asc", "desc"}, q.Get("order")) {
		WriteError(w, ErrValidation, "order must be asc or desc", 400)
		return
	}
	limit, offset := 200, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if q.Has(key) {
			v, err := strconv.Atoi(q.Get(key))
			if err != nil {
				WriteError(w, ErrValidation, "invalid "+key, http.StatusBadRequest)
				return
			}
			*target = v
		}
	}
	if errs := ValidatePagination(limit, offset); len(errs) > 0 {
		WriteValidation(w, errs)
		return
	}
	records, snapshot, err := s.snapshot(r.Context())
	if err != nil {
		readError(w, err)
		return
	}
	issues := make([]models.Issue, 0, len(records))
	for _, v := range records {
		issues = append(issues, v.Issue)
	}
	search, mode := q.Get("search"), q.Get("search_mode")
	if !slices.Contains([]string{"", "auto", "text", "tdq"}, mode) {
		WriteError(w, ErrValidation, "invalid search_mode", 400)
		return
	}
	sortCol, sortDesc := resolveSortOptions(q.Get("sort"), q.Get("order"))
	usedTDQ := false
	if search != "" && mode != "text" {
		parsed, parseErr := query.Parse(search)
		if parseErr == nil && len(parsed.Validate()) == 0 {
			result, err := query.ExecuteDetailed(snapshot, search, sessionID, query.ExecuteOptions{MaxResults: len(records) + 1, SortBy: sortCol, SortDesc: sortDesc})
			if err != nil {
				readError(w, err)
				return
			}
			issues = result.Issues
			usedTDQ = true
		} else if mode == "tdq" {
			WriteError(w, ErrValidation, "invalid TDQ query", 400)
			return
		}
	}
	if !usedTDQ && search != "" {
		out := []models.Issue{}
		for _, v := range issues {
			if strings.Contains(strings.ToLower(v.Title+"\n"+v.Description), strings.ToLower(search)) {
				out = append(out, v)
			}
		}
		issues = out
	}
	statuses := parseStatusParams(q["status"])
	filtered := []models.Issue{}
	for _, v := range issues {
		if len(statuses) > 0 {
			if !slices.Contains(statuses, v.Status) {
				continue
			}
		} else if q.Get("include_closed") != "true" && v.Status == models.StatusClosed {
			continue
		}
		filtered = append(filtered, v)
	}
	issues = filterIssues(filtered, parseTypeParams(q["type"]), q["priority"])
	labels := parseStringParams(q["labels"])
	if len(labels) == 0 {
		labels = parseStringParams(q["label"])
	}
	issues = filterByLabels(issues, labels)
	epic := q.Get("epic")
	if epic == "" {
		epic = q.Get("epic_id")
	}
	if epic != "" {
		parent, err := snapshot.GetIssue(epic)
		if err != nil {
			WriteError(w, ErrValidation, err.Error(), 400)
			return
		}
		out := []models.Issue{}
		for _, v := range issues {
			ancestor := v.ParentID
			seen := map[string]bool{v.ID: true}
			for ancestor != "" {
				if seen[ancestor] {
					readError(w, fmt.Errorf("parent cycle at %s", ancestor))
					return
				}
				seen[ancestor] = true
				if ancestor == parent.ID {
					out = append(out, v)
					break
				}
				a, err := snapshot.GetIssue(ancestor)
				if err != nil {
					break
				}
				ancestor = a.ParentID
			}
		}
		issues = out
	}
	if !usedTDQ {
		if err := issuestore.SortGitHubIssues(issues, sortCol, sortDesc); err != nil {
			readError(w, err)
			return
		}
	}
	total := len(issues)
	paged := applyPagination(issues, offset, limit)
	dtos := make([]IssueDTO, 0, len(paged))
	for _, v := range paged {
		dto := IssueToDTO(&v).slimForBoard()
		deps, _ := snapshot.GetDependencies(v.ID)
		refs := []BlockerRefDTO{}
		for _, id := range deps {
			target, err := snapshot.GetIssue(id)
			if err == nil && target.Status != models.StatusClosed {
				refs = append(refs, BlockerRefDTO{DepID: db.DependencyID(v.ID, id, "depends_on"), IssueID: id, Title: target.Title, Status: string(target.Status), RelationType: "depends_on"})
			}
		}
		if len(refs) > 0 {
			dto.DependencySummary = &DependencySummaryDTO{Blockers: refs}
		}
		dtos = append(dtos, dto)
	}
	WriteSuccess(w, map[string]any{"issues": dtos, "total": total, "limit": limit, "offset": offset, "has_more": offset+limit < total}, 200)
}
func (s *GitHubReadStore) detail(baseDir, sessionID string, withWorkflow bool, w http.ResponseWriter, r *http.Request) {
	for key, values := range r.URL.Query() {
		if key != "with" {
			WriteError(w, ErrValidation, "unsupported query parameter: "+key, 400)
			return
		}
		for _, value := range values {
			for _, part := range strings.Split(value, ",") {
				if part != "reviews" {
					WriteError(w, ErrValidation, "unsupported detail expansion: "+part, 400)
					return
				}
			}
		}
	}
	if _, err := ghstore.Number(r.PathValue("id")); err != nil {
		WriteError(w, ErrValidation, err.Error(), 400)
		return
	}
	c, err := s.open(r.Context())
	if err != nil {
		readError(w, err)
		return
	}
	record, err := c.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		readError(w, err)
		return
	}
	records, err := c.List(r.Context(), true)
	if err != nil {
		readError(w, err)
		return
	}
	// Use the explicitly fetched root; other issues are an independent listing.
	found := false
	for i, v := range records {
		if v.ID == record.ID {
			records[i] = *record
			found = true
		}
	}
	if !found {
		// GitHub's listing can lag a newly created issue even when its direct
		// GET succeeds. Keep that explicit observation; availability re-reads
		// it before returning. The wider relationship listing is not atomic.
		records = append(records, *record)
	}
	snapshot, err := issuestore.NewGitHubQuerySnapshot(r.Context(), records, c)
	if err != nil {
		readError(w, err)
		return
	}
	logs, err := snapshot.GetLogs(record.ID, 0)
	if err != nil {
		readError(w, err)
		return
	}
	comments, err := snapshot.GetComments(record.ID)
	if err != nil {
		readError(w, err)
		return
	}
	handoff, err := snapshot.GetLatestHandoff(record.ID)
	if err != nil {
		readError(w, err)
		return
	}
	var hd *HandoffDTO
	if handoff != nil {
		v := HandoffToDTO(handoff)
		hd = &v
	}
	children := []IssueDTO{}
	deps := []DependencyDTO{}
	incoming := []DependencyDTO{}
	edge := func(from, to string) DependencyDTO {
		return DependencyDTO{DepID: db.DependencyID(from, to, "depends_on"), IssueID: from, DependsOnID: to, RelationType: "depends_on"}
	}
	for _, v := range records {
		if v.ParentID == record.ID {
			children = append(children, IssueToDTO(&v.Issue))
		}
		if v.Details != nil {
			for _, dep := range v.Details.Dependencies {
				if v.ID == record.ID {
					deps = append(deps, edge(v.ID, dep))
				}
				if dep == record.ID {
					incoming = append(incoming, edge(v.ID, dep))
				}
			}
		}
	}
	dto, err := githubIssueDTO(r.Context(), c, record, baseDir, sessionID, withWorkflow)
	if err != nil {
		githubWriteError(w, err)
		return
	}
	if record.Details != nil {
		for _, review := range record.Details.Reviews {
			if hasWithValue(r.URL.Query().Get("with"), "reviews") {
				dto.Reviews = append(dto.Reviews, IssueReviewToDTO(&review))
			}
			if review.SupersededAt == nil && review.Decision == "approved" && record.ReviewedAt != nil && record.ReviewerSession == review.ReviewerSession {
				dto.ActiveReview = &IssueReviewSummary{ID: review.ID, Decision: review.Decision, ReviewerSession: review.ReviewerSession, RequestedBySession: review.RequestedBySession, Summary: review.Summary, CreatedAt: review.CreatedAt, SelfReview: review.SelfReview, ReviewedBy: review.ReviewedBy}
			}
		}
	}
	WriteSuccess(w, map[string]any{"issue": dto, "logs": logsToDTOsNonNil(logs), "comments": commentsToDTOsNonNil(comments), "latest_handoff": hd, "children": children, "dependencies": deps, "blocked_by": incoming}, 200)
}
