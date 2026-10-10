package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type activeActionGroup struct {
	Plans        map[string]*issueUpdatePlan
	Journal      *ActionJournalRecord
	Expected     map[string]ActionIssueState
	Attempted    map[string]bool
	Receipts     []*ActionJournalRecord
	NativeEvents map[string][]nativeStateEvent
}

// BeginActionGroup declares explicit membership before any target changes.
// Calls with one ID keep the existing single-target receipt contract.
func (c *Client) BeginActionGroup(ctx context.Context, command string, ids []string) error {
	if command == "review" {
		return c.beginReviewActionGroup(ctx, ids)
	}
	return c.beginBasicActionGroup(ctx, command, ids, nil)
}

// BeginObservedActionGroup keeps a sweep or work-session selection tied to the
// observations that authorized it. A fresh group read cannot adopt a new owner
// or changed task in place of a selected observation.
func (c *Client) BeginObservedActionGroup(ctx context.Context, command string, records []Record) error {
	ids := make([]string, 0, len(records))
	observed := make(map[string]ActionIssueState, len(records))
	for i := range records {
		r := &records[i]
		if r.repository != c.repo || r.revision == ([32]byte{}) {
			return fmt.Errorf("group requires observations from this repository; no target write attempted")
		}
		if _, duplicate := observed[r.ID]; duplicate {
			return fmt.Errorf("group repeats %s; no target write attempted", r.ID)
		}
		state, err := actionIssueState(r)
		if err != nil {
			return err
		}
		ids = append(ids, r.ID)
		observed[r.ID] = state
	}
	return c.beginBasicActionGroup(ctx, command, ids, observed)
}

func (c *Client) beginBasicActionGroup(ctx context.Context, command string, ids []string, observed map[string]ActionIssueState) error {
	if len(ids) < 2 {
		return nil
	}
	if c.journal == nil || c.journal.Group != nil || !groupBasicCommand(command) {
		return fmt.Errorf("group requires tracked basic explicit transitions; no target write attempted")
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return err
	}
	records := map[string]Record{}
	for _, r := range snapshot.Issues(true) {
		records[r.ID] = r
	}
	g := &ActionGroup{Command: command, Members: []ActionGroupMember{}}
	expected := map[string]ActionIssueState{}
	for _, id := range ids {
		if !canonicalGroupID(id) {
			return fmt.Errorf("group requires canonical issue IDs; no target write attempted")
		}
		if _, duplicate := expected[id]; duplicate {
			return fmt.Errorf("group repeats %s; no target write attempted", id)
		}
		record, found := records[id]
		if !found || record.DeletedAt != nil {
			return fmt.Errorf("group member %s missing or deleted; no target write attempted", id)
		}
		state, err := actionIssueState(&record)
		if err != nil {
			return err
		}
		if previous, pinned := observed[id]; pinned && previous.Revision != state.Revision {
			return &ConflictError{ID: id}
		}
		if state.State != "open" {
			return &UndoScopeError{Reason: "basic group native-state changes need their own compensation integration; no target write attempted"}
		}
		expected[id] = state
		g.Members = append(g.Members, ActionGroupMember{IssueID: id, Before: state})
	}
	carrier := c.journal.Carrier.ID
	if carrier == "" {
		carrier = "gh-2147483647"
		for _, id := range ids {
			if id == carrier {
				carrier = "gh-2147483646"
			}
		}
	}
	a := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID, IssueID: ids[0], CarrierID: carrier, Action: "group", Phase: "prepared", Group: g}
	if err := checkGroupArchiveCapacity(a, expected); err != nil {
		return fmt.Errorf("group capacity preflight (no target write attempted): %w", err)
	}
	if err := c.EnableActionJournal(ctx, c.journal.SessionID); err != nil {
		return err
	}
	a.CarrierID = c.journal.Carrier.ID
	// Bootstrap never replaces the original member observations.
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return err
	}
	journal, err := c.writePreparedJournal(ctx, a)
	if err != nil {
		return err
	}
	c.journal.Group = &activeActionGroup{Journal: journal, Expected: expected, Attempted: map[string]bool{}}
	return nil
}

