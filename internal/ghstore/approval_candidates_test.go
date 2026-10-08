package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func TestApprovalCandidatesRespectPolicyHistoryAndRecordedApproval(t *testing.T) {
	now := time.Now().UTC()
	items := []map[string]any{}
	for n := 1; n <= 11; n++ {
		d := IssueDetails{Status: models.StatusInReview, ImplementerSession: "other", CreatorSession: "other"}
		entity, native := "issue", "open"
		switch n {
		case 2:
			d.ImplementerSession = "worker"
		case 3:
			d.CreatorSession = "worker"
		case 4:
			d.Sessions = []models.IssueSessionHistory{{SessionID: "worker", Action: models.ActionSessionUnstarted, CreatedAt: now}}
		case 5:
			d.ImplementerSession = "worker"
			d.Minor = true
		case 6, 7:
			d.ImplementerSession = "worker"
			d.Reviews = []models.IssueReview{{ID: "rv-fixture", ReviewerSession: "reviewer", Decision: reviewpolicy.DecisionApproved, CreatedAt: now}}
			if n == 7 {
				d.Reviews[0].SupersededAt = &now
			}
		case 8:
			d.Status = models.StatusBlocked
		case 9:
			native = "closed"
		case 10:
			d.DeletedAt = &now
		case 11:
			entity = "board"
		}
		body, err := encodeBody("Fixture", metadata{Type: models.TypeTask, Priority: models.PriorityP2, EntityKind: entity, Details: &d})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, map[string]any{"number": n, "title": fmt.Sprintf("Fixture %d", n), "state": native, "body": body})
	}
	client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		if !slices.Contains(args, "GET") || !slices.Contains(args, "--paginate") {
			t.Fatalf("candidate selection wrote or lost pagination: %v", args)
		}
		return json.Marshal([][]map[string]any{items[5:], items[:5]})
	}}
	for _, tc := range []struct {
		mode             reviewpolicy.Mode
		self, recordOnly bool
		want             []int
	}{
		{reviewpolicy.ModeStrict, false, false, []int{1, 5}},
		{reviewpolicy.ModeBalanced, false, false, []int{1, 3, 5}},
		{reviewpolicy.ModeDelegated, false, false, []int{1, 3, 5, 6}},
		{reviewpolicy.ModeTrusted, false, false, []int{1, 3, 5, 6}},
		{reviewpolicy.ModeTrusted, true, false, []int{1, 2, 3, 4, 5, 6, 7}},
		{reviewpolicy.ModeDelegated, false, true, []int{1, 3, 5}},
	} {
		t.Run(fmt.Sprintf("%s-self-%v-record-%v", tc.mode, tc.self, tc.recordOnly), func(t *testing.T) {
			records, err := client.ApprovalCandidates(context.Background(), TransitionOptions{SessionID: "worker", Mode: tc.mode, SelfReview: tc.self, RecordOnly: tc.recordOnly, Reason: "Fixture evidence"})
			if err != nil {
				t.Fatal(err)
			}
			got := []int{}
			for _, record := range records {
				got = append(got, record.Number)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestApprovalSelectionDoesNotAuthorizeLaterChangedContent(t *testing.T) {
	f := newReviewFixture(t)
	f.transition(t, "review", TransitionOptions{SessionID: "worker", Mode: reviewpolicy.ModeStrict})
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "repos/owner/repo/issues?state=open&per_page=100") {
			data, err := fixtureIssueJSON(f.issue)
			return []byte("[[" + string(data) + "]]"), err
		}
		return base(ctx, dir, payload, args...)
	}
	options := TransitionOptions{SessionID: "reviewer", Mode: reviewpolicy.ModeStrict}
	candidates, err := f.client.ApprovalCandidates(context.Background(), options)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("%+v %v", candidates, err)
	}
	f.issue["title"] = "Changed after selection"
	before := f.writes
	if _, _, err := f.client.Transition(context.Background(), candidates[0].ID, "approve", options); err == nil || f.writes != before {
		t.Fatalf("stale selection approved: %v", err)
	}
}
