package ghstore

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// This is a private sizing projection, never published as native evidence.
// Every inverse step preserves a fixed comment collection and native identity;
// only mutable clocks and at most one close/reopen event can grow. Reserve the
// complete result before the inverse's first target write, using maximum-width
// clocks/event IDs and the original executor's known session identity.
// Forward execution checks each actual publication; it may stop with a capacity
// error after earlier effects. Predicting all future forward sizes is not required.
func checkImportInverseCapacity(a ActionJournal) error {
	if a.Phase != "undo_prepared" || a.ImportInverse == nil || a.ImportInverse.Started != 0 || len(a.ImportInverse.Results) != 0 {
		return fmt.Errorf("import inverse capacity requires an unstarted complete plan")
	}
	if _, err := planActionJournalPublication(a); err != nil {
		return err
	}
	probe, err := copyCreationJournal(a)
	if err != nil {
		return err
	}
	clock := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	actor := strings.Repeat("x", max(39, len(a.ImportForward.Observation.Actor)))
	observation := CreationObservation{Proof: "observed", SessionID: a.SessionID, Actor: actor, ObservedAt: clock}
	states := slices.Clone(probe.ImportForward.Targets)
	for _, step := range probe.ImportInverse.Steps {
		before := states[step.Member]
		state := before
		state.Issue = importInputNative(state.Issue, step.Input)
		state.Issue.UpdatedAt = clock
		state.Issue.ClosedAt = nil
		if state.Issue.State == "closed" {
			state.Issue.ClosedAt = &clock
		}
		if state.Issue.State != before.Issue.State {
			event := "closed"
			if state.Issue.State == "open" {
				event = "reopened"
			}
			state.Events = append(slices.Clone(before.Events), nativeStateEvent{ID: 1<<63 - 1, Event: event})
		}
		probe.ImportInverse.Results = append(probe.ImportInverse.Results, ImportInverseResult{State: state, Observation: observation})
		states[step.Member] = state
	}
	probe.Phase, probe.ImportInverse.Started = "undone", len(probe.ImportInverse.Steps)
	probe.ImportInverse.Result = &observation
	if _, err := planActionJournalPublication(probe); err != nil {
		return fmt.Errorf("import inverse final-result capacity (no inverse target write attempted): %w", err)
	}
	return nil
}
