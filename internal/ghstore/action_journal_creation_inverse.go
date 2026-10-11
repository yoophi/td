package ghstore

import (
	"fmt"
	"reflect"
	"slices"
)

// The deletion input is fixed before any inverse write. Checkpoint is recorded
// only after all actual reverse edges have been restored. It lets confirmation
// verify those real results even after the source is logically deleted.
type ActionCreationInverse struct {
	DeleteInput ActionUpdateInput          `json:"delete_input"`
	Checkpoint  *CreationInverseCheckpoint `json:"checkpoint,omitempty"`
	Result      *CreationObservation       `json:"result,omitempty"`
}

type CreationInverseCheckpoint struct {
	Relations *ActionJournalRecord   `json:"relations,omitempty"`
	Receipts  []*ActionJournalRecord `json:"receipts"`
}

func (a ActionJournal) validateCreationInverse() error {
	i := a.CreationCommand.Inverse
	inverse := a.Phase == "undo_prepared" || a.Phase == "undone"
	if inverse != (i != nil) {
		return fmt.Errorf("creation inverse requires its declared phase and complete deletion input")
	}
	if i == nil {
		return nil
	}
	want, err := i.DeleteInput.contents()
	if err != nil {
		return err
	}
	// The original clock is used only for semantic validation, never as a
	// purported observation of the requested future deletion.
	want.UpdatedAt = a.After.UpdatedAt
	want.Revision, err = want.checksum()
	if err != nil {
		return err
	}
	probe := &ActionJournalRecord{ActionJournal: ActionJournal{Action: "update", SessionID: a.SessionID, IssueID: a.IssueID, Before: a.After, After: &want}}
	if ok, err := deletionUndoScope(probe); err != nil || !ok {
		return fmt.Errorf("creation compensation must only logically delete its original allocation")
	}
	if (a.Phase == "undone") != (i.Result != nil) {
		return fmt.Errorf("creation inverse result requires completed phase")
	}
	if i.Result != nil {
		r := i.Result
		if i.Checkpoint == nil || !sameActionContents(want, *a.Restored) || a.Restored.UpdatedAt.Before(a.After.UpdatedAt) || r.Actor == "" || r.ObservedAt.IsZero() || r.Proof != "observed" && r.Proof != "verified" {
			return fmt.Errorf("invalid creation compensation outcome")
		}
		identity := ActionJournal{OperationID: "probe", SessionID: r.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
		if err := identity.validate(); err != nil || r.Proof == "verified" && r.SessionID != a.SessionID {
			return fmt.Errorf("invalid creation compensation session")
		}
	}
	if i.Checkpoint == nil {
		return nil
	}
	cp := i.Checkpoint
	if cp.Receipts == nil || (cp.Relations != nil) != (a.CreationCommand.Relations != nil) {
		return fmt.Errorf("creation inverse checkpoint lost original relationship membership")
	}
	if cp.Relations == nil {
		if len(cp.Receipts) != 0 {
			return fmt.Errorf("creation without child cannot acquire effect receipts")
		}
		return nil
	}
	root := &ActionJournalRecord{ActionJournal: a, Author: a.CreationCommand.Allocation.Actor}
	if err := validateCreationChild(root, cp.Relations); err != nil {
		return err
	}
	if err := creationChildDescendant(a.CreationCommand.Relations, cp.Relations); err != nil {
		return err
	}
	if cp.Relations.Phase != "undone" || cp.Relations.Group.Inverse == nil {
		return fmt.Errorf("creation deletion requires a completed relationship inverse")
	}
	if _, err := orderedGroupEffects(cp.Relations, cp.Receipts); err != nil {
		return err
	}
	if err := validatePairedGroupForward(cp.Relations, cp.Receipts); err != nil {
		return err
	}
	states := map[string]ActionIssueState{}
	for _, m := range cp.Relations.Group.Members {
		states[m.IssueID] = m.Before
	}
	for _, r := range cp.Receipts {
		if r.Action != "update" || r.Phase != "prepared" && r.Phase != "undone" {
			return fmt.Errorf("creation inverse checkpoint contains an unfinished effect")
		}
		if r.Phase == "undone" {
			if r.Restored == nil || !sameActionContents(*r.Before, *r.Restored) {
				return fmt.Errorf("creation inverse checkpoint did not restore the original relation")
			}
			states[r.IssueID] = *r.Restored
		}
	}
	for _, m := range cp.Relations.Group.Inverse.Members {
		if states[m.IssueID].Revision != m.State.Revision {
			return fmt.Errorf("creation checkpoint result differs from actual effect receipts")
		}
	}
	return nil
}

// A relationship inverse may append its own undo identity and result. Every
// original input, observation and forward proof remains byte-for-byte stable.
func creationChildDescendant(frozen, actual *ActionJournalRecord) error {
	if frozen == nil || actual == nil || !sameJournalIdentity(frozen, actual) || frozen.URL != actual.URL || actual.UpdatedAt.Before(frozen.UpdatedAt) {
		return fmt.Errorf("creation child identity changed")
	}
	if actual.Phase == "applied" {
		if !sameCreationChildReceipt(frozen, actual) {
			return fmt.Errorf("creation child forward result changed")
		}
		return nil
	}
	if actual.Phase != "undo_prepared" && actual.Phase != "undone" {
		return fmt.Errorf("creation child phase regressed")
	}
	copy, err := copyCreationJournal(actual.ActionJournal)
	if err != nil {
		return err
	}
	copy.Phase, copy.UndoOperationID, copy.Restored, copy.Group.Inverse = frozen.Phase, "", nil, nil
	if !reflect.DeepEqual(copy, frozen.ActionJournal) {
		return fmt.Errorf("creation child inverse replaced original forward evidence")
	}
	return nil
}

func validateCreationChildProgress(root, child *ActionJournalRecord) error {
	frozen := root.CreationCommand.Relations
	if frozen == nil {
		if root.Phase != "prepared" && child != nil {
			return fmt.Errorf("completed partial creation acquired a new child")
		}
		return nil
	}
	if root.CreationCommand.Inverse == nil {
		if !sameCreationChildReceipt(frozen, child) {
			return fmt.Errorf("creation relationship result changed after its parent receipt")
		}
		return nil
	}
	if err := creationChildDescendant(frozen, child); err != nil {
		return err
	}
	if cp := root.CreationCommand.Inverse.Checkpoint; cp != nil && !sameCreationChildReceipt(cp.Relations, child) {
		return fmt.Errorf("creation inverse checkpoint child changed")
	}
	return nil
}

func validateCreationInverseRewrite(old, next ActionJournal) error {
	if next.CreationCommand == nil || old.CreationCommand == nil || next.CreationCommand.Inverse == nil {
		return fmt.Errorf("creation inverse requires complete command scope")
	}
	base, err := copyCreationJournal(next)
	if err != nil {
		return err
	}
	base.Phase, base.UndoOperationID, base.Restored = old.Phase, old.UndoOperationID, old.Restored
	base.CreationCommand.Inverse = old.CreationCommand.Inverse
	if !reflect.DeepEqual(base, old) {
		return fmt.Errorf("creation inverse changed original inputs or results")
	}
	previous, current := old.CreationCommand.Inverse, next.CreationCommand.Inverse
	if previous == nil {
		if old.Phase != "applied" || next.Phase != "undo_prepared" || current.Checkpoint != nil || current.Result != nil {
			return fmt.Errorf("invalid creation inverse intent")
		}
		return nil
	}
	if old.Phase != "undo_prepared" || old.UndoOperationID != next.UndoOperationID || !reflect.DeepEqual(previous.DeleteInput, current.DeleteInput) || previous.Checkpoint != nil && !reflect.DeepEqual(previous.Checkpoint, current.Checkpoint) {
		return fmt.Errorf("creation inverse intent or checkpoint is immutable")
	}
	if next.Phase == "undo_prepared" && previous.Checkpoint == nil && current.Checkpoint != nil && current.Result == nil {
		return nil
	}
	if next.Phase == "undone" && previous.Checkpoint != nil && current.Result != nil {
		return nil
	}
	return fmt.Errorf("invalid creation inverse completion")
}

func creationInverseStates(root *ActionJournalRecord) map[string]ActionIssueState {
	out := map[string]ActionIssueState{}
	for _, watch := range root.CreationCommand.Watches {
		out[watch.ID] = watch.State
	}
	if i := root.CreationCommand.Inverse; i != nil && i.Checkpoint != nil && i.Checkpoint.Relations != nil {
		for _, m := range i.Checkpoint.Relations.Group.Inverse.Members {
			if slices.Contains(root.CreationCommand.Blocks, m.IssueID) {
				out[m.IssueID] = m.State
			}
		}
	}
	return out
}