func checkGroupArchiveCapacity(a ActionJournal, expected map[string]ActionIssueState) error {
	if _, err := planActionJournalPublication(a); err != nil {
		return err
	}
	group := *a.Group
	if group.updateActivityGroup() {
		input := *group.UpdateActivity
		input.ForwardDigests = []string{strings.Repeat("f", 64), strings.Repeat("f", 64)}
		group.UpdateActivity = &input
	}
	if group.handoffReviewGroup() {
		input := *group.HandoffReview
		input.ForwardDigests = make([]string, len(group.handoffEffects()))
		for i := range input.ForwardDigests {
			input.ForwardDigests[i] = strings.Repeat("f", 64)
		}
		group.HandoffReview = &input
	}
	observation := &ActionGroupObservation{SessionID: a.SessionID, Actor: strings.Repeat("x", 39), ObservedAt: time.Now().UTC(), Proof: "group-contents-observed", Members: []ActionGroupResultMember{}}
	for _, m := range group.Members {
		result := ActionGroupResultMember{IssueID: m.IssueID, ReceiptID: "ghc-9223372036854775807", Status: "applied", State: expected[m.IssueID]}
		if m.Noop {
			result.ReceiptID, result.Status, result.State = "", "noop", m.Before
		}
		observation.Members = append(observation.Members, result)
	}
	group.Forward = observation
	probe := a
	probe.Phase = "applied"
	probe.Group = &group
	if err := checkJournalSizingProbe(probe); err != nil {
		return err
	}
	// Reserve the eventual group inverse receipt too; these are never stored
	// observations and use only already observed states for sizing.
	inverse := *observation
	inverse.Members = append([]ActionGroupResultMember{}, observation.Members...)
	for i := range inverse.Members {
		if group.Members[i].Noop {
			continue
		}
		inverse.Members[i].Status = "undone"
		state, err := projectGroupInverseState(a, group.Members[i], expected[group.Members[i].IssueID])
		if err != nil {
			return err
		}
		inverse.Members[i].State = state
	}
	group.Inverse = &inverse
	probe.Phase = "undone"
	probe.UndoOperationID = "td-op-" + strings.Repeat("x", 32)
	return checkJournalSizingProbe(probe)
}

func (c *Client) verifyGroupExpected(ctx context.Context, expected map[string]ActionIssueState) error {
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
	if err != nil {
		return err
	}
	actual := map[string]ActionIssueState{}
	for _, record := range snapshot.Issues(true) {
		if _, watched := expected[record.ID]; watched {
			state, err := actionIssueState(&record)
			if err != nil {
				return err
			}
			actual[record.ID] = state
		}
	}
	for id, state := range expected {
		now, found := actual[id]
		if !found || now.Revision != state.Revision {
			return &ConflictError{ID: id}
		}
	}
	return nil
}

func validateGroupMember(group *ActionJournalRecord, member *ActionJournalRecord) error {
	if group == nil || group.Group == nil || member == nil {
		return fmt.Errorf("member requires original group and receipt")
	}
	var before *ActionIssueState
	for _, m := range group.Group.Members {
		if m.IssueID == member.IssueID {
			copy := m.Before
			before = &copy
		}
	}
	return validateGroupMemberAtState(group, member, before)
}

