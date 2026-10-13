package backendstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
)

const testProject = "prj_0199dc38-1200-7000-8000-000000000001"
const testSession = "ses_0199dc38-1200-7000-8000-000000000002"
const testCredential = "backend-test-credential-only-0123456789"

type fakeInitBackend struct {
	mu                      sync.Mutex
	server                  *httptest.Server
	dir                     string
	projectBodies           map[string]string
	sessionBodies           map[string]string
	created, sessionCreated int
	loseResponse            bool
	beforeProject           func()
	beforeProjectGet        func()
}

func newInitBackend(t *testing.T, dir string) *fakeInitBackend {
	t.Helper()
	f := &fakeInitBackend{dir: dir, projectBodies: map[string]string{}, sessionBodies: map[string]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testCredential {
			w.WriteHeader(401)
			return
		}
		write := func(data any) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
		}
		switch r.URL.Path {
		case "/healthz":
			write(map[string]string{"status": "ok", "api_version": "v1"})
		case "/v1/capabilities":
			write(map[string]any{"api_version": "v1", "capabilities": []string{"projects", "sessions", "idempotent_init"}})
		case "/v1/projects":
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			intent, e := loadIntent(f.dir, f.server.URL)
			if e != nil || intent.Key != r.Header.Get("Idempotency-Key") {
				t.Error("request preceded durable init key", e)
				w.WriteHeader(500)
				return
			}
			if f.beforeProject != nil {
				f.beforeProject()
			}
			body, _ := io.ReadAll(r.Body)
			key := r.Header.Get("Idempotency-Key")
			if prior, ok := f.projectBodies[key]; ok {
				if prior != string(body) {
					w.WriteHeader(409)
					return
				}
			} else {
				f.projectBodies[key] = string(body)
				f.created++
			}
			if f.loseResponse {
				f.loseResponse = false
				fmt.Fprint(w, "truncated committed response")
				return
			}
			write(map[string]any{"project": Project{ID: testProject, Name: intent.Name}})
		case "/v1/projects/" + testProject + "/sessions":
			if r.Method != "POST" {
				w.WriteHeader(405)
				return
			}
			intent, e := loadIntent(f.dir, f.server.URL)
			if e != nil || intent.SessionKeys[testProject] != r.Header.Get("Idempotency-Key") {
				t.Error("request preceded durable session key", e)
				w.WriteHeader(500)
				return
			}
			body, _ := io.ReadAll(r.Body)
			var input struct {
				ContextID string `json:"context_id"`
			}
			json.Unmarshal(body, &input)
			if input.ContextID == "" {
				w.WriteHeader(400)
				return
			}
			key := r.Header.Get("Idempotency-Key")
			if prior, ok := f.sessionBodies[key]; ok {
				if prior != string(body) {
					w.WriteHeader(409)
					return
				}
			} else {
				f.sessionBodies[key] = string(body)
				f.sessionCreated++
			}
			write(map[string]any{"session": map[string]string{"id": testSession}})
		case "/v1/projects/" + testProject:
			if r.Header.Get("X-TD-Session") != testSession {
				w.WriteHeader(403)
				return
			}
			if f.beforeProjectGet != nil {
				f.beforeProjectGet()
			}
			write(map[string]any{"project": Project{ID: testProject}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func initClient(t *testing.T, f *fakeInitBackend) *Client {
	t.Helper()
	c, e := NewClient(f.server.URL, testCredential)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestInitializeCreateReuseLossAndNoSecretConfig(t *testing.T) {
	dir := t.TempDir()
	f := newInitBackend(t, dir)
	f.loseResponse = true
	c := initClient(t, f)
	if _, e := c.Initialize(context.Background(), dir, ""); e == nil {
		t.Fatal("lost committed response accepted")
	}
	intent, e := loadIntent(f.dir, f.server.URL)
	if e != nil || intent.Key == "" {
		t.Fatal(intent, e)
	}
	// Directory rename / display-name changes cannot alter a persisted request.
	renamed := filepath.Join(filepath.Dir(dir), filepath.Base(dir)+"-renamed")
	if e = os.Rename(dir, renamed); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(renamed) })
	dir = renamed
	f.mu.Lock()
	f.dir = dir
	f.mu.Unlock()

	association, e := c.Initialize(context.Background(), dir, "")
	if e != nil {
		t.Fatal(e)
	}
	if association.ProjectID != testProject {
		t.Fatal(association)
	}
	if _, e = c.Initialize(context.Background(), dir, ""); e != nil {
		t.Fatal(e)
	}
	if f.created != 1 || f.sessionCreated != 1 {
		t.Fatal(f.created, f.sessionCreated)
	}
	data, e := os.ReadFile(filepath.Join(dir, ".todos", "config.json"))
	if e != nil {
		t.Fatal(e)
	}
	if containsBytes(data, testCredential) {
		t.Fatal("credential persisted")
	}
	if _, e = os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(e) {
		t.Fatal("SQLite created", e)
	}
}
func containsBytes(data []byte, value string) bool { return bytes.Contains(data, []byte(value)) }
func TestInitializeAttachConcurrentAndSaveFailureRecovery(t *testing.T) {
	t.Run("attach", func(t *testing.T) {
		dir := t.TempDir()
		f := newInitBackend(t, dir)
		c := initClient(t, f)
		if _, e := c.Initialize(context.Background(), dir, testProject); e != nil {
			t.Fatal(e)
		}
		if f.created != 0 || f.sessionCreated != 1 {
			t.Fatal(f.created, f.sessionCreated)
		}
	})
	t.Run("concurrent", func(t *testing.T) {
		dir := t.TempDir()
		f := newInitBackend(t, dir)
		c := initClient(t, f)
		var wg sync.WaitGroup
		errors := make(chan error, 6)
		for n := 0; n < 6; n++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, e := c.Initialize(context.Background(), dir, ""); errors <- e }()
		}
		wg.Wait()
		close(errors)
		for e := range errors {
			if e != nil {
				t.Fatal(e)
			}
		}
		if f.created != 1 || f.sessionCreated != 1 {
			t.Fatal(f.created, f.sessionCreated)
		}
	})
	t.Run("save failure", func(t *testing.T) {
		dir := t.TempDir()
		f := newInitBackend(t, dir)
		f.beforeProjectGet = func() { os.Mkdir(filepath.Join(dir, ".todos", "config.json"), 0700) }
		c := initClient(t, f)
		if _, e := c.Initialize(context.Background(), dir, ""); e == nil {
			t.Fatal("config save failure ignored")
		}
		if e := os.Remove(filepath.Join(dir, ".todos", "config.json")); e != nil {
			t.Fatal(e)
		}
		f.beforeProjectGet = nil
		if _, e := c.Initialize(context.Background(), dir, ""); e != nil {
			t.Fatal(e)
		}
		if f.created != 1 || f.sessionCreated != 1 {
			t.Fatal(f.created, f.sessionCreated)
		}
	})
}
func TestInitializeRefusesAssociationAndConcurrentStoreChange(t *testing.T) {
	dir := t.TempDir()
	f := newInitBackend(t, dir)
	c := initClient(t, f)
	if e := config.Save(dir, &models.Config{PaneHeights: [3]float64{.2, .3, .5}, FeatureFlags: map[string]bool{"custom": true}}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Initialize(context.Background(), dir, ""); e != nil {
		t.Fatal(e)
	}
	cfg, e := config.Load(dir)
	if e != nil || !cfg.FeatureFlags["custom"] || cfg.PaneHeights[1] != .3 {
		t.Fatal(cfg, e)
	}
	if _, e = c.Initialize(context.Background(), dir, "prj_0199dc38-1200-7000-8000-000000000003"); e == nil {
		t.Fatal("overwrote association")
	}
	other, e := NewClient(f.server.URL+"/other", testCredential)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = other.Initialize(context.Background(), dir, ""); e == nil {
		t.Fatal("changed URL accepted")
	}
	fresh := t.TempDir()
	otherBackend := newInitBackend(t, fresh)
	otherBackend.beforeProject = func() {
		config.SetStore(fresh, config.StoreGitHub, &models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"})
	}
	if _, e = initClient(t, otherBackend).Initialize(context.Background(), fresh, ""); e == nil {
		t.Fatal("concurrent store selection overwritten")
	}
	cfg, e = config.Load(fresh)
	if e != nil || cfg.Store != config.StoreGitHub || cfg.Backend != nil {
		t.Fatal(cfg, e)
	}
}
