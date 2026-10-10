package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func TestLabelRepairPreflightBeforeAnyWrite(t *testing.T) {
	for _, kind := range []string{"state", "priority"} {
		for _, mode := range []string{"invalid-later-target", "stale-selection"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newJournalWriteFixture(t, "")
				f.client.stateLabels = fixtureStateLabels()
				body, err := encodeBody("Managed", metadata{Type: models.TypeTask, Priority: models.PriorityP2})
				if err != nil {
					t.Fatal(err)
				}
				first := f.issues[1]
				first.Body = body
				second := first
				second.Number, second.Title = 2, "Second"
				if mode == "invalid-later-target" {
					second.Title = ""
				}
				f.issues[1], f.issues[2] = first, second
				base := f.client.run
				f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
					if args[4] == "GET" && strings.Contains(args[5], "/issues?state=all") {
						// Stable order ensures that a valid first target cannot be
						// written before the invalid/stale second target is checked.
						data, err := json.Marshal([][]apiIssue{{f.issues[1], f.issues[2], f.issues[9]}})
						if mode == "stale-selection" {
							second.Title = "Foreign same-clock edit"
							f.issues[2] = second
						}
						return data, err
					}
					return base(ctx, dir, payload, args...)
				}
				if err := f.client.TrackActions("ses_actor"); err != nil {
					t.Fatal(err)
				}
				var count int
				if kind == "state" {
					count, err = f.client.SyncStateLabels(context.Background())
				} else {
					result, repairErr := f.client.SyncPriorityLabels(context.Background(), true)
					count, err = result.Applied, repairErr
				}
				if err == nil || count != 0 || f.targetWrites != 0 || f.intentWrites != 0 || f.carrierWrites != 0 || f.receiptWrites != 0 {
					t.Fatal("label batch wrote before complete preflight", count, err, f)
				}
				var conflict *ConflictError
				if mode == "stale-selection" && (!errors.As(err, &conflict) || conflict.ID != "gh-2") {
					t.Fatal("stale member was silently rebased", err)
				}
			})
		}
	}
}
