package ghstore

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

const actionGroupJournalStart = actionJournalPrefix + "v10\n"
const actionReviewGroupJournalStart = actionJournalPrefix + "v12\n"
const actionNativeGroupJournalStart = actionJournalPrefix + "v13\n"
const actionOrderedGroupJournalStart = actionJournalPrefix + "v14\n"
const actionOrderedGroupMemberJournalStart = actionJournalPrefix + "v15\n"
const actionPlannedUpdateGroupJournalStart = actionJournalPrefix + "v16\n"
const actionGroupMemberJournalStart = actionJournalPrefix + "v11\n"

type ActionGroupMember struct {
	IssueID string              `json:"issue_id"`
	Before  ActionIssueState    `json:"before"`
	Input   *ActionUpdateIntent `json:"input,omitempty"`
}

type ActionGroupResultMember struct {
	IssueID   string           `json:"issue_id"`
	ReceiptID string           `json:"receipt_id,omitempty"`
	Status    string           `json:"status"`
	State     ActionIssueState `json:"state"`
}

type ActionGroupObservation struct {
	SessionID  string                    `json:"session_id"`
	Actor      string                    `json:"actor"`
	ObservedAt time.Time                 `json:"observed_at"`
	Proof      string                    `json:"proof"`
	Partial    bool                      `json:"partial"`
	Members    []ActionGroupResultMember `json:"members"`
}

// The group records command membership and actual starting revisions before
// any target write. Every attempted member independently records its complete
// update input before its write; future unattempted inputs are never invented.
type ActionGroup struct {
	Ordered *ActionOrderedGroup     `json:"ordered,omitempty"`
	Command string                  `json:"command"`
	Roots   []string                `json:"roots,omitempty"`
	Cascade bool                    `json:"cascade,omitempty"`
	Members []ActionGroupMember     `json:"members"`
	Forward *ActionGroupObservation `json:"forward,omitempty"`
	Inverse *ActionGroupObservation `json:"inverse,omitempty"`
}

func groupBasicCommand(command string) bool {
	return slices.Contains([]string{"start", "unstart", "block", "unblock"}, command)
}

func canonicalGroupID(id string) bool {
	n, err := Number(id)
	return err == nil && id == fmt.Sprintf("gh-%d", n)
}

