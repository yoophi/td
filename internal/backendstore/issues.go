package backendstore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
)

// Issue preserves the observed revision alongside the shared CLI model.
// Nullable API strings decode to the model's empty strings; timestamps retain UTC.
type Issue struct {
	models.Issue
	Revision             string          `json:"revision"`
	Reviews              json.RawMessage `json:"reviews,omitempty"`
	ActiveReview         json.RawMessage `json:"active_review,omitempty"`
	AvailableTransitions []string        `json:"available_transitions,omitempty"`
	ReviewOptions        *ReviewOptions  `json:"review_options,omitempty"`
}

type ReviewOptions struct {
	SelfReviewPrompt      bool `json:"self_review_prompt"`
	RecordReviewAvailable bool `json:"record_review_available"`
}

var issueIDPattern = regexp.MustCompile(`^td-[0-9a-f]{16}$`)
var revisionPattern = regexp.MustCompile(`^r-[1-9][0-9]*$`)

func scopedPath(project, session string) (string, error) {
	if !config.ValidBackendProjectID(project) || !validSessionID(session) {
		return "", fmt.Errorf("invalid backend project or session ID")
	}
	return "/v1/projects/" + project, nil
}
func validSessionID(id string) bool {
	return len(id) > 4 && id[:4] == "ses_" && config.ValidBackendProjectID("prj_"+id[4:])
}
func ValidIssueID(id string) bool { return issueIDPattern.MatchString(id) }
func ValidateIssue(i Issue) error {
	if !issueIDPattern.MatchString(i.ID) || !revisionPattern.MatchString(i.Revision) {
		return fmt.Errorf("invalid backend issue identity or revision")
	}
	return nil
}
func (c *Client) GetIssue(ctx context.Context, project, session, id string) (Issue, error) {
	return c.getIssue(ctx, project, session, id, false)
}

func (c *Client) GetIssueIncludingDeleted(ctx context.Context, project, session, id string) (Issue, error) {
	return c.getIssue(ctx, project, session, id, true)
}

func (c *Client) getIssue(ctx context.Context, project, session, id string, withDeleted bool) (Issue, error) {
	var out struct {
		Issue Issue `json:"issue"`
	}
	path, e := scopedPath(project, session)
	if e != nil {
		return out.Issue, e
	}
	if !issueIDPattern.MatchString(id) {
		return out.Issue, fmt.Errorf("invalid backend issue ID")
	}
	resource := path + "/issues/" + id
	if withDeleted {
		resource += "?with_deleted=true"
	}
	e = c.request(ctx, "GET", resource, session, "", nil, &out)
	if e == nil {
		e = ValidateIssue(out.Issue)
	}
	if e == nil && out.Issue.ID != id {
		e = fmt.Errorf("backend returned a different issue")
	}
	return out.Issue, e
}

// ListIssues collects an entire consistent snapshot, restarting from offset zero
// when a later page observes a different opaque change token. Never return a
// partial or mixed snapshot as a successful complete Reader result.
func (c *Client) ListIssues(ctx context.Context, project, session string, all bool) ([]Issue, error) {
	return c.listIssues(ctx, project, session, all, false)
}

func (c *Client) ListIssuesIncludingDeleted(ctx context.Context, project, session string) ([]Issue, error) {
	return c.listIssues(ctx, project, session, true, true)
}

func (c *Client) listIssues(ctx context.Context, project, session string, all, withDeleted bool) ([]Issue, error) {
	path, e := scopedPath(project, session)
	if e != nil {
		return nil, e
	}
	for attempt := 0; attempt < 3; attempt++ {
		issues := []Issue{}
		seen := map[string]bool{}
		token, total := "", -1
		restart := false
		for offset := 0; ; {
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			values := url.Values{"limit": {"1000"}, "offset": {strconv.Itoa(offset)}, "include_closed": {strconv.FormatBool(all)}, "sort": {"id"}, "order": {"asc"}}
			if withDeleted {
				values.Set("with_deleted", "true")
			}
			var page struct {
				Issues      []Issue `json:"issues"`
				Total       int     `json:"total"`
				Limit       int     `json:"limit"`
				Offset      int     `json:"offset"`
				HasMore     bool    `json:"has_more"`
				ChangeToken string  `json:"change_token"`
			}
			if e = c.request(ctx, "GET", path+"/issues?"+values.Encode(), session, "", nil, &page); e != nil {
				return nil, e
			}
			if page.ChangeToken == "" {
				return nil, fmt.Errorf("backend page has no change token")
			}
			if token != "" && token != page.ChangeToken {
				restart = true
				break
			}
			if token == "" {
				token, total = page.ChangeToken, page.Total
			}
			if page.Offset != offset || page.Limit < 1 || page.Limit > 1000 || page.Total != total || total < offset || len(page.Issues) > page.Limit || len(page.Issues) != min(page.Limit, total-offset) || page.HasMore != (offset+len(page.Issues) < total) {
				return nil, fmt.Errorf("invalid backend pagination metadata")
			}
			for _, i := range page.Issues {
				if e := ValidateIssue(i); e != nil {
					return nil, e
				}
				if seen[i.ID] {
					return nil, fmt.Errorf("duplicate issue in backend snapshot")
				}
				seen[i.ID] = true
				issues = append(issues, i)
			}
			if !page.HasMore {
				return issues, nil
			}
			offset += len(page.Issues)
		}
		if !restart {
			break
		}
	}
	return nil, fmt.Errorf("backend snapshot changed repeatedly; retry the read")
}

func validateIssue(i Issue) error { return ValidateIssue(i) }
