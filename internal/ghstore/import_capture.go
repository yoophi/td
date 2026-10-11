package ghstore

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

const importCapturePrefix = "<!-- td:import-capture:"

// Capture identity is published before backup chunks. Native comment writes
// change issue clocks; retain those clocks and the original comment boundary so
// a later process can reconstruct identical bytes while fields remain unchanged.
type importCapture struct {
	Version       int                   `json:"version"`
	Owner         *ImportExecutionOwner `json:"owner,omitempty"`
	Repository    string                `json:"repository"`
	SourceKey     string                `json:"source_key"`
	SourceDigest  string                `json:"source_digest"`
	TargetID      string                `json:"target_id"`
	BeforeBasis   string                `json:"before_basis"`
	ArchiveDigest string                `json:"archive_digest"`
	Parts         int                   `json:"parts"`
	LastCommentID int64                 `json:"last_comment_id"`
	CommentCount  int                   `json:"comment_count"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
	ClosedAt      *time.Time            `json:"closed_at"`
	commentID     int64
}

func (capture importCapture) validate() error {
	receipt := importReceipt{Version: capture.Version, Owner: capture.Owner, SourceKey: capture.SourceKey, SourceDigest: capture.SourceDigest, TargetID: capture.TargetID, TargetBasis: capture.BeforeBasis}
	if err := receipt.validate(); err != nil {
		return err
	}
	if !repositoryPattern.MatchString(capture.Repository) || !importDigestPattern.MatchString(capture.ArchiveDigest) || capture.Parts < 1 || capture.CommentCount < 0 || capture.LastCommentID < 0 || (capture.CommentCount == 0) != (capture.LastCommentID == 0) {
		return fmt.Errorf("invalid destination capture")
	}
	return nil
}

func renderImportCapture(capture importCapture) (string, error) {
	if err := capture.validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(capture)
	if err != nil {
		return "", err
	}
	return "td import destination capture.\n\n" + importCheckpointStart(importCapturePrefix, capture.Version) + string(data) + markerEnd, nil
}

func decodeImportCapture(body string) (*importCapture, error) {
	if !strings.Contains(body, importCapturePrefix) {
		return nil, nil
	}
	data, err := importCheckpointJSON(body, importCapturePrefix)
	if err != nil {
		return nil, err
	}
	var capture importCapture
	if err := decodeImportJSON(data, &capture); err != nil {
		return nil, err
	}
	canonical, err := renderImportCapture(capture)
	if err != nil {
		return nil, err
	}
	if canonical != body {
		return nil, fmt.Errorf("edited destination capture")
	}
	return &capture, nil
}

func snapshotImportCaptures(snapshot *Snapshot) (map[string]importCapture, error) {
	captures := map[string]importCapture{}
	for _, entry := range snapshot.entries {
		id := fmt.Sprintf("gh-%d", entry.Issue.Number)
		for _, comment := range entry.Comments {
			capture, err := decodeImportCapture(comment.Body)
			if err != nil {
				return nil, fmt.Errorf("capture comment %d on %s: %w", comment.ID, id, err)
			}
			if capture == nil {
				continue
			}
			if capture.TargetID != id {
				return nil, fmt.Errorf("capture comment %d refers to another issue", comment.ID)
			}
			prior, exists := captures[capture.SourceKey]
			if exists && prior.TargetID != id {
				return nil, fmt.Errorf("capture source %s appears on multiple targets", capture.SourceKey)
			}
			capture.commentID = comment.ID
			if !exists || comment.ID > prior.commentID {
				captures[capture.SourceKey] = *capture
			}
		}
	}
	return captures, nil
}

func canonicalImportDestination(repo string, issue apiIssue, comments []apiComment) ([]byte, error) {
	issue.Labels = slices.Clone(issue.Labels)
	if issue.Labels == nil {
		issue.Labels = []struct {
			Name string `json:"name"`
		}{}
	}
	slices.SortFunc(issue.Labels, func(a, b struct {
		Name string `json:"name"`
	}) int {
		return strings.Compare(a.Name, b.Name)
	})
	comments = slices.Clone(comments)
	if comments == nil {
		comments = []apiComment{}
	}
	slices.SortFunc(comments, func(a, b apiComment) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return json.Marshal(importDestinationArchive{Repository: repo, Issue: issue, Comments: comments})
}

func newImportCapture(repo string, row ImportPlanRow, entry snapshotEntry) (importCapture, []importProvenancePart, error) {
	data, err := canonicalImportDestination(repo, entry.Issue, entry.Comments)
	if err != nil {
		return importCapture{}, nil, err
	}
	parts, err := importProvenanceParts(row.SourceKey, row.Digest, row.TargetID, "destination", data)
	if err != nil {
		return importCapture{}, nil, err
	}
	var last int64
	for _, comment := range entry.Comments {
		if comment.ID > last {
			last = comment.ID
		}
	}
	capture := importCapture{Version: 1, Repository: repo, SourceKey: row.SourceKey, SourceDigest: row.Digest, TargetID: row.TargetID, BeforeBasis: row.basis, ArchiveDigest: parts[0].ContentDigest, Parts: len(parts), LastCommentID: last, CommentCount: len(entry.Comments), CreatedAt: entry.Issue.CreatedAt, UpdatedAt: entry.Issue.UpdatedAt, ClosedAt: entry.Issue.ClosedAt}
	return capture, parts, capture.validate()
}

func recoverImportDestinationCapture(capture importCapture, repo string, row ImportPlanRow, entry snapshotEntry) ([]importProvenancePart, error) {
	if err := capture.validate(); err != nil {
		return nil, err
	}
	basis, err := importTargetBasis(entry.Issue)
	if err != nil {
		return nil, err
	}
	if capture.Repository != repo || capture.SourceKey != row.SourceKey || capture.SourceDigest != row.Digest || capture.TargetID != row.TargetID || basis != capture.BeforeBasis || row.basis != capture.BeforeBasis {
		return nil, fmt.Errorf("destination capture/source/content changed; no archive or issue write attempted: %w", &ConflictError{ID: row.TargetID})
	}
	original := []apiComment{}
	for _, comment := range entry.Comments {
		if comment.ID <= capture.LastCommentID {
			original = append(original, comment)
		}
	}
	if len(original) != capture.CommentCount {
		return nil, fmt.Errorf("original destination comments missing or changed; no archive or issue write attempted")
	}
	issue := entry.Issue
	issue.CreatedAt, issue.UpdatedAt, issue.ClosedAt = capture.CreatedAt, capture.UpdatedAt, capture.ClosedAt
	data, err := canonicalImportDestination(repo, issue, original)
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != capture.ArchiveDigest {
		return nil, fmt.Errorf("original destination archive digest changed; no archive or issue write attempted")
	}
	parts, err := importProvenanceParts(row.SourceKey, row.Digest, row.TargetID, "destination", data)
	if err != nil {
		return nil, err
	}
	if len(parts) != capture.Parts {
		return nil, fmt.Errorf("destination archive part count changed")
	}
	return parts, nil
}
