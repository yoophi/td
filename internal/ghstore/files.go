package ghstore

import (
	"context"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

// ReplaceLinkedFilesObserved updates the complete file set from the revision the
// caller read. One PATCH preserves unrelated details; it is not a GitHub CAS.
func (c *Client) ReplaceLinkedFilesObserved(ctx context.Context, observed *Record, files []models.IssueFile) (*Record, bool, error) {
	change, noop, err := prepareLinkedFileChange(observed, files, c.repo)
	if err != nil {
		return nil, false, err
	}
	if noop {
		return observed, true, nil
	}
	result, err := c.UpdateObserved(ctx, observed, change)
	return result, false, err
}

// ReplaceLinkedFilesWithActivityObserved journals a CLI file edit together with
// its automatic activity. HTTP/monitor callers without an activity keep the
// standalone file update contract above.
func (c *Client) ReplaceLinkedFilesWithActivityObserved(ctx context.Context, observed *Record, files []models.IssueFile, activity models.Activity, command string) (*Record, bool, error) {
	change, noop, err := prepareLinkedFileChange(observed, files, c.repo)
	if err != nil {
		return nil, false, err
	}
	if noop {
		return observed, true, nil
	}
	result, err := c.UpdateWithActivityObserved(ctx, observed, change, activity, command)
	return result, false, err
}

func prepareLinkedFileChange(observed *Record, files []models.IssueFile, repository string) (Changes, bool, error) {
	if observed == nil || observed.repository != repository || observed.revision == ([32]byte{}) {
		return Changes{}, false, fmt.Errorf("file change requires an observation from this repository")
	}
	if observed.DeletedAt != nil {
		return Changes{}, false, fmt.Errorf("cannot change files of deleted issue %s", observed.ID)
	}
	details, err := observed.CopyDetails()
	if err != nil {
		return Changes{}, false, err
	}
	files, err = validatedLinkedFiles(observed.ID, files)
	if err != nil {
		return Changes{}, false, err
	}
	if (len(details.Files) == 0 && len(files) == 0) || reflect.DeepEqual(details.Files, files) {
		return Changes{}, true, nil
	}
	details.Files = files
	// Review basis includes linked files; keep historical review rows but revoke
	// an active approval immediately, rather than presenting it as still valid.
	now := time.Now().UTC()
	supersedeReviews(&details, now)
	details.ReviewerSession = ""
	details.ReviewedAt = nil
	details.ReviewBasis = ""
	return Changes{Details: &details}, false, nil
}

func validatedLinkedFiles(issueID string, files []models.IssueFile) ([]models.IssueFile, error) {
	files = append([]models.IssueFile{}, files...)
	seen := map[string]bool{}
	for i, f := range files {
		if f.FilePath == "" || f.FilePath == "." || path.IsAbs(f.FilePath) || path.Clean(f.FilePath) != f.FilePath || strings.Contains(f.FilePath, "\\") || strings.HasPrefix(f.FilePath, "../") || seen[f.FilePath] {
			return nil, fmt.Errorf("linked file path must be unique and repository-relative: %q", f.FilePath)
		}
		if f.IssueID != "" && f.IssueID != issueID {
			return nil, fmt.Errorf("linked file belongs to a different issue")
		}
		if !slices.Contains([]models.FileRole{models.FileRoleImplementation, models.FileRoleTest, models.FileRoleReference, models.FileRoleConfig}, f.Role) {
			return nil, fmt.Errorf("invalid file role %q", f.Role)
		}
		files[i].IssueID = issueID
		seen[f.FilePath] = true
	}
	return files, nil
}
