package backendstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const mutationKey = "0199dc38-1200-4000-8000-000000000003"

func TestMutationObservedRevisionAndErrorMapping(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 409, 412, 428, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("If-Match") != `"r-7"` || r.Header.Get("Idempotency-Key") != mutationKey || r.Header.Get("X-TD-Session") != sessionID {
					t.Error("mutation preconditions missing")
				}
				w.WriteHeader(status)
				if status == 200 {
					io.WriteString(w, `{"ok":true,"data":{"result":"saved"}}`)
				} else {
					io.WriteString(w, `{"ok":false,"error":{"message":"private-server-message"}}`)
				}
			}))
			defer server.Close()
			c, _ := NewClient(server.URL, "fixture-token")
			var out struct {
				Result string `json:"result"`
			}
			e := c.Mutate(context.Background(), "PATCH", projectID, sessionID, "/issues/td-0000000000000001", mutationKey, "r-7", map[string]string{"title": "updated"}, &out)
			if requests != 1 {
				t.Fatal("hidden retry", requests)
			}
			if status == 200 {
				if e != nil || out.Result != "saved" {
					t.Fatal(out, e)
				}
				return
			}
			var api *APIError
			if !errors.As(e, &api) || api.StatusCode != status || api.Code != statusCode(status) || strings.Contains(e.Error(), "private-server-message") {
				t.Fatal(e)
			}
			var uncertain *UncertainWriteError
			if errors.As(e, &uncertain) != (status >= 500) {
				t.Fatal("wrong certainty", e)
			}
		})
	}
}

func TestMutationUncertainResponsePreservesKeyAndValidatesBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; io.WriteString(w, `{"ok":true,"data":`) }))
	defer server.Close()
	c, _ := NewClient(server.URL, "fixture-token")
	var out any
	e := c.Mutate(context.Background(), "PATCH", projectID, sessionID, "/issues/td-0000000000000001", mutationKey, "r-1", map[string]string{"title": "updated"}, &out)
	var uncertain *UncertainWriteError
	if !errors.As(e, &uncertain) || uncertain.Key != mutationKey || requests != 1 {
		t.Fatal(e, requests)
	}
	for _, tc := range []struct{ method, path, key, revision string }{
		{"PATCH", "/issues/x", mutationKey, ""}, {"DELETE", "/issues/x", mutationKey, "7"},
		{"PATCH", "/../sessions", mutationKey, "r-1"}, {"PATCH", "/issues/x?secret=x", mutationKey, "r-1"},
		{"PATCH", "/issues/x", "fresh-key", "r-1"},
	} {
		if e := c.Mutate(context.Background(), tc.method, projectID, sessionID, tc.path, tc.key, tc.revision, nil, &out); e == nil {
			t.Fatal("invalid mutation accepted", tc)
		}
	}
	if requests != 1 {
		t.Fatal("invalid mutation sent", requests)
	}
}

func TestBackendDoesNotForwardCredentialOnRedirect(t *testing.T) {
	forwarded := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++ }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer source.Close()
	c, _ := NewClient(source.URL, "fixture-token")
	if e := c.Check(context.Background()); e == nil {
		t.Fatal("redirect accepted")
	}
	if forwarded != 0 {
		t.Fatal("credential request forwarded", forwarded)
	}
}
