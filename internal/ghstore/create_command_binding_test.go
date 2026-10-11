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

func creationBindingFixture(t *testing.T) (*journalWriteFixture, *creationCommandPlan, *Record) {
	t.Helper()
	f := newJournalWriteFixture(t, "")
	base := f.issues[1]
	base.CreatedAt, base.URL = base.UpdatedAt.Add(-time.Hour), "https://github.com/owner/repo/issues/1"
	base.User.Login = "github-author"
	f.issues[1] = base
	for _, n := range []int{3, 4} {
		r := f.issues[1]
		r.Number, r.Title = n, fmt.Sprintf("Existing %d", n)
		f.issues[n] = r
	}
	if err := f.client.TrackActions("creation-test-session"); err != nil {
		t.Fatal(err)
	}
	depends, blocks := []string{"gh-4"}, []string{"gh-1"}
	plan, err := f.client.prepareRelatedCreation(context.Background(), &models.Issue{Title: "Actual allocated task", Type: models.TypeTask, Priority: models.PriorityP2, ParentID: "gh-3"}, RelationChanges{DependsOn: &depends, Blocks: &blocks}, "td-op-created-binding")
	if err != nil {
		t.Fatal(err)
	}
	// This is a native fixture response, not a guessed future observation used
	// in production. The binding must match these exact server-owned fields.
	r := apiIssue{Number: 2, Title: plan.create.input.Title, Body: plan.create.input.Body, State: "open", UpdatedAt: f.issues[1].UpdatedAt.Add(time.Minute)}
	r.CreatedAt, r.URL, r.User.Login = r.UpdatedAt, "https://github.com/owner/repo/issues/2", "github-author"
	for _, name := range plan.create.input.Labels {
		r.Labels = append(r.Labels, struct {
			Name string `json:"name"`
		}{name})
	}
	f.issues[2] = r
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	created, err := decodeIssue(data)
	if err != nil {
		t.Fatal(err)
	}
	created.repository = f.client.repo
	return f, plan, created
}

func TestCreationBindingPreservesOriginalObservationsAndActualIdentity(t *testing.T) {
	f, original, created := creationBindingFixture(t)
	run := f.client.run
	requests := []string{}
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		requests = append(requests, args[4]+" "+args[5])
		return run(ctx, dir, payload, args...)
	}
	command, err := f.client.prepareCreatedRelations(context.Background(), original, created)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatal("binding re-read originals or wrote before its parent intent", requests)
	}
	if command.root.OperationID != original.relationOperationID || command.root.Group.pairedEffectCount() != 1 {
		t.Fatal("binding lost its preallocated child operation or added a source update")
	}
	g := command.root.Group
	if !slices.Equal(g.RelationUpdates.Selected, []string{"gh-2"}) || len(g.Members) != 2 {
		t.Fatal("wrong creation relationship membership", g)
	}
	actual, err := actionIssueState(created)
	if err != nil || !reflect.DeepEqual(command.expected["gh-2"], actual) {
		t.Fatal("created observation was fabricated", err)
	}
	for i, member := range g.Members {
		input := g.RelationUpdates.Inputs[i].Update
		if member.IssueID == created.ID {
			if input != nil {
				t.Fatal("initial dependencies should already be in POST")
			}
			continue
		}
		before := original.records[member.IssueID]
		state, err := actionIssueState(&before)
		if err != nil || !reflect.DeepEqual(member.Before, state) {
			t.Fatal("external baseline was refreshed", err)
		}
		_, meta, _, err := decodeBody(input.Target.Body)
		if err != nil || meta.Details == nil || !slices.Equal(meta.Details.Dependencies, []string{created.ID}) {
			t.Fatal("reverse edge did not bind the actual allocated identity", err)
		}
	}
	for _, id := range []string{"gh-3", "gh-4"} {
		if !slices.ContainsFunc(g.RelationUpdates.Watches, func(w ActionRelatedObservation) bool { return w.ID == id }) {
			t.Fatal("binding dropped parent or dependency watch", id)
		}
	}
	created.Details.Dependencies[0] = "gh-999"
	created.Labels[0] = "foreign"
	if !slices.Equal(command.records["gh-2"].Details.Dependencies, []string{"gh-4"}) || command.records["gh-2"].Labels[0] == "foreign" {
		t.Fatal("bound command retained caller-owned values")
	}
	if strings.Contains(command.root.Group.RelationUpdates.Inputs[1].Update.Target.Body, creationGraphNode) {
		t.Fatal("symbolic graph node entered a native update")
	}
}

