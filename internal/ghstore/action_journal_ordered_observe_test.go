package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type orderedObservationFixture struct {
	client   *Client
	group    *ActionJournalRecord
	comments map[int64]apiComment
	issues   map[int]apiIssue
	events   map[string][]nativeStateEvent
}

func observedFixtureIssue(t *testing.T, id string, state ActionIssueState) apiIssue {
	t.Helper()
	n, _ := Number(id)
	labels := []map[string]string{}
	for _, label := range state.Labels {
		labels = append(labels, map[string]string{"name": label})
	}
	data, _ := json.Marshal(map[string]any{"number": n, "title": state.Title, "body": state.Body, "state": state.State, "updated_at": state.UpdatedAt, "labels": labels})
	var item apiIssue
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	return item
}

func newOrderedObservationFixture(t *testing.T, a ActionJournal) *orderedObservationFixture {
	t.Helper()
	f := newJournalWriteFixture(t, "")
	o := &orderedObservationFixture{client: f.client, comments: map[int64]apiComment{}, issues: f.issues, events: map[string][]nativeStateEvent{}}
	for _, effect := range a.Group.Ordered.Forward {
		r := effect.Record
		n, _ := CommentNumber(r.ID)
		body, err := renderExistingActionJournal(r)
		if err != nil {
			t.Fatal(err)
		}
		c := apiComment{ID: n, IssueURL: "https://api.github.com/repos/owner/repo/issues/9", Body: body, URL: r.URL, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
		c.User.Login = r.Author
		o.comments[n] = c
	}
	for _, m := range a.Group.Forward.Members {
		item := observedFixtureIssue(t, m.IssueID, m.State)
		o.issues[item.Number] = item
	}
	for id, events := range a.Group.Ordered.Events {
		o.events[id] = append([]nativeStateEvent{}, events...)
	}
	for _, step := range a.Group.Ordered.Steps {
		if step.Result == nil {
			continue
		}
		for _, effect := range a.Group.Ordered.Forward {
			if effect.Record.ID == step.ForwardReceiptID {
				item := observedFixtureIssue(t, effect.Record.IssueID, *step.Result)
				o.issues[item.Number], o.events[effect.Record.IssueID] = item, *step.EventsAfter
			}
		}
	}
	body, err := renderWriteActionJournal(a)
	if err != nil {
		t.Fatal(err)
	}
	root := apiComment{ID: 200, IssueURL: "https://api.github.com/repos/owner/repo/issues/9", Body: body, CreatedAt: a.Group.Ordered.Forward[0].Record.CreatedAt.Add(-time.Second), UpdatedAt: time.Now().UTC()}
	root.User.Login = a.Group.Forward.Actor
	o.comments[200] = root
	o.group, err = decodeActionJournal(root, "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		method, path := args[4], strings.TrimPrefix(args[5], "repos/owner/repo")
		if method != "GET" {
			t.Fatalf("readonly observation reached %s %s", method, path)
		}
		if path == "/issues/comments?per_page=100" {
			comments := []apiComment{}
			for _, c := range o.comments {
				comments = append(comments, c)
			}
			return json.Marshal([][]apiComment{comments})
		}
		if strings.HasPrefix(path, "/issues/comments/") {
			var n int64
			_, _ = fmt.Sscanf(path, "/issues/comments/%d", &n)
			if c, found := o.comments[n]; found {
				return json.Marshal(c)
			}
			return nil, fmt.Errorf("missing native comment")
		}
		if strings.Contains(path, "/events?") {
			var n int
			_, _ = fmt.Sscanf(path, "/issues/%d/events", &n)
			return json.Marshal([][]nativeStateEvent{o.events[fmt.Sprintf("gh-%d", n)]})
		}
		return base(ctx, dir, payload, args...)
	}
	return o
}

func TestOrderedGroupObservationBindsFrozenNativeReceipts(t *testing.T) {
	o := newOrderedObservationFixture(t, orderedArchiveFixture(t))
	observed, err := o.client.observeOrderedGroup(context.Background(), o.group, false)
	if err != nil || !observed.Remaining || len(observed.Effects) != 2 || len(observed.Members) != 2 {
		t.Fatal(observed, err)
	}
	if err := o.client.pointOrderedEffect(context.Background(), observed.Effects[0]); err != nil {
		t.Fatal(err)
	}
	native := o.comments[201]
	native.UpdatedAt = native.UpdatedAt.Add(time.Second)
	o.comments[201] = native
	if _, err := o.client.observeOrderedGroup(context.Background(), o.group, false); err == nil {
		t.Fatal("changed native original receipt accepted")
	}
	if err := o.client.pointOrderedEffect(context.Background(), observed.Effects[0]); err == nil {
		t.Fatal("point read accepted changed native receipt clock")
	}
}

func TestOrderedGroupObservationFencesWatchedNoopEvents(t *testing.T) {
	a := orderedArchiveFixture(t)
	a.Group.Ordered.Events["gh-2"] = []nativeStateEvent{}
	o := newOrderedObservationFixture(t, a)
	if _, err := o.client.observeOrderedGroup(context.Background(), o.group, false); err != nil {
		t.Fatal(err)
	}
	o.events["gh-2"] = []nativeStateEvent{o.events["gh-1"][0]}
	if _, err := o.client.observeOrderedGroup(context.Background(), o.group, false); err == nil {
		t.Fatal("changed watched noop event observation accepted")
	}
}

func TestOrderedGroupPointReadRejectsMissingReceipt(t *testing.T) {
	o := newOrderedObservationFixture(t, orderedArchiveFixture(t))
	if err := o.client.pointOrderedEffect(context.Background(), OrderedGroupEffect{}); err == nil {
		t.Fatal("missing frozen receipt accepted")
	}
	expected := o.group.Group.Ordered.Forward[0]
	raw := o.comments[201]
	raw.Body = "ordinary comment"
	o.comments[201] = raw
	err := o.client.pointOrderedEffect(context.Background(), expected)
	if err == nil || strings.Contains(err.Error(), "%!w") {
		t.Fatal("missing native journal needs an explicit error", err)
	}
}

func TestOrderedGroupObservationRetainsPartialInverseState(t *testing.T) {
	a := orderedArchiveFixture(t)
	orderedArchiveStepFixture(t, &a, true)
	o := newOrderedObservationFixture(t, a)
	observed, err := o.client.observeOrderedGroup(context.Background(), o.group, false)
	if err != nil || !observed.Remaining || observed.PendingStep || observed.Members[1].Status != "partially-undone" {
		t.Fatal("partial inverse lost its verified current state", observed, err)
	}
	item := o.issues[2]
	item.Title += " foreign"
	o.issues[2] = item
	if _, err := o.client.observeOrderedGroup(context.Background(), o.group, false); err == nil {
		t.Fatal("foreign watched target edit accepted")
	}
}

func TestOrderedGroupReadonlyReconciliationUsesActualObservation(t *testing.T) {
	o := newOrderedObservationFixture(t, orderedArchiveFixture(t))
	result, err := o.client.reconcileActionGroup(context.Background(), o.group, o.group.SessionID, o.group.Author, false, false)
	if err != nil || !result.AlreadyReconciled || result.Applied || result.TargetWriteReplayed || len(result.GroupMembers) != 2 {
		t.Fatal(result, err)
	}
	if _, err := o.client.reconcileActionGroup(context.Background(), o.group, o.group.SessionID, "foreign", false, false); err == nil {
		t.Fatal("foreign inspection accepted")
	}
}

func TestOrderedGroupObservationPendingInverseNeverReplays(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(fmt.Sprint(applied), func(t *testing.T) {
			a := orderedArchiveFixture(t)
			step := orderedArchiveStepFixture(t, &a, false)
			o := newOrderedObservationFixture(t, a)
			if applied {
				completeOrderedStepFixture(t, step, false)
				item := observedFixtureIssue(t, "gh-1", *step.Result)
				o.issues[item.Number], o.events["gh-1"] = item, *step.EventsAfter
			}
			out, err := o.client.observeOrderedGroup(context.Background(), o.group, false)
			if err != nil || !out.PendingStep || out.PendingApplied != applied {
				t.Fatal(out, err)
			}
			if step.Result != nil && !applied {
				t.Fatal("observer invented a result")
			}
			if _, err := o.client.reconcileActionGroup(context.Background(), o.group, o.group.SessionID, o.group.Author, true, false); err == nil {
				t.Fatal("unconnected recovery attempted a write")
			}
		})
	}
}

