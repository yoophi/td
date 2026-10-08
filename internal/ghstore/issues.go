package ghstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

// Client uses only the gh executable; it never opens the local issue database.
type Client struct {
	stateLabels map[string]bool
	dir, repo   string
	run         func(context.Context, string, []byte, ...string) ([]byte, error)
}

// Open revalidates the selected remote on each invocation. A changed remote must
// be explicitly reconfigured rather than silently directing writes elsewhere.
func Open(ctx context.Context, dir string, cfg *models.GitHubStoreConfig) (*Client, error) {
	if cfg == nil || cfg.Remote == "" || cfg.Repo == "" {
		return nil, fmt.Errorf("gh-issue repository is not configured; run 'td config set store gh-issue'")
	}
	resolved, err := ResolveRepository(ctx, dir, cfg.Remote)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(resolved.Repo, cfg.Repo) {
		return nil, fmt.Errorf("git remote %q now resolves to %s, but the configured store is %s; run 'td config set store gh-issue --remote %s' to select it explicitly", cfg.Remote, resolved.Repo, cfg.Repo, cfg.Remote)
	}
	return &Client{dir: dir, repo: resolved.Repo, run: runAPI}, nil
}

func runAPI(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=true")
	cmd.Stdin = bytes.NewReader(payload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (c *Client) request(ctx context.Context, method, endpoint string, payload any, paginate bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := []string{"api", "--hostname", "github.com", "--method", method, "repos/" + c.repo + endpoint,
		"--header", "Accept: application/vnd.github+json", "--header", "X-GitHub-Api-Version: 2022-11-28"}
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		args = append(args, "--input", "-")
	}
	if paginate {
		args = append(args, "--paginate", "--slurp")
	}
	data, err := c.run(ctx, c.dir, body, args...)
	if err != nil {
		hint := ""
		if method != "GET" {
			hint = "; the write may have reached GitHub: inspect the issue before retrying"
		}
		return nil, fmt.Errorf("gh %s %s failed: %w%s", method, endpoint, err, hint)
	}
	return data, nil
}

// Record exposes td-compatible fields plus the GitHub number and URL.
type Record struct {
	CascadedReviews []Record `json:"cascaded_reviews,omitempty"`
	models.Issue
	StateLabelDiagnostic string `json:"state_label_warning,omitempty"`
	Number               int    `json:"number"`
	URL                  string `json:"url"`
	meta                 metadata
	managed              bool
	revision             [32]byte
	repository           string
	Details              *IssueDetails `json:"details,omitempty"`
}

type apiIssue struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	State     string     `json:"state"`
	URL       string     `json:"html_url"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"`
}

func decodeIssue(data []byte) (*Record, error) {
	var item apiIssue
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, fmt.Errorf("invalid gh issue response: %w", err)
	}
	return item.record()
}

func (item apiIssue) record() (*Record, error) {
	if len(item.PullRequest) > 0 && string(item.PullRequest) != "null" {
		return nil, fmt.Errorf("#%d is a pull request, not an issue", item.Number)
	}
	if item.Number <= 0 || (item.State != "open" && item.State != "closed") {
		return nil, fmt.Errorf("invalid GitHub issue response")
	}
	description, meta, managed, err := decodeBody(item.Body)
	if err != nil {
		return nil, fmt.Errorf("gh-%d: %w", item.Number, err)
	}
	labels := make([]string, 0, len(item.Labels))
	for _, label := range item.Labels {
		labels = append(labels, label.Name)
	}
	snapshot, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	record := &Record{Issue: models.Issue{
		ID: fmt.Sprintf("gh-%d", item.Number), Title: item.Title, Description: description,
		Status: models.Status(item.State), Type: meta.Type, Priority: meta.Priority,
		Points: meta.Points, Acceptance: meta.Acceptance, Labels: labels,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, ClosedAt: item.ClosedAt,
	}, Number: item.Number, URL: item.URL, meta: meta, managed: managed, revision: sha256.Sum256(snapshot), Details: meta.Details}
	if meta.Details != nil {
		meta.Details.apply(&record.Issue)
	}
	record.StateLabelDiagnostic = record.StateLabelWarning()
	return record, nil
}

