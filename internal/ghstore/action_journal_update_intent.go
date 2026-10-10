package ghstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const actionUpdateIntentJournalStart = actionJournalPrefix + "v8\n"

// The requested contents have no invented server clock. Before remains an
// actual observation; completion must match all intended contents exactly.
type ActionUpdateInput struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	State  string   `json:"state"`
	Labels []string `json:"labels"`
}

type ActionRelatedObservation struct {
	ID    string           `json:"id"`
	State ActionIssueState `json:"state"`
}

// Event baselines are needed for native state changes and review checkpoints.
// A non-nil empty baseline differs from having no recorded event evidence.
type ActionUpdateIntent struct {
	Target       ActionUpdateInput          `json:"target"`
	NativeEvents *[]nativeStateEvent        `json:"native_events,omitempty"`
	Related      []ActionRelatedObservation `json:"related,omitempty"`
}

func (input ActionUpdateInput) contents() (ActionIssueState, error) {
	state := ActionIssueState{Title: input.Title, Body: input.Body, State: input.State, Labels: slices.Clone(input.Labels)}
	var err error
	state.Revision, err = state.checksum()
	return state, err
}

func updateIntentNeedsEvents(before, target ActionIssueState) bool {
	if before.State != target.State {
		return true
	}
	for _, state := range []ActionIssueState{before, target} {
		_, meta, _, err := decodeBody(state.Body)
		if err == nil && meta.Details != nil && (meta.Details.ReviewBasis != "" || meta.Details.ReviewEvents != "") {
			return true
		}
	}
	return false
}

