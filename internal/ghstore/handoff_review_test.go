package ghstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

func handoffReviewFixture(t *testing.T) (*commentWriteFixture, *[]nativeStateEvent, ActivityGroupInput) {
	t.Helper()
	f := newCommentWriteFixture(t)
	f.client.stateLabels = fixtureStateLabels()
	issue := f.issues[1]
	issue.User.Login = "fixture-issue-author"
	issue.URL = "https://github.com/owner/repo/issues/1"
	var err error
	issue.Body, err = encodeBody("Work", metadata{Type: models.TypeTask, Priority: models.PriorityP2,
		Details: &IssueDetails{Status: models.StatusInProgress, ImplementerSession: "ses_actor"}})
	if err != nil {
		t.Fatal(err)
	}
	f.issues[1] = issue
	events := []nativeStateEvent{}
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		method, path := args[4], strings.TrimPrefix(args[5], "repos/owner/repo")
		if path == "/issues/1/events?per_page=100" {
			return json.Marshal([][]nativeStateEvent{events})
		}
		if path == "/issues/1/comments?per_page=100" {
			return json.Marshal([][]apiComment{f.native})
		}
		if strings.HasPrefix(path, "/issues/comments/20") && method == "GET" {
			for _, comment := range f.native {
				if path == fmt.Sprintf("/issues/comments/%d", comment.ID) {
					return json.Marshal(comment)
				}
			}
			return nil, fmt.Errorf("HTTP 404 comment missing")
		}
		return base(ctx, dir, payload, args...)
	}
	r, err := f.client.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	return f, &events, ActivityGroupInput{Issue: r, Activity: models.Activity{Kind: "handoff", SessionID: "ses_actor",
		WorkSessionID: "ws-fixture", OperationID: "handoff-fixture", Done: []string{"Implemented"},
		Snapshot: &models.GitSnapshot{Branch: "original", Event: "handoff"}}}
}

func TestPreparedHandoffReviewFreezesInputsAndUsesActualComment(t *testing.T) {
	f, _, input := handoffReviewFixture(t)
	ctx := context.Background()
	prepared, err := f.client.PrepareHandoffReview(ctx, []ActivityGroupInput{input}, TransitionOptions{Mode: reviewpolicy.ModeTrusted, SessionID: "ses_actor", Reason: "Original reason"})
	if err != nil {
		t.Fatal(err)
	}
	if f.writes+f.intentWrites+f.carrierWrites+f.targetWrites != 0 {
		t.Fatal("preparation wrote remote data")
	}
	p := prepared[0]
	frozen, _ := json.Marshal(p.review)
	input.Activity.Done[0] = "Caller changed content"
	input.Activity.Snapshot.Branch = "changed"
	input.Issue.Details.ImplementerSession = "another-session"
	input.Issue.Title = "Changed title"
	if err := f.client.AppendPreparedHandoff(ctx, p); err != nil {
		t.Fatal(err)
	}
	if p.published.Activity.Done[0] != "Implemented" || p.published.Activity.Snapshot.Branch != "original" {
		t.Fatal("caller replaced prepared activity")
	}
	// Another handoff can have the same native second. It must not become this
	// request's checkpoint merely because it sorts last in the comments list.
	foreign := f.native[0]
	foreign.ID++
	foreign.Body, err = renderActivity(activityData{Kind: "handoff", SessionID: "someone-else", OperationID: "foreign-handoff", Done: []string{"Different work"}})
	if err != nil {
		t.Fatal(err)
	}
	f.native = append(f.native, foreign)
	requested, err := f.client.SubmitPreparedHandoffReview(ctx, p)
	if err != nil || !requested {
		t.Fatal(requested, err)
	}
	r, err := f.client.Get(ctx, "gh-1")
	if err != nil || r.Status != models.StatusInReview || r.Details.ReviewHandoffID != p.published.Activity.ID || r.ImplementerSession != "ses_actor" {
		t.Fatalf("review lost original attribution or handoff: %+v %v", r, err)
	}
	after, _ := json.Marshal(p.review)
	if string(after) != string(frozen) || p.review.Details.ReviewHandoffID != "" {
		t.Fatal("binding actual comment rewrote the requested template")
	}
	last := r.Details.Transitions[len(r.Details.Transitions)-1]
	want := p.review.Details.Transitions[len(p.review.Details.Transitions)-1]
	if !reflect.DeepEqual(last, want) || last.Reason != "Original reason" {
		t.Fatal("review regenerated frozen transition identity or contents")
	}
	writes := f.targetWrites
	if _, err := f.client.SubmitPreparedHandoffReview(ctx, p); err == nil || f.targetWrites != writes {
		t.Fatal("repeated submission replayed a target write")
	}
}

