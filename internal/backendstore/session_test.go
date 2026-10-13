package backendstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
)

func TestOpenSessionRecoversLostRegistrationAndReusesContext(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	t.Setenv("TD_BACKEND_CONTEXT_ROOT", root)
	t.Setenv("TD_BACKEND_TOKEN", "session-fixture-token")
	t.Setenv("TD_SESSION_ID", "backend-session-test")
	t.Setenv("TD_CONTEXT_ID", "context-one")
	var mu sync.Mutex
	receipts := map[string]string{}
	bodies := map[string]string{}
	registrations, creates := 0, 0
	lose := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/sessions") {
			registrations++
			key := r.Header.Get("Idempotency-Key")
			data, _ := io.ReadAll(r.Body)
			var command SessionCommand
			if e := json.Unmarshal(data, &command); e != nil || command.ContextID == "" {
				t.Error("invalid command", e)
			}
			files, _ := filepath.Glob(filepath.Join(root, "*.json"))
			persisted := false
			for _, file := range files {
				intent, e := readSessionIntent(file)
				if e == nil && intent.Key == key && intent.Command == command {
					persisted = true
				}
			}
			if !persisted {
				t.Error("session request preceded durable intent")
			}
			if prior, ok := bodies[key]; ok && prior != string(data) {
				t.Error("registration payload changed")
			}
			if receipts[key] == "" {
				creates++
				receipts[key] = fmt.Sprintf("ses_0199dc38-1200-7000-8000-%012x", creates)
				bodies[key] = string(data)
			}
			if lose {
				lose = false
				io.WriteString(w, `{"ok":true,"data":`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"session": map[string]string{"id": receipts[key]}}})
			return
		}
		if r.URL.Path == "/v1/projects/"+testProject && r.Header.Get("X-TD-Session") != "" {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"project": map[string]string{"id": testProject}}})
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	if e := config.SetBackend(dir, models.BackendStoreConfig{URL: server.URL, ProjectID: testProject}, config.StoreSQLite); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(context.Background(), dir, dir); e == nil {
		t.Fatal("lost response accepted")
	}
	first, e := Open(context.Background(), dir, dir)
	if e != nil {
		t.Fatal(e)
	}
	second, e := Open(context.Background(), dir, dir)
	if e != nil || second.SessionID != first.SessionID {
		t.Fatal(second, e)
	}
	if creates != 1 || registrations != 2 {
		t.Fatal("context not recovered/reused", creates, registrations)
	}
	t.Setenv("TD_CONTEXT_ID", "context-two")
	third, e := Open(context.Background(), dir, dir)
	if e != nil || third.SessionID == first.SessionID {
		t.Fatal("contexts shared identity", third, e)
	}
	t.Setenv("TD_BACKEND_TOKEN", "different-actor-token")
	fourth, e := Open(context.Background(), dir, dir)
	if e != nil || fourth.SessionID == third.SessionID {
		t.Fatal("credential scopes shared identity", fourth, e)
	}
	t.Setenv("TD_CONTEXT_ID", "concurrent-context")
	beforeCreates := creates
	results := make(chan *Connection, 6)
	errors := make(chan error, 6)
	for n := 0; n < 6; n++ {
		go func() {
			connection, err := Open(context.Background(), dir, dir)
			results <- connection
			errors <- err
		}()
	}
	concurrentID := ""
	for n := 0; n < 6; n++ {
		connection, err := <-results, <-errors
		if err != nil {
			t.Fatal(err)
		}
		if concurrentID == "" {
			concurrentID = connection.SessionID
		}
		if connection.SessionID != concurrentID {
			t.Fatal("concurrent sessions duplicated")
		}
	}
	if creates != beforeCreates+1 {
		t.Fatal("multiple committed registrations", creates, beforeCreates)
	}
	other, err := Open(context.Background(), dir, t.TempDir())
	if err != nil || other.SessionID == concurrentID {
		t.Fatal("different checkouts shared session", other, err)
	}
	files, _ := filepath.Glob(filepath.Join(root, "*.json"))
	for _, file := range files {
		data, _ := os.ReadFile(file)
		if strings.Contains(string(data), "session-fixture-token") || strings.Contains(string(data), "different-actor-token") {
			t.Fatal("token persisted")
		}
		info, e := os.Stat(file)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatal("context permissions", info, e)
		}
	}
	if _, e := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(e) {
		t.Fatal("local DB created", e)
	}
}
