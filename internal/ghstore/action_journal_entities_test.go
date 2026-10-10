package ghstore

import (
	"context"
	"strings"
	"testing"
)

func TestJournalEntitySchemaUsesV3AndPreservesOlderRecords(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		f := newJournalWriteFixture(t, "")
		if err := f.client.TrackActions("ses_actor"); err != nil {
			t.Fatal(err)
		}
		var record *Record
		if kind == "note" {
			n, err := f.client.CreateNote(context.Background(), "Note fixture", "Original note", "ses_actor")
			if err != nil {
				t.Fatal(err)
			}
			record = &n.observed
		} else {
			b, err := f.client.CreateBoard(context.Background(), "Board fixture", "status = open", "ses_actor")
			if err != nil {
				t.Fatal(err)
			}
			record = &b.observed
		}
		if !strings.Contains(f.comment.Body, actionEntityJournalStart) {
			t.Fatal("entity used issue journal version", f.comment.Body)
		}
		action, err := decodeActionJournal(*f.comment, "gh-9")
		if err != nil || action.Phase != "applied" || action.Action != "create" || action.Before != nil || action.After.Body != record.nativeBody {
			t.Fatal(action, err)
		}
		typ, id := action.EntityIdentity()
		if typ != kind || (kind == "note" && id != "nt-gh-2") || (kind == "board" && id != "bd-gh-2") {
			t.Fatal(typ, id)
		}
		bad := *f.comment
		bad.Body = strings.Replace(bad.Body, actionEntityJournalStart, actionJournalStart, 1)
		if _, err := decodeActionJournal(bad, "gh-9"); err == nil {
			t.Fatal("v1 accepted an entity payload")
		}
	}
	legacy := journalCommentFixture(t, journalFixture(t))
	if _, err := decodeActionJournal(legacy, "gh-1"); err != nil {
		t.Fatal(err)
	}
	comment := journalCommentFixture(t, commentJournalFixture(t))
	if _, err := decodeActionJournal(comment, "gh-9"); err != nil {
		t.Fatal(err)
	}
}

func TestJournalEntityCreationAndUpdateFailureBoundaries(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		for _, mode := range []string{"intent-denied", "create-denied", "create-uncertain", "receipt-denied", "receipt-uncertain"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newJournalWriteFixture(t, mode)
				if err := f.client.TrackActions("ses_actor"); err != nil {
					t.Fatal(err)
				}
				var err error
				if kind == "note" {
					_, err = f.client.CreateNote(context.Background(), "Note fixture", "Body", "ses_actor")
				} else {
					_, err = f.client.CreateBoard(context.Background(), "Board fixture", "status = open", "ses_actor")
				}
				if err == nil {
					t.Fatal("unverified creation succeeded")
				}
				if mode == "intent-denied" && f.creationWrites != 0 {
					t.Fatal("intent denial wrote target")
				}
				if f.creationWrites > 1 {
					t.Fatal("uncertain creation retried")
				}
				if f.comment != nil {
					action, e := decodeActionJournal(*f.comment, "gh-9")
					if e != nil || (action.Phase != "prepared" && mode != "receipt-uncertain") {
						t.Fatal(action, e)
					}
				}
			})
		}
	}
}

func TestJournalEntityUpdatesValidateActorAndStoreExactBeforeAfter(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := newJournalWriteFixture(t, "")
			// Prepare a native carrier with the legacy writer, then enable history.
			if kind == "note" {
				n, err := f.client.CreateNote(context.Background(), "Note fixture", "Original note", "ses_actor")
				if err != nil {
					t.Fatal(err)
				}
				if err := f.client.TrackActions("ses_actor"); err != nil {
					t.Fatal(err)
				}
				title := "Changed note"
				if _, _, err := f.client.UpdateNoteObserved(context.Background(), n, NoteChanges{Title: &title}, "ses_other"); err == nil {
					t.Fatal("actor mismatch wrote")
				}
				changed, wrote, err := f.client.UpdateNoteObserved(context.Background(), n, NoteChanges{Title: &title}, "ses_actor")
				if err != nil || !wrote {
					t.Fatal(changed, wrote, err)
				}
			} else {
				b, err := f.client.CreateBoard(context.Background(), "Board fixture", "status = open", "ses_actor")
				if err != nil {
					t.Fatal(err)
				}
				if err := f.client.TrackActions("ses_actor"); err != nil {
					t.Fatal(err)
				}
				title := "Changed board"
				if _, err := f.client.UpdateBoardObserved(context.Background(), b, BoardChanges{Name: &title}, "ses_other"); err == nil {
					t.Fatal("actor mismatch wrote")
				}
				if _, err := f.client.UpdateBoardObserved(context.Background(), b, BoardChanges{Name: &title}, "ses_actor"); err != nil {
					t.Fatal(err)
				}
			}
			action, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil || action.Action != kind || action.Phase != "applied" || action.UpdateIntent == nil || action.UpdateIntent.Target.Title != action.After.Title || action.UpdateIntent.Target.Body != action.After.Body || action.Before.Title == action.After.Title || f.targetWrites != 1 {
				t.Fatal(action, err, f.targetWrites)
			}
		})
	}
}

func TestJournalEntityUpdateUncertaintyAndConcurrentChangesDoNotInventResults(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		for _, mode := range []string{"intent-denied", "intent-read", "target-conflict", "target-denied", "target-uncertain", "receipt-denied", "receipt-uncertain", "target-later"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newJournalWriteFixture(t, "")
				var n *NoteRecord
				var b *BoardRecord
				var err error
				if kind == "note" {
					n, err = f.client.CreateNote(context.Background(), "Original", "Before", "ses_actor")
				} else {
					b, err = f.client.CreateBoard(context.Background(), "Original", "status = open", "ses_actor")
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := f.client.TrackActions("ses_actor"); err != nil {
					t.Fatal(err)
				}
				f.mode = mode
				f.targetNumber = 2
				title := "Changed"
				if kind == "note" {
					_, _, err = f.client.UpdateNoteObserved(context.Background(), n, NoteChanges{Title: &title}, "ses_actor")
				} else {
					_, err = f.client.UpdateBoardObserved(context.Background(), b, BoardChanges{Name: &title}, "ses_actor")
				}
				if err == nil || f.targetWrites > 1 {
					t.Fatal("uncertain entity update succeeded/retried", err, f.targetWrites)
				}
				if (mode == "intent-denied" || mode == "intent-read" || mode == "target-conflict") && f.targetWrites != 0 {
					t.Fatal("unverified intent or revision allowed PATCH")
				}
				if f.comment != nil {
					action, e := decodeActionJournal(*f.comment, "gh-9")
					if e != nil || (action.Phase != "prepared" && mode != "receipt-uncertain" && mode != "target-later") {
						t.Fatal(action, e)
					}
				}
			})
		}
	}
}
