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
	if observed == nil || observed.repository != c.repo || observed.revision == ([32]byte{}) {
		return nil, false, fmt.Errorf("file change requires an observation from this repository")
	}
	if observed.DeletedAt != nil {
		return nil, false, fmt.Errorf("cannot change files of deleted issue %s", observed.ID)
	}
	details, err := observed.CopyDetails()
	if err != nil {
		return nil, false, err
	}
	files = append([]models.IssueFile{}, files...)
	seen := map[string]bool{}
	for i, f := range files {
		if f.FilePath == "" || f.FilePath == "." || path.IsAbs(f.FilePath) || path.Clean(f.FilePath) != f.FilePath || strings.Contains(f.FilePath, "\\") || strings.HasPrefix(f.FilePath, "../") || seen[f.FilePath] {
			return nil, false, fmt.Errorf("linked file path must be unique and repository-relative: %q", f.FilePath)
		}
		if f.IssueID != "" && f.IssueID != observed.ID {
			return nil, false, fmt.Errorf("linked file belongs to a different issue")
		}
		if !slices.Contains([]models.FileRole{models.FileRoleImplementation, models.FileRoleTest, models.FileRoleReference, models.FileRoleConfig}, f.Role) {
			return nil, false, fmt.Errorf("invalid file role %q", f.Role)
		}
		files[i].IssueID = observed.ID
		seen[f.FilePath] = true
	}
	if (len(details.Files) == 0 && len(files) == 0) || reflect.DeepEqual(details.Files, files) {
		return observed, true, nil
	}
	details.Files = files
	// Review basis includes linked files; keep historical review rows but revoke
	// an active approval immediately, rather than presenting it as still valid.
	now := time.Now().UTC()
	supersedeReviews(&details, now)
	details.ReviewerSession = ""
	details.ReviewedAt = nil
	details.ReviewBasis = ""
	result, err := c.UpdateObserved(ctx, observed, Changes{Details: &details})
	return result, false, err
}
