package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func importInverseFixture(t *testing.T, overwrite bool) (*ownedImportFixture, ActionJournal) {
	t.Helper()
	f, a := completedImportFixture(t, overwrite)
	snapshot, err := f.client.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now().UTC()
	for _, target := range a.ImportForward.Targets {
		if target.Issue.UpdatedAt.After(clock) {
			clock = target.Issue.UpdatedAt
		}
	}
	a.Phase, a.UndoOperationID = "undo_prepared", "td-op-import-inverse-fixture"
	a.ImportInverse, err = planImportInverse(a, snapshot, a.UndoOperationID, clock.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.validate(); err != nil {
		t.Fatal(err)
	}
	return f, a
}

func completeImportInverseFixture(t *testing.T, a ActionJournal) ActionJournal {
	t.Helper()
	states := append([]ImportNativeState{}, a.ImportForward.Targets...)
	for n, step := range a.ImportInverse.Steps {
		previous, err := copyCreationJournal(a)
		if err != nil {
			t.Fatal(err)
		}
		a.ImportInverse.Started++
		if err := validateImportInverseRewrite(previous, a.Phase, a.UndoOperationID, a.ImportInverse); err != nil {
			t.Fatal("could not record step before its target write", n, err)
		}
		previous, err = copyCreationJournal(a)
		if err != nil {
			t.Fatal(err)
		}
		actual := states[step.Member]
		actual.Issue = importInputNative(actual.Issue, step.Input)
		actual.Issue.UpdatedAt = a.ImportInverse.At.Add(time.Duration(n+1) * time.Second)
		observation := CreationObservation{Proof: "verified", SessionID: a.SessionID, Actor: "github-author", ObservedAt: actual.Issue.UpdatedAt}
		a.ImportInverse.Results = append(a.ImportInverse.Results, ImportInverseResult{State: actual, Observation: observation})
		states[step.Member] = actual
		if err := validateImportInverseRewrite(previous, a.Phase, a.UndoOperationID, a.ImportInverse); err != nil {
			t.Fatal("could not record a completed step", err)
		}
		if err := a.validate(); err != nil {
			t.Fatal(err)
		}
	}
	previous, err := copyCreationJournal(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Phase = "undone"
	a.ImportInverse.Result = &CreationObservation{Proof: "verified", SessionID: a.SessionID, Actor: "github-author", ObservedAt: a.ImportInverse.At.Add(time.Hour)}
	if err := validateImportInverseRewrite(previous, a.Phase, a.UndoOperationID, a.ImportInverse); err != nil {
		t.Fatal(err)
	}
	if err := a.validate(); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestImportInverseModelKeepsOriginalEvidenceAndRejectsAlteredProgress(t *testing.T) {
	f, prepared := importInverseFixture(t, true)
	done := completeImportInverseFixture(t, prepared)
	if len(done.ImportInverse.Steps) != 2 {
		t.Fatal("mixed existing/new import lost its complete inverse membership")
	}
	for _, step := range done.ImportInverse.Steps {
		member := done.ImportCommand.Members[step.Member]
		record, err := importInputNative(done.ImportForward.Targets[step.Member].Issue, step.Input).record()
		if err != nil {
			t.Fatal(err)
		}
		if member.Before == nil && record.DeletedAt == nil {
			t.Fatal("new allocation is not logically deleted")
		}
		if member.Before != nil && (record.Title != member.Before.Issue.Title || record.DeletedAt != nil) {
			t.Fatal("existing task was deleted instead of restoring its fields")
		}
	}
	for _, change := range []string{"step input", "member", "missing step", "skipped start", "early completion", "removed history", "changed identity", "verified observer", "changed native events", "rewritten result"} {
		t.Run(change, func(t *testing.T) {
			a, err := copyCreationJournal(done)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "step input":
				a.ImportInverse.Steps[0].Input.Title = "Unrelated overwrite"
			case "member":
				a.ImportInverse.Steps[0].Member = 1
			case "missing step":
				a.ImportInverse.Steps = a.ImportInverse.Steps[:1]
			case "skipped start":
				a.ImportInverse.Started = 0
			case "early completion":
				a.ImportInverse.Results = nil
			case "removed history":
				a.ImportInverse.Results[0].State.Comments = []apiComment{}
			case "changed identity":
				a.ImportInverse.Results[0].State.Issue.User.Login = "another-author"
			case "verified observer":
				a.ImportInverse.Results[0].Observation.SessionID = "another-session"
			case "changed native events":
				a.ImportInverse.Results[0].State.Events = []nativeStateEvent{{ID: 1, Event: "closed"}, {ID: 2, Event: "reopened"}}
			case "rewritten result":
				a.ImportInverse.Results[0].State.Issue.Title = "Changed result"
			}
			if err := a.validate(); err == nil {
				t.Fatal("invalid inverse accepted", change)
			}
		})
	}
	inputBefore, _ := importOriginalDigest(prepared.ImportCommand)
	inputAfter, _ := importOriginalDigest(done.ImportCommand)
	if inputBefore != inputAfter || !reflect.DeepEqual(prepared.ImportForward, done.ImportForward) {
		t.Fatal("inverse replaced original input/forward evidence")
	}
	root := *f.root
	root.UpdatedAt = done.ImportInverse.At.Add(2 * time.Hour)
	root.Body, _ = renderActionJournal(done)
	if _, err := decodeActionJournal(root, done.CarrierID); err != nil {
		t.Fatal(err)
	}
	root.User.Login = "another-user"
	if _, err := decodeActionJournal(root, done.CarrierID); err == nil {
		t.Fatal("foreign native root author acquired inverse authority")
	}
}

func TestImportInverseArchiveKeepsLargeResultsOutsideRoot(t *testing.T) {
	f, a := importInverseFixture(t, false)
	// The imported target's native history is retained, not erased by undo.
	clock := a.ImportForward.Targets[0].Issue.UpdatedAt
	for n := 0; n < 80; n++ {
		comment := apiComment{ID: int64(2000000 + n), Body: strings.Repeat("native history ", 4000), IssueURL: fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", a.ImportForward.Targets[0].Issue.Number), CreatedAt: clock, UpdatedAt: clock}
		comment.User.Login = "history-author"
		a.ImportForward.Targets[0].Comments = append(a.ImportForward.Targets[0].Comments, comment)
	}
	a = completeImportInverseFixture(t, a)
	publication, err := planActionJournalPublication(a)
	if err != nil {
		t.Fatal(err)
	}
	largeResult := false
	for _, doc := range publication.importArchive.manifest.Documents {
		if strings.HasPrefix(doc.Name, "inverse-result:") && doc.Size > actionDecodedBudget {
			largeResult = true
		}
	}
	if !largeResult {
		t.Fatal("fixture did not exercise a large inverse result document")
	}
	clock = a.ImportInverse.At.Add(2 * time.Hour)
	parts := make([]apiComment, len(publication.importArchive.bodies))
	for n, body := range publication.importArchive.bodies {
		parts[n] = apiComment{ID: int64(100 + n), Body: body, IssueURL: f.root.IssueURL, CreatedAt: clock, UpdatedAt: clock}
		parts[n].User.Login = f.root.User.Login
	}
	rootPlan, err := publication.importArchive.bindParts(parts)
	if err != nil || len(rootPlan.partBodies) != 0 {
		t.Fatal("unexpected outer root", err)
	}
	root := *f.root
	root.Body, root.UpdatedAt = rootPlan.body, clock.Add(time.Second)
	decoded, err := decodeMultipartActionJournal(root, a.CarrierID, parts)
	canonical, cloneErr := copyCreationJournal(a)
	if err != nil || cloneErr != nil || !reflect.DeepEqual(decoded.ActionJournal, canonical) {
		t.Fatal("inverse reference archive lost original/step/result evidence", err, cloneErr)
	}
}

func TestImportInverseProgressReusesImmutableArchiveParts(t *testing.T) {
	_, prepared := importInverseFixture(t, false)
	copy, err := copyCreationJournal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	done := completeImportInverseFixture(t, copy)
	current, err := planActionJournalPublication(prepared)
	if err != nil {
		t.Fatal(err)
	}
	for n := range done.ImportInverse.Steps {
		prepared.ImportInverse.Started++
		started, err := planActionJournalPublication(prepared)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(current.importArchive.parts, started.importArchive.parts) || !reflect.DeepEqual(current.importArchive.bodies, started.importArchive.bodies) {
			t.Fatal("starting a step replaces existing immutable archive parts", n)
		}
		prepared.ImportInverse.Results = append(prepared.ImportInverse.Results, done.ImportInverse.Results[n])
		completed, err := planActionJournalPublication(prepared)
		if err != nil {
			t.Fatal(err)
		}
		before, after := current.importArchive.parts, completed.importArchive.parts
		if len(after) <= len(before) || !reflect.DeepEqual(before, after[:len(before)]) {
			t.Fatal("appending a result rewrites the plan or earlier results", n)
		}
		current = completed
	}
	finished, err := planActionJournalPublication(done)
	if err != nil || !reflect.DeepEqual(current.importArchive.parts, finished.importArchive.parts) {
		t.Fatal("whole completion republishes inverse evidence", err)
	}
}

func TestImportInverseCancellationKeepsOriginalExecutorAndAudit(t *testing.T) {
	_, prior := importInverseFixture(t, false)
	prior.ImportInverse.Started = 1
	canceled, err := copyCreationJournal(prior)
	if err != nil {
		t.Fatal(err)
	}
	canceled.ImportInverse.Started = 0
	canceled.ImportInverse.Canceled = []ImportInverseCancellation{{Step: 0, Stage: "target", Observation: CreationObservation{Proof: "verified", SessionID: prior.SessionID, Actor: "github-author", ObservedAt: prior.ImportInverse.At}}}
	if err := canceled.validate(); err != nil {
		t.Fatal(err)
	}
	if err := validateImportInverseRewrite(prior, canceled.Phase, canceled.UndoOperationID, canceled.ImportInverse); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"observer", "other session", "wrong step", "wrong stage", "no pending step"} {
		t.Run(kind, func(t *testing.T) {
			next, err := copyCreationJournal(canceled)
			if err != nil {
				t.Fatal(err)
			}
			old, err := copyCreationJournal(prior)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "observer":
				next.ImportInverse.Canceled[0].Observation.Proof = "observed"
			case "other session":
				next.ImportInverse.Canceled[0].Observation.SessionID = "other-session"
			case "wrong step":
				next.ImportInverse.Canceled[0].Step = 1
			case "wrong stage":
				next.ImportInverse.Canceled[0].Stage = "after-patch"
			case "no pending step":
				old.ImportInverse.Started = 0
			}
			if next.validate() == nil && validateImportInverseRewrite(old, next.Phase, next.UndoOperationID, next.ImportInverse) == nil {
				t.Fatal("invalid no-write evidence authorized replay")
			}
		})
	}
	resumed, err := copyCreationJournal(canceled)
	if err != nil {
		t.Fatal(err)
	}
	resumed.ImportInverse.Started = 1
	if err := validateImportInverseRewrite(canceled, resumed.Phase, resumed.UndoOperationID, resumed.ImportInverse); err != nil {
		t.Fatal(err)
	}
	resumed.ImportInverse.Canceled = nil
	if err := validateImportInverseRewrite(canceled, resumed.Phase, resumed.UndoOperationID, resumed.ImportInverse); err == nil {
		t.Fatal("resume erased the earlier canceled attempt")
	}
}

func TestImportInversePlansBothGraphDetachmentsAndInvalidatesOldApproval(t *testing.T) {
	clock := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	native := func(number int, parent string, dependencies []string, approved bool) apiIssue {
		details := IssueDetails{Status: models.StatusOpen, ParentID: parent, Dependencies: dependencies}
		if approved {
			details.ReviewBasis, details.ReviewEvents, details.ReviewerSession = "old-basis", "old-events", "old-reviewer"
			details.ReviewedAt = &clock
			details.Reviews = []models.IssueReview{{ID: "old-approval", IssueID: fmt.Sprintf("gh-%d", number), ReviewerSession: "old-reviewer", CreatedAt: clock, Decision: "approved"}}
		}
		body, err := encodeBody("original description", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &details})
		if err != nil {
			t.Fatal(err)
		}
		return apiIssue{Number: number, Title: fmt.Sprintf("Task %d", number), Body: body, State: "open", CreatedAt: clock, UpdatedAt: clock}
	}
	state := func(issue apiIssue) ImportNativeState {
		return ImportNativeState{Issue: issue, Comments: []apiComment{}, Events: []nativeStateEvent{}}
	}
	beforeA, beforeB := state(native(1, "", []string{"gh-2"}, true)), state(native(2, "gh-1", nil, false))
	forwardA, forwardB := state(native(1, "gh-2", nil, false)), state(native(2, "", []string{"gh-1"}, false))
	a := ActionJournal{OperationID: "graph-import", SessionID: "graph-session", ImportCommand: &ActionImportCommand{Repository: "owner/repo", Members: []ActionImportMember{
		{SourceKey: "legacy:graph:td-a", Action: "overwrite", TargetID: "gh-1", Before: &beforeA},
		{SourceKey: "legacy:graph:td-b", Action: "overwrite", TargetID: "gh-2", Before: &beforeB},
	}}, ImportForward: &ActionImportForward{Targets: []ImportNativeState{forwardA, forwardB}}}
	snapshot := &Snapshot{fullHistory: true}
	for _, state := range []ImportNativeState{forwardA, forwardB, {Issue: native(3, "", nil, false)}} {
		record, err := state.Issue.record()
		if err != nil {
			t.Fatal(err)
		}
		snapshot.records = append(snapshot.records, *record)
	}
	inverse, err := planImportInverse(a, snapshot, "graph-inverse", clock.Add(time.Hour))
	if err != nil || len(inverse.Steps) != 4 || len(inverse.Watches) != 0 {
		t.Fatal("combined parent/dependency reversal did not detach both graphs", inverse, err)
	}
	parents, dependencies := map[string]string{}, map[string][]string{}
	for _, record := range snapshot.records {
		parents[record.ID], dependencies[record.ID] = record.ParentID, record.Details.Dependencies
	}
	for _, step := range inverse.Steps {
		record, err := importInputNative(a.ImportForward.Targets[step.Member].Issue, step.Input).record()
		if err != nil {
			t.Fatal(err)
		}
		parents[record.ID], dependencies[record.ID] = record.ParentID, record.Details.Dependencies
		if importReachableCycle(record.ID, func(id string) []string {
			if parents[id] == "" {
				return nil
			}
			return []string{parents[id]}
		}) || importReachableCycle(record.ID, func(id string) []string { return dependencies[id] }) {
			t.Fatal("inverse creates a transient graph cycle")
		}
	}
	if parents["gh-2"] != "gh-1" || len(dependencies["gh-1"]) != 1 || dependencies["gh-1"][0] != "gh-2" {
		t.Fatal("inverse lost original parent/dependency graph")
	}
	input, err := importCompensationInput(a, 0, "graph-inverse", clock.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, meta, _, err := decodeBody(input.Body)
	if err != nil || meta.Details.ReviewBasis != "" || meta.Details.ReviewerSession != "" || len(meta.Details.Reviews) != 1 || meta.Details.Reviews[0].SupersededAt == nil {
		t.Fatal("inverse reactivated a historical approval", err)
	}
	// A change outside both selected graphs must not become a false conflict.
	snapshot.records[2].Title = "Unrelated edit"
	again, err := planImportInverse(a, snapshot, "graph-inverse", inverse.At)
	if err != nil || !reflect.DeepEqual(inverse, again) {
		t.Fatal("unrelated task changed the frozen inverse", err)
	}
}

func TestImportInverseRejectsOutsideReferencesBeforeLogicalDeletion(t *testing.T) {
	f, a := importInverseFixture(t, false)
	snapshot, err := f.client.ReadSnapshot(WithFreshSnapshot(context.Background()), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"parent", "dependency"} {
		t.Run(kind, func(t *testing.T) {
			details := IssueDetails{Status: models.StatusOpen}
			target := fmt.Sprintf("gh-%d", a.ImportForward.Targets[0].Issue.Number)
			if kind == "parent" {
				details.ParentID = target
			} else {
				details.Dependencies = []string{target}
			}
			body, err := encodeBody("outside", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &details})
			if err != nil {
				t.Fatal(err)
			}
			native := apiIssue{Number: 999, Title: "Outside", Body: body, State: "open", UpdatedAt: a.ImportInverse.At}
			record, err := native.record()
			if err != nil {
				t.Fatal(err)
			}
			graph := *snapshot
			graph.records = append(append([]Record{}, snapshot.records...), *record)
			if _, err := planImportInverse(a, &graph, a.UndoOperationID, a.ImportInverse.At); err == nil || !strings.Contains(err.Error(), "outside task") {
				t.Fatal("inverse ignored an outside relationship", kind, err)
			}
		})
	}
}

