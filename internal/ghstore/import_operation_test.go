package ghstore

import (
	"context"
	"strings"
	"testing"
)

func TestPinnedImportUsesOriginalRootAndRejectsChangedInputOrOperation(t *testing.T) {
	for _, stage := range []string{"complete", "wrong_operation", "changed_input", "foreign_session"} {
		t.Run(stage, func(t *testing.T) {
			f := newWritableOwnedAdoptionFixture(t)
			if err := f.client.TrackActions("ses_owner"); err != nil {
				t.Fatal(err)
			}
			operation := f.journal.OperationID
			if stage == "wrong_operation" {
				if _, err := f.client.ImportWithOperationID(context.Background(), f.items, false, "other-operation"); err == nil || *f.writes != 0 {
					t.Fatal("pending command changed operation", err, *f.writes)
				}
				return
			}
			result, err := f.client.ImportWithOperationID(context.Background(), f.items, false, operation)
			if err != nil || result.Created != 1 || result.Completed != 1 || result.Skipped != 1 {
				t.Fatal(result, err)
			}
			writes, parts := *f.writes, len(f.parts)
			switch stage {
			case "changed_input":
				f.items[0].Issue.Title = "new source payload"
			case "foreign_session":
				f.client.journal.SessionID = "ses_elsewhere"
			}
			result, err = f.client.ImportWithOperationID(context.Background(), f.items, false, operation)
			if stage == "complete" {
				if err != nil || result.Skipped != 2 {
					t.Fatal("confirmed repeat did not observe original command", result, err)
				}
			} else if err == nil {
				t.Fatal("changed source/actor reused pinned operation", stage)
			}
			if *f.writes != writes || len(f.parts) != parts {
				t.Fatal("repeat or rejected identity wrote again")
			}
		})
	}
}

func TestPinnedImportRejectsInvalidIdentityBeforeClientAccess(t *testing.T) {
	var nilClient *Client
	if _, err := nilClient.ImportWithOperationID(context.Background(), nil, false, "operation"); err == nil {
		t.Fatal("untracked pinned import reached client")
	}
	f := newWritableOwnedAdoptionFixture(t)
	if err := f.client.TrackActions("ses_owner"); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"", "unsafe/operation", strings.Repeat("x", 129)} {
		if _, err := f.client.ImportWithOperationID(context.Background(), f.items, false, operation); err == nil || *f.writes != 0 {
			t.Fatal("invalid pinned operation attempted a write", operation, err)
		}
	}
}
