package backendstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMonitorSnapshotRequestAndValidation(t *testing.T) {
	const project = "prj_0199f3bb-9ff2-7777-8888-123456789abc"
	const session = "ses_0199f3bb-9ff2-7777-8888-123456789abc"
	var observed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed = true
		if r.URL.Path != "/v1/projects/"+project+"/monitor" || r.Header.Get("X-TD-Session") != session || r.URL.Query().Get("search") != "title ~ test" || r.URL.Query().Get("sort") != "-updated" || r.URL.Query().Get("include_closed") != "true" {
			t.Errorf("unexpected monitor request: %s %s", r.URL, r.Header.Get("X-TD-Session"))
		}
		row := map[string]any{"id": "td-0123456789abcdef", "revision": "r-1", "review_options": map[string]bool{"self_review_prompt": true, "record_review_available": true}}
		groups := map[string]any{"review_verification_required": false}
		for _, name := range []string{"reviewable", "needs_rework", "in_progress", "ready", "pending_review", "ready_to_close", "pending_other", "blocked", "closed"} {
			groups[name] = []any{}
		}
		groups["ready"] = []any{row}
		data := map[string]any{"session_id": session, "change_token": "token-1", "monitor": map[string]any{"observation_mode": "snapshot", "timestamp": "2026-10-14T00:00:00Z", "in_progress": []any{}, "activity": []any{}, "task_list": groups, "recent_handoffs": []any{}, "active_sessions": []any{session}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	conn := &Connection{Client: client, ProjectID: project, SessionID: session}
	got, err := conn.Monitor(context.Background(), "title ~ test", true, "-updated")
	if err != nil || !observed || got.ChangeToken != "token-1" || len(got.Dashboard.TaskList.Ready) != 1 || got.Dashboard.TaskList.Ready[0].ReviewOptions == nil || !got.Dashboard.TaskList.Ready[0].ReviewOptions.SelfReviewPrompt || !got.Dashboard.TaskList.Ready[0].ReviewOptions.RecordReviewAvailable {
		t.Fatalf("monitor snapshot = %+v, %v", got, err)
	}
}