func (a ActionJournal) validateUpdateIntent() error {
	i := a.UpdateIntent
	if i == nil {
		return nil
	}
	if a.Before == nil || !slices.Contains([]string{"update", "note", "board"}, a.Action) || a.CreateInput != nil || a.CommentInput != nil || a.CommentAfter != nil || a.CommentBefore != nil || a.Reconciliation != nil {
		return fmt.Errorf("update intent requires one issue or auxiliary update")
	}
	if strings.TrimSpace(i.Target.Title) == "" || (i.Target.State != "open" && i.Target.State != "closed") || i.Target.Labels == nil {
		return fmt.Errorf("update intent requires complete native contents and a label array")
	}
	want, err := i.Target.contents()
	if err != nil {
		return err
	}
	// Decode the expected metadata using the original observed clock only for
	// validation; that clock is never represented as the requested result.
	probe := want
	probe.UpdatedAt = a.Before.UpdatedAt
	probe.Revision, err = probe.checksum()
	if err != nil {
		return err
	}
	if err := probe.validate(); err != nil {
		return err
	}
	if _, err := actionStateRecord(&probe, a.IssueID); err != nil {
		return fmt.Errorf("update intent target metadata is invalid: %w", err)
	}
	_, beforeMeta, _, err := decodeBody(a.Before.Body)
	if err != nil {
		return err
	}
	_, targetMeta, _, err := decodeBody(want.Body)
	if err != nil || beforeMeta.EntityKind != targetMeta.EntityKind {
		return fmt.Errorf("update intent cannot change its entity kind")
	}
	entityProbe := a
	entityProbe.After, entityProbe.Restored = &probe, nil
	if err := entityProbe.validateEntityAction(); err != nil {
		return err
	}
	if a.After != nil && (!sameActionContents(want, *a.After) || a.After.UpdatedAt.Before(a.Before.UpdatedAt)) {
		return fmt.Errorf("update result differs from its recorded full input or reverses its clock")
	}
	if updateIntentNeedsEvents(*a.Before, want) && i.NativeEvents == nil {
		return fmt.Errorf("update intent lacks its required native event baseline")
	}
	if i.NativeEvents != nil {
		if *i.NativeEvents == nil {
			return fmt.Errorf("update event baseline must be an array")
		}
		last := int64(0)
		for _, event := range *i.NativeEvents {
			if event.ID <= last || (event.Event != "closed" && event.Event != "reopened") {
				return fmt.Errorf("update event baseline is invalid")
			}
			last = event.ID
		}
	}
	seen := map[string]bool{a.IssueID: true, a.CarrierID: true}
	for _, parent := range i.Related {
		if number, err := Number(parent.ID); err != nil || parent.ID != fmt.Sprintf("gh-%d", number) || seen[parent.ID] {
			return fmt.Errorf("update parent observations have invalid or repeated identities")
		}
		seen[parent.ID] = true
		if err := parent.State.validate(); err != nil {
			return err
		}
		if _, err := actionStateRecord(&parent.State, parent.ID); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) plannedUpdateIntent(ctx context.Context, before *Record, payload map[string]any, parents []Record) (*ActionUpdateIntent, error) {
	state, err := actionIssueState(before)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var patch struct {
		Title, Body, State *string
		Labels             *[]string
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&patch); err != nil {
		return nil, fmt.Errorf("update input is invalid: %w", err)
	}
	input := ActionUpdateInput{Title: state.Title, Body: state.Body, State: state.State, Labels: append([]string{}, state.Labels...)}
	if patch.Title != nil {
		input.Title = *patch.Title
	}
	if patch.Body != nil {
		input.Body = *patch.Body
	}
	if patch.State != nil {
		input.State = *patch.State
	}
	if patch.Labels != nil {
		input.Labels = append([]string{}, (*patch.Labels)...)
	}
	intent := &ActionUpdateIntent{Target: input}
	want, err := input.contents()
	if err != nil {
		return nil, err
	}
	if updateIntentNeedsEvents(state, want) {
		events, err := c.readStateEvents(withReviewCost(ctx), before.ID)
		if err != nil {
			return nil, fmt.Errorf("record update event baseline (no target write attempted): %w", err)
		}
		intent.NativeEvents = &events
	}
	for _, parent := range parents {
		observed, err := actionIssueState(&parent)
		if err != nil {
			return nil, err
		}
		intent.Related = append(intent.Related, ActionRelatedObservation{ID: parent.ID, State: observed})
	}
	_, bm, _, _ := decodeBody(state.Body)
	_, tm, _, _ := decodeBody(want.Body)
	bd, td := IssueDetails{}, IssueDetails{}
	if bm.Details != nil {
		bd = *bm.Details
	}
	if tm.Details != nil {
		td = *tm.Details
	}
	// Dependency writers already captured and fenced their target graph. Reuse
	// those actual observations instead of adding a bulk read or validating an
	// unchanged legacy edge. Generic detail writes collect only newly added roots.
	added := []string{}
	for _, id := range td.Dependencies {
		if !slices.Contains(bd.Dependencies, id) {
			added = append(added, id)
		}
	}
	if len(parents) == 0 && len(added) > 0 {
		observations, err := c.dependencyObservations(ctx, before.ID, added)
		if err != nil {
			return nil, err
		}
		for _, record := range observations {
			state, err := actionIssueState(&record)
			if err != nil {
				return nil, err
			}
			intent.Related = append(intent.Related, ActionRelatedObservation{ID: record.ID, State: state})
		}
	}
	if bm.Board != nil && tm.Board != nil {
		old := map[string]BoardPosition{}
		for _, position := range bm.Board.Positions {
			old[position.IssueID] = position
		}
		wanted := []string{}
		for _, position := range tm.Board.Positions {
			previous, found := old[position.IssueID]
			if !found || !previous.AddedAt.Equal(position.AddedAt) {
				wanted = append(wanted, position.IssueID)
			}
		}
		// Position removal/respace can deliberately retain or clean up stale
		// tasks. Only the task explicitly added/moved needs membership proof.
		if len(wanted) > 0 {
			snapshot, err := c.ReadSnapshot(WithFreshSnapshot(ctx), false)
			if err != nil {
				return nil, fmt.Errorf("record board position targets (no target write attempted): %w", err)
			}
			all := map[string]Record{}
			for _, record := range snapshot.Issues(true) {
				all[record.ID] = record
			}
			for _, id := range wanted {
				record, found := all[id]
				if !found || record.DeletedAt != nil {
					return nil, fmt.Errorf("board position target %s is missing or deleted; no target write attempted", id)
				}
				observed, err := actionIssueState(&record)
				if err != nil {
					return nil, err
				}
				intent.Related = append(intent.Related, ActionRelatedObservation{ID: id, State: observed})
			}
		}
	}
	unique := map[string]ActionRelatedObservation{}
	for _, related := range intent.Related {
		if prior, found := unique[related.ID]; found && prior.State.Revision != related.State.Revision {
			return nil, &ConflictError{ID: related.ID}
		}
		unique[related.ID] = related
	}
	intent.Related = nil
	for _, related := range unique {
		intent.Related = append(intent.Related, related)
	}
	slices.SortFunc(intent.Related, func(a, b ActionRelatedObservation) int { return strings.Compare(a.ID, b.ID) })
	return intent, nil
}
