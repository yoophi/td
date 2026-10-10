package ghstore

import (
	"context"
	"errors"
	"testing"
)

func TestObservedGroupRejectsChangedSelectionBeforeAnyWrite(t *testing.T) {
	f := newJournalWriteFixture(t, "")
	second := f.issues[1]
	second.Number, second.Title = 2, "Second"
	f.issues[2] = second
	ctx := context.Background()
	if err := f.client.TrackActions("ses_actor"); err != nil {
		t.Fatal(err)
	}
	selected := []Record{}
	for _, id := range []string{"gh-1", "gh-2"} {
		r, err := f.client.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		selected = append(selected, *r)
	}
	changed := f.issues[2]
	changed.Title = "Changed after selection"
	f.issues[2] = changed
	err := f.client.BeginObservedActionGroup(ctx, "unstart", selected)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || conflict.ID != "gh-2" || conflict.AfterWrite {
		t.Fatal("new observation silently replaced original selection", err)
	}
	if f.targetWrites != 0 || f.intentWrites != 0 || f.carrierWrites != 0 || f.receiptWrites != 0 {
		t.Fatal("stale selection wrote remote state", f)
	}
}
