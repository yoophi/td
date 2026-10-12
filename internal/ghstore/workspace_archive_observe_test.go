package ghstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/transferlimit"
)

func TestObserveWorkspaceArchiveUsesFreshBulkEvidenceWithoutReplayingWrites(t *testing.T) {
	for _, stage := range []string{"complete", "partial", "empty", "absent_carrier", "changed_part", "foreign_author", "changed_mappings", "changed_source", "duplicate_index", "duplicate_root", "carrier_changed", "capacity", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			plan, root, parts, source := workspaceArchiveFixture(t)
			body, err := encodeBody("journal", metadata{EntityKind: "journal", Type: models.TypeTask, Priority: models.PriorityP2})
			if err != nil {
				t.Fatal(err)
			}
			carrier := apiIssue{Number: 9, Title: "journal", Body: body, State: "open", CreatedAt: root.CreatedAt, UpdatedAt: root.CreatedAt}
			rows := []apiIssue{carrier}
			comments := append(append([]apiComment{}, parts...), root)
			mappings := map[string]string{"source:task": "gh-10"}
			maxBytes := int64(0)
			switch stage {
			case "partial":
				comments = append([]apiComment{}, parts[:1]...)
			case "empty":
				comments = []apiComment{}
			case "absent_carrier":
				rows, comments = []apiIssue{}, []apiComment{}
			case "changed_part":
				comments[0].UpdatedAt = comments[0].UpdatedAt.Add(1)
			case "foreign_author":
				comments[0].User.Login = "someone-else"
			case "changed_mappings":
				mappings["source:task"] = "gh-11"
			case "changed_source":
				source = []byte(strings.Replace(string(source), "history", "foreign", 1))
			case "duplicate_index":
				duplicate := parts[0]
				duplicate.ID = 9999
				comments = append(comments, duplicate)
			case "duplicate_root":
				duplicate := root
				duplicate.ID = 9999
				comments = append(comments, duplicate)
			case "capacity":
				maxBytes = 1
			}
			client, calls := snapshotFixture([][]apiIssue{rows}, [][]apiComment{comments}, nil)
			if err := client.TrackActions("ses_original"); err != nil {
				t.Fatal(err)
			}
			if stage == "carrier_changed" {
				client.journal.Carrier.ID = "gh-8"
			}
			base, actors := client.run, 0
			client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
				if args[4] != "GET" {
					t.Fatal("observation attempted a mutation", args)
				}
				if args[5] == "user" {
					actors++
					return []byte(`{"login":"publisher"}`), nil
				}
				return base(ctx, dir, payload, args...)
			}
			ctx := context.Background()
			if stage == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := client.ObserveWorkspaceArchiveData(ctx, plan.manifest.OperationID, source, mappings, maxBytes)
			switch stage {
			case "complete":
				if err != nil || result.Archive == nil || result.Archive.ID != root.ID || len(result.MissingParts) != 0 || len(result.Parts) != len(parts) || !reflect.DeepEqual(result.Archive.Mappings, mappings) || len(*calls) != 2 || actors != 1 {
					t.Fatal("published result lost or N+1 query", result, err, *calls, actors)
				}
			case "partial":
				if err != nil || result.Archive != nil || len(result.Parts) != 1 || result.Parts[0].ID != parts[0].ID || len(result.MissingParts) != len(parts)-1 || len(*calls) != 2 || actors != 1 {
					t.Fatal("partial evidence lost or became completion", result, err)
				}
			case "empty", "absent_carrier":
				if err != nil || result.Archive != nil || len(result.Parts) != 0 || len(result.MissingParts) != len(parts) {
					t.Fatal("absent upload bootstrapped or became completed", result, err)
				}
			default:
				if err == nil || result != nil {
					t.Fatal("invalid observation returned partial evidence", result, err)
				}
			}
			if stage == "capacity" && (!errors.Is(err, transferlimit.ErrCapacity) || len(*calls) != 0 || actors != 0) {
				t.Fatal("capacity failure accessed GitHub", err)
			}
			if stage == "cancelled" && (!errors.Is(err, context.Canceled) || len(*calls) != 0 || actors != 0) {
				t.Fatal("cancelled observation accessed GitHub", err)
			}
		})
	}
}