func TestPreparedHandoffReviewRejectsForeignChangesAndUncertainReplay(t *testing.T) {
	for _, change := range []string{"source", "comment", "events", "lost-comment", "original-noop"} {
		t.Run(change, func(t *testing.T) {
			f, events, input := handoffReviewFixture(t)
			ctx := context.Background()
			if change == "original-noop" {
				issue := f.issues[1]
				issue.Body = "Originally open"
				f.issues[1] = issue
				input.Issue, _ = f.client.Get(ctx, "gh-1")
			}
			prepared, err := f.client.PrepareHandoffReview(ctx, []ActivityGroupInput{input}, TransitionOptions{Mode: reviewpolicy.ModeTrusted, SessionID: "ses_actor"})
			if err != nil {
				t.Fatal(err)
			}
			p := prepared[0]
			if change == "lost-comment" {
				f.mode = "lost"
			}
			err = f.client.AppendPreparedHandoff(ctx, p)
			if change == "lost-comment" {
				if err == nil || len(f.native) != 1 {
					t.Fatal("lost native response was not exercised", err)
				}
				f.mode = ""
				if err := f.client.AppendPreparedHandoff(ctx, p); err == nil || f.writes != 1 {
					t.Fatal("uncertain comment was replayed")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "source", "original-noop":
				issue := f.issues[1]
				issue.Title = "Same-clock foreign change"
				if change == "original-noop" {
					issue.Body, _ = encodeBody("Changed", metadata{Type: models.TypeTask, Priority: models.PriorityP2,
						Details: &IssueDetails{Status: models.StatusInProgress, ImplementerSession: "foreign"}})
				}
				f.issues[1] = issue
			case "comment":
				f.native[0].Body = "Edited handoff"
			case "events":
				*events = []nativeStateEvent{{ID: 1, Event: "closed"}, {ID: 2, Event: "reopened"}}
			}
			writes := f.intentWrites + f.receiptWrites + f.targetWrites + f.writes
			if _, err := f.client.SubmitPreparedHandoffReview(ctx, p); err == nil {
				t.Fatal("foreign or uncertain observation accepted")
			}
			if f.intentWrites+f.receiptWrites+f.targetWrites+f.writes != writes {
				t.Fatal("rejected submission wrote remote data")
			}
		})
	}
}

func TestPreparedHandoffReviewValidatesEveryTargetBeforeWrites(t *testing.T) {
	for _, invalid := range []string{"oversized", "empty-content", "other-operation", "duplicate", "stale"} {
		t.Run(invalid, func(t *testing.T) {
			f, _, input := handoffReviewFixture(t)
			ctx := context.Background()
			second := f.issues[1]
			second.Number = 2
			// Only the first is reviewed; the later handoff still needs preflight.
			second.Body = "Open second task"
			f.issues[2] = second
			r, err := f.client.Get(ctx, "gh-2")
			if err != nil {
				t.Fatal(err)
			}
			next := ActivityGroupInput{Issue: r, Activity: input.Activity}
			switch invalid {
			case "oversized":
				next.Activity.Done = []string{strings.Repeat("x", actionCommentBudget)}
			case "empty-content":
				next.Activity.Done = nil
			case "other-operation":
				next.Activity.OperationID = "different"
			case "duplicate":
				next.Issue = input.Issue
			case "stale":
				second.UpdatedAt = second.UpdatedAt.Add(time.Second)
				f.issues[2] = second
			}
			_, err = f.client.PrepareHandoffReview(ctx, []ActivityGroupInput{input, next}, TransitionOptions{Mode: reviewpolicy.ModeTrusted, SessionID: "ses_actor"})
			want := map[string]string{"oversized": "publication budget", "empty-content": "at least one item", "other-operation": "one complete work-session operation", "duplicate": "repeats gh-1", "stale": "changed since"}[invalid]
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected %q before writes, got %v", want, err)
			}
			if f.intentWrites+f.receiptWrites+f.carrierWrites+f.targetWrites+f.writes != 0 {
				t.Fatal("invalid later input caused earlier writes")
			}
		})
	}
}

func TestPreparedHandoffReviewCapacityReusesEventBaseline(t *testing.T) {
	f, _, input := handoffReviewFixture(t)
	reads := 0
	base := f.client.run
	f.client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		if strings.HasSuffix(args[5], "/events?per_page=100") {
			reads++
		}
		return base(ctx, dir, payload, args...)
	}
	_, err := f.client.PrepareHandoffReview(context.Background(), []ActivityGroupInput{input},
		TransitionOptions{Mode: reviewpolicy.ModeTrusted, SessionID: "ses_actor"})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("capacity checks reread native event baseline: got %d event reads, want 1", reads)
	}
}
