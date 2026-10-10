package ghstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

type groupUndoContextKey struct{}
type groupUndoGate struct {
	Plan         func(*ActionJournalRecord, *Record, string, string) (*IssueUndoPlan, error)
	UpdateFence  func(*ActionJournalRecord, *Record) error
	CommentFence func(*ActionJournalRecord, *Record, bool) error
	OperationID  string
	Fence        func() error
	Member       func(*ActionJournalRecord) error
}

func checkGroupUpdateUndoGate(ctx context.Context, member *ActionJournalRecord, current *Record) error {
	gate, _ := ctx.Value(groupUndoContextKey{}).(*groupUndoGate)
	if gate != nil && gate.UpdateFence != nil {
		return gate.UpdateFence(member, current)
	}
	return nil
}

func groupUndoContext(ctx context.Context) string {
	gate, _ := ctx.Value(groupUndoContextKey{}).(*groupUndoGate)
	if gate == nil {
		return ""
	}
	return gate.OperationID
}
func checkGroupUndoMember(ctx context.Context, a *ActionJournalRecord) error {
	gate, _ := ctx.Value(groupUndoContextKey{}).(*groupUndoGate)
	if gate != nil && gate.Member != nil {
		return gate.Member(a)
	}
	return nil
}
func checkGroupUndoGate(ctx context.Context) error {
	gate, _ := ctx.Value(groupUndoContextKey{}).(*groupUndoGate)
	if gate != nil && gate.Fence != nil {
		return gate.Fence()
	}
	return nil
}

