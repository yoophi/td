package ghstore

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func workflowUpdateModel(t *testing.T) ActionJournal {
	t.Helper()
	a := updateCommentsModel(t)
	g := a.Group
	inputs := g.UpdateComments.Inputs
	g.Command, g.UpdateComments, g.Ordered = workflowUpdateContract, nil, &ActionOrderedGroup{}
	w := &ActionWorkflowUpdates{Status: models.StatusInProgress, Options: TransitionOptions{SessionID: a.SessionID, Mode: reviewpolicy.ModeTrusted}, Baselines: map[string][]nativeStateEvent{}}
	g.WorkflowUpdates = w
	for i, m := range g.Members {
		w.Selected = append(w.Selected, m.IssueID)
		w.Baselines[m.IssueID] = []nativeStateEvent{}
		events := []nativeStateEvent{}
		inputs[i].Update.NativeEvents = &events
		w.Steps = append(w.Steps, ActionWorkflowStep{Kind: "fields", IssueID: m.IssueID, Update: inputs[i].Update})
	}
	for _, m := range g.Members {
		w.Steps = append(w.Steps, ActionWorkflowStep{Kind: "workflow", IssueID: m.IssueID})
	}
	for i, m := range g.Members {
		w.Steps = append(w.Steps, ActionWorkflowStep{Kind: "comment", IssueID: m.IssueID, Comment: inputs[i].Comment})
	}
	return a
}

func TestWorkflowHistoricalHandoffRemovalRequiresOwnObservedInverse(t *testing.T) {
	for _, mode := range []string{"completed", "pending-applied", "no-input", "not-observed", "still-present", "pending-unapplied", "existing", "other-task", "changed-clock", "changed-comment", "later-creation", "forward-only"} {
		t.Run(mode, func(t *testing.T) {
			root := &ActionJournalRecord{ActionJournal: workflowUpdateModel(t)}
			root.Phase = "undo_prepared"
			input := &ActionCommentInput{OperationID: "automatic-handoff", Body: "frozen handoff"}
			comment := &ActionCommentState{ID: "ghc-301", IssueID: root.IssueID, Body: input.Body, UpdatedAt: time.Unix(100, 0).UTC()}
			root.Group.WorkflowUpdates.Handoffs = map[string]ActionWorkflowHandoff{root.IssueID: {Create: input}}
			created := &ActionJournalRecord{ID: "ghc-401", ActionJournal: ActionJournal{IssueID: root.IssueID, GroupSequence: 1, CommentInput: input, CommentAfter: comment}}
			effect := &ActionJournalRecord{ActionJournal: ActionJournal{IssueID: root.IssueID, GroupSequence: 3}}
			root.Group.Ordered.Forward = []OrderedGroupEffect{{Status: "applied", Record: created}}
			step := &OrderedGroupUndoStep{ForwardReceiptID: created.ID, Comment: comment, Result: &root.Group.Members[0].Before}
			root.Group.Ordered.Steps = []*OrderedGroupUndoStep{step}
			observed := &orderedGroupObservation{Comments: map[string]*ActionCommentState{created.ID: nil}}
			details := &IssueDetails{ReviewHandoffID: comment.ID, ReviewHandoffUpdatedAt: comment.UpdatedAt}
			switch mode {
			case "pending-applied":
				step.Result, observed.PendingStep, observed.PendingApplied = nil, true, true
			case "no-input":
				root.Group.Ordered.Steps = nil
			case "not-observed":
				delete(observed.Comments, created.ID)
			case "still-present":
				observed.Comments[created.ID] = comment
			case "pending-unapplied":
				step.Result, observed.PendingStep = nil, true
			case "existing":
				root.Group.WorkflowUpdates.Handoffs[root.IssueID] = ActionWorkflowHandoff{Existing: comment}
			case "other-task":
				created.IssueID = "gh-999"
			case "changed-clock":
				details.ReviewHandoffUpdatedAt = details.ReviewHandoffUpdatedAt.Add(time.Second)
			case "changed-comment":
				copy := *comment
				copy.ID = "ghc-302"
				step.Comment = &copy
			case "later-creation":
				created.GroupSequence = 4
			case "forward-only":
				root.Phase = "applied"
			}
			want := mode == "completed" || mode == "pending-applied"
			if got := workflowHandoffRemovalObserved(root, effect, details, observed); got != want {
				t.Fatalf("historical handoff skip=%v, want %v", got, want)
			}
		})
	}
}

