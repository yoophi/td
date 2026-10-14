package backendstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type intentServer struct {
	mu                sync.Mutex
	root              string
	requests, commits int
	bodies            map[string]string
	replies           map[string]string
	keys              []string
	lose              bool
}

func newIntentServer(t *testing.T, lose bool) (*intentServer, *Connection) {
	t.Helper()
	f := &intentServer{root: t.TempDir(), bodies: map[string]string{}, replies: map[string]string{}, lose: lose}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		key := r.Header.Get("Idempotency-Key")
		f.keys = append(f.keys, key)
		payload, _ := io.ReadAll(r.Body)
		fingerprint := r.Header.Get("If-Match") + "|" + string(payload)
		paths, _ := filepath.Glob(filepath.Join(f.root, "operations", testSession, "*.json"))
		persisted := false
		for _, path := range paths {
			intent, e := readMutationIntent(path)
			if e == nil && intent.Key == key {
				persisted = true
			}
		}
		if !persisted {
			t.Error("request sent before durable intent")
		}
		if prior, ok := f.bodies[key]; ok && prior != fingerprint {
			t.Error("replay changed immutable request")
		}
		if f.replies[key] == "" {
			f.commits++
			f.bodies[key] = fingerprint
			f.replies[key] = fmt.Sprintf(`{"ok":true,"data":{"result":"saved-%d"}}`, f.commits)
		}
		if f.lose {
			f.lose = false
			io.WriteString(w, `{"ok":true,"data":`)
			return
		}
		io.WriteString(w, f.replies[key])
	}))
	t.Cleanup(server.Close)
	client, e := NewClient(server.URL, "intent-fixture-token")
	if e != nil {
		t.Fatal(e)
	}
	return f, &Connection{Client: client, StateDirectory: f.root, ProjectID: testProject, SessionID: testSession}
}
func TestRunIntentLostReplyPreservesPayloadAndLaterInvocationIsFresh(t *testing.T) {
	f, c := newIntentServer(t, true)
	prepared := 0
	prepare := func() (MutationRequest, error) {
		prepared++
		return MutationRequest{Method: "PATCH", Resource: "/issues/td-0000000000000001", Revision: fmt.Sprintf("r-%d", prepared), Body: json.RawMessage(fmt.Sprintf(`{"title":"prepared-%d"}`, prepared))}, nil
	}
	var out struct {
		Result string `json:"result"`
	}
	verify := func() error {
		if out.Result == "" {
			return errors.New("missing result")
		}
		return nil
	}
	e := c.RunIntent(context.Background(), "same append invocation", prepare, &out, verify)
	var uncertain *UncertainWriteError
	if !errors.As(e, &uncertain) {
		t.Fatal("lost reply reported definite", e)
	}
	if e = c.RunIntent(context.Background(), "same append invocation", prepare, &out, verify); e != nil || out.Result != "saved-1" || prepared != 1 || f.commits != 1 || f.keys[0] != f.keys[1] {
		t.Fatal(out, e, prepared, f.commits, f.keys)
	}
	if e = c.RunIntent(context.Background(), "same append invocation", prepare, &out, verify); e != nil || out.Result != "saved-2" || prepared != 2 || f.commits != 2 || f.keys[2] == f.keys[1] {
		t.Fatal("later explicit invocation replayed old command", out, e, f.keys)
	}
	files, _ := filepath.Glob(filepath.Join(f.root, "operations", testSession, "*.json"))
	for _, file := range files {
		info, _ := os.Stat(file)
		data, _ := os.ReadFile(file)
		if info.Mode().Perm() != 0600 || string(data) == "" {
			t.Fatal(info)
		}
		var intent mutationIntent
		if json.Unmarshal(data, &intent) != nil || intent.FinishedAt == nil {
			t.Fatal("completion not durable")
		}
	}
}
func TestRunIntentVerificationAndLocalSaveFailuresRemainPending(t *testing.T) {
	for _, failure := range []string{"verification", "save"} {
		t.Run(failure, func(t *testing.T) {
			f, c := newIntentServer(t, false)
			prepares := 0
			prepare := func() (MutationRequest, error) {
				prepares++
				return MutationRequest{Method: "POST", Resource: "/issues", Body: json.RawMessage(`{"title":"verified creation"}`)}, nil
			}
			var out struct {
				Result string `json:"result"`
			}
			injected := false
			backup, path := "", ""
			verify := func() error {
				if injected {
					return nil
				}
				injected = true
				if failure == "verification" {
					return errors.New("required receipt field missing")
				}
				files, _ := filepath.Glob(filepath.Join(f.root, "operations", testSession, "*.json"))
				path = files[0]
				backup = path + ".backup"
				if e := os.Rename(path, backup); e != nil {
					t.Fatal(e)
				}
				if e := os.Mkdir(path, 0700); e != nil {
					t.Fatal(e)
				}
				return nil
			}
			e := c.RunIntent(context.Background(), "create invocation", prepare, &out, verify)
			var uncertain *UncertainWriteError
			if !errors.As(e, &uncertain) {
				t.Fatal("committed but incomplete result reported definite", e)
			}
			if failure == "save" {
				if e := os.Remove(path); e != nil {
					t.Fatal(e)
				}
				if e := os.Rename(backup, path); e != nil {
					t.Fatal(e)
				}
			}
			if e = c.RunIntent(context.Background(), "create invocation", prepare, &out, verify); e != nil || prepares != 1 || f.commits != 1 || f.keys[0] != f.keys[1] {
				t.Fatal(e, prepares, f.keys)
			}
		})
	}
}
func TestRunIntentDefiniteStaleDoesNotRetryAndNextInvocationReobserves(t *testing.T) {
	requests := 0
	keys := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(412)
		io.WriteString(w, `{"ok":false,"error":{"code":"conflict"}}`)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "fixture-token")
	c := &Connection{Client: client, StateDirectory: t.TempDir(), ProjectID: testProject, SessionID: testSession}
	prepares := 0
	prepare := func() (MutationRequest, error) {
		prepares++
		return MutationRequest{Method: "PATCH", Resource: "/issues/td-0000000000000001", Revision: fmt.Sprintf("r-%d", prepares), Body: json.RawMessage(`{"title":"updated title"}`)}, nil
	}
	var out any
	e := c.RunIntent(context.Background(), "update invocation", prepare, &out, nil)
	var api *APIError
	if !errors.As(e, &api) || api.StatusCode != 412 || requests != 1 {
		t.Fatal(e, requests)
	}
	e = c.RunIntent(context.Background(), "update invocation", prepare, &out, nil)
	if !errors.As(e, &api) || prepares != 2 || requests != 2 || keys[0] == keys[1] {
		t.Fatal("stale retried/replayed without reobservation", e, prepares, keys)
	}
}
func TestRunIntentConcurrentPrepareRecoversCompletedReceipt(t *testing.T) {
	f, c := newIntentServer(t, false)
	waiting, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		var out any
		result <- c.RunIntent(context.Background(), "concurrent invocation", func() (MutationRequest, error) {
			close(waiting)
			<-release
			return MutationRequest{Method: "POST", Resource: "/issues", Body: json.RawMessage(`{"title":"new observation discarded"}`)}, nil
		}, &out, nil)
	}()
	<-waiting
	var out any
	e := c.RunIntent(context.Background(), "concurrent invocation", func() (MutationRequest, error) {
		return MutationRequest{Method: "POST", Resource: "/issues", Body: json.RawMessage(`{"title":"original observation"}`)}, nil
	}, &out, nil)
	if e != nil {
		t.Fatal(e)
	}
	close(release)
	if e = <-result; e != nil || f.commits != 1 || len(f.keys) != 2 || f.keys[0] != f.keys[1] {
		t.Fatal(e, f.commits, f.keys)
	}
}