func (c *Client) undoActionGroup(ctx context.Context, a *ActionJournalRecord, session string) (*ActionUndoResult, error) {
	if a.Group.updateActivityGroup() {
		return c.undoUpdateActivity(ctx, a, session)
	}
	if a.Group.Ordered != nil {
		return c.undoOrderedGroup(ctx, a, session)
	}
	if a.SessionID != session {
		return nil, fmt.Errorf("group undo requires its original session")
	}
	if a.Phase == "prepared" {
		return nil, &UndoScopeError{Reason: "group forward result is unverified; use undo --reconcile <group receipt> --apply before undo; no target write attempted"}
	}
	if _, err := c.journalCarrier(ctx, a.CarrierID); err != nil {
		return nil, err
	}
	profile, err := c.requestAPI(ctx, "GET", "user", "/user", nil, false)
	if err != nil {
		return nil, err
	}
	var user struct{ Login string }
	if json.Unmarshal(profile, &user) != nil || user.Login == "" || !strings.EqualFold(user.Login, a.Author) {
		return nil, fmt.Errorf("group undo is not attributed to authenticated GitHub author")
	}
	observed, err := c.observeActionGroup(ctx, a, false)
	if err != nil {
		return nil, err
	}
	if observed.Pending {
		return nil, fmt.Errorf("group has an uncertain inverse receipt; reconcile it without target replay before continuing")
	}
	root := observed.Records[a.IssueID]
	if a.Phase == "undone" {
		return &ActionUndoResult{Issue: &root, Journal: a, AlreadyUndone: true, GroupMembers: observed.Members, UndoOutcome: groupResultOutcome(a, true)}, nil
	}
	// Check every remaining member policy before the first inverse. No causal
	// bridging or sibling changes are accepted as equivalent current revisions.
	for _, m := range observed.Members {
		if m.Status != "applied" && m.Status != "observed" {
			continue
		}
		receipt := observed.Receipts[m.IssueID]
		current := observed.Records[m.IssueID]
		if receipt.Action == "comment_create" {
			if _, err := receipt.planActionUndo(&current, session, user.Login); err != nil {
				return nil, err
			}
			if err := c.readNativeComment(ctx, receipt.CommentAfter); err != nil {
				return nil, err
			}
			continue
		}
		if _, native, err := nativeUndoScope(receipt); err != nil {
			return nil, err
		} else if native {
			if err := c.preflightNativeGroupUndo(ctx, receipt, &current, session, user.Login); err != nil {
				return nil, err
			}
			continue
		}
		if err := scalarUndoScope(receipt); err != nil {
			return nil, err
		}
		if _, err := receipt.PlanIssueUndo(&current, session, user.Login); err != nil {
			return nil, err
		}
		if err := c.checkUndoOriginalReview(ctx, receipt, false); err != nil {
			return nil, err
		}
	}
	if a.Phase == "applied" {
		group := *a.Group
		pending := a.ActionJournal
		pending.Group = &group
		pending.Phase = "undo_prepared"
		pending.UndoOperationID = "td-op-" + rand.Text()
		written, err := c.writeGroupReceipt(ctx, a, pending, func() error {
			fresh, err := c.observeActionGroup(ctx, a, false)
			if err != nil {
				return err
			}
			if !sameGroupObservation(observed, fresh) {
				return fmt.Errorf("group changed before inverse intent")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		a = written
		observed, err = c.observeActionGroup(ctx, a, false)
		if err != nil {
			return nil, err
		}
	}
	expected := map[string]ActionIssueState{}
	for _, m := range observed.Members {
		expected[m.IssueID] = m.State
	}
	fence := func() error {
		body, err := renderExistingActionJournal(a)
		if err != nil {
			return err
		}
		number, err := CommentNumber(a.ID)
		if err != nil {
			return err
		}
		receipt, err := c.journalComment(ctx, fmt.Sprintf("/issues/comments/%d", number), nil, body, a.CarrierID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(receipt, a) {
			return fmt.Errorf("group inverse intent changed")
		}
		if a.Group.hasPlannedInputs() {
			if err := c.verifyPlannedUpdateEvents(ctx, a.Group); err != nil {
				return err
			}
		} else {
			if err := c.checkGroupNativeEventReceipts(ctx, observed.Receipts); err != nil {
				return err
			}
		}
		if len(a.Group.Roots) > 0 {
			return c.verifyGraphGroupExpected(ctx, a.Group, expected)
		}
		return c.verifyGroupExpected(ctx, expected)
	}
	gate := &groupUndoGate{OperationID: a.OperationID, Fence: fence, Member: func(member *ActionJournalRecord) error {
		if err := validateGroupMember(a, member); err != nil {
			return err
		}
		if old := observed.Receipts[member.IssueID]; old == nil || !reflect.DeepEqual(old, member) {
			return fmt.Errorf("group member receipt changed before inverse")
		}
		return nil
	}}
	if a.Group.activityGroup() {
		gate.CommentFence = func(member *ActionJournalRecord, current *Record, deleted bool) error {
			return c.checkActivityGroupInverse(ctx, a, observed.Receipts, expected, member, current, deleted)
		}
	}
	gated := context.WithValue(ctx, groupUndoContextKey{}, gate)
	// Reverse the declared command order. A failure retains all member receipts
	// and the original group undo identity; no automatic write retry is made.
	ordered := slices.Clone(observed.Members)
	if len(a.Group.Roots) > 0 {
		slices.SortStableFunc(ordered, func(x, y ActionGroupResultMember) int {
			xr, yr := observed.Receipts[x.IssueID], observed.Receipts[y.IssueID]
			if xr == nil && yr == nil {
				return 0
			}
			if xr == nil {
				return -1
			}
			if yr == nil {
				return 1
			}
			xn, _ := CommentNumber(xr.ID)
			yn, _ := CommentNumber(yr.ID)
			if xn < yn {
				return -1
			}
			if xn > yn {
				return 1
			}
			return 0
		})
	}
	for i := len(ordered) - 1; i >= 0; i-- {
		m := ordered[i]
		if m.Status == "noop" || m.Status == "unattempted" || m.Status == "unapplied" || m.Status == "undone" {
			continue
		}
		if err := fence(); err != nil {
			return nil, err
		}
		result, err := c.UndoAction(gated, m.ReceiptID, session)
		if err != nil {
			return nil, fmt.Errorf("group %s inverse stopped at %s; earlier inverses remain; inspect and reconcile before continuing: %w", a.ID, m.IssueID, err)
		}
		state, err := actionIssueState(result.Issue)
		if err != nil {
			return nil, err
		}
		expected[m.IssueID] = state
		observed.Receipts[m.IssueID] = result.Journal
	}
	result, err := c.reconcileActionGroup(ctx, a, session, user.Login, true, true)
	if err != nil {
		return nil, err
	}
	verified := true
	count := 0
	for _, m := range result.GroupMembers {
		if m.Status != "undone" {
			continue
		}
		count++
		receipt := result.Journal.Group.Inverse
		if receipt.Proof != "verified-group-receipts" {
			verified = false
		}
	}
	return &ActionUndoResult{Issue: result.Issue, Journal: result.Journal, GroupMembers: result.GroupMembers, UndoOutcome: &ActionUndoOutcome{Proof: result.Journal.Group.Inverse.Proof, TargetWriteVerified: verified && count > 0}}, nil
}

// Collapse members into one command-level history entry. Missing/foreign
// groups fail explicitly rather than letting one member be undone alone.
func CollapseActionGroups(actions []ActionJournalRecord) ([]ActionJournalRecord, error) {
	groups := map[string]ActionJournalRecord{}
	members := map[string][]*ActionJournalRecord{}
	for _, a := range actions {
		if a.Group != nil {
			groups[a.OperationID] = a
		}
	}
	for i := range actions {
		a := &actions[i]
		if a.GroupOperationID != "" {
			members[a.GroupOperationID] = append(members[a.GroupOperationID], a)
		}
	}
	for op, group := range groups {
		if group.Group.Ordered == nil && !group.Group.updateActivityGroup() {
			continue
		}
		if err := group.validate(); err != nil {
			return nil, err
		}
		chains, err := orderedGroupEffects(&group, members[op])
		if err != nil {
			return nil, err
		}
		if group.Group.updateActivityGroup() {
			if err := validateUpdateActivityForward(&group, chains[group.IssueID]); err != nil {
				return nil, err
			}
		}
		if group.Phase != "prepared" && group.Group.Ordered != nil {
			if len(members[op]) != len(group.Group.Ordered.Forward) {
				return nil, fmt.Errorf("ordered history lost its frozen original membership")
			}
			for _, frozen := range group.Group.Ordered.Forward {
				found := false
				for _, actual := range members[op] {
					if actual.ID == frozen.Record.ID {
						observed, err := orderedEffectSnapshot(actual, frozen.Status)
						if err != nil || !sameOrderedEffect(frozen, observed) {
							return nil, fmt.Errorf("ordered history original body, identity or result changed")
						}
						found = true
					}
				}
				if !found {
					return nil, fmt.Errorf("ordered history lost an original receipt")
				}
			}
		}
	}
	out := []ActionJournalRecord{}
	for _, a := range actions {
		if a.GroupOperationID != "" {
			group, found := groups[a.GroupOperationID]
			if !found {
				return nil, fmt.Errorf("member %s has no observed group receipt", a.ID)
			}
			if group.Group.Ordered == nil && !group.Group.updateActivityGroup() {
				if err := validateGroupMember(&group, &a); err != nil {
					return nil, err
				}
			}
			continue
		}
		out = append(out, a)
	}

	// Sort by the latest linked member's time and numeric ID without mutating
	// actual stored native receipt identity or timestamps.
	type order struct {
		at     time.Time
		number int64
	}
	ordering := map[string]order{}
	for _, a := range out {
		n, _ := CommentNumber(a.ID)
		ordering[a.OperationID] = order{a.CreatedAt, n}
	}
	for _, a := range actions {
		if a.GroupOperationID != "" {
			current := ordering[a.GroupOperationID]
			n, _ := CommentNumber(a.ID)
			if a.CreatedAt.After(current.at) || a.CreatedAt.Equal(current.at) && n > current.number {
				ordering[a.GroupOperationID] = order{a.CreatedAt, n}
			}
		}
	}
	slices.SortStableFunc(out, func(a, b ActionJournalRecord) int {
		oa, ob := ordering[a.OperationID], ordering[b.OperationID]
		if c := ob.at.Compare(oa.at); c != 0 {
			return c
		}
		if oa.number > ob.number {
			return -1
		}
		if oa.number < ob.number {
			return 1
		}
		return 0
	})
	return out, nil
}
