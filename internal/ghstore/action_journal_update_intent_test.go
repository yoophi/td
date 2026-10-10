package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestUpdateIntentSurvivesDeniedAndUncertainTargetWrites(t *testing.T) {
	for _, mode := range []string{"target-denied", "target-uncertain", "receipt-denied", "receipt-uncertain", ""} {
		t.Run(mode, func(t *testing.T) {
			f := newJournalWriteFixture(t, mode)
			if err := f.client.TrackActions("ses_actor"); err != nil {
				t.Fatal(err)
			}
			title := "Requested title"
			_, err := f.client.Update(context.Background(), "gh-1", Changes{Title: &title})
			if (err != nil) != (mode != "") {
				t.Fatal(err)
			}
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil || a.UpdateIntent == nil || a.UpdateIntent.Target.Title != title || a.UpdateIntent.Target.Body != a.Before.Body || a.UpdateIntent.Target.State != a.Before.State || a.UpdateIntent.Target.Labels == nil || a.UpdateIntent.NativeEvents != nil || f.targetWrites != 1 {
				t.Fatal("complete input missing or target replayed", a, err)
			}
			if !strings.Contains(f.comment.Body, actionUpdateIntentJournalStart) {
				t.Fatal("new input was not versioned")
			}
			if mode == "receipt-uncertain" || mode == "" {
				if a.Phase != "applied" || a.After.Title != title {
					t.Fatal(a)
				}
			} else if a.Phase != "prepared" || a.After != nil {
				t.Fatal("uncertain write invented an after-state", a)
			}
			bad := *f.comment
			bad.Body = strings.Replace(bad.Body, actionUpdateIntentJournalStart, actionJournalStart, 1)
			if _, err := decodeActionJournal(bad, "gh-9"); err == nil {
				t.Fatal("old version accepted planned update")
			}
		})
	}
}

func TestUpdateIntentRejectsUnrequestedResponseChanges(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if args[4] == "PATCH" && args[5] == "repos/owner/repo/issues/1" {
			issue := f.issues[1]
			issue.Body = "Unrequested body mutation"
			f.issues[1] = issue
			return json.Marshal(issue)
		}
		return data, err
	}
	title := "Requested title"
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Title: &title}); err == nil || !strings.Contains(err.Error(), "recorded full input") {
		t.Fatal("unrequested response content was accepted", err)
	}
	a, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || a.Phase != "prepared" || a.After != nil || a.UpdateIntent.Target.Body != "Before" || f.targetWrites != 1 {
		t.Fatal(a, err)
	}
}

func TestUpdateIntentCapturesRequiredNativeEventsAndPreservesTheirErrors(t *testing.T) {
	for _, denied := range []bool{false, true} {
		f := newJournalWriteFixture(t, "")
		if err := f.client.TrackActions("ses_actor"); err != nil {
			t.Fatal(err)
		}
		base := f.client.run
		f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
			if strings.Contains(strings.Join(args, " "), "/labels?per_page=100") {
				return []byte(`[[{"name":"td:closed"}]]`), nil
			}
			if strings.Contains(strings.Join(args, " "), "/issues/1/events?per_page=100") {
				if denied {
					return nil, errors.New("HTTP 403 native event baseline denied")
				}
				return []byte("[[]]"), nil
			}
			return base(ctx, dir, payload, args...)
		}
		status := models.StatusClosed
		_, err := f.client.Update(context.Background(), "gh-1", Changes{Status: &status})
		if denied {
			if err == nil || !strings.Contains(err.Error(), "HTTP 403 native event baseline denied") || f.targetWrites != 0 || f.intentWrites != 0 || f.carrierWrites != 0 {
				t.Fatal("event denial hidden or write started", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		a, err := decodeActionJournal(*f.comment, "gh-9")
		if err != nil || a.UpdateIntent.NativeEvents == nil || *a.UpdateIntent.NativeEvents == nil || len(*a.UpdateIntent.NativeEvents) != 0 {
			t.Fatal("empty event evidence confused with absent proof", a, err)
		}
	}
}

func TestUpdateIntentValidatesRelatedSnapshotsAndResultImmutability(t *testing.T) {
	f := prepareUndoFixture(t)
	a, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil {
		t.Fatal(err)
	}
	original := a.UpdateIntent
	for _, mode := range []string{"labels", "body", "kind", "parent-clock", "parent-duplicate", "parent-canonical"} {
		t.Run(mode, func(t *testing.T) {
			data, _ := json.Marshal(a.ActionJournal)
			var copy ActionJournal
			if err := json.Unmarshal(data, &copy); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "labels":
				copy.UpdateIntent.Target.Labels = nil
			case "body":
				copy.UpdateIntent.Target.Body = "Different result"
			case "kind":
				copy.Action = "create"
			case "parent-clock":
				copy.UpdateIntent.Related = []ActionRelatedObservation{{ID: "gh-2", State: ActionIssueState{}}}
			case "parent-duplicate":
				copy.UpdateIntent.Related = []ActionRelatedObservation{{ID: copy.IssueID, State: *copy.Before}}
			case "parent-canonical":
				copy.UpdateIntent.Related = []ActionRelatedObservation{{ID: "gh-02", State: *copy.Before}}
			}
			if _, err := renderActionJournal(copy); err == nil {
				t.Fatal("invalid planned result accepted", mode)
			}
		})
	}
	if !reflect.DeepEqual(original, a.UpdateIntent) {
		t.Fatal("validation mutated the original plan")
	}
}

func TestUpdateIntentKeepsLegacyNativeCompensationReadable(t *testing.T) {
	f, _ := nativeExecutionFixture(t, "close-approved")
	out, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
	if err != nil {
		t.Fatal(err)
	}
	legacy := out.Journal.ActionJournal
	legacy.UpdateIntent = nil
	comment := *f.comment
	comment.Body, err = renderActionJournal(legacy)
	if err != nil || !strings.Contains(comment.Body, actionNativeUndoJournalStart) {
		t.Fatal(err)
	}
	if _, err := decodeActionJournal(comment, "gh-9"); err != nil {
		t.Fatal("v7 compatibility regressed", err)
	}
}
