package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func prepareEntityUndoFixture(t *testing.T, kind string) (*journalWriteFixture, apiIssue) {
	t.Helper()
	f := newJournalWriteFixture(t, "")
	ctx := context.Background()
	title := "Changed auxiliary entity"
	var before apiIssue
	if kind == "note" {
		n, err := f.client.CreateNote(ctx, "Original note", "Original content", "ses_actor")
		if err != nil {
			t.Fatal(err)
		}
		before = f.issues[2]
		if err := f.client.TrackActions("ses_actor"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.client.UpdateNoteObserved(ctx, n, NoteChanges{Title: &title}, "ses_actor"); err != nil {
			t.Fatal(err)
		}
	} else {
		b, err := f.client.CreateBoard(ctx, "Original board", "status = open", "ses_actor")
		if err != nil {
			t.Fatal(err)
		}
		before = f.issues[2]
		if err := f.client.TrackActions("ses_actor"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.client.UpdateBoardObserved(ctx, b, BoardChanges{Name: &title}, "ses_actor"); err != nil {
			t.Fatal(err)
		}
	}
	f.targetNumber = 2
	f.commentReads = 0
	return f, before
}

func TestEntityUndoRestoresExactNativeBodyAndReplaysWithoutWrites(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		t.Run(kind, func(t *testing.T) {
			f, before := prepareEntityUndoFixture(t, kind)
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil {
				t.Fatal(err)
			}
			if result.Journal.Phase != "undone" || result.Issue.nativeBody != before.Body || result.Issue.Title != before.Title || f.targetWrites != 2 {
				t.Fatal(result, f.targetWrites)
			}
			writes, receipts := f.targetWrites, f.receiptWrites
			result, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err != nil || !result.AlreadyUndone || f.targetWrites != writes || f.receiptWrites != receipts {
				t.Fatal(result, err)
			}
		})
	}
}

func TestEntityUndoFailureBoundariesNeverRetryOrOverwriteLaterEdits(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		for _, mode := range []string{"undo-actor", "target-conflict", "target-denied", "target-uncertain", "receipt-denied", "receipt-uncertain", "journal-concurrent", "undo-later", "undo-receipt-denied", "undo-receipt-uncertain"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f, _ := prepareEntityUndoFixture(t, kind)
				f.mode = mode
				writes := f.targetWrites
				result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
				if err == nil || result != nil || f.targetWrites > writes+1 {
					t.Fatal(result, err, f.targetWrites)
				}
				if mode == "undo-actor" || mode == "target-conflict" || mode == "receipt-denied" || mode == "receipt-uncertain" || mode == "journal-concurrent" {
					if f.targetWrites != writes {
						t.Fatal("unverified intent/actor wrote target")
					}
				}
				if mode == "target-conflict" && f.issues[2].Body != "Concurrent edit" {
					t.Fatal("later edit overwritten")
				}
				if mode == "undo-later" {
					var conflict *ConflictError
					if !errors.As(err, &conflict) || !conflict.AfterWrite {
						t.Fatal(err)
					}
				}
				a, decodeErr := decodeActionJournal(*f.comment, "gh-9")
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if a != nil && a.Phase == "applied" {
					return
				} // Rejected author/intent left no uncertain write.
				f.mode = ""
				writes = f.targetWrites
				_, err = f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
				if f.targetWrites > writes {
					t.Fatal("uncertain inverse automatically replayed", err)
				}
			})
		}
	}
}

func TestBoardUndoRejectsCatalogCollisionBeforeAndAfterIntent(t *testing.T) {
	for _, stage := range []string{"before-intent", "after-intent", "after-target"} {
		t.Run(stage, func(t *testing.T) {
			f, before := prepareEntityUndoFixture(t, "board")
			original := f.client.run
			reads := 0
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if args[4] == "GET" && args[5] == "repos/owner/repo/issues?state=all&per_page=100" {
					reads++
					at := map[string]int{"before-intent": 1, "after-intent": 2, "after-target": 3}[stage]
					if reads == at {
						other := before
						other.Number = 3
						f.issues[3] = other
					}
				}
				return original(ctx, dir, payload, args...)
			}
			writes := f.targetWrites
			result, err := f.client.UndoAction(context.Background(), "ghc-101", "ses_actor")
			if err == nil || result != nil {
				t.Fatal("catalog collision restored duplicate name", result, err)
			}
			allowed := 0
			if stage == "after-target" {
				allowed = 1
			}
			if f.targetWrites != writes+allowed {
				t.Fatal("unexpected target writes", f.targetWrites)
			}
			a, err := decodeActionJournal(*f.comment, "gh-9")
			if err != nil {
				t.Fatal(err)
			}
			phase := "undo_prepared"
			if stage == "before-intent" {
				phase = "applied"
			}
			if a.Phase != phase {
				t.Fatal("invented verified inverse", a)
			}
		})
	}
}

func TestEntityUndoRetainsClosedNativeStateAndLabelsWithoutPatchingThem(t *testing.T) {
	for _, kind := range []string{"note", "board"} {
		t.Run(kind, func(t *testing.T) {
			f := newJournalWriteFixture(t, "")
			ctx := context.Background()
			if kind == "note" {
				if _, err := f.client.CreateNote(ctx, "Original", "Body", "ses_actor"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.client.CreateBoard(ctx, "Original", "status = open", "ses_actor"); err != nil {
					t.Fatal(err)
				}
			}
			native := f.issues[2]
			native.State = "closed"
			native.Labels = append(native.Labels, struct {
				Name string `json:"name"`
			}{"native-user-label"})
			f.issues[2] = native
			if err := f.client.TrackActions("ses_actor"); err != nil {
				t.Fatal(err)
			}
			title := "Changed"
			if kind == "note" {
				n, err := f.client.noteCarrier(ctx, 2)
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := f.client.UpdateNoteObserved(ctx, n, NoteChanges{Title: &title}, "ses_actor"); err != nil {
					t.Fatal(err)
				}
			} else {
				b, err := f.client.boardCarrier(ctx, 2)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.client.UpdateBoardObserved(ctx, b, BoardChanges{Name: &title}, "ses_actor"); err != nil {
					t.Fatal(err)
				}
			}
			original := f.client.run
			f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if args[4] == "PATCH" && args[5] == "repos/owner/repo/issues/2" {
					var values map[string]json.RawMessage
					if err := json.Unmarshal(payload, &values); err != nil {
						t.Fatal(err)
					}
					if len(values) != 2 || values["title"] == nil || values["body"] == nil {
						t.Fatal("auxiliary inverse patched native state/labels", string(payload))
					}
				}
				return original(ctx, dir, payload, args...)
			}
			result, err := f.client.UndoAction(ctx, "ghc-101", "ses_actor")
			if err != nil || result.Issue.nativeState != "closed" || len(result.Issue.Labels) != 1 || result.Issue.Labels[0] != "native-user-label" {
				t.Fatal(result, err)
			}
		})
	}
}
