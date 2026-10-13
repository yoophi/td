package backendstore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/marcus/td/internal/models"
)

const projectID = "prj_0199dc38-1200-7000-8000-000000000001"
const sessionID = "ses_0199dc38-1200-7000-8000-000000000002"

func TestListIssuesCollectsAndRestartsSnapshot(t *testing.T) {
	for _, changing := range []bool{false, true} {
		t.Run(fmt.Sprint(changing), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-TD-Session") != sessionID || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("missing caller")
				}
				offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
				requests++
				token := "first"
				if changing && requests >= 2 {
					token = "second"
				}
				rows := []Issue{}
				for n := offset; n < min(offset+2, 3); n++ {
					rows = append(rows, Issue{Issue: models.Issue{ID: fmt.Sprintf("td-%016x", n+1), Title: token}, Revision: "r-1"})
				}
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"issues": rows, "limit": 2, "offset": offset, "total": 3, "has_more": offset+len(rows) < 3, "change_token": token}})
			}))
			defer server.Close()
			c, e := NewClient(server.URL, "fixture-token")
			if e != nil {
				t.Fatal(e)
			}
			rows, e := c.ListIssues(context.Background(), projectID, sessionID, true)
			if e != nil || len(rows) != 3 {
				t.Fatal(rows, e)
			}
			expected, n := "first", 2
			if changing {
				expected, n = "second", 4
			}
			if requests != n {
				t.Fatal("wrong restart requests", requests)
			}
			for _, row := range rows {
				if row.Title != expected {
					t.Fatal("mixed snapshot", rows)
				}
			}
		})
	}
}

func TestListIssuesRejectsUnstableOrMalformedSnapshots(t *testing.T) {
	for _, kind := range []string{"unstable", "missing-token", "duplicate", "empty-progress", "bad-revision"} {
		t.Run(kind, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
				token := "same"
				if kind == "unstable" {
					token = strconv.Itoa(requests)
				}
				if kind == "missing-token" {
					token = ""
				}
				id := fmt.Sprintf("td-%016x", offset+1)
				if kind == "duplicate" {
					id = "td-0000000000000001"
				}
				revision := "r-1"
				if kind == "bad-revision" {
					revision = "1"
				}
				rows := []Issue{{Issue: models.Issue{ID: id}, Revision: revision}}
				if kind == "empty-progress" {
					rows = nil
				}
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"issues": rows, "limit": 1, "offset": offset, "total": 2, "has_more": offset == 0, "change_token": token}})
			}))
			defer server.Close()
			c, _ := NewClient(server.URL, "fixture-token")
			rows, e := c.ListIssues(context.Background(), projectID, sessionID, false)
			if e == nil || rows != nil {
				t.Fatal("invalid snapshot accepted", rows, e)
			}
			if requests > 6 {
				t.Fatal("unbounded retries", requests)
			}
		})
	}
}

func TestGetIssueMapsNullableFieldsAndRejectsIdentityMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"issue": map[string]any{
			"id": "td-0000000000000001", "revision": "r-2", "parent_id": nil, "creator_session": nil,
			"title": "Remote issue", "created_at": "2026-10-13T00:00:00Z", "closed_at": nil,
		}}})
	}))
	defer server.Close()
	c, _ := NewClient(server.URL, "fixture-token")
	issue, e := c.GetIssue(context.Background(), projectID, sessionID, "td-0000000000000001")
	if e != nil || issue.ParentID != "" || issue.CreatorSession != "" || issue.ClosedAt != nil || issue.CreatedAt != time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) || issue.Revision != "r-2" {
		t.Fatal(issue, e)
	}
	if _, e = c.GetIssue(context.Background(), projectID, sessionID, "td-0000000000000002"); e == nil {
		t.Fatal("mismatched issue accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.ListIssues(ctx, projectID, sessionID, true); e != context.Canceled {
		t.Fatal(e)
	}
}