func TestOrderedGroupObservationPreparedForwardSeparatesAppliedAndUnapplied(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(fmt.Sprint(applied), func(t *testing.T) {
			a := orderedArchiveFixture(t)
			o := newOrderedObservationFixture(t, a)
			tail := a.Group.Ordered.Forward[1].Record
			tail.Phase, tail.After = "prepared", nil
			body, err := renderWriteActionJournal(tail.ActionJournal)
			if err != nil {
				t.Fatal(err)
			}
			raw := o.comments[202]
			raw.Body = body
			o.comments[202] = raw
			if !applied {
				item := observedFixtureIssue(t, "gh-1", *tail.Before)
				o.issues[item.Number] = item
				o.events["gh-1"] = append([]nativeStateEvent{}, (*tail.UpdateIntent.NativeEvents)...)
			}
			a.Phase, a.Group.Forward, a.Group.Ordered = "prepared", nil, &ActionOrderedGroup{}
			root := o.comments[200]
			root.Body, err = renderWriteActionJournal(a)
			if err != nil {
				t.Fatal(err)
			}
			o.comments[200] = root
			o.group, err = decodeActionJournal(root, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			out, err := o.client.observeOrderedGroup(context.Background(), o.group, false)
			if err != nil {
				t.Fatal(err)
			}
			if applied {
				if out.PendingReceipt != "ghc-202" || out.Members[1].Status != "pending-applied" {
					t.Fatal("applied pending input not identified", out)
				}
			} else if !out.Partial || out.PendingReceipt != "" || out.Members[1].ReceiptID != "ghc-201" || out.Members[1].Status != "applied" {
				t.Fatal("earlier actual result hidden behind unapplied input", out)
			}
			if _, err := o.client.observeOrderedGroup(context.Background(), o.group, true); err == nil {
				t.Fatal("unverified forward accepted as successful callback")
			}
		})
	}
}
