package ghstore

import (
	"fmt"
)

// A completed native inverse permits superseding exactly one prior source
// receipt, not arbitrary later edits. The new command freezes this link with
// its actual Before observation; source/allocation identities remain stable.
type ImportCompensation struct {
	CommandID       string `json:"command_id"`
	OperationID     string `json:"operation_id"`
	UndoOperationID string `json:"undo_operation_id"`
	SourceKey       string `json:"source_key"`
	SourceDigest    string `json:"source_digest"`
	ReceiptID       int64  `json:"receipt_id"`
	TargetID        string `json:"target_id"`
	TargetBasis     string `json:"target_basis"`
	NativeSHA256    string `json:"native_sha256"`
	EventsSHA256    string `json:"events_sha256"`
}

func importCompensationFromRoot(root *ActionJournalRecord, member int) (*ImportCompensation, error) {
	if root.Phase != "undone" || root.ImportInverse == nil || root.ImportInverse.Result == nil || root.ImportCommand.Members[member].Action == "skip" {
		return nil, nil
	}
	source := root.ImportCommand.Members[member].SourceKey
	var receipt *importReceipt
	for _, comment := range root.ImportForward.Targets[member].Comments {
		candidate, err := decodeImportReceipt(comment.Body)
		if err != nil {
			return nil, err
		}
		if candidate != nil && candidate.SourceKey == source && (receipt == nil || comment.ID > receipt.commentID) {
			candidate.commentID = comment.ID
			receipt = candidate
		}
	}
	if receipt == nil || receipt.Owner == nil || receipt.Owner.CommandID != root.ID || receipt.Owner.OperationID != root.OperationID || receipt.Owner.SessionID != root.SessionID {
		return nil, fmt.Errorf("completed import inverse lost its own source receipt")
	}
	state := importInverseStates(root)[member]
	basis, err := importTargetBasis(state.Issue)
	if err != nil {
		return nil, err
	}
	native, err := canonicalImportDestination(root.ImportCommand.Repository, state.Issue, state.Comments)
	if err != nil {
		return nil, err
	}
	return &ImportCompensation{CommandID: root.ID, OperationID: root.OperationID, UndoOperationID: root.UndoOperationID, SourceKey: source, SourceDigest: receipt.SourceDigest, ReceiptID: receipt.commentID, TargetID: receipt.TargetID, TargetBasis: basis, NativeSHA256: journalTransportHash(string(native)), EventsSHA256: stateEventDigest(state.Events)}, nil
}

func snapshotImportCompensations(snapshot *Snapshot, items []ImportItem) ([]ImportCompensation, error) {
	if snapshot == nil || len(items) == 0 {
		return nil, nil
	}
	history, err := snapshot.ActionHistory("")
	if err != nil {
		return nil, err
	}
	wanted, found := map[string]bool{}, map[string]bool{}
	for _, item := range items {
		wanted[item.SourceKey()] = true
	}
	var result []ImportCompensation
	for _, root := range history { // Newest original command first.
		if root.Phase != "undone" || root.ImportInverse == nil {
			continue
		}
		for n, member := range root.ImportCommand.Members {
			if !wanted[member.SourceKey] || found[member.SourceKey] {
				continue
			}
			link, err := importCompensationFromRoot(&root, n)
			if err != nil {
				return nil, err
			}
			if link != nil {
				found[member.SourceKey] = true
				result = append(result, *link)
			}
		}
	}
	return result, nil
}

func matchImportCompensation(repository string, snapshot *Snapshot, receipt importReceipt, basis string, links []ImportCompensation) (*ImportCompensation, error) {
	for _, link := range links {
		if link.SourceKey != receipt.SourceKey || link.SourceDigest != receipt.SourceDigest || link.TargetID != receipt.TargetID || link.ReceiptID != receipt.commentID || link.TargetBasis != basis || receipt.Owner == nil || link.CommandID != receipt.Owner.CommandID || link.OperationID != receipt.Owner.OperationID {
			continue
		}
		probe := ActionJournal{OperationID: link.UndoOperationID, SessionID: receipt.Owner.SessionID, IssueID: link.TargetID, Action: "create", Phase: "prepared"}
		if err := probe.validate(); err != nil || link.UndoOperationID == link.OperationID || !importDigestPattern.MatchString(link.NativeSHA256) || !importDigestPattern.MatchString(link.EventsSHA256) {
			return nil, fmt.Errorf("invalid import compensation link")
		}
		for _, entry := range snapshot.entries {
			if fmt.Sprintf("gh-%d", entry.Issue.Number) != link.TargetID {
				continue
			}
			native, err := canonicalImportDestination(repository, entry.Issue, entry.Comments)
			if err != nil {
				return nil, err
			}
			if journalTransportHash(string(native)) == link.NativeSHA256 {
				return &link, nil
			}
		}
	}
	return nil, &ConflictError{ID: receipt.TargetID}
}
