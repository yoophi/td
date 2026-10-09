package ghstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var collaboratorLoginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*(\[bot\])?$`)

// Custom role names are forwarded only with an explicit namespace choice.
func ValidateCollaboratorPermission(permission string, custom bool) error {
	if custom {
		if permission == "" || strings.TrimSpace(permission) != permission || strings.ContainsFunc(permission, unicode.IsControl) {
			return fmt.Errorf("an explicit nonempty GitHub custom role name is required")
		}
		return nil
	}
	switch permission {
	case "pull", "triage", "triage_plus", "push", "maintain", "admin":
		return nil
	default:
		return fmt.Errorf("use an explicit GitHub permission (pull, triage, triage_plus, push, maintain, admin), or --custom-role for an exact custom role name; td-sync roles are not converted")
	}
}

type CollaboratorGrantResult struct {
	Outcome             string `json:"outcome"`
	RequestedPermission string `json:"requested_permission"`
	InvitationID        int64  `json:"invitation_id,omitempty"`
}

// GrantCollaboratorPermission never retries. Existing-only changes check before
// PUT and detect invitation creation afterwards, but cannot prevent that race.
func (c *Client) GrantCollaboratorPermission(ctx context.Context, login, permission string, custom, existingOnly bool) (*CollaboratorGrantResult, error) {
	if err := ValidateCollaboratorLogin(login); err != nil {
		return nil, err
	}
	if err := ValidateCollaboratorPermission(permission, custom); err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", "", nil, false)
	if err != nil {
		return nil, err
	}
	var repo struct {
		FullName string `json:"full_name"`
		Owner    struct {
			Type string `json:"type"`
		} `json:"owner"`
	}
	if err := json.Unmarshal(data, &repo); err != nil || !strings.EqualFold(repo.FullName, c.repo) {
		return nil, fmt.Errorf("invalid GitHub repository ownership response; no permission write attempted")
	}
	switch repo.Owner.Type {
	case "Organization":
	case "User":
		if custom || permission != "push" {
			return nil, fmt.Errorf("GitHub API permission selection is organization-only; personal repositories support only the default push request here; no write attempted")
		}
	default:
		return nil, fmt.Errorf("GitHub repository owner type is unknown; no permission write attempted")
	}
	endpoint := "/collaborators/" + login
	if existingOnly {
		data, err := c.request(ctx, "GET", endpoint, nil, false)
		if err != nil {
			return nil, fmt.Errorf("existing GitHub collaborator verification failed; no permission write attempted: %w", err)
		}
		if len(bytes.TrimSpace(data)) != 0 {
			return nil, fmt.Errorf("unexpected GitHub collaborator verification response; no permission write attempted")
		}
	}
	data, err = c.request(ctx, "PUT", endpoint, map[string]string{"permission": permission}, false)
	if err != nil {
		return nil, err
	}
	result := &CollaboratorGrantResult{RequestedPermission: permission, Outcome: "assignment-accepted"}
	if len(bytes.TrimSpace(data)) == 0 {
		return result, nil
	}
	var invitation struct {
		ID      int64 `json:"id"`
		Invitee struct {
			Login string `json:"login"`
		} `json:"invitee"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(data, &invitation); err != nil || invitation.ID <= 0 || !strings.EqualFold(invitation.Invitee.Login, login) || !strings.EqualFold(invitation.Repository.FullName, c.repo) {
		return nil, fmt.Errorf("invalid GitHub invitation response after permission write; inspect repository/access state before retrying")
	}
	if existingOnly {
		return nil, fmt.Errorf("GitHub created invitation %d instead of updating an existing collaborator; inspect/cancel it explicitly before retrying: %w", invitation.ID, &ConflictError{ID: c.repo + endpoint, AfterWrite: true})
	}
	result.Outcome = "invitation-created"
	result.InvitationID = invitation.ID
	return result, nil
}

// ValidateCollaboratorLogin accepts GitHub handles, not emails, paths or td-sync
// identifiers. A numeric handle is used literally, never resolved as a user ID.
func ValidateCollaboratorLogin(login string) error {
	if !collaboratorLoginPattern.MatchString(login) {
		return fmt.Errorf("a GitHub login is required (not an email, URL, or td-sync user ID)")
	}
	return nil
}

// RemoveCollaborator removes only the repository collaborator grant. Other
// grants may remain and GitHub may apply related side effects asynchronously.
func (c *Client) RemoveCollaborator(ctx context.Context, login string) error {
	if err := ValidateCollaboratorLogin(login); err != nil {
		return err
	}
	data, err := c.request(ctx, "DELETE", "/collaborators/"+login, nil, false)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) != 0 {
		return fmt.Errorf("unexpected GitHub collaborator removal response; the write may have applied: inspect repository/access state before retrying")
	}
	return nil
}

// Collaborator preserves GitHub roles and effective base permissions. These
// are not td-sync owner/writer/reader roles or evidence of a direct grant.
type Collaborator struct {
	ID          int64           `json:"id"`
	Login       string          `json:"login"`
	RoleName    string          `json:"role_name,omitempty"`
	Permissions map[string]bool `json:"permissions,omitempty"`
}

// Collaborators uses one paginated collection, never one request per account.
func (c *Client) Collaborators(ctx context.Context) ([]Collaborator, error) {
	data, err := c.request(ctx, "GET", "/collaborators?affiliation=all&per_page=100", nil, true)
	if err != nil {
		return nil, fmt.Errorf("list GitHub collaborators: %w; this operation requires collaborator-list access, which may exceed issue access", err)
	}
	var pages []json.RawMessage
	if err := json.Unmarshal(data, &pages); err != nil || pages == nil {
		return nil, fmt.Errorf("invalid gh collaborator pages")
	}
	result := []Collaborator{}
	for _, page := range pages {
		var members []Collaborator
		if err := json.Unmarshal(page, &members); err != nil || members == nil {
			return nil, fmt.Errorf("invalid gh collaborator page")
		}
		for _, member := range members {
			if member.ID <= 0 || member.Login == "" {
				return nil, fmt.Errorf("invalid gh collaborator identity")
			}
			result = append(result, member)
		}
	}
	return result, nil
}