func TestCreationBindingRejectsForeignChangesBeforeAnyChildWrite(t *testing.T) {
	for _, target := range []int{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			f, original, created := creationBindingFixture(t)
			if target == 5 {
				// A newly introduced reverse edge must not be silently adopted.
				r := f.issues[1]
				r.Number = 5
				body, err := encodeBody("Foreign dependent", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Status: models.StatusOpen, Dependencies: []string{"gh-2"}}})
				if err != nil {
					t.Fatal(err)
				}
				r.Body = body
				f.issues[5] = r
			} else {
				r := f.issues[target]
				r.Title = "Foreign change"
				r.UpdatedAt = r.UpdatedAt.Add(time.Second)
				f.issues[target] = r
			}
			writes := 0
			run := f.client.run
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if args[4] != "GET" {
					writes++
				}
				return run(ctx, dir, payload, args...)
			}
			command, err := f.client.prepareCreatedRelations(context.Background(), original, created)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.client.writeRelationCommand(context.Background(), command); err == nil || writes != 0 {
				t.Fatal("foreign state adopted or child write attempted", target, err, writes)
			}
		})
	}
}

func TestCreationBindingRejectsUnallocatedOrReplacedResult(t *testing.T) {
	for _, mode := range []string{"nil", "foreign-repo", "collision", "number", "title", "body", "operation", "labels", "clock", "revision"} {
		t.Run(mode, func(t *testing.T) {
			f, original, created := creationBindingFixture(t)
			switch mode {
			case "nil":
				created = nil
			case "foreign-repo":
				created.repository = "other/repo"
			case "collision":
				created.ID = "gh-1"
			case "number":
				created.Number = 99
			case "title":
				created.Title = "Replaced"
			case "body":
				created.nativeBody += "Replaced"
			case "operation":
				created.meta.OperationID = "other-operation"
			case "labels":
				created.Labels = nil
			case "clock":
				created.UpdatedAt = time.Time{}
			case "revision":
				created.revision = [32]byte{}
			}
			calls := 0
			f.client.run = func(context.Context, string, []byte, ...string) ([]byte, error) {
				calls++
				return nil, fmt.Errorf("unexpected API request")
			}
			if _, err := f.client.prepareCreatedRelations(context.Background(), original, created); err == nil || calls != 0 {
				t.Fatal("invalid creation result accepted or caused API calls", err, calls)
			}
		})
	}
}

func TestCreationBindingKeepsPreCreationNativeEventEvidence(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			f, _, created := creationBindingFixture(t)
			nativeCreated := f.issues[2]
			delete(f.issues, 2)
			r := f.issues[1]
			body, err := encodeBody("Review checkpoint", metadata{Type: models.TypeTask, Priority: models.PriorityP2, Details: &IssueDetails{Status: models.StatusOpen, ReviewBasis: strings.Repeat("a", 64), ReviewEvents: stateEventDigest([]nativeStateEvent{})}})
			if err != nil {
				t.Fatal(err)
			}
			r.Body = body
			f.issues[1] = r
			events := []nativeStateEvent{}
			eventReads, writes := 0, 0
			run := f.client.run
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if args[4] != "GET" {
					writes++
				}
				if args[5] == "repos/owner/repo/issues/1/events?per_page=100" {
					eventReads++
					return json.Marshal([][]nativeStateEvent{events})
				}
				return run(ctx, dir, payload, args...)
			}
			depends, blocks := []string{"gh-4"}, []string{"gh-1"}
			original, err := f.client.prepareRelatedCreation(context.Background(), &models.Issue{Title: "Actual allocated task", Type: models.TypeTask, Priority: models.PriorityP2, ParentID: "gh-3"}, RelationChanges{DependsOn: &depends, Blocks: &blocks}, "td-op-created-binding")
			if err != nil || eventReads != 1 || original.events["gh-1"] == nil {
				t.Fatal("event evidence not captured before creation", err, eventReads)
			}
			f.issues[2] = nativeCreated
			if changed {
				// Native state has returned to open with identical visible data.
				// A new baseline must not erase the intervening event evidence.
				events = []nativeStateEvent{{ID: 1, Event: "closed"}, {ID: 2, Event: "reopened"}}
			}
			_, err = f.client.prepareCreatedRelations(context.Background(), original, created)
			if (err != nil) != changed || writes != 0 || eventReads != 2 {
				t.Fatal("binding refreshed away pre-creation event evidence", err, writes, eventReads)
			}
		})
	}
}