// The sequence validator supplies only an actual initial or predecessor After
// observation. The legacy member contract always uses immutable initial state.
func validateGroupMemberAtState(group *ActionJournalRecord, member *ActionJournalRecord, before *ActionIssueState) error {
	if group.Group == nil || member.GroupOperationID != group.OperationID || member.Before == nil || member.SessionID != group.SessionID || member.CarrierID != group.CarrierID || !strings.EqualFold(member.Author, group.Author) {
		return fmt.Errorf("member receipt differs from original group scope or native author")
	}
	groupNumber, groupErr := CommentNumber(group.ID)
	memberNumber, memberErr := CommentNumber(member.ID)
	if member.ID != "" && (groupErr != nil || memberErr != nil || memberNumber <= groupNumber || member.CreatedAt.Before(group.CreatedAt)) {
		return fmt.Errorf("member attempt predates group intent")
	}
	if err := member.Before.validate(); err != nil {
		return err
	}
	if before == nil || member.Before.Revision != before.Revision {
		return fmt.Errorf("member initial revision differs from immutable group membership")
	}
	if group.Group.updateActivityGroup() {
		return validateUpdateActivityMember(group, member)
	}
	if group.Group.handoffReviewGroup() {
		return validateHandoffReviewMember(group, member)
	}
	if member.GroupMixed || member.GroupContract != "" {
		return fmt.Errorf("mixed member is outside an update/activity command")
	}
	if group.Group.activityGroup() {
		for _, m := range group.Group.Members {
			if m.IssueID == member.IssueID && member.Action == "comment_create" && member.UpdateIntent == nil && m.CommentInput != nil && reflect.DeepEqual(m.CommentInput, member.CommentInput) {
				return member.validate()
			}
		}
		return fmt.Errorf("comment member differs from immutable activity group input")
	}
	if member.Action != "update" || member.UpdateIntent == nil {
		return fmt.Errorf("task group member requires a full update input")
	}
	after, err := member.UpdateIntent.Target.contents()
	if err != nil {
		return err
	}
	after.UpdatedAt = member.Before.UpdatedAt
	after.Revision, err = after.checksum()
	if err != nil {
		return err
	}
	probe := *member
	probe.After = &after
	if group.Group.hasPlannedInputs() {
		for _, m := range group.Group.Members {
			if m.IssueID == member.IssueID {
				if m.Noop || m.Input == nil || !reflect.DeepEqual(m.Input, member.UpdateIntent) {
					return fmt.Errorf("member input differs from immutable planned group input")
				}
				return plannedTaskEffectScope(group.Group.Command, &probe)
			}
		}
		return fmt.Errorf("planned update is not a declared group member")
	}
	matched, err := workflowUndoScope(&probe)
	if group.Group.Command == "review" {
		matched, err = reviewGroupMemberScope(group.Group, &probe)
	}
	if nativeGroupCommand(group.Group.Command) {
		matched, err = nativeGroupMemberScope(group.Group, &probe)
	}
	if err != nil || !matched {
		return &UndoScopeError{Reason: "member input is not the complete declared basic transition effect; no target write attempted"}
	}
	_, meta, _, _ := decodeBody(after.Body)
	tr := meta.Details.Transitions[len(meta.Details.Transitions)-1]
	if tr.Action != group.Group.Command && !group.Group.Cascade {
		return fmt.Errorf("member transition differs from declared group command")
	}
	return nil
}

func (c *Client) attachActionGroupMember(ctx context.Context, a *ActionJournal) error {
	if c.journal == nil || c.journal.Group == nil {
		return nil
	}
	active := c.journal.Group
	if active.Journal.Group.handoffReviewGroup() {
		return c.bindHandoffReviewMember(a)
	}
	if active.Journal.Group.updateActivityGroup() {
		return c.bindUpdateActivityMember(a)
	}
	if active.Journal.Group.Ordered != nil {
		return c.attachOrderedGroupMember(ctx, a)
	}
	if active.Attempted[a.IssueID] {
		return fmt.Errorf("group target %s was already attempted; no target write attempted", a.IssueID)
	}
	a.GroupOperationID = active.Journal.OperationID
	probe := &ActionJournalRecord{ActionJournal: *a, Author: active.Journal.Author}
	if err := validateGroupMember(active.Journal, probe); err != nil {
		return err
	}
	sizing := map[string]ActionIssueState{}
	for id, state := range active.Expected {
		sizing[id] = state
	}
	state, err := a.UpdateIntent.Target.contents()
	if err != nil {
		return err
	}
	state.UpdatedAt = a.Before.UpdatedAt
	state.Revision, err = state.checksum()
	if err != nil {
		return err
	}
	sizing[a.IssueID] = state
	if err := checkGroupArchiveCapacity(active.Journal.ActionJournal, sizing); err != nil {
		return fmt.Errorf("group completion capacity preflight (no target write attempted): %w", err)
	}
	if len(active.Journal.Group.Roots) > 0 {
		return c.verifyGraphGroupExpected(ctx, active.Journal.Group, active.Expected)
	}
	return c.verifyGroupExpected(ctx, active.Expected)
}

// CompleteActionGroup is called only after all command callbacks succeeded.
// It retains actual per-member receipts and writes only the group completion.
func (c *Client) CompleteActionGroup(ctx context.Context) (*ActionReconcileResult, error) {
	if c.journal == nil || c.journal.Group == nil {
		return nil, nil
	}
	active := c.journal.Group
	result, err := c.reconcileActionGroup(ctx, active.Journal, c.journal.SessionID, active.Journal.Author, true, true)
	if err != nil {
		return nil, fmt.Errorf("group %s completion unverified; inspect before retrying (earlier target changes remain): %w", active.Journal.ID, err)
	}
	c.journal.Group = nil
	return result, nil
}
