package backendstore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGroupIntentFreezesPlanAcrossPartialRetryAndRefreshesAfterCompletion(t *testing.T) {
	fixture, connection := newIntentServer(t, false)
	ctx := context.Background()
	input := struct{ Command string }{"ws handoff --done @file"}
	var firstKey string
	first := errors.New("second write failed")
	prepareCount := 0
	prepare := func() (json.RawMessage, error) {
		prepareCount++
		return json.Marshal(struct{ Content string }{"original file content"})
	}
	content := func(plan json.RawMessage) string {
		var decoded struct{ Content string }
		if e := json.Unmarshal(plan, &decoded); e != nil {
			t.Fatal(e)
		}
		return decoded.Content
	}
	err := connection.RunGroupIntent(ctx, input, prepare, func(key string, plan json.RawMessage) error {
		firstKey = key
		if content(plan) != "original file content" {
			t.Fatalf("unexpected plan: %s", plan)
		}
		paths, e := filepath.Glob(filepath.Join(fixture.root, "operations", "groups", testSession, "*.json"))
		if e != nil || len(paths) != 1 {
			t.Fatalf("group plan must be durable before apply: %v %v", paths, e)
		}
		if _, e = os.ReadFile(paths[0]); e != nil {
			t.Fatal(e)
		}
		return first
	})
	if !errors.Is(err, first) || prepareCount != 1 {
		t.Fatal("first partial result", err, prepareCount)
	}
	err = connection.RunGroupIntent(ctx, input, func() (json.RawMessage, error) {
		t.Fatal("pending plan was prepared again")
		return nil, nil
	}, func(key string, plan json.RawMessage) error {
		if key != firstKey || content(plan) != "original file content" {
			t.Fatalf("retry changed immutable group: %s %s", key, plan)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = connection.RunGroupIntent(ctx, input, func() (json.RawMessage, error) {
		return json.Marshal(struct{ Content string }{"new file content"})
	}, func(key string, plan json.RawMessage) error {
		if key == firstKey || content(plan) != "new file content" {
			t.Fatalf("completed group did not refresh: %s %s", key, plan)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGroupStepReplaysCommittedReceiptAcrossLaterRetry(t *testing.T) {
	fixture, connection := newIntentServer(t, false)
	request := MutationRequest{Method: "POST", Resource: "/issues/td-0123456789abcdef/logs", Body: json.RawMessage(`{"message":"handoff step"}`)}
	var reply struct {
		Result string `json:"result"`
	}
	input := struct{ Group, Step string }{"group-1", "issue-1"}
	if e := connection.RunGroupStepIntent(context.Background(), input, func() (MutationRequest, error) { return request, nil }, &reply, nil); e != nil {
		t.Fatal(e)
	}
	first := reply.Result
	if first == "" || fixture.commits != 1 {
		t.Fatal(first, fixture.commits)
	}
	if e := connection.RunGroupStepIntent(context.Background(), input, func() (MutationRequest, error) {
		t.Fatal("committed group step was prepared again")
		return MutationRequest{}, nil
	}, &reply, nil); e != nil {
		t.Fatal(e)
	}
	if reply.Result != first || fixture.commits != 1 || fixture.requests != 2 {
		t.Fatal(reply.Result, fixture.commits, fixture.requests)
	}
}

func TestGroupRetryDoesNotDuplicateEarlierCommittedStep(t *testing.T) {
	fixture, connection := newIntentServer(t, false)
	ctx := context.Background()
	planReads := 0
	steps := func(groupID string, plan json.RawMessage, stopAfterFirst bool) error {
		for index := 0; index < 2; index++ {
			if index == 1 && stopAfterFirst {
				return errors.New("second issue temporarily unavailable")
			}
			input := struct {
				GroupID string
				Index   int
			}{groupID, index}
			request := MutationRequest{Method: "POST", Resource: "/issues/td-0123456789abcdef/activity", Body: plan}
			var reply struct {
				Result string `json:"result"`
			}
			if e := connection.RunGroupStepIntent(ctx, input, func() (MutationRequest, error) { return request, nil }, &reply, nil); e != nil {
				return e
			}
		}
		return nil
	}
	command := struct{ Name string }{"ws handoff"}
	err := connection.RunGroupIntent(ctx, command, func() (json.RawMessage, error) {
		planReads++
		return json.RawMessage(`{"kind":"handoff","done":["original"]}`), nil
	}, func(groupID string, plan json.RawMessage) error { return steps(groupID, plan, true) })
	if err == nil || fixture.commits != 1 {
		t.Fatal(err, fixture.commits)
	}
	err = connection.RunGroupIntent(ctx, command, func() (json.RawMessage, error) {
		planReads++
		return json.RawMessage(`{"kind":"handoff","done":["changed"]}`), nil
	}, func(groupID string, plan json.RawMessage) error { return steps(groupID, plan, false) })
	if err != nil || planReads != 1 || fixture.commits != 2 || fixture.requests != 3 {
		t.Fatal(err, planReads, fixture.commits, fixture.requests)
	}
}

func TestGroupStepReobservesAfterDefiniteRejection(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = io.WriteString(w, `{"ok":false,"error":{"code":"stale","message":"stale revision"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"data":{"result":"saved"}}`)
	}))
	defer server.Close()
	client, e := NewClient(server.URL, "intent-fixture-token")
	if e != nil {
		t.Fatal(e)
	}
	connection := &Connection{Client: client, StateDirectory: t.TempDir(), ProjectID: testProject, SessionID: testSession}
	prepareCount := 0
	prepare := func() (MutationRequest, error) {
		prepareCount++
		return MutationRequest{Method: "POST", Resource: "/issues/td-0123456789abcdef/activity", Revision: "r-1", Body: json.RawMessage(`{"kind":"handoff"}`)}, nil
	}
	var reply struct {
		Result string `json:"result"`
	}
	if e = connection.RunGroupStepIntent(context.Background(), "group/issue", prepare, &reply, nil); e == nil {
		t.Fatal("stale write unexpectedly succeeded")
	}
	if e = connection.RunGroupStepIntent(context.Background(), "group/issue", prepare, &reply, nil); e != nil {
		t.Fatal(e)
	}
	if prepareCount != 2 || requests != 2 || reply.Result != "saved" {
		t.Fatal(prepareCount, requests, reply.Result)
	}
}
