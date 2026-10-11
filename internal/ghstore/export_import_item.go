package ghstore

import "github.com/marcus/td/internal/models"

// ImportItemFromExport is the shared portable task projection used by export
// and store transfer. The native source remains provenance, not write authority.
func ImportItemFromExport(row *ExportedIssue) (ImportItem, error) {
	item := ImportItem{Issue: row.Record.Issue, GitHub: row, Logs: []models.Log{}, Handoffs: []models.Handoff{}, Dependencies: []models.IssueDependency{}, Files: []models.IssueFile{}}
	if row.Record.Details != nil {
		item.Files = append(item.Files, row.Record.Details.Files...)
		for _, dep := range row.Record.Details.Dependencies {
			item.Dependencies = append(item.Dependencies, models.IssueDependency{IssueID: row.Record.ID, DependsOnID: dep, RelationType: "depends_on"})
		}
	}
	for _, a := range row.Activity {
		switch a.Kind {
		case "log":
			item.Logs = append(item.Logs, models.Log{ID: a.ID, IssueID: a.IssueID, SessionID: a.SessionID, WorkSessionID: a.WorkSessionID, Message: a.Message, Type: a.LogType, Timestamp: a.CreatedAt})
		case "handoff":
			item.Handoffs = append(item.Handoffs, models.Handoff{ID: a.ID, IssueID: a.IssueID, SessionID: a.SessionID, Done: a.Done, Remaining: a.Remaining, Decisions: a.Decisions, Uncertain: a.Uncertain, Timestamp: a.CreatedAt})
		}
	}
	logs, handoffs, err := ProjectImportedHistory(row.Activity, row.Record.ID)
	if err != nil {
		return ImportItem{}, err
	}
	NamespacePortableHistorySessions(logs, handoffs)
	item.Logs = append(item.Logs, logs...)
	item.Handoffs = append(item.Handoffs, handoffs...)
	return item, nil
}