// Number accepts only repository-local issue numbers, never URLs or td IDs.
func Number(id string) (int, error) {
	value := id
	if strings.HasPrefix(value, "gh-") {
		value = strings.TrimPrefix(value, "gh-")
	} else {
		value = strings.TrimPrefix(value, "#")
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid GitHub issue ID %q (use gh-123, #123 or 123)", id)
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid GitHub issue ID %q (use gh-123, #123 or 123)", id)
	}
	return n, nil
}

func (c *Client) Get(ctx context.Context, id string) (*Record, error) {
	record, err := c.GetIncludingDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	if record.DeletedAt != nil {
		return nil, fmt.Errorf("issue %s is deleted; restore it before use", record.ID)
	}
	return record, nil
}

// GetIncludingDeleted is reserved for restore, history and migration flows.
func (c *Client) GetIncludingDeleted(ctx context.Context, id string) (*Record, error) {
	n, err := Number(id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/%d", n), nil, false)
	if err != nil {
		return nil, err
	}
	record, err := decodeIssue(data)
	if err != nil {
		return nil, err
	}
	if record.Number != n {
		return nil, fmt.Errorf("gh returned issue #%d when #%d was requested", record.Number, n)
	}
	if record.meta.EntityKind != "" && record.meta.EntityKind != "issue" {
		return nil, fmt.Errorf("%s is a td %s entity, not an issue", record.ID, record.meta.EntityKind)
	}
	record.repository = c.repo
	return record, nil
}

// List follows all pages and removes pull requests before decoding td metadata.
// Filtering and limits are applied by the CLI after this complete read.
func (c *Client) List(ctx context.Context, all bool) ([]Record, error) {
	return c.list(ctx, all, false)
}

func (c *Client) ListIncludingDeleted(ctx context.Context, all bool) ([]Record, error) {
	return c.list(ctx, all, true)
}

func (c *Client) list(ctx context.Context, all, includeDeleted bool) ([]Record, error) {
	state := "open"
	if all {
		state = "all"
	}
	data, err := c.request(ctx, "GET", "/issues?state="+state+"&per_page=100", nil, true)
	if err != nil {
		return nil, err
	}
	var pages [][]apiIssue
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid gh issue list: %w", err)
	}
	results := make([]Record, 0)
	for _, page := range pages {
		for _, item := range page {
			if len(item.PullRequest) > 0 && string(item.PullRequest) != "null" {
				continue
			}
			record, err := item.record()
			if err != nil {
				return nil, err
			}
			if record.meta.EntityKind != "" && record.meta.EntityKind != "issue" {
				continue
			}
			if record.DeletedAt != nil && !includeDeleted {
				continue
			}
			record.repository = c.repo
			results = append(results, *record)
		}
	}
	return results, nil
}

func (c *Client) Create(ctx context.Context, issue *models.Issue) (*Record, error) {
	if issue == nil {
		return nil, fmt.Errorf("issue is required")
	}
	if issue.Status != "" && issue.Status != models.StatusOpen {
		return nil, fmt.Errorf("create requires open status; transition after creation")
	}
	if err := validateIssue(issue); err != nil {
		return nil, err
	}
	parents, err := c.parentObservations(ctx, "", issue.ParentID)
	if err != nil {
		return nil, err
	}
	operationID := "td-op-" + rand.Text()
	meta := metadata{OperationID: operationID, Type: issue.Type, Priority: issue.Priority, Points: issue.Points, Acceptance: issue.Acceptance}
	details := detailsFromIssue(issue)
	baseline := IssueDetails{Status: issue.Status}
	detailJSON, _ := json.Marshal(details)
	baselineJSON, _ := json.Marshal(baseline)
	if !bytes.Equal(detailJSON, baselineJSON) || (issue.Status != "" && issue.Status != models.StatusOpen) {
		meta.Details = &details
	}
	body, err := encodeBody(issue.Description, meta)
	if err != nil {
		return nil, err
	}
	labels := mirroredLabels(issue.Labels, models.StatusOpen)
	if err := c.ensureStateLabel(ctx, "td:open"); err != nil {
		return nil, err
	}
	payload := map[string]any{"title": issue.Title, "body": body, "labels": labels}
	if err := c.verifyParentObservations(ctx, "new issue", parents, false); err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "POST", "/issues", payload, false)
	if err != nil {
		return nil, fmt.Errorf("%w; operation %s is embedded in the issue body if created; inspect recent issues before retrying (search indexing can lag)", err, operationID)
	}
	record, err := decodeIssue(data)
	if err != nil {
		return nil, fmt.Errorf("create response unreadable; inspect GitHub for operation %s before retrying: %w", operationID, err)
	}
	record.repository = c.repo
	var returned apiIssue
	if err := json.Unmarshal(data, &returned); err != nil {
		return nil, err
	}
	if returned.Title != issue.Title || returned.Body != body || returned.State != "open" {
		return nil, fmt.Errorf("%s was created, but returned fields differ from operation %s; inspect %s before retrying", record.ID, operationID, record.URL)
	}
	if err := checkLabels(record, labels, "created"); err != nil {
		return nil, err
	}
	if warning := record.StateLabelWarning(); warning != "" {
		return nil, fmt.Errorf("issue %s was created, but %s", record.ID, warning)
	}
	if err := c.verifyParentObservations(ctx, record.ID, parents, true); err != nil {
		return nil, fmt.Errorf("%s was created, but its parent hierarchy changed; inspect %s before retrying: %w", record.ID, record.URL, err)
	}
	return record, nil
}

// Changes uses pointers so omitted fields, empty strings and cleared labels differ.
type Changes struct {
	ParentID                       *string
	Details                        *IssueDetails
	Minor                          *bool
	Sprint                         *string
	Title, Description, Acceptance *string
	Type                           *models.Type
	Priority                       *models.Priority
	Points                         *int
	Labels                         *[]string
	Status                         *models.Status
	Reason                         *string
	Append                         bool
}

func (c *Client) Update(ctx context.Context, id string, change Changes) (*Record, error) {
	record, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.UpdateObserved(ctx, record, change)
}

// UpdateObserved checks the same revision the caller used to validate policy.
// It cannot make GitHub PATCH atomic, but never refreshes away a known stale
// policy decision. Callers must not edit observed before passing it back.
func (c *Client) UpdateObserved(ctx context.Context, observed *Record, change Changes) (*Record, error) {
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, fmt.Errorf("an observed issue from this repository is required")
	}
	if n, err := Number(observed.ID); err != nil || n != observed.Number {
		return nil, fmt.Errorf("observed issue identity is inconsistent")
	}
	copy := *observed
	record := &copy
	id := record.ID
	var err error
	if change.Details != nil {
		if change.Minor != nil || change.Sprint != nil || change.ParentID != nil {
			return nil, fmt.Errorf("specify full details or individual detail fields, not both")
		}
		if err := change.Details.validate(); err != nil {
			return nil, err
		}
	}
	parents := []Record{}
	var newParent *string
	if change.ParentID != nil {
		newParent = change.ParentID
	} else if change.Details != nil && change.Details.ParentID != record.ParentID {
		newParent = &change.Details.ParentID
	}
	if newParent != nil {
		parents, err = c.parentObservations(ctx, record.ID, *newParent)
		if err != nil {
			return nil, err
		}
	}
	// Legacy/native state writes must not resurrect an old detailed claim or
	// masquerade as reviewed closes. Policy-aware transitions supply Details.
	if change.Status != nil && change.Details == nil && record.Details != nil {
		details, copyErr := record.CopyDetails()
		if copyErr != nil {
			return nil, copyErr
		}
		details.Status = *change.Status
		details.ReviewerSession = ""
		details.ReviewedAt = nil
		details.ReviewRequestedBySession = ""
		details.ClosedBySession = ""
		if *change.Status == models.StatusOpen {
			details.ImplementerSession = ""
		}
		now := time.Now().UTC()
		for i := range details.Reviews {
			if details.Reviews[i].SupersededAt == nil {
				details.Reviews[i].SupersededAt = &now
			}
		}
		change.Details = &details
	}
	payload := make(map[string]any)
	if change.Title != nil {
		record.Title = *change.Title
		payload["title"] = record.Title
	}
	if change.Description != nil {
		if change.Append && record.Description != "" {
			record.Description += "\n\n" + *change.Description
		} else {
			record.Description = *change.Description
		}
	}
	if change.Acceptance != nil {
		if change.Append && record.Acceptance != "" {
			record.Acceptance += "\n\n" + *change.Acceptance
		} else {
			record.Acceptance = *change.Acceptance
		}
	}
	if change.Type != nil {
		record.Type = *change.Type
	}
	if change.Priority != nil {
		record.Priority = *change.Priority
	}
	if change.Points != nil {
		record.Points = *change.Points
	}
	if change.Labels != nil {
		labels := append([]string{}, (*change.Labels)...)
		payload["labels"] = labels
	}
	if change.Status != nil {
		if *change.Status != models.StatusOpen && *change.Status != models.StatusClosed {
			return nil, fmt.Errorf("gh-issue supports only open and closed states; session/review workflows are not supported yet")
		}
		payload["state"] = *change.Status
	}
	if err := validateIssue(&record.Issue); err != nil {
		return nil, err
	}
	metadataChanged := change.Details != nil || change.ParentID != nil || change.Minor != nil || change.Sprint != nil || change.Acceptance != nil || change.Type != nil || change.Priority != nil || change.Points != nil || change.Reason != nil
	if change.Description != nil || metadataChanged {
		body := record.Description
		if record.managed || metadataChanged {
			meta := record.meta
			if change.Details != nil {
				meta.Details = change.Details
			}
			if change.Minor != nil || change.Sprint != nil || change.ParentID != nil {
				details := detailsFromIssue(&record.Issue)
				if meta.Details != nil {
					details = *meta.Details
				}
				if change.Minor != nil {
					details.Minor = *change.Minor
				}
				if change.Sprint != nil {
					details.Sprint = *change.Sprint
				}
				if change.ParentID != nil {
					details.ParentID = *change.ParentID
				}
				meta.Details = &details
			}
			meta.Type, meta.Priority, meta.Points, meta.Acceptance = record.Type, record.Priority, record.Points, record.Acceptance
			if change.Reason != nil {
				meta.LastStateReason = *change.Reason
			}
			body, err = encodeBody(record.Description, meta)
			if err != nil {
				return nil, err
			}
		} else if strings.Contains(body, markerPrefix) {
			return nil, fmt.Errorf("description contains reserved td metadata marker")
		}
		payload["body"] = body
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("no issue changes specified")
	}
	// Preserve unrelated labels and mirror the effective metadata/native state
	// in the same PATCH as the workflow write. Labels are never read as state.
	if record.managed || metadataChanged || change.Status != nil {
		status := record.Status
		if change.Status != nil {
			status = *change.Status
		}
		if change.Details != nil && change.Details.Status != "" && nativeStatus(change.Details.Status) == nativeStatus(status) {
			status = change.Details.Status
		}
		base := record.Labels
		if change.Labels != nil {
			base = *change.Labels
		}
		labels := mirroredLabels(base, status)
		if !sameLabels(labels, record.Labels) || change.Labels != nil {
			if err := c.ensureStateLabel(ctx, "td:"+string(status)); err != nil {
				return nil, err
			}
			payload["labels"] = labels
			change.Labels = &labels
		}
	}
	// This is best-effort detection, not conditional PATCH or a distributed lock.
	latest, err := c.GetIncludingDeleted(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("verify %s before update (no write attempted): %w", record.ID, err)
	}
	if latest.revision != record.revision {
		return nil, &ConflictError{ID: record.ID}
	}
	if err := c.verifyParentObservations(ctx, record.ID, parents, false); err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "PATCH", fmt.Sprintf("/issues/%d", record.Number), payload, false)
	if err != nil {
		return nil, err
	}
	updated, err := decodeIssue(data)
	if err != nil {
		return nil, fmt.Errorf("update response unreadable; inspect GitHub before retrying: %w", err)
	}
	if updated.Number != record.Number {
		return nil, fmt.Errorf("update response returned unexpected issue; inspect %s before retrying", record.ID)
	}
	var applied map[string]json.RawMessage
	if err := json.Unmarshal(data, &applied); err != nil {
		return nil, err
	}
	for _, field := range []string{"title", "body", "state"} {
		if expected, ok := payload[field]; ok {
			var actual string
			if json.Unmarshal(applied[field], &actual) != nil || actual != fmt.Sprint(expected) {
				return nil, fmt.Errorf("%s update was accepted but %s did not match the requested value; inspect GitHub before retrying", record.ID, field)
			}
		}
	}
	if change.Labels != nil {
		if err := checkLabels(updated, *change.Labels, "updated"); err != nil {
			return nil, err
		}
	}
	verified, err := c.GetIncludingDeleted(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%s update was accepted, but verification failed; inspect GitHub before retrying: %w", record.ID, err)
	}
	if verified.revision != updated.revision {
		return nil, &ConflictError{ID: record.ID, AfterWrite: true}
	}
	updated.repository = c.repo
	if err := c.verifyParentObservations(ctx, record.ID, parents, true); err != nil {
		return nil, err
	}
	return updated, nil
}