func (a ActionJournal) validateGroupAction() error {
	g := a.Group
	if g == nil || a.Action != "group" || a.GroupOperationID != "" || a.GroupSequence != 0 || a.GroupPreviousReceiptID != "" || (!groupBasicCommand(g.Command) && g.Command != "review" && g.Command != "update" && !nativeGroupCommand(g.Command)) || len(g.Members) < 2 || !canonicalGroupID(a.IssueID) || !canonicalGroupID(a.CarrierID) || a.CarrierID == a.IssueID {
		return fmt.Errorf("group requires its immutable explicit command membership and separate carrier")
	}
	if a.Before != nil || a.After != nil || a.Restored != nil || a.CreateInput != nil || a.UpdateIntent != nil || a.UpdateObservation != nil || a.NativeUndo != nil || a.CommentInput != nil || a.CommentAfter != nil || a.CommentBefore != nil || a.UndoObservation != nil || a.Reconciliation != nil {
		return fmt.Errorf("group cannot replace member snapshots or native proofs")
	}
	seen := map[string]bool{}
	for _, m := range g.Members {
		if !canonicalGroupID(m.IssueID) || m.IssueID == a.CarrierID || seen[m.IssueID] {
			return fmt.Errorf("group has duplicate or invalid member identity")
		}
		seen[m.IssueID] = true
		if err := m.Before.validate(); err != nil {
			return err
		}
		record, err := actionStateRecord(&m.Before, m.IssueID)
		if err != nil || record.meta.EntityKind != "" && record.meta.EntityKind != "issue" || record.DeletedAt != nil {
			return fmt.Errorf("group member must be an observed live task")
		}
		if (g.Command == "update") != (m.Input != nil) {
			return fmt.Errorf("planned update group requires every complete input; other groups cannot claim planned inputs")
		}
		if m.Input != nil {
			if g.Ordered != nil || len(m.Input.Related) != 0 {
				return fmt.Errorf("planned scalar updates cannot declare ordered or related effects")
			}
			probe := ActionJournal{Action: "update", IssueID: m.IssueID, CarrierID: a.CarrierID, Before: &m.Before, UpdateIntent: m.Input}
			if err := probe.validateUpdateIntent(); err != nil {
				return err
			}
			after, err := m.Input.Target.contents()
			if err != nil {
				return err
			}
			probe.After = &after
			if err := scalarFieldUndoScope(&ActionJournalRecord{ActionJournal: probe}); err != nil {
				return err
			}
		}
	}
	if g.Command == "review" || nativeGroupCommand(g.Command) {
		rootSeen := map[string]bool{}
		if len(g.Roots) == 0 {
			return fmt.Errorf("review group requires original roots")
		}
		for _, id := range g.Roots {
			if !seen[id] || rootSeen[id] {
				return fmt.Errorf("invalid review group roots")
			}
			rootSeen[id] = true
		}
		if g.Roots[0] != a.IssueID {
			return fmt.Errorf("review group root differs")
		}
	} else if len(g.Roots) != 0 {
		return fmt.Errorf("basic group cannot claim cascade roots")
	}
	if g.Command == "close" && !g.Cascade {
		return fmt.Errorf("close group requires closure graph membership")
	}
	if g.Cascade && (!nativeGroupCommand(g.Command) || g.Command == "reopen") {
		return fmt.Errorf("invalid native cascade group")
	}
	if a.IssueID != g.Members[0].IssueID {
		return fmt.Errorf("group root differs from its first member")
	}
	switch a.Phase {
	case "prepared":
		if g.Forward != nil || g.Inverse != nil || a.UndoOperationID != "" {
			return fmt.Errorf("prepared group cannot claim results")
		}
	case "applied":
		if g.Forward == nil || g.Inverse != nil || a.UndoOperationID != "" {
			return fmt.Errorf("applied group requires its forward observation")
		}
	case "undo_prepared":
		if g.Forward == nil || g.Inverse != nil || a.UndoOperationID == "" || a.UndoOperationID == a.OperationID {
			return fmt.Errorf("group inverse requires an immutable undo intent")
		}
	case "undone":
		if g.Forward == nil || g.Inverse == nil || a.UndoOperationID == "" || a.UndoOperationID == a.OperationID {
			return fmt.Errorf("undone group requires verified or observed inverse results")
		}
	default:
		return fmt.Errorf("unknown group phase")
	}
	if a.UndoOperationID != "" {
		probe := ActionJournal{OperationID: a.UndoOperationID, SessionID: a.SessionID, IssueID: a.IssueID, Action: "create", Phase: "prepared"}
		if err := probe.validate(); err != nil {
			return err
		}
	}
	for _, item := range []struct {
		o       *ActionGroupObservation
		inverse bool
	}{{g.Forward, false}, {g.Inverse, true}} {
		o := item.o
		if o == nil {
			continue
		}
		if o.Actor == "" || o.ObservedAt.IsZero() || len(o.Members) != len(g.Members) {
			return fmt.Errorf("group result lacks actual observer or full membership")
		}
		probe := ActionJournal{OperationID: "probe", SessionID: o.SessionID, IssueID: "gh-1", Action: "create", Phase: "prepared"}
		if err := probe.validate(); err != nil {
			return err
		}
		if !slices.Contains([]string{"verified-group-receipts", "group-contents-observed"}, o.Proof) {
			return fmt.Errorf("unknown group proof")
		}
		for i, m := range o.Members {
			if m.IssueID != g.Members[i].IssueID {
				return fmt.Errorf("group result changed membership or order")
			}
			if err := m.State.validate(); err != nil {
				return err
			}
			if !slices.Contains([]string{"applied", "observed", "noop", "unattempted", "unapplied", "undone"}, m.Status) {
				return fmt.Errorf("unknown group member result")
			}
			if item.inverse {
				if m.Status != "undone" && m.Status != "noop" && m.Status != "unattempted" && m.Status != "unapplied" {
					return fmt.Errorf("incomplete group inverse")
				}
			} else if m.Status == "undone" {
				return fmt.Errorf("forward group cannot claim an inverse")
			}
			if m.Status == "noop" || m.Status == "unattempted" || m.Status == "unapplied" {
				if m.State.Revision != g.Members[i].Before.Revision {
					return fmt.Errorf("unchanged group member revision differs from starting observation")
				}
			}
			needs := m.Status == "applied" || m.Status == "observed" || m.Status == "undone" || m.Status == "unapplied"
			if needs != (m.ReceiptID != "") {
				return fmt.Errorf("group member receipt evidence is inconsistent")
			}
			if m.ReceiptID != "" {
				if _, err := CommentNumber(m.ReceiptID); err != nil {
					return err
				}
			}
		}
	}
	return a.validateOrderedGroup()
}

func validateGroupRewrite(old, next ActionJournal) error {
	if old.GroupOperationID != next.GroupOperationID {
		return fmt.Errorf("member group identity is immutable")
	}
	if old.Group == nil || next.Group == nil {
		if old.Group != nil || next.Group != nil {
			return fmt.Errorf("group identity is immutable")
		}
		return nil
	}
	if err := validateOrderedGroupRewrite(old.Group.Ordered, next.Group.Ordered); err != nil {
		return err
	}
	if old.Group.Command != next.Group.Command || old.Group.Cascade != next.Group.Cascade || !slices.Equal(old.Group.Roots, next.Group.Roots) || !reflect.DeepEqual(old.Group.Members, next.Group.Members) {
		return fmt.Errorf("group command membership is immutable")
	}
	if old.Group.Forward != nil && !reflect.DeepEqual(old.Group.Forward, next.Group.Forward) {
		return fmt.Errorf("group forward observation is immutable")
	}
	if old.Group.Inverse != nil && !reflect.DeepEqual(old.Group.Inverse, next.Group.Inverse) {
		return fmt.Errorf("group inverse observation is immutable")
	}
	if old.UndoOperationID != "" && old.UndoOperationID != next.UndoOperationID {
		return fmt.Errorf("group undo identity is immutable")
	}
	return nil
}

func groupNativeAuthor(a ActionJournal, author string) bool {
	return a.Group == nil || (a.Group.Forward == nil || strings.EqualFold(a.Group.Forward.Actor, author)) && (a.Group.Inverse == nil || strings.EqualFold(a.Group.Inverse.Actor, author))
}

func equalGroupMemberResults(a, b []ActionGroupResultMember) bool {
	if len(a) != len(b) {
		return false
	}
	for i, m := range a {
		n := b[i]
		if m.IssueID != n.IssueID || m.ReceiptID != n.ReceiptID || m.Status != n.Status || m.State.Revision != n.State.Revision {
			return false
		}
	}
	return true
}