func TestWorkflowUpdateJournalStrictInputAndFraming(t *testing.T) {
	a := workflowUpdateModel(t)
	body, err := renderWriteActionJournal(a)
	if err != nil || !strings.Contains(body, actionWorkflowUpdateJournalStart) {
		t.Fatal("workflow root framing", err)
	}
	comment := journalCommentFixture(t, a)
	comment.Body = body
	decoded, err := decodeActionJournal(comment, a.CarrierID)
	if err != nil || !reflect.DeepEqual(decoded.ActionJournal, a) {
		t.Fatal("workflow root round trip", err)
	}
	for _, start := range []string{actionOrderedGroupJournalStart, actionUpdateRelationsJournalStart, actionUpdateCommentsJournalStart} {
		comment.Body = strings.Replace(body, actionWorkflowUpdateJournalStart, start, 1)
		if _, err := decodeActionJournal(comment, a.CarrierID); err == nil {
			t.Fatal("legacy framing accepted mixed workflow input")
		}
	}
	m := a.Group.Members[0]
	member := ActionJournal{OperationID: "field-attempt", SessionID: a.SessionID, IssueID: m.IssueID, CarrierID: a.CarrierID, Action: "update", Phase: "prepared", Before: &m.Before, UpdateIntent: a.Group.WorkflowUpdates.Steps[0].Update, GroupOperationID: a.OperationID, GroupContract: workflowUpdateContract, GroupSequence: 1, WorkflowStep: 1}
	body, err = renderWriteActionJournal(member)
	if err != nil || !strings.Contains(body, actionWorkflowUpdateMemberStart) {
		t.Fatal("workflow member framing", err)
	}
	comment = journalCommentFixture(t, member)
	comment.Body = body
	if _, err := decodeActionJournal(comment, a.CarrierID); err != nil {
		t.Fatal(err)
	}
	comment.Body = strings.Replace(body, actionWorkflowUpdateMemberStart, actionOrderedGroupMemberJournalStart, 1)
	if _, err := decodeActionJournal(comment, a.CarrierID); err == nil {
		t.Fatal("legacy member accepted a workflow step")
	}
}

func TestWorkflowUpdateJournalRejectsIncompleteOrForeignInputs(t *testing.T) {
	for name, mutate := range map[string]func(*ActionJournal){
		"missing selection":         func(a *ActionJournal) { a.Group.WorkflowUpdates.Selected = nil },
		"different actor":           func(a *ActionJournal) { a.Group.WorkflowUpdates.Options.SessionID = "peer" },
		"projected observed events": func(a *ActionJournal) { a.Group.Ordered.Events = map[string][]nativeStateEvent{} },
		"missing baseline":          func(a *ActionJournal) { delete(a.Group.WorkflowUpdates.Baselines, a.IssueID) },
		"nil baseline":              func(a *ActionJournal) { a.Group.WorkflowUpdates.Baselines[a.IssueID] = nil },
		"replaced field events": func(a *ActionJournal) {
			*a.Group.WorkflowUpdates.Steps[0].Update.NativeEvents = []nativeStateEvent{{ID: 1, Event: "closed"}}
		},
		"missing status step":     func(a *ActionJournal) { a.Group.WorkflowUpdates.Steps = a.Group.WorkflowUpdates.Steps[:1] },
		"repeated status target":  func(a *ActionJournal) { a.Group.WorkflowUpdates.Steps[3].IssueID = a.IssueID },
		"undeclared native write": func(a *ActionJournal) { a.Group.WorkflowUpdates.Steps[0].Update.Target.State = "closed" },
		"unrelated graph observation": func(a *ActionJournal) {
			a.Group.WorkflowUpdates.Steps[0].Update.Related = []ActionRelatedObservation{{ID: "gh-999", State: a.Group.Members[0].Before}}
		},
		"missing companion": func(a *ActionJournal) { a.Group.WorkflowUpdates.Steps[4].Comment = nil },
		"outcome as input":  func(a *ActionJournal) { a.Group.Members[0].Noop = true },
		"non-ordered group": func(a *ActionJournal) { a.Group.Ordered = nil },
	} {
		t.Run(name, func(t *testing.T) {
			a := workflowUpdateModel(t)
			mutate(&a)
			if err := a.validate(); err == nil {
				t.Fatal("invalid workflow command accepted")
			}
		})
	}
}
