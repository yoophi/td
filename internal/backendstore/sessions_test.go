package backendstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestBackendListSessionsRequiresCurrentAndValidRows(t *testing.T) {
	const project = "prj_0199dc38-1200-7000-8000-000000000001"
	const current = "ses_0199dc38-1200-7000-8000-000000000002"
	var response atomic.Value
	response.Store(`{"ok":true,"data":{"current_session_id":"` + current + `","sessions":[{"id":"` + current + `","last_activity":"2026-10-14T00:00:00Z"}]}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/"+project+"/sessions" || r.Header.Get("X-TD-Session") != current {
			t.Errorf("session request path=%s header=%s", r.URL.Path, r.Header.Get("X-TD-Session"))
		}
		_, _ = w.Write([]byte(response.Load().(string)))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	conn := &Connection{Client: client, ProjectID: project, SessionID: current}
	rows, err := conn.ListSessions(context.Background())
	if err != nil || len(rows) != 1 || rows[0].ID != current {
		t.Fatalf("sessions=%+v err=%v", rows, err)
	}
	response.Store(`{"ok":true,"data":{"current_session_id":"` + current + `","sessions":[]}}`)
	if _, err := conn.ListSessions(context.Background()); err == nil {
		t.Fatal("omitted current session accepted")
	}
}