// GitHub may ignore label changes when the actor lacks permission. Report the
// issue ID on partial success so callers do not blindly retry issue creation.
func checkLabels(record *Record, requested []string, action string) error {
	names := func(labels []string) []string {
		out := make([]string, len(labels))
		for i, label := range labels {
			out[i] = strings.ToLower(label)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	actual, expected := names(record.Labels), names(requested)
	matched := slices.Equal(actual, expected)
	if action == "created" {
		matched = true
		for _, name := range expected {
			if !slices.Contains(actual, name) {
				matched = false
			}
		}
	}
	if !matched {
		return fmt.Errorf("%s was %s, but GitHub did not apply the requested labels; check label permissions and inspect %s before retrying", record.ID, action, record.URL)
	}
	return nil
}

func validateIssue(issue *models.Issue) error {
	if strings.TrimSpace(issue.Title) == "" {
		return fmt.Errorf("issue title is required")
	}
	if !models.IsValidType(issue.Type) {
		return fmt.Errorf("invalid issue type %q", issue.Type)
	}
	if !models.IsValidPriority(issue.Priority) {
		return fmt.Errorf("invalid priority %q", issue.Priority)
	}
	if issue.Points != 0 && !models.IsValidPoints(issue.Points) {
		return fmt.Errorf("invalid points %d (use 0 or Fibonacci points)", issue.Points)
	}
	return nil
}
