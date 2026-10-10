package ghstore

import (
	"context"
	"testing"
)

func TestActionJournalTrackingDefersBootstrapUntilValidatedWrite(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	delete(f.issues, 9)
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	if f.carrierWrites != 0 || f.intentWrites != 0 {
		t.Fatal("tracking wrote remote state")
	}
	if err := f.client.TrackActions("ses_other"); err == nil {
		t.Fatal("tracking changed the actor")
	}
	issue := journalCreationFixtureIssue()
	issue.Title = ""
	if _, err := f.client.Create(context.Background(), issue); err == nil || f.carrierWrites != 0 || f.intentWrites != 0 || f.creationWrites != 0 {
		t.Fatal("invalid creation bootstrapped a journal", err)
	}
	empty := ""
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Title: &empty}); err == nil || f.carrierWrites != 0 || f.intentWrites != 0 || f.targetWrites != 0 {
		t.Fatal("invalid update bootstrapped a journal", err)
	}
	body := "After"
	if _, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &body}); err != nil {
		t.Fatal(err)
	}
	if f.carrierWrites != 1 || f.intentWrites != 1 || f.receiptWrites != 1 || f.targetWrites != 1 {
		t.Fatal("validated write was not tracked", f)
	}
	if err := f.client.TrackActions("ses_actor"); err != nil || f.carrierWrites != 1 {
		t.Fatal("same actor reinitialized journal", err)
	}
}

func TestActionJournalLazyTrackingRetainsFailuresWithoutTargetWrites(t *testing.T) {
	for _, mode := range []string{"duplicate-bootstrap", "intent-denied", "intent-uncertain", "intent-read"} {
		t.Run(mode, func(t *testing.T) {
			f := newJournalWriteFixture(t, mode)
			if mode == "duplicate-bootstrap" {
				delete(f.issues, 9)
			}
			if err := f.client.TrackActions("ses_actor"); err != nil {
				t.Fatal(err)
			}
			body := "After"
			if _, err := f.client.Update(context.Background(), "gh-1", Changes{Description: &body}); err == nil || f.targetWrites != 0 {
				t.Fatal("failed lazy journal allowed target write", err)
			}
		})
	}
}
