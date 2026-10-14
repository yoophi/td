package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/pkg/notes"
)

func TestBackendMonitorNotesKeepRevisionAndRejectStaleWrites(t *testing.T) {
	const project = "prj_0199f3bb-9ff2-7777-8888-123456789abc"
	const session = "ses_0199f3bb-9ff2-7777-8888-123456789abc"
	now := time.Now().UTC().Truncate(time.Second)
	row := backendstore.Note{ID: "nt-1", Title: "Work", Content: "First", Revision: "r-1", CreatedAt: now, UpdatedAt: now}
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-TD-Session") != session {
			t.Errorf("wrong caller: %q", r.Header.Get("X-TD-Session"))
		}
		var data any
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/projects/"+project+"/notes":
			data = map[string]any{"notes": []backendstore.Note{row}}
		case r.Method == "GET" && r.URL.Path == "/v1/projects/"+project+"/notes/nt-1":
			data = map[string]any{"note": row}
		case r.Method == "PATCH" && r.URL.Path == "/v1/projects/"+project+"/notes/nt-1":
			if r.Header.Get("If-Match") != `"r-1"` || r.Header.Get("Idempotency-Key") == "" {
				t.Error("missing observed revision or durable key")
			}
			writes.Add(1)
			row.Title, row.Content, row.Revision, row.UpdatedAt = "Updated", "Second", "r-2", now.Add(time.Second)
			data = map[string]any{"note": row}
		default:
			t.Errorf("unexpected backend request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	client, err := backendstore.NewClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	conn := &backendstore.Connection{Client: client, ProjectID: project, SessionID: session, StateDirectory: t.TempDir()}
	store := &backendNoteStore{ctx: context.Background(), conn: conn, guard: func(context.Context) error { return nil }}
	listed, err := store.List(notes.ListOptions{Search: "wor", IncludeDeleted: true})
	if err != nil || len(listed) != 1 || listed[0].BackendRevision != "r-1" {
		t.Fatalf("note list lost revision: %+v, %v", listed, err)
	}
	observed, err := store.GetAny("nt-1")
	if err != nil || observed.BackendRevision != "r-1" {
		t.Fatalf("note detail lost revision: %+v, %v", observed, err)
	}
	updated, err := store.UpdateObserved(observed, "Updated", "Second")
	if err != nil || updated.BackendRevision != "r-2" || updated.Content != "Second" || writes.Load() != 1 {
		t.Fatalf("observed note update failed: %+v, %v, writes=%d", updated, err, writes.Load())
	}
	if _, err := store.SetPinnedObserved(observed, true); err == nil || !strings.Contains(err.Error(), "changed since") || writes.Load() != 1 {
		t.Fatalf("stale note mutation accepted: %v, writes=%d", err, writes.Load())
	}
}

func TestBackendMonitorNoteLifecycleUsesObservedMutations(t *testing.T) {
	const project = "prj_0199f3bb-9ff2-7777-8888-123456789abc"
	const session = "ses_0199f3bb-9ff2-7777-8888-123456789abc"
	now := time.Now().UTC().Truncate(time.Second)
	row := backendstore.Note{ID: "nt-1", Title: "Work", Revision: "r-1", CreatedAt: now, UpdatedAt: now}
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := "/v1/projects/" + project + "/notes"
		if r.Header.Get("X-TD-Session") != session {
			t.Error("wrong backend session")
		}
		var data any
		switch {
		case r.Method == "GET" && r.URL.Path == path+"/nt-1":
			data = map[string]any{"note": row}
		case r.Method == "POST" && r.URL.Path == path:
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("missing note create key")
			}
			writes.Add(1)
			data = map[string]any{"note": backendstore.Note{ID: "nt-2", Title: "Fresh", Content: "Text", Revision: "r-1", CreatedAt: now, UpdatedAt: now}}
		case r.Method == "PATCH" && r.URL.Path == path+"/nt-1":
			if r.Header.Get("If-Match") != `"`+row.Revision+`"` || r.Header.Get("Idempotency-Key") == "" {
				t.Error("missing note patch observation")
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if value, ok := body["pinned"].(bool); ok {
				row.Pinned = value
			}
			if value, ok := body["archived"].(bool); ok {
				row.Archived = value
			}
			writes.Add(1)
			row.Revision = "r-" + string(rune('1'+writes.Load()))
			data = map[string]any{"note": row}
		case r.Method == "DELETE" && r.URL.Path == path+"/nt-1":
			if r.Header.Get("If-Match") != `"`+row.Revision+`"` {
				t.Error("missing note delete observation")
			}
			writes.Add(1)
			deleted := now.Add(time.Second)
			row.DeletedAt = &deleted
			row.Revision = "r-4"
			data = map[string]any{"deleted": true}
		case r.Method == "POST" && r.URL.Path == path+"/nt-1/restore":
			if r.Header.Get("If-Match") != `"r-4"` {
				t.Error("missing note restore observation")
			}
			writes.Add(1)
			row.DeletedAt = nil
			row.Revision = "r-5"
			data = map[string]any{"note": row}
		default:
			t.Errorf("unexpected backend request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	client, err := backendstore.NewClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	store := &backendNoteStore{ctx: context.Background(), conn: &backendstore.Connection{Client: client, ProjectID: project, SessionID: session, StateDirectory: t.TempDir()}, guard: func(context.Context) error { return nil }}
	observed, err := store.GetAny("nt-1")
	if err != nil {
		t.Fatal(err)
	}
	observed, err = store.SetPinnedObserved(observed, true)
	if err != nil || !observed.Pinned || observed.BackendRevision != "r-2" {
		t.Fatalf("pin failed: %+v, %v", observed, err)
	}
	observed, err = store.SetArchivedObserved(observed, true)
	if err != nil || !observed.Archived || observed.BackendRevision != "r-3" {
		t.Fatalf("archive failed: %+v, %v", observed, err)
	}
	if _, err = store.DeleteObserved(observed); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.GetAny("nt-1")
	if err != nil || deleted.DeletedAt == nil || deleted.BackendRevision != "r-4" {
		t.Fatalf("deleted note unavailable: %+v, %v", deleted, err)
	}
	restored, err := store.RestoreObserved(deleted)
	if err != nil || restored.DeletedAt != nil || restored.BackendRevision != "r-5" {
		t.Fatalf("restore failed: %+v, %v", restored, err)
	}
	created, err := store.Create("Fresh", "Text")
	if err != nil || created.ID != "nt-2" || writes.Load() != 5 {
		t.Fatalf("create failed: %+v, %v, writes=%d", created, err, writes.Load())
	}
}
