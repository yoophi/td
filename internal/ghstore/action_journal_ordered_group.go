package ghstore

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
)

// Forward records are frozen original receipts. The digest binds the native
// comment body, including its original compressed transport. Executors must
// point-read and match that digest and record identity before target writes.
type OrderedGroupEffect struct {
	Record     *ActionJournalRecord `json:"record"`
	BodySHA256 string               `json:"body_sha256"`
	Status     string               `json:"status"`
}

type ActionOrderedGroup struct {
	Forward []OrderedGroupEffect          `json:"forward"`
	Events  map[string][]nativeStateEvent `json:"events"`
	Steps   []*OrderedGroupUndoStep       `json:"steps,omitempty"`
}

func (a ActionJournal) validateOrderedGroup() error {
	o := a.Group.Ordered
	if o == nil {
		return nil
	}
	if a.GroupSequence != 0 || a.GroupPreviousReceiptID != "" {
		return fmt.Errorf("ordered group cannot be its own member")
	}
	if a.Phase == "prepared" {
		if len(o.Forward) != 0 || o.Events != nil || len(o.Steps) != 0 {
			return fmt.Errorf("ordered initial intent cannot assert future effects or compensation")
		}
		return nil
	}
	if a.Group.Forward == nil || o.Events == nil {
		return fmt.Errorf("ordered result requires frozen forward records and events")
	}
	group := &ActionJournalRecord{ActionJournal: a, ID: "ghc-1", Author: a.Group.Forward.Actor}
	all, applied := []*ActionJournalRecord{}, []*ActionJournalRecord{}
	byID := map[string][]OrderedGroupEffect{}
	lastNumber := int64(0)
	for _, effect := range o.Forward {
		r := effect.Record
		if r == nil || r.Group != nil || r.GroupOperationID != a.OperationID || r.GroupSequence == 0 || !strings.EqualFold(r.Author, group.Author) {
			return fmt.Errorf("ordered forward snapshot differs from original member scope")
		}
		if err := r.validate(); err != nil {
			return err
		}
		n, err := CommentNumber(r.ID)
		if err != nil || n <= lastNumber || r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
			return fmt.Errorf("ordered forward identities or clocks are invalid")
		}
		lastNumber = n
		digest, err := hex.DecodeString(effect.BodySHA256)
		if err != nil || len(digest) != 32 || strings.ToLower(effect.BodySHA256) != effect.BodySHA256 {
			return fmt.Errorf("ordered forward snapshot omitted its native body digest")
		}
		switch effect.Status {
		case "applied", "observed":
			if r.Phase != "applied" || effectWasObserved(r) != (effect.Status == "observed") {
				return fmt.Errorf("ordered forward proof differs from original receipt")
			}
			if effect.Status == "observed" && a.Group.Forward.Proof == "verified-group-receipts" {
				return fmt.Errorf("ordered forward observation cannot be promoted")
			}
			applied = append(applied, r)
		case "unapplied":
			if r.Phase != "prepared" || !a.Group.Forward.Partial {
				return fmt.Errorf("unapplied ordered input needs explicit partial outcome")
			}
		default:
			return fmt.Errorf("ordered forward snapshot has unknown outcome")
		}
		all = append(all, r)
		byID[r.IssueID] = append(byID[r.IssueID], effect)
	}
	if _, err := orderedGroupEffects(group, all); err != nil {
		return err
	}
	members := map[string]bool{}
	for i, member := range a.Group.Members {
		members[member.IssueID] = true
		result := a.Group.Forward.Members[i]
		chain := byID[member.IssueID]
		if len(chain) == 0 {
			if result.Status != "noop" && result.Status != "unattempted" || result.State.Revision != member.Before.Revision || result.ReceiptID != "" {
				return fmt.Errorf("ordered unattempted member changed its original observation")
			}
			continue
		}
		last := chain[len(chain)-1]
		if last.Status == "unapplied" && len(chain) > 1 {
			last = chain[len(chain)-2]
		}
		want := last.Record.After
		if last.Status == "unapplied" {
			want = last.Record.Before
		}
		if want == nil || result.ReceiptID != last.Record.ID || result.State.Revision != want.Revision || result.Status != last.Status {
			return fmt.Errorf("ordered member summary differs from its last actual forward result")
		}
	}
	for id, events := range o.Events {
		if !members[id] || events == nil {
			return fmt.Errorf("ordered event observation is outside membership or absent")
		}
		if err := orderedStepEventProof(&OrderedGroupUndoStep{Baseline: events}, events, false); err != nil {
			return err
		}
	}
	for id, chain := range byID {
		last := chain[len(chain)-1]
		if last.Status == "unapplied" && workflowEffectEvents(&last.Record.ActionJournal) != nil {
			baseline, found := o.Events[id]
			if !found || !reflect.DeepEqual(baseline, *workflowEffectEvents(&last.Record.ActionJournal)) {
				return fmt.Errorf("unapplied ordered input replaced its original native event baseline")
			}
		}
		if last.Status == "unapplied" && len(chain) > 1 {
			last = chain[len(chain)-2]
		}
		if last.Status != "unapplied" {
			baseline, found := o.Events[id]
			if !found || baseline == nil {
				return fmt.Errorf("ordered forward effect omitted its event observation")
			}
			if workflowEffectEvents(&last.Record.ActionJournal) != nil {
				if err := workflowEffectEventProof(last.Record.ActionJournal, baseline); err != nil {
					return err
				}
			}
		}
	}
	if a.Phase == "applied" {
		if len(o.Steps) != 0 {
			return fmt.Errorf("ordered forward result cannot assert inverse steps")
		}
		return nil
	}
	if len(o.Steps) > len(applied) {
		return fmt.Errorf("ordered inverse has more steps than original applied effects")
	}
	for i, step := range o.Steps {
		if step == nil {
			return fmt.Errorf("ordered inverse has a missing step")
		}
		want, err := PlanOrderedGroupUndoStep(group, applied, o.Events, o.Steps[:i], step.Source, step.Baseline, a.UndoOperationID, step.OperationID, a.SessionID, group.Author, step.At)
		if err != nil {
			return err
		}
		if !sameOrderedStepIntent(step, want) {
			return fmt.Errorf("ordered inverse replaced its original plan")
		}
		if step.Result == nil {
			if i != len(o.Steps)-1 || a.Phase == "undone" || step.EventsAfter != nil || step.Proof != "" || step.ObserverSession != "" || !step.ObservedAt.IsZero() {
				return fmt.Errorf("pending ordered inverse cannot authorize completion or a successor")
			}
		} else if err := validateOrderedStepResult(step); err != nil {
			return err
		}
	}
	if a.Phase == "undone" {
		if len(o.Steps) != len(applied) || a.Group.Inverse == nil {
			return fmt.Errorf("ordered group completion omitted remaining inverses")
		}
		lastResults := map[string]*ActionIssueState{}
		for _, step := range o.Steps {
			for _, effect := range applied {
				if effect.ID == step.ForwardReceiptID {
					lastResults[effect.IssueID] = step.Result
				}
			}
			if step.Proof == inverseObservationProof && a.Group.Inverse.Proof != "group-contents-observed" {
				return fmt.Errorf("observed ordered inverse cannot be promoted at group completion")
			}
		}
		for i, original := range a.Group.Forward.Members {
			result := a.Group.Inverse.Members[i]
			if result.ReceiptID != original.ReceiptID {
				return fmt.Errorf("ordered inverse replaced original forward membership")
			}
			if restored := lastResults[original.IssueID]; restored != nil {
				if result.Status != "undone" || result.State.Revision != restored.Revision {
					return fmt.Errorf("ordered inverse summary differs from its final compensation")
				}
			} else if !reflect.DeepEqual(result, original) {
				return fmt.Errorf("ordered inverse changed an untouched member")
			}
		}
	}
	return nil
}

