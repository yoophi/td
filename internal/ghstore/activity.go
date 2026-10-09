package ghstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

const activityPrefix = "<!-- td:activity:"
const activityStart = activityPrefix + "v1\n"

// Only portable authored fields are stored in the comment. Identity, URL and
// timestamps come from GitHub, so a JSON field cannot impersonate an author.
type activityData struct {
	Kind          string              `json:"kind"`
	OperationID   string              `json:"operation_id"`
	SessionID     string              `json:"session_id"`
	WorkSessionID string              `json:"work_session_id,omitempty"`
	Message       string              `json:"message,omitempty"`
	LogType       models.LogType      `json:"log_type,omitempty"`
	Done          []string            `json:"done,omitempty"`
	Remaining     []string            `json:"remaining,omitempty"`
	Decisions     []string            `json:"decisions,omitempty"`
	Uncertain     []string            `json:"uncertain,omitempty"`
	Snapshot      *models.GitSnapshot `json:"snapshot,omitempty"`
}

type apiComment struct {
	ID        int64     `json:"id"`
	IssueURL  string    `json:"issue_url"`
	Body      string    `json:"body"`
	URL       string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

func validateActivity(a activityData) error {
	if a.SessionID == "" || a.OperationID == "" {
		return fmt.Errorf("activity requires session and operation identity")
	}
	switch a.Kind {
	case "comment", "log":
		if strings.TrimSpace(a.Message) == "" {
			return fmt.Errorf("activity message must not be empty")
		}
		if a.Kind == "log" && !slices.Contains([]models.LogType{models.LogTypeProgress, models.LogTypeBlocker, models.LogTypeDecision, models.LogTypeHypothesis, models.LogTypeTried, models.LogTypeResult, models.LogTypeOrchestration}, a.LogType) {
			return fmt.Errorf("invalid log type %q", a.LogType)
		}
	case "handoff":
		if len(a.Done)+len(a.Remaining)+len(a.Decisions)+len(a.Uncertain) == 0 {
			return fmt.Errorf("handoff must contain at least one item")
		}
		for _, items := range [][]string{a.Done, a.Remaining, a.Decisions, a.Uncertain} {
			for _, item := range items {
				if strings.TrimSpace(item) == "" {
					return fmt.Errorf("handoff items must not be empty")
				}
			}
		}
	default:
		return fmt.Errorf("unknown activity kind %q", a.Kind)
	}
	return nil
}

func renderActivity(a activityData) (string, error) {
	if err := validateActivity(a); err != nil {
		return "", err
	}
	visible := a.Message
	if a.Kind == "handoff" {
		var text strings.Builder
		for _, section := range []struct {
			name  string
			items []string
		}{{"Done", a.Done}, {"Remaining", a.Remaining}, {"Decisions", a.Decisions}, {"Uncertain", a.Uncertain}} {
			if len(section.items) == 0 {
				continue
			}
			fmt.Fprintf(&text, "## %s\n", section.name)
			for _, item := range section.items {
				fmt.Fprintf(&text, "- %s\n", item)
			}
			text.WriteByte('\n')
		}
		visible = strings.TrimSuffix(text.String(), "\n")
	}
	if strings.Contains(visible, activityPrefix) {
		return "", fmt.Errorf("activity text contains reserved td metadata marker")
	}
	data, err := json.Marshal(a)
	if err != nil {
		return "", err
	}
	return visible + "\n\n" + activityStart + string(data) + markerEnd, nil
}

func decodeActivity(comment apiComment, issueID string) (models.Activity, error) {
	result := models.Activity{ID: fmt.Sprintf("ghc-%d", comment.ID), IssueID: issueID, Kind: "comment", Message: comment.Body, Author: comment.User.Login, CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt, URL: comment.URL, Native: true, Edited: comment.UpdatedAt.After(comment.CreatedAt)}
	if comment.ID <= 0 {
		return result, fmt.Errorf("invalid GitHub comment identity")
	}
	index := strings.Index(comment.Body, activityPrefix)
	if index < 0 {
		return result, nil
	}
	block := strings.TrimSpace(comment.Body[index:])
	if strings.Count(comment.Body, activityPrefix) != 1 || !strings.HasPrefix(block, activityStart) || !strings.HasSuffix(block, markerEnd) {
		return result, fmt.Errorf("comment %s has unknown or malformed td activity metadata", result.ID)
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(block, activityStart), markerEnd)
	if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return result, fmt.Errorf("activity metadata must be an object")
	}
	var data activityData
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return result, fmt.Errorf("invalid activity %s: %w", result.ID, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return result, fmt.Errorf("trailing activity metadata")
	}
	if err := validateActivity(data); err != nil {
		return result, fmt.Errorf("invalid activity %s: %w", result.ID, err)
	}
	expected, err := renderActivity(data)
	if err != nil {
		return result, err
	}
	// A web edit to visible text must not silently leave stale hidden records.
	if comment.Body != expected {
		return result, fmt.Errorf("comment %s visible text and td activity metadata disagree; inspect the edited comment", result.ID)
	}
	result.Kind = data.Kind
	result.OperationID = data.OperationID
	result.SessionID = data.SessionID
	result.WorkSessionID = data.WorkSessionID
	result.Message = data.Message
	result.LogType = data.LogType
	result.Done = data.Done
	result.Remaining = data.Remaining
	result.Decisions = data.Decisions
	result.Uncertain = data.Uncertain
	result.Snapshot = data.Snapshot
	result.Native = false
	return result, nil
}

