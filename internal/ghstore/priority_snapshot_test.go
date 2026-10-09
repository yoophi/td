package ghstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestPriorityDiagnosticsSurviveCacheAndIncrementalLabelEdits(t *testing.T) {
	f := newIncrementalFixture(t)
	body, err := encodeBody("Managed priority", metadata{Type: models.TypeTask, Priority: models.PriorityP1})
	if err != nil {
		t.Fatal(err)
	}
	item := apiIssue{Number: 1, State: "open", Body: body, UpdatedAt: time.Now().Add(-time.Hour).UTC()}
	f.fullIssues = [][]apiIssue{{item}}
	ctx := context.Background()
	check := func(warning bool, calls int) {
		t.Helper()
		snapshot, err := f.client.ReadSnapshot(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		rows := snapshot.Issues(false)
		if len(rows) != 1 || rows[0].Priority != models.PriorityP1 || (rows[0].PriorityLabelDiagnostic != "") != warning {
			t.Fatalf("metadata priority or diagnostic lost: %+v", rows)
		}
		if len(f.calls) != calls {
			t.Fatalf("unexpected requests: %v", f.calls)
		}
	}
	// A legacy issue and the persisted cache both retain the metadata priority
	// and missing-label warning without individual issue/label requests.
	check(true, 2)
	f.calls = nil
	check(true, 0)
	// An external label edit is reflected by the next incremental observation;
	// it cannot replace the metadata priority with the label's priority.
	ageIncrementalCache(t, f.client, 31*time.Second, time.Minute)
	item.UpdatedAt = time.Now().UTC()
	item.Labels = []struct {
		Name string `json:"name"`
	}{{Name: "td:priority:P4"}}
	f.deltaIssues = [][]apiIssue{{item}}
	check(true, 2)
	for _, endpoint := range f.calls {
		if !strings.Contains(endpoint, "since=") {
			t.Fatalf("label update abandoned incremental reads: %v", f.calls)
		}
	}
	// A matching mirror clears the warning in both the new observation and
	// subsequent cached reads; case-insensitive canonical labels are accepted.
	ageIncrementalCache(t, f.client, 31*time.Second, time.Minute)
	f.calls = nil
	item.UpdatedAt = time.Now().UTC()
	item.Labels[0].Name = "TD:PRIORITY:P1"
	f.deltaIssues = [][]apiIssue{{item}}
	check(false, 2)
	f.calls = nil
	check(false, 0)
}