func validateOrderedGroupRewrite(old, next *ActionOrderedGroup) error {
	if old == nil || next == nil {
		if old != nil || next != nil {
			return fmt.Errorf("ordered group identity is immutable")
		}
		return nil
	}
	if old.Events != nil && (!reflect.DeepEqual(old.Events, next.Events) || !reflect.DeepEqual(old.Forward, next.Forward)) {
		return fmt.Errorf("ordered original forward snapshots and event observations are immutable")
	}
	if len(next.Steps) < len(old.Steps) || len(next.Steps) > len(old.Steps)+1 {
		return fmt.Errorf("ordered inverse may record only one next intent or completion")
	}
	for i, step := range old.Steps {
		newStep := next.Steps[i]
		if step == nil || newStep == nil || !sameOrderedStepIntent(step, newStep) {
			return fmt.Errorf("ordered inverse intent is immutable")
		}
		if step.Result != nil {
			if !reflect.DeepEqual(step, newStep) {
				return fmt.Errorf("completed ordered inverse and provenance are immutable")
			}
		} else if i != len(old.Steps)-1 || len(next.Steps) != len(old.Steps) || newStep.Result == nil {
			return fmt.Errorf("pending ordered inverse needs receipt-only completion before another intent")
		}
	}
	return nil
}
