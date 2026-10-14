package backendstore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadSnapshotRestartsWholeComputation(t *testing.T) {
	for _, transient := range []bool{false, true} {
		t.Run(map[bool]string{false: "changed", true: "transient-read-error"}[transient], func(t *testing.T) {
			tokenReads, lists, callbacks := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var data any
				if r.URL.Path == "/v1/projects/"+testProject+"/changes" {
					tokenReads++
					token := "new"
					if tokenReads == 1 {
						token = "old"
					}
					data = map[string]string{"change_token": token}
				} else {
					lists++
					title := "new"
					if lists == 1 {
						title = "old"
					}
					data = map[string]any{"issues": []any{map[string]string{"id": "td-0000000000000001", "revision": "r-1", "title": title}}, "total": 1, "limit": 1000, "offset": 0, "has_more": false, "change_token": title}
				}
				json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
			}))
			defer server.Close()
			c, _ := NewClient(server.URL, "fixture-token")
			connection := &Connection{Client: c, ProjectID: testProject, SessionID: testSession}
			result := ""
			e := connection.ReadSnapshot(context.Background(), func(rows []Issue) error {
				callbacks++
				result = rows[0].Title
				if transient && callbacks == 1 {
					return &APIError{StatusCode: 404, Code: "not_found"}
				}
				return nil
			})
			if e != nil || callbacks != 2 || lists != 2 || result != "new" {
				t.Fatal(e, callbacks, lists, result)
			}
		})
	}
}
func TestReadSnapshotPreservesStableFailureAndBoundsRetries(t *testing.T) {
	for _, unstable := range []bool{false, true} {
		reads := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var data any
			if r.URL.Path == "/v1/projects/"+testProject+"/changes" {
				reads++
				token := "same"
				if unstable {
					token = string(rune('a' + reads))
				}
				data = map[string]string{"change_token": token}
			} else {
				data = map[string]any{"issues": []any{}, "total": 0, "limit": 1000, "offset": 0, "has_more": false, "change_token": "same"}
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": data})
		}))
		c, _ := NewClient(server.URL, "fixture-token")
		connection := &Connection{Client: c, ProjectID: testProject, SessionID: testSession}
		failure := errors.New("detail failed")
		e := connection.ReadSnapshot(context.Background(), func([]Issue) error { return failure })
		server.Close()
		if !unstable && !errors.Is(e, failure) {
			t.Fatal(e)
		}
		if unstable && (e == nil || reads != 6) {
			t.Fatal(e, reads)
		}
	}
}
