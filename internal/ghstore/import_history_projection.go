package ghstore

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/marcus/td/internal/models"
)

// ProjectImportedHistory is for read views and portable exports only. Native
// ListActivity and review/transition input stay unchanged: archived handoffs and
// logs cannot satisfy a current review checkpoint or become active events.
func ProjectImportedHistory(activity []models.Activity, targetID string) ([]models.Log, []models.Handoff, error) {
	carriers := slices.Clone(activity)
	slices.SortFunc(carriers, func(a, b models.Activity) int {
		ai, _ := strconv.ParseInt(strings.TrimPrefix(a.ID, "ghc-"), 10, 64)
		bi, _ := strconv.ParseInt(strings.TrimPrefix(b.ID, "ghc-"), 10, 64)
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
		return 0
	})
	logs := map[string]models.Log{}
	handoffs := map[string]models.Handoff{}
	for _, carrier := range carriers {
		doc := carrier.PreservedHistory
		if doc == nil || !doc.Complete || doc.Kind != "source" {
			continue
		}
		if err := validatePreservedHistoryActivity(carrier); err != nil {
			return nil, nil, err
		}
		var source ImportItem
		if err := source.decodeJSON(doc.Data, true); err != nil {
			return nil, nil, err
		}
		if source.SourceKey() != doc.SourceKey {
			return nil, nil, fmt.Errorf("preserved source history identity differs")
		}
		identity := func(kind, id string) string {
			return fmt.Sprintf("import-history-%x", sha256.Sum256([]byte(doc.SourceKey+"\n"+kind+"\n"+id)))
		}
		for _, log := range source.Logs {
			originalID := log.ID
			key := doc.SourceKey
			if log.Historical && log.SourceKey != "" && log.OriginalID != "" {
				key, originalID = log.SourceKey, log.OriginalID
			}
			log.ID = fmt.Sprintf("import-history-%x", sha256.Sum256([]byte(key+"\nlog\n"+originalID)))
			log.IssueID, log.Historical, log.SourceKey, log.OriginalID = targetID, true, key, originalID
			if log.OriginalSessionID != "" {
				log.SessionID = log.OriginalSessionID
			}
			logs[log.ID] = log
		}
		for _, handoff := range source.Handoffs {
			originalID := handoff.ID
			key := doc.SourceKey
			if handoff.Historical && handoff.SourceKey != "" && handoff.OriginalID != "" {
				key, originalID = handoff.SourceKey, handoff.OriginalID
			}
			handoff.ID = fmt.Sprintf("import-history-%x", sha256.Sum256([]byte(key+"\nhandoff\n"+originalID)))
			handoff.IssueID, handoff.Historical, handoff.SourceKey, handoff.OriginalID = targetID, true, key, originalID
			if handoff.OriginalSessionID != "" {
				handoff.SessionID = handoff.OriginalSessionID
			}
			handoffs[handoff.ID] = handoff
		}
		// GitHub's portable log/handoff arrays can mirror its activity. Prefer the
		// richer native representation with the same original ID, not a duplicate.
		if source.GitHub != nil {
			for _, a := range source.GitHub.Activity {
				if a.Kind == "log" {
					id := identity("log", a.ID)
					logs[id] = models.Log{ID: id, IssueID: targetID, SessionID: a.SessionID, WorkSessionID: a.WorkSessionID, Message: a.Message, Type: a.LogType, Timestamp: a.CreatedAt, Historical: true, SourceKey: doc.SourceKey, OriginalID: a.ID}
				}
				if a.Kind == "handoff" {
					id := identity("handoff", a.ID)
					handoffs[id] = models.Handoff{ID: id, IssueID: targetID, SessionID: a.SessionID, Done: a.Done, Remaining: a.Remaining, Decisions: a.Decisions, Uncertain: a.Uncertain, Timestamp: a.CreatedAt, Historical: true, SourceKey: doc.SourceKey, OriginalID: a.ID}
				}
			}
		}
	}
	logRows, handoffRows := []models.Log{}, []models.Handoff{}
	for _, row := range logs {
		logRows = append(logRows, row)
	}
	for _, row := range handoffs {
		handoffRows = append(handoffRows, row)
	}
	slices.SortFunc(logRows, func(a, b models.Log) int {
		if order := b.Timestamp.Compare(a.Timestamp); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortFunc(handoffRows, func(a, b models.Handoff) int {
		if order := b.Timestamp.Compare(a.Timestamp); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return logRows, handoffRows, nil
}

// Portable rows keep attribution separately from a historical session key.
// SQLite import does not persist the added historical flags, so the ordinary
// session_id itself must not impersonate an active source worker there.
func NamespacePortableHistorySessions(logs []models.Log, handoffs []models.Handoff) {
	for i := range logs {
		if logs[i].Historical {
			if logs[i].OriginalSessionID == "" {
				logs[i].OriginalSessionID = logs[i].SessionID
			}
			logs[i].SessionID = fmt.Sprintf("imported-%x:%s", sha256.Sum256([]byte(logs[i].SourceKey)), logs[i].OriginalSessionID)
		}
	}
	for i := range handoffs {
		if handoffs[i].Historical {
			if handoffs[i].OriginalSessionID == "" {
				handoffs[i].OriginalSessionID = handoffs[i].SessionID
			}
			handoffs[i].SessionID = fmt.Sprintf("imported-%x:%s", sha256.Sum256([]byte(handoffs[i].SourceKey)), handoffs[i].OriginalSessionID)
		}
	}
}
