package backendstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

func TestIssueActivityIncludingDeletedUsesArchiveRead(t *testing.T) {
	const project = "prj_0199f3bb-9ff2-7777-8888-123456789abc"
	const session = "ses_0199f3bb-9ff2-7777-8888-123456789abc"
	const issue = "td-0123456789abcdef"
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/projects/"+project+"/issues/"+issue && r.URL.Query().Get("with") == "reviews" && r.URL.Query().Get("with_deleted") == "true" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": Detail{Issue: Issue{Issue: models.Issue{ID: issue, Title: "Archived"}, Revision: "r-2"}}})
			return
		}
		if r.Method != "GET" || r.URL.Path != "/v1/projects/"+project+"/issues/"+issue+"/activity" || r.URL.Query().Get("with_deleted") != "true" || r.Header.Get("X-TD-Session") != session {
			t.Errorf("wrong activity archive request: %s %s", r.Method, r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"activity": []models.Activity{{ID: "act-1", IssueID: issue, Kind: "handoff", SessionID: session, Done: []string{"finished"}, CreatedAt: now, UpdatedAt: now}}}})
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	conn := &Connection{Client: client, ProjectID: project, SessionID: session}
	events, err := conn.IssueActivityIncludingDeleted(context.Background(), issue)
	if err != nil || len(events) != 1 || events[0].Done[0] != "finished" {
		t.Fatalf("archived handoff activity unavailable: %+v, %v", events, err)
	}
	detail, err := conn.DetailIncludingDeleted(context.Background(), issue)
	if err != nil || detail.Issue.Title != "Archived" {
		t.Fatalf("archived issue detail unavailable: %+v, %v", detail, err)
	}
}
