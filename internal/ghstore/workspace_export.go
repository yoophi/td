package ghstore

import (
	"context"
	"github.com/marcus/td/internal/transferlimit"
	"time"
)

// NativeExport is source evidence, never a destination write or active actor.
// It retains modeled native fields and raw comment bodies, including journal
// transport comments that ordinary activity views intentionally hide.
type NativeExport struct {
	Number    int                   `json:"number"`
	Kind      string                `json:"kind"`
	Title     string                `json:"title"`
	Body      string                `json:"body"`
	State     string                `json:"state"`
	URL       string                `json:"url"`
	Author    string                `json:"author"`
	CreatedAt time.Time             `json:"created_at"`
	UpdatedAt time.Time             `json:"updated_at"`
	ClosedAt  *time.Time            `json:"closed_at,omitempty"`
	Labels    []string              `json:"labels"`
	Comments  []NativeExportComment `json:"comments"`
}

type NativeExportComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	IssueURL  string    `json:"issue_url"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// WorkspaceExport collects every shared entity from the same full observation.
// Notes and boards include logical deletions; Native binds their carrier number
// and original body/history. Local UI preferences, credentials and device
// session registries are not collected. Sources are not atomic repository
// snapshots and imported journal records must remain passive archives.
type WorkspaceExport struct {
	Repository string                 `json:"repository"`
	ObservedAt time.Time              `json:"observed_at"`
	Tasks      []ExportedIssue        `json:"tasks"`
	Notes      []NoteRecord           `json:"notes"`
	Boards     []BoardRecord          `json:"boards"`
	Native     []NativeExport         `json:"native"`
	Journal    *PortableJournalBundle `json:"journal"`
}

func (c *Client) ExportWorkspace(ctx context.Context) (*WorkspaceExport, error) {
	return c.ExportWorkspaceLimited(ctx, 0)
}

// ExportWorkspaceLimited checks serialized projections during collection. The
// existing bulk snapshot precedes projection; its peak memory is not bounded
// by this input-size limit. No extra GitHub calls or per-entity queries occur.
func (c *Client) ExportWorkspaceLimited(ctx context.Context, maxBytes int64) (*WorkspaceExport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	budget, err := transferlimit.New(maxBytes)
	if err != nil {
		return nil, err
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return nil, err
	}
	tasks, err := snapshot.exportIssues(c.repo, true)
	if err != nil {
		return nil, err
	}
	journal, err := snapshot.exportJournal(c.repo)
	if err != nil {
		return nil, err
	}
	result := &WorkspaceExport{Repository: c.repo, ObservedAt: snapshot.ObservedAt(), Tasks: tasks,
		Notes: []NoteRecord{}, Boards: []BoardRecord{}, Native: []NativeExport{}, Journal: journal}
	if err := budget.Add(result); err != nil {
		return nil, err
	}
	records := make(map[int]Record, len(snapshot.records))
	for _, record := range snapshot.records {
		records[record.Number] = record
	}
	for _, entry := range snapshot.entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item := entry.Issue
		record := records[item.Number]
		kind := record.meta.EntityKind
		if kind == "" {
			kind = "issue"
		}
		native := NativeExport{Number: item.Number, Kind: kind, Title: item.Title, Body: item.Body,
			State: item.State, URL: item.URL, Author: item.User.Login, CreatedAt: item.CreatedAt,
			UpdatedAt: item.UpdatedAt, ClosedAt: item.ClosedAt, Labels: []string{}, Comments: []NativeExportComment{}}
		for _, label := range item.Labels {
			native.Labels = append(native.Labels, label.Name)
		}
		for _, comment := range entry.Comments {
			native.Comments = append(native.Comments, NativeExportComment{ID: comment.ID, Body: comment.Body,
				URL: comment.URL, IssueURL: comment.IssueURL, Author: comment.User.Login,
				CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt})
		}
		if err := budget.Add(native); err != nil {
			return nil, err
		}
		result.Native = append(result.Native, native)
		switch kind {
		case "note":
			note, err := noteRecord(&record)
			if err != nil {
				return nil, err
			}
			if err := budget.Add(note); err != nil {
				return nil, err
			}
			result.Notes = append(result.Notes, *note)
		case "board":
			board, err := boardRecord(&record)
			if err != nil {
				return nil, err
			}
			// This is a device-local preference, not migrated board content.
			// The original public native body remains source evidence unchanged.
			board.LastViewedAt, board.Details.LastViewedAt = nil, nil
			if err := budget.Add(board); err != nil {
				return nil, err
			}
			result.Boards = append(result.Boards, *board)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