func (c *Client) ListActivity(ctx context.Context, id string) ([]models.Activity, error) {
	return c.listActivity(ctx, id, false)
}

// ListActivityIncludingDeleted is for retained history and aggregate statistics;
// ordinary issue reads and writes continue to reject logically deleted issues.
func (c *Client) ListActivityIncludingDeleted(ctx context.Context, id string) ([]models.Activity, error) {
	return c.listActivity(ctx, id, true)
}

func (c *Client) listActivity(ctx context.Context, id string, includeDeleted bool) ([]models.Activity, error) {
	var issue *Record
	var err error
	if includeDeleted {
		issue, err = c.GetIncludingDeleted(ctx, id)
	} else {
		issue, err = c.Get(ctx, id)
	}
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", fmt.Sprintf("/issues/%d/comments?per_page=100", issue.Number), nil, true)
	if err != nil {
		return nil, err
	}
	var pages [][]apiComment
	if err := json.Unmarshal(data, &pages); err != nil {
		return nil, fmt.Errorf("invalid GitHub comments response: %w", err)
	}
	result := make([]models.Activity, 0)
	seen := map[int64]bool{}
	operations := map[string]string{}
	for _, page := range pages {
		for _, comment := range page {
			if seen[comment.ID] {
				return nil, fmt.Errorf("comment pagination repeated %d; retry the read", comment.ID)
			}
			seen[comment.ID] = true
			activity, err := decodeActivity(comment, issue.ID)
			if err != nil {
				return nil, err
			}
			if activity.OperationID != "" {
				if previous, exists := operations[activity.OperationID]; exists {
					return nil, fmt.Errorf("duplicate activity operation %s in %s and %s; inspect comments before continuing", activity.OperationID, previous, activity.ID)
				}
				operations[activity.OperationID] = activity.ID
			}
			result = append(result, activity)
		}
	}
	return result, nil
}

func (c *Client) AppendActivity(ctx context.Context, id string, input models.Activity) (*models.Activity, error) {
	if input.OperationID == "" {
		input.OperationID = "td-op-" + rand.Text()
	}
	data := activityData{Kind: input.Kind, OperationID: input.OperationID, SessionID: input.SessionID, WorkSessionID: input.WorkSessionID, Message: input.Message, LogType: input.LogType, Done: input.Done, Remaining: input.Remaining, Decisions: input.Decisions, Uncertain: input.Uncertain, Snapshot: input.Snapshot}
	body, err := renderActivity(data)
	if err != nil {
		return nil, err
	}
	issue, err := c.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if data.Snapshot != nil {
		snapshot := *data.Snapshot
		snapshot.IssueID = issue.ID
		data.Snapshot = &snapshot
		body, err = renderActivity(data)
		if err != nil {
			return nil, err
		}
	}
	response, err := c.request(ctx, "POST", fmt.Sprintf("/issues/%d/comments", issue.Number), map[string]string{"body": body}, false)
	if err != nil {
		return nil, fmt.Errorf("%w; operation %s identifies this activity in comments on %s", err, input.OperationID, issue.ID)
	}
	var comment apiComment
	if err := json.Unmarshal(response, &comment); err != nil {
		return nil, fmt.Errorf("activity response unreadable for operation %s; inspect %s before retrying: %w", input.OperationID, issue.ID, err)
	}
	if comment.Body != body {
		return nil, fmt.Errorf("activity write response differs from operation %s; inspect %s before retrying", input.OperationID, issue.ID)
	}
	result, err := decodeActivity(comment, issue.ID)
	if err != nil {
		return nil, fmt.Errorf("activity may have been created for operation %s; inspect %s before retrying: %w", input.OperationID, issue.ID, err)
	}
	return &result, nil
}
