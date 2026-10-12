package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/td/internal/models"
	"strings"
	"syscall"
	"testing"
)

type mutationRecorderFixture struct {
	requests []MutationRequest
	results  [][]byte
	before   error
	after    error
}

func TestMutationIntentExtractsNativeOperationWithoutRetainingBodies(t *testing.T) {
	body, err := encodeBody("private content", metadata{EntityKind: "note", OperationID: "fixed-note-op", Type: models.TypeTask, Priority: models.PriorityP2, Note: &NoteDetails{Version: 1}})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"body": body})
	request := mutationRequest("POST", "repos/owner/repo/issues", payload)
	if request.OperationID != "fixed-note-op" || request.EntityKind != "note" {
		t.Fatal("native creation operation not captured", request)
	}
	root := `td action\n` + actionImportCommandStart + `{"operation_id":"fixed-command-op"}` + markerEnd
	payload, _ = json.Marshal(map[string]string{"body": root})
	request = mutationRequest("POST", "repos/owner/repo/issues/9/comments", payload)
	if request.OperationID != "fixed-command-op" || request.EntityKind != "" {
		t.Fatal("native root operation not captured", request)
	}
	root = "td import action\n" + actionImportArchiveStart + `{"journal":{"operation_id":"fixed-archive-op"},"documents":[]}` + markerEnd
	payload, _ = json.Marshal(map[string]string{"body": root})
	request = mutationRequest("POST", "repos/owner/repo/issues/9/comments", payload)
	if request.OperationID != "fixed-archive-op" {
		t.Fatal("nested import root operation not captured", request)
	}
}

func (r *mutationRecorderFixture) BeginMutation(request MutationRequest) (int, error) {
	r.requests = append(r.requests, request)
	return len(r.requests), r.before
}

func (r *mutationRecorderFixture) ReceiveMutation(sequence int, data []byte) error {
	if sequence != len(r.requests) {
		return errors.New("receipt lost its original sequence")
	}
	r.results = append(r.results, append([]byte{}, data...))
	return r.after
}

func TestTransferMutationRecorderFencesRealGHInvocationAndPreservesFailures(t *testing.T) {
	for _, stage := range []string{"success", "before_enospc", "after_enospc", "uncertain_transport", "read", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			recorder := &mutationRecorderFixture{}
			if stage == "before_enospc" {
				recorder.before = syscall.ENOSPC
			}
			if stage == "after_enospc" {
				recorder.after = syscall.ENOSPC
			}
			calls := 0
			lost := errors.New("connection lost after accepted write")
			client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
				calls++
				if args[4] != "GET" && len(recorder.requests) != 1 {
					t.Fatal("gh started before durable intent")
				}
				if stage == "uncertain_transport" {
					return nil, lost
				}
				return []byte(`{"number":12}`), nil
			}}
			ctx := WithMutationRecorder(context.Background(), recorder)
			if stage == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			method := "POST"
			if stage == "read" {
				method = "GET"
			}
			data, err := client.request(ctx, method, "/issues", map[string]string{"body": "private source content"}, false)
			switch stage {
			case "success":
				if err != nil || len(data) == 0 || calls != 1 || len(recorder.results) != 1 || recorder.requests[0].Path != "repos/owner/repo/issues" || strings.Contains(recorder.requests[0].PayloadSHA256, "private") {
					t.Fatal(data, err, recorder, calls)
				}
			case "before_enospc":
				if !errors.Is(err, syscall.ENOSPC) || calls != 0 || len(recorder.results) != 0 || !strings.Contains(err.Error(), "no API write attempted") {
					t.Fatal("intent failure reached gh or hid ENOSPC", err, calls)
				}
			case "after_enospc":
				if !errors.Is(err, syscall.ENOSPC) || calls != 1 || data != nil || !strings.Contains(err.Error(), "remote write may remain") {
					t.Fatal("receipt failure retried or became success", err, calls)
				}
			case "uncertain_transport":
				if !errors.Is(err, lost) || calls != 1 || len(recorder.results) != 0 {
					t.Fatal("uncertain write retried or became received evidence", err, recorder)
				}
			case "read":
				if err != nil || calls != 1 || len(recorder.requests) != 0 || len(recorder.results) != 0 {
					t.Fatal("GET triggered mutation receipt", err, recorder)
				}
			case "cancelled":
				if !errors.Is(err, context.Canceled) || calls != 0 || len(recorder.requests) != 0 {
					t.Fatal("cancelled mutation reached gh", err, calls)
				}
			}
		})
	}
}
