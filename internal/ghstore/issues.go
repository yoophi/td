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
	dir, repo string
	run       func(context.Context, string, []byte, ...string) ([]byte, error)
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
	models.Issue
	Number   int    `json:"number"`
	URL      string `json:"url"`
	meta     metadata
	managed  bool
	revision [32]byte
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
	return &Record{Issue: models.Issue{
		ID: fmt.Sprintf("gh-%d", item.Number), Title: item.Title, Description: description,
		Status: models.Status(item.State), Type: meta.Type, Priority: meta.Priority,
		Points: meta.Points, Acceptance: meta.Acceptance, Labels: labels,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, ClosedAt: item.ClosedAt,
	}, Number: item.Number, URL: item.URL, meta: meta, managed: managed, revision: sha256.Sum256(snapshot)}, nil
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
	return record, nil
}

// List follows all pages and removes pull requests before decoding td metadata.
// Filtering and limits are applied by the CLI after this complete read.
func (c *Client) List(ctx context.Context, all bool) ([]Record, error) {
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
			results = append(results, *record)
		}
	}
	return results, nil
}

func (c *Client) Create(ctx context.Context, issue *models.Issue) (*Record, error) {
	if err := validateIssue(issue); err != nil {
		return nil, err
	}
	operationID := "td-op-" + rand.Text()
	body, err := encodeBody(issue.Description, metadata{OperationID: operationID, Type: issue.Type, Priority: issue.Priority, Points: issue.Points, Acceptance: issue.Acceptance})
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"title": issue.Title, "body": body}
	if len(issue.Labels) > 0 {
		payload["labels"] = issue.Labels
	}
	data, err := c.request(ctx, "POST", "/issues", payload, false)
	if err != nil {
		return nil, fmt.Errorf("%w; operation %s is embedded in the issue body if created; inspect recent issues before retrying (search indexing can lag)", err, operationID)
	}
	record, err := decodeIssue(data)
	if err != nil {
		return nil, fmt.Errorf("create response unreadable; inspect GitHub for operation %s before retrying: %w", operationID, err)
	}
	if err := checkLabels(record, issue.Labels, "created"); err != nil {
		return nil, err
	}
	return record, nil
}

// Changes uses pointers so omitted fields, empty strings and cleared labels differ.
type Changes struct {
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
	metadataChanged := change.Acceptance != nil || change.Type != nil || change.Priority != nil || change.Points != nil || change.Reason != nil
	if change.Description != nil || metadataChanged {
		body := record.Description
		if record.managed || metadataChanged {
			meta := record.meta
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
	// This is best-effort detection, not conditional PATCH or a distributed lock.
	latest, err := c.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("verify %s before update (no write attempted): %w", record.ID, err)
	}
	if latest.revision != record.revision {
		return nil, &ConflictError{ID: record.ID}
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
	observed, err := c.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%s update was accepted, but verification failed; inspect GitHub before retrying: %w", record.ID, err)
	}
	if observed.revision != updated.revision {
		return nil, &ConflictError{ID: record.ID, AfterWrite: true}
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
