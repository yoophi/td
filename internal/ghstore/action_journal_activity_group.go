package ghstore

import (
	"context"
	"crypto/rand"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/marcus/td/internal/models"
)

const actionActivityGroupJournalStart = actionJournalPrefix + "v19\n"

func (g *ActionGroup) activityGroup() bool {
	return g.Command == "ws-log" || g.Command == "ws-handoff"
}

func (a ActionJournal) validateActivityGroupMemberInput(m ActionGroupMember) error {
	if a.Group.activityGroup() != (m.CommentInput != nil) {
		return fmt.Errorf("activity group requires every immutable comment input")
	}
	if m.CommentInput == nil {
		return nil
	}
	if a.Group.Ordered != nil || a.Group.Planned || m.Input != nil || m.Noop {
		return fmt.Errorf("activity group cannot claim task effects or no-ops")
	}
	probe := ActionJournal{OperationID: "input-probe", SessionID: a.SessionID, IssueID: m.IssueID, CarrierID: a.CarrierID,
		Action: "comment_create", Phase: "prepared", Before: &m.Before, CommentInput: m.CommentInput}
	if err := probe.validate(); err != nil {
		return err
	}
	activity, err := decodeActivity(apiComment{ID: 1, Body: m.CommentInput.Body}, m.IssueID)
	if err != nil || activity.Kind != strings.TrimPrefix(a.Group.Command, "ws-") || activity.WorkSessionID == "" {
		return fmt.Errorf("activity group command or work-session differs from its input")
	}
	first, err := decodeActivity(apiComment{ID: 1, Body: a.Group.Members[0].CommentInput.Body}, a.Group.Members[0].IssueID)
	if err != nil || first.OperationID != activity.OperationID || first.WorkSessionID != activity.WorkSessionID {
		return fmt.Errorf("activity group replaced its shared operation or work-session")
	}
	return nil
}

type ActivityGroupInput struct {
	Issue    *Record
	Activity models.Activity
}

// BeginActivityGroup fixes complete per-target activities before any journal
// bootstrap or comment POST. Local work-session state remains the caller's.
func (c *Client) BeginActivityGroup(ctx context.Context, command string, inputs []ActivityGroupInput) error {
	g := &ActionGroup{Command: command}
	if len(inputs) < 2 || c.journal == nil || c.journal.Group != nil || !g.activityGroup() {
		return fmt.Errorf("activity group requires tracked work-session inputs and multiple targets; no write attempted")
	}
	expected := map[string]ActionIssueState{}
	for _, input := range inputs {
		r := input.Issue
		if r == nil || r.repository != c.repo || r.revision == ([32]byte{}) || r.DeletedAt != nil || input.Activity.SessionID != c.journal.SessionID || input.Activity.OperationID == "" {
			return fmt.Errorf("activity group requires original live observations and complete attribution; no write attempted")
		}
		if _, exists := expected[r.ID]; exists {
			return fmt.Errorf("activity group repeats %s; no write attempted", r.ID)
		}
		before, err := actionIssueState(r)
		if err != nil {
			return err
		}
		data, body, err := prepareActivityInput(r.ID, input.Activity)
		if err != nil {
			return err
		}
		g.Members = append(g.Members, ActionGroupMember{IssueID: r.ID, Before: before, CommentInput: &ActionCommentInput{Body: body, OperationID: data.OperationID}})
		expected[r.ID] = before
	}
	carrier := c.journal.Carrier.ID
	if carrier == "" {
		for n := 2147483647; ; n-- {
			carrier = fmt.Sprintf("gh-%d", n)
			if _, found := expected[carrier]; !found {
				break
			}
		}
	}
	a := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: c.journal.SessionID, IssueID: g.Members[0].IssueID, CarrierID: carrier, Action: "group", Phase: "prepared", Group: g}
	if err := checkGroupArchiveCapacity(a, expected); err != nil {
		return err
	}
	for _, m := range g.Members {
		probe := ActionJournal{OperationID: "td-op-" + rand.Text(), SessionID: a.SessionID, IssueID: m.IssueID, CarrierID: carrier, Action: "comment_create", Phase: "prepared", Before: &m.Before, CommentInput: m.CommentInput, GroupOperationID: a.OperationID}
		if err := checkActivityGroupMemberCapacity(probe); err != nil {
			return err
		}
	}
	snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true)
	if err != nil {
		return err
	}
	if _, err := snapshot.ActionHistory(""); err != nil {
		return err
	}
	for _, entry := range snapshot.entries {
		id := fmt.Sprintf("gh-%d", entry.Issue.Number)
		for _, m := range g.Members {
			if m.IssueID != id {
				continue
			}
			for _, comment := range entry.Comments {
				activity, err := decodeActivity(comment, id)
				if err != nil {
					return err
				}
				if activity.Kind != "action_journal" && activity.OperationID == m.CommentInput.OperationID {
					return fmt.Errorf("activity operation already exists on %s; no write attempted", id)
				}
			}
		}
	}
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return err
	}
	if err := c.EnableActionJournal(ctx, a.SessionID); err != nil {
		return err
	}
	a.CarrierID = c.journal.Carrier.ID
	if err := c.verifyGroupExpected(ctx, expected); err != nil {
		return err
	}
	root, err := c.writePreparedJournal(ctx, a)
	if err != nil {
		return err
	}
	c.journal.Group = &activeActionGroup{Journal: root, Expected: expected, Attempted: map[string]bool{}}
	return nil
}

func checkActivityGroupMemberCapacity(a ActionJournal) error {
	state := *a.Before
	state.UpdatedAt = state.UpdatedAt.Add(time.Second)
	state.Revision, _ = state.checksum()
	comment := &ActionCommentState{ID: "ghc-9223372036854775807", IssueID: a.IssueID, Body: a.CommentInput.Body, Author: strings.Repeat("x", 39), CreatedAt: state.UpdatedAt, UpdatedAt: state.UpdatedAt}
	comment.Revision, _ = comment.checksum()
	a.Phase, a.After, a.CommentAfter = "applied", &state, comment
	a.CommentObservation = &ActionCommentObservation{SessionID: a.SessionID, Actor: comment.Author, ObservedAt: state.UpdatedAt, Proof: commentObservationProof, State: &state}
	if err := checkJournalSizingProbe(a); err != nil {
		return err
	}
	a.Phase, a.UndoOperationID = "undone", "td-op-"+rand.Text()
	restored := state
	restored.UpdatedAt = state.UpdatedAt.Add(time.Second)
	restored.Revision, _ = restored.checksum()
	a.Restored = &restored
	a.UndoObservation = &ActionUndoObservation{SessionID: a.SessionID, Actor: comment.Author, ObservedAt: restored.UpdatedAt, Proof: inverseObservationProof, State: &restored}
	return checkJournalSizingProbe(a)
}

func (c *Client) bindActivityGroupIntent(a *ActionJournal) error {
	if c.journal.Group == nil {
		return nil
	}
	active := c.journal.Group
	if !active.Journal.Group.activityGroup() || active.Attempted[a.IssueID] {
		return fmt.Errorf("activity is outside the current command or was already attempted; no comment POST attempted")
	}
	for _, m := range active.Journal.Group.Members {
		if m.IssueID == a.IssueID {
			if a.Before.Revision != m.Before.Revision || !reflect.DeepEqual(a.CommentInput, m.CommentInput) {
				return fmt.Errorf("activity differs from original group observation or input; no comment POST attempted")
			}
			a.GroupOperationID = active.Journal.OperationID
			return nil
		}
	}
	return fmt.Errorf("activity target was not selected; no comment POST attempted")
}
