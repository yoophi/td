package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/marcus/td/internal/models"
)

func transitionFixture(t *testing.T) (*Client, map[string]any, *int) {
	t.Helper()
	state := map[string]any{"number": 1, "state": "open", "title": "Fixture"}
	writes := 0
	client := &Client{stateLabels: fixtureStateLabels(), repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "PATCH") {
			writes++
			var patch map[string]any
			if err := json.Unmarshal(payload, &patch); err != nil {
				t.Fatal(err)
			}
			for key, value := range patch {
				state[key] = value
			}
		}
		return fixtureIssueJSON(state)
	}}
	return client, state, &writes
}

func TestClaimLifecyclePreservesHistoryAndNativeState(t *testing.T) {
	client, state, writes := transitionFixture(t)
	ctx := context.Background()
	options := TransitionOptions{SessionID: "implementer", Reason: "working", Snapshot: &models.GitSnapshot{CommitSHA: "abc", Branch: "feature"}}
	result, noop, err := client.Transition(ctx, "1", "start", options)
	if err != nil || noop || result.Status != models.StatusInProgress || result.ImplementerSession != "implementer" || state["state"] != "open" || *writes != 1 {
		t.Fatalf("%+v noop=%v writes=%d %v", result, noop, *writes, err)
	}
	if len(result.Details.Transitions) != 1 || result.Details.Transitions[0].Snapshot.IssueID != "gh-1" || result.Details.Transitions[0].Reason != "working" {
		t.Fatalf("missing transition evidence: %+v", result.Details)
	}
	_, noop, err = client.Transition(ctx, "1", "start", options)
	if err != nil || !noop || *writes != 1 {
		t.Fatalf("repeat start: %v %v %d", noop, err, *writes)
	}
	if _, _, err := client.Transition(ctx, "1", "start", TransitionOptions{SessionID: "other", Force: true}); err == nil || *writes != 1 {
		t.Fatal("force stole live claim")
	}
	result, _, err = client.Transition(ctx, "1", "block", options)
	if err != nil || result.Status != models.StatusBlocked || result.ImplementerSession != "implementer" {
		t.Fatalf("%+v %v", result, err)
	}
	if _, _, err := client.Transition(ctx, "1", "start", options); err == nil {
		t.Fatal("blocked claim started without force")
	}
	result, _, err = client.Transition(ctx, "1", "unblock", options)
	if err != nil || result.Status != models.StatusOpen || result.ImplementerSession != "" || len(result.Details.Sessions) != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	_, _, err = client.Transition(ctx, "1", "start", options)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err = client.Transition(ctx, "1", "unstart", TransitionOptions{SessionID: "supervisor", Reason: "worker stopped"})
	if err != nil || result.ImplementerSession != "" || result.Status != models.StatusOpen {
		t.Fatalf("%+v %v", result, err)
	}
	last := result.Details.Transitions[len(result.Details.Transitions)-1]
	if last.SessionID != "supervisor" || result.Details.Sessions[len(result.Details.Sessions)-1].SessionID != "implementer" {
		t.Fatal("lost release actor or former holder")
	}
	count := *writes
	_, noop, err = client.Transition(ctx, "1", "unstart", options)
	if err != nil || !noop || *writes != count {
		t.Fatal("repeat release wrote again")
	}
	state["state"] = "closed"
	for _, action := range []string{"start", "unstart", "block", "unblock"} {
		if _, _, err := client.Transition(ctx, "1", action, options); err == nil {
			t.Errorf("allowed %s closed issue", action)
		}
	}
	if *writes != count {
		t.Fatal("invalid transition wrote data")
	}
}

func TestConcurrentClaimAndFailure(t *testing.T) {
	for _, mode := range []string{"conflict", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			reads, writes := 0, 0
			state := map[string]any{"number": 1, "state": "open", "title": "Fixture"}
			client := &Client{stateLabels: fixtureStateLabels(), repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
				if slices.Contains(args, "GET") {
					reads++
					if mode == "conflict" && reads == 2 {
						state["title"] = "other writer"
					}
				} else {
					writes++
					return nil, context.DeadlineExceeded
				}
				return fixtureIssueJSON(state)
			}}
			_, _, err := client.Transition(context.Background(), "1", "start", TransitionOptions{SessionID: "ses"})
			if err == nil || !strings.Contains(err.Error(), "td-op-") {
				t.Fatalf("no recovery identity: %v", err)
			}
			if mode == "conflict" {
				var conflict *ConflictError
				if !errors.As(err, &conflict) || writes != 0 {
					t.Fatalf("writes=%d %v", writes, err)
				}
			} else if writes != 1 {
				t.Fatal("retried uncertain write")
			}
		})
	}
}

func TestDetailsCopyDetachesNestedHistory(t *testing.T) {
	client, _, _ := transitionFixture(t)
	observed, _, err := client.Transition(context.Background(), "1", "start", TransitionOptions{SessionID: "ses", Snapshot: &models.GitSnapshot{Branch: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	detached, err := observed.CopyDetails()
	if err != nil {
		t.Fatal(err)
	}
	detached.Sessions[0].SessionID = "changed"
	detached.Transitions[0].Snapshot.Branch = "changed"
	if observed.Details.Sessions[0].SessionID != "ses" || observed.Details.Transitions[0].Snapshot.Branch != "main" {
		t.Fatal("copy mutated observation")
	}
}

func TestNativeCloseReopenDoesNotResurrectClaim(t *testing.T) {
	client, _, _ := transitionFixture(t)
	ctx := context.Background()
	if _, _, err := client.Transition(ctx, "1", "start", TransitionOptions{SessionID: "implementer"}); err != nil {
		t.Fatal(err)
	}
	closed := models.StatusClosed
	result, err := client.Update(ctx, "1", Changes{Status: &closed})
	if err != nil || result.Status != models.StatusClosed || result.ReviewerSession != "" {
		t.Fatalf("%+v %v", result, err)
	}
	open := models.StatusOpen
	result, err = client.Update(ctx, "1", Changes{Status: &open})
	if err != nil || result.Status != models.StatusOpen || result.ImplementerSession != "" {
		t.Fatalf("stale claim resurrected: %+v %v", result, err)
	}
	if len(result.Details.Sessions) != 1 || result.Details.Sessions[0].SessionID != "implementer" {
		t.Fatal("lost implementation evidence")
	}
}
