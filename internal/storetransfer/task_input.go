package storetransfer

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type TaskImportInput struct {
	SourceSHA256 string `json:"source_sha256"`
	// Keys bridge qualified transfer task keys to the existing importer keys.
	Keys  map[string]string    `json:"keys"`
	Items []ghstore.ImportItem `json:"items"`
}

// BuildTaskImportInput reuses the checked task importer. This is a projection,
// not a workspace copy: the executor must preserve the complete source capsule
// for notes/boards, comments/reviews/snapshots and other historical provenance.
// No destination is opened and no source workflow actor gains authority here.
func BuildTaskImportInput(source *Source) (*TaskImportInput, error) {
	if source == nil {
		return nil, fmt.Errorf("transfer source is required")
	}
	s, err := source.Envelope.decode()
	if err != nil {
		return nil, err
	}
	preview, err := s.validateReferences()
	if err != nil {
		return nil, err
	}
	result := &TaskImportInput{SourceSHA256: s.Envelope.SHA256, Keys: map[string]string{}, Items: []ghstore.ImportItem{}}
	if s.SQLite != nil {
		// Import namespaces have a restricted alphabet/length. Hash the stable
		// store+namespace, never the changing payload or a local filesystem path.
		namespace := fmt.Sprintf("td-transfer-%x", sha256.Sum256([]byte(s.Envelope.Store+"\x00"+s.Envelope.Namespace)))
		// SQLite can contain normalized imported IDs such as td-gh-1, while
		// the existing importer accepts only canonical td/gh IDs. Use stable
		// source-local proxies, keeping original identities in the key bridge
		// and full source capsule; never relax GitHub's native ID validation.
		proxies := map[string]string{}
		for _, task := range s.SQLite.Tasks {
			proxies[task.Issue.ID] = fmt.Sprintf("td-%x", sha256.Sum256([]byte(task.Issue.ID)))
		}
		for _, task := range s.SQLite.Tasks {
			item := ghstore.ImportItem{SourceNamespace: namespace, Issue: task.Issue, Logs: task.Logs, Handoffs: task.Handoffs, Files: task.Files, Dependencies: []models.IssueDependency{}}
			item.Issue.ID = proxies[task.Issue.ID]
			if item.Issue.ParentID != "" {
				item.Issue.ParentID = proxies[item.Issue.ParentID]
			}
			for i := range item.Logs {
				item.Logs[i].IssueID = item.Issue.ID
			}
			for i := range item.Handoffs {
				item.Handoffs[i].IssueID = item.Issue.ID
			}
			for i := range item.Files {
				item.Files[i].IssueID = item.Issue.ID
			}
			for _, dep := range task.Dependencies {
				if dep.RelationType == "depends_on" {
					dep.IssueID = item.Issue.ID
					dep.DependsOnID = proxies[dep.DependsOnID]
					item.Dependencies = append(item.Dependencies, dep)
				}
			}
			result.Items = append(result.Items, item)
		}
	} else {
		for i := range s.GitHub.Tasks {
			item, err := ghstore.ImportItemFromExport(&s.GitHub.Tasks[i])
			if err != nil {
				return nil, err
			}
			result.Items = append(result.Items, item)
		}
	}
	if err := ghstore.ValidateImportItems(result.Items); err != nil {
		return nil, fmt.Errorf("prepare transfer tasks: %w", err)
	}
	byID := map[string]string{}
	for i, item := range result.Items {
		originalID := item.Issue.ID
		if s.SQLite != nil {
			originalID = s.SQLite.Tasks[i].Issue.ID
		}
		byID[originalID] = item.SourceKey()
	}
	for _, id := range preview.Identities {
		if id.Kind == "task" {
			result.Keys[id.Key] = byID[id.ID]
		}
	}
	return result, nil
}

// ProjectSQLiteTasks resolves caller-supplied IDs, not receipts or ownership.
// The checked executor must prove those before applying these rows. Historical
// approval/session records are absent; the full source capsule preserves them.
// targetIDs contains only qualified task keys, not note/board assignments.
func ProjectSQLiteTasks(source *Source, targetIDs map[string]string) ([]db.SQLiteExportTask, error) {
	input, err := BuildTaskImportInput(source)
	if err != nil {
		return nil, err
	}
	if source.Envelope.Store != "gh-issue" {
		return nil, fmt.Errorf("SQLite transfer task projection requires a GitHub source")
	}
	byImport := map[string]string{}
	used := map[string]bool{}
	for key, importKey := range input.Keys {
		id := targetIDs[key]
		if id == "" || id == "td-" || db.NormalizeIssueID(id) != id || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\x00") || used[id] {
			return nil, fmt.Errorf("missing, noncanonical or duplicate SQLite task mapping for %q", key)
		}
		used[id] = true
		byImport[importKey] = id
	}
	for key := range targetIDs {
		if _, exists := input.Keys[key]; !exists {
			return nil, fmt.Errorf("unknown task mapping %q", key)
		}
	}
	rows := []db.SQLiteExportTask{}
	for _, item := range input.Items {
		id := byImport[item.SourceKey()]
		issue := item.Issue
		issue.ID = id
		if issue.ParentID != "" {
			issue.ParentID = byImport[item.ReferenceKey(issue.ParentID)]
		}
		issue.CreatorSession, issue.ImplementerSession, issue.ReviewerSession = "", "", ""
		issue.ReviewRequestedBySession, issue.ClosedBySession, issue.ReviewedAt = "", "", nil
		row := db.SQLiteExportTask{Issue: issue, Logs: item.Logs, Handoffs: item.Handoffs, Files: item.Files, Dependencies: []models.IssueDependency{}}
		for _, dep := range item.Dependencies {
			to := byImport[item.ReferenceKey(dep.DependsOnID)]
			row.Dependencies = append(row.Dependencies, models.IssueDependency{IssueID: id, DependsOnID: to, RelationType: dep.RelationType})
		}
		for i := range row.Files {
			f := &row.Files[i]
			// Portable GitHub files may have no original row ID. Paths are
			// validated unique within a task; an empty ID must not merge files.
			f.ID = transferHistoryID(id, item.SourceKey(), "file", f.FilePath)
			f.IssueID = id
		}
		for i := range row.Logs {
			l := &row.Logs[i]
			if !l.Historical || l.SourceKey == "" || l.OriginalID == "" {
				l.SourceKey, l.OriginalID = item.SourceKey(), l.ID
			}
			l.ID, l.IssueID, l.Historical = transferHistoryID(id, l.SourceKey, "log", l.OriginalID), id, true
			// Device work bundles are not imported. Keep their original linkage
			// only in the passive source capsule, not a destination work session.
			l.WorkSessionID = ""
		}
		for i := range row.Handoffs {
			h := &row.Handoffs[i]
			if !h.Historical || h.SourceKey == "" || h.OriginalID == "" {
				h.SourceKey, h.OriginalID = item.SourceKey(), h.ID
			}
			h.ID, h.IssueID, h.Historical = transferHistoryID(id, h.SourceKey, "handoff", h.OriginalID), id, true
		}
		ghstore.NamespacePortableHistorySessions(row.Logs, row.Handoffs)
		rows = append(rows, row)
	}
	return rows, nil
}

func transferHistoryID(target, key, kind, id string) string {
	return fmt.Sprintf("import-history-%x", sha256.Sum256([]byte(target+"\x00"+key+"\x00"+kind+"\x00"+id)))
}
