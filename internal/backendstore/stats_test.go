package backendstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatsRejectsIncompleteSuccessfulResponse(t *testing.T) {
	for _, data := range []map[string]any{{}, {"total": 0}, {"total": -1, "by_status": map[string]int{}, "by_type": map[string]int{}, "by_priority": map[string]int{}}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
		}))
		client, _ := NewClient(server.URL, "fixture-token")
		c := &Connection{Client: client, ProjectID: testProject, SessionID: testSession}
		if _, e := c.Stats(context.Background()); e == nil {
			t.Fatal("incomplete response shown as zero stats", data)
		}
		server.Close()
	}
}
