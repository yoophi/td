package ghstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func journalCreationFixtureIssue() *models.Issue {
	return &models.Issue{Title: "New task", Description: "Description", Type: models.TypeTask, Priority: models.PriorityP2}
}

func TestJournalCreationLinksVerifiedNumberAndRejectsSameOperationReplay(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	f.client.stateLabels = fixtureStateLabels()
	if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
		t.Fatal(err)
	}
	operation := "td-op-create-fixed"
	created, err := f.client.createWithOperationID(context.Background(), journalCreationFixtureIssue(), operation)
	if err != nil || created.ID != "gh-2" || f.creationWrites != 1 || f.intentWrites != 1 || f.receiptWrites != 1 {
		t.Fatal(created, err, f)
	}
	a, err := decodeActionJournal(*f.comment, "gh-9")
	if err != nil || a.Phase != "applied" || a.IssueID != "gh-2" || a.Before != nil || a.CreateInput == nil || a.After.Body != a.CreateInput.Body || a.OperationID != operation {
		t.Fatal(a, err)
	}
	if _, err := a.PlanIssueUndo(created, "ses_actor", "github-author"); err == nil || !strings.Contains(err.Error(), "logical delete") {
		t.Fatal("creation compensation limit missing", err)
	}
	if _, err := f.client.createWithOperationID(context.Background(), journalCreationFixtureIssue(), operation); err == nil {
		t.Fatal("same creation operation replayed")
	}
	if f.creationWrites != 1 || f.intentWrites != 1 {
		t.Fatal("replay created another issue or intent", f)
	}
}

func TestJournalCreationLeavesPreparedOutcomeAndNeverAutomaticallyReplays(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		creations int
		phase     string
	}{
		{"intent-denied", 0, ""}, {"intent-response", 0, "prepared"}, {"intent-read", 0, "prepared"},
		{"create-denied", 1, "prepared"}, {"create-uncertain", 1, "prepared"}, {"create-response", 1, "prepared"},
		{"create-author", 1, "prepared"}, {"create-labels", 1, "prepared"},
		{"create-readback", 1, "prepared"}, {"create-concurrent", 1, "prepared"}, {"create-duplicate", 1, "prepared"},
		{"receipt-denied", 1, "prepared"}, {"receipt-uncertain", 1, "applied"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newJournalWriteFixture(t, tc.mode)
			f.client.stateLabels = fixtureStateLabels()
			if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
				t.Fatal(err)
			}
			operation := "td-op-create-fixed"
			created, err := f.client.createWithOperationID(context.Background(), journalCreationFixtureIssue(), operation)
			if err == nil || created != nil || f.creationWrites != tc.creations || f.intentWrites != 1 || f.receiptWrites > 1 {
				t.Fatal(created, err, f)
			}
			if tc.phase != "" {
				a, e := decodeActionJournal(*f.comment, "gh-9")
				if e != nil || a.Phase != tc.phase || a.CreateInput == nil || (tc.phase == "prepared" && a.IssueID != "") {
					t.Fatal(a, e)
				}
				// A later attempt with the same operation scans its old journal
				// and refuses to POST, even if the target response was lost.
				f.mode = ""
				if _, e := f.client.createWithOperationID(context.Background(), journalCreationFixtureIssue(), operation); e == nil {
					t.Fatal("uncertain creation was replayed")
				}
				if f.creationWrites != tc.creations || f.intentWrites != 1 {
					t.Fatal("same-operation retry duplicated creation", f)
				}
			}
		})
	}
}

func TestCreationIntentCannotMasqueradeAsVerifiedState(t *testing.T) {
	input := journalCreationFixtureIssue()
	body, err := encodeBody(input.Description, metadata{OperationID: "td-op-create-fixed", Type: input.Type, Priority: input.Priority})
	if err != nil {
		t.Fatal(err)
	}
	base := ActionJournal{OperationID: "td-op-create-fixed", SessionID: "ses_actor", CarrierID: "gh-9", Action: "create", Phase: "prepared",
		CreateInput: &ActionCreateInput{Title: input.Title, Body: body, Labels: []string{}}}
	if _, err := renderActionJournal(base); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*ActionJournal){
		func(a *ActionJournal) { a.CarrierID = "" },
		func(a *ActionJournal) { a.Phase = "applied" },
		func(a *ActionJournal) { a.Action = "update" },
		func(a *ActionJournal) { a.CreateInput = nil },
		func(a *ActionJournal) { copy := *a.CreateInput; copy.Body = "Unmanaged text"; a.CreateInput = &copy },
		func(a *ActionJournal) { a.OperationID = "td-op-other" },
	} {
		a := base
		edit(&a)
		if _, err := renderActionJournal(a); err == nil {
			t.Fatal("invalid creation intent accepted", a)
		}
	}
}

func TestJournalCreationReobservesDelayedCollectionWithoutReplayingWrite(t *testing.T) {
	f := newJournalWriteFixture(t, "create-list-delayed")
	f.client.stateLabels = fixtureStateLabels()
	if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
		t.Fatal(err)
	}
	created, err := f.client.createWithOperationID(context.Background(), journalCreationFixtureIssue(), "td-op-delayed")
	if err != nil || created == nil || created.ID != "gh-2" {
		t.Fatalf("created issue missing from first collection must be reobserved: %v %v", created, err)
	}
	if f.creationCollectionReads != 2 || f.creationWrites != 1 || f.intentWrites != 1 || f.receiptWrites != 1 {
		t.Fatal(f)
	}
}

func TestJournalCreationObservationFailureNeverReplays(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		reads  int
		cancel bool
	}{
		{"create-list-missing", 3, false}, {"create-list-error", 1, false}, {"create-duplicate", 1, false}, {"create-list-missing", 1, true},
	} {
		name := tc.mode
		if tc.cancel {
			name += "-canceled"
		}
		t.Run(name, func(t *testing.T) {
			f := newJournalWriteFixture(t, tc.mode)
			f.client.stateLabels = fixtureStateLabels()
			if err := f.client.EnableActionJournal(context.Background(), "ses_actor"); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			created, err := f.client.createWithOperationID(ctx, journalCreationFixtureIssue(), "td-op-observation-failure")
			if err == nil || created != nil || f.creationCollectionReads != tc.reads || f.creationWrites != 1 || f.receiptWrites != 0 {
				t.Fatal(created, err, f)
			}
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil || a.Phase != "prepared" {
				t.Fatal(a, err)
			}
		})
	}
}