func TestRunIntentMissingFieldCannotReusePreviousSuccessfulOutput(t *testing.T) {
	requests := 0
	keys := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if requests == 2 {
			io.WriteString(w, `{"ok":true,"data":{}}`)
		} else {
			io.WriteString(w, `{"ok":true,"data":{"result":"verified"}}`)
		}
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "fixture-token")
	c := &Connection{Client: client, StateDirectory: t.TempDir(), ProjectID: testProject, SessionID: testSession}
	prepares := 0
	prepare := func() (MutationRequest, error) {
		prepares++
		return MutationRequest{Method: "POST", Resource: "/issues", Body: json.RawMessage(`{"title":"remote issue creation"}`)}, nil
	}
	var out struct {
		Result string `json:"result"`
	}
	verify := func() error {
		if out.Result == "" {
			return errors.New("missing result")
		}
		return nil
	}
	if e := c.RunIntent(context.Background(), "same create", prepare, &out, verify); e != nil {
		t.Fatal(e)
	}
	e := c.RunIntent(context.Background(), "same create", prepare, &out, verify)
	var uncertain *UncertainWriteError
	if !errors.As(e, &uncertain) || out.Result != "" {
		t.Fatal("previous output masked incomplete response", out, e)
	}
	if e = c.RunIntent(context.Background(), "same create", prepare, &out, verify); e != nil || prepares != 2 || keys[1] != keys[2] {
		t.Fatal(e, prepares, keys)
	}
}
