package ghstore

import (
	"fmt"
	"slices"
	"strings"
)

// ImportTargetIdentity retains the complete task-to-allocation mapping, without
// copying unrelated task bodies or activity into an import command. An absent
// operation ID is meaningful: a native issue number alone cannot recover a
// source imported from another repository.
type ImportTargetIdentity struct {
	IssueID     string `json:"issue_id"`
	OperationID string `json:"operation_id,omitempty"`
}

func snapshotImportIdentities(snapshot *Snapshot) []ImportTargetIdentity {
	identities := []ImportTargetIdentity{}
	if snapshot != nil {
		for _, record := range snapshot.Issues(true) {
			identities = append(identities, ImportTargetIdentity{IssueID: record.ID, OperationID: record.meta.OperationID})
		}
	}
	slices.SortFunc(identities, func(a, b ImportTargetIdentity) int { return strings.Compare(a.IssueID, b.IssueID) })
	return identities
}

type importTargetIndex struct {
	byID        map[string]string
	byOperation map[string]string
}

func newImportTargetIndex(identities []ImportTargetIdentity) (*importTargetIndex, error) {
	if identities == nil {
		return nil, fmt.Errorf("import requires the original task identity inventory")
	}
	index := &importTargetIndex{byID: map[string]string{}, byOperation: map[string]string{}}
	for n, identity := range identities {
		if !canonicalGroupID(identity.IssueID) || n > 0 && identities[n-1].IssueID >= identity.IssueID {
			return nil, fmt.Errorf("import target identities must be canonical, unique and sorted")
		}
		index.byID[identity.IssueID] = identity.OperationID
		if strings.HasPrefix(identity.OperationID, "td-import-v1-") {
			if prior, exists := index.byOperation[identity.OperationID]; exists {
				return nil, fmt.Errorf("import identity appears in both %s and %s; resolve duplicate targets before import", prior, identity.IssueID)
			}
			index.byOperation[identity.OperationID] = identity.IssueID
		}
	}
	return index, nil
}