func TestImportInverseCapacityReservesCompletedReferenceMetadata(t *testing.T) {
	_, a := importInverseFixture(t, false)
	clock := a.ImportForward.Targets[0].Issue.UpdatedAt
	for n := 0; n < 80; n++ {
		comment := apiComment{ID: int64(2000000 + n), Body: strings.Repeat("native history ", 4000), IssueURL: fmt.Sprintf("https://api.github.com/repos/owner/repo/issues/%d", a.ImportForward.Targets[0].Issue.Number), CreatedAt: clock, UpdatedAt: clock}
		comment.User.Login = "history-author"
		a.ImportForward.Targets[0].Comments = append(a.ImportForward.Targets[0].Comments, comment)
	}
	if err := checkImportInverseCapacity(a); err != nil {
		t.Fatal("ordinary large history acquired a total 4 MiB limit", err)
	}
	// Fill the existing original-identity inventory near the root metadata
	// limit. The unstarted inverse still fits, but final native result refs do
	// not. This models the actual late-publication failure we must preflight.
	publication, err := planActionJournalPublication(a)
	if err != nil {
		t.Fatal(err)
	}
	body, err := renderImportArchiveManifest(publication.importArchive.manifest)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := json.Marshal(ImportTargetIdentity{IssueID: "gh-1000000", OperationID: "td-op-" + strings.Repeat("f", 64)})
	if err != nil {
		t.Fatal(err)
	}
	count := (actionDecodedBudget - len(body) - 10_000) / (len(entry) + 1)
	for n := 0; n < count; n++ {
		a.ImportCommand.OriginalTargets = append(a.ImportCommand.OriginalTargets, ImportTargetIdentity{IssueID: fmt.Sprintf("gh-%d", 1000000+n), OperationID: "td-op-" + journalTransportHash(fmt.Sprint(n))})
	}
	slices.SortFunc(a.ImportCommand.OriginalTargets, func(a, b ImportTargetIdentity) int { return strings.Compare(a.IssueID, b.IssueID) })
	if _, err := planActionJournalPublication(a); err != nil {
		t.Fatal("test must fit the prepared publication before final results", err)
	}
	if err := checkImportInverseCapacity(a); err == nil || !strings.Contains(err.Error(), "final-result capacity") {
		t.Fatal("capacity preflight omitted final native result references", err)
	}
}
