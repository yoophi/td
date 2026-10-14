package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcus/td/internal/backendstore"
	"github.com/marcus/td/internal/models"
)

func TestBackendMonitorFormEditResumesCompletedSteps(t *testing.T) {
	const project = "prj_0199f3bb-9ff2-7777-8888-123456789abc"
	const session = "ses_0199f3bb-9ff2-7777-8888-123456789abc"
	const issueID = "td-0123456789abcdef"
	const oldDep = "td-aaaaaaaaaaaaaaaa"
	const newDep = "td-bbbbbbbbbbbbbbbb"
	row := backendstore.Issue{Issue: models.Issue{ID: issueID, Title: "Old", Status: models.StatusOpen, Type: models.TypeTask, Priority: models.PriorityP2, Labels: []string{"alpha"}}, Revision: "r-1"}
	observed := row
	edges := []models.IssueDependency{{ID: "dep-1", IssueID: issueID, DependsOnID: oldDep}}
	completed := map[string][]byte{}
	writes := map[string]int{}
	failedAdd := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := "/v1/projects/" + project + "/issues/" + issueID
		if r.Method == "GET" && r.URL.Path == path {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": backendstore.Detail{Issue: row, Dependencies: edges}})
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" || r.Header.Get("X-TD-Session") != session {
			t.Error("mutation lacks caller or durable key")
		}
		if receipt, ok := completed[key]; ok {
			_, _ = w.Write(receipt)
			return
		}
		if r.Header.Get("If-Match") != `"`+row.Revision+`"` {
			t.Errorf("stale form step: %s %s, match=%q, revision=%s", r.Method, r.URL.Path, r.Header.Get("If-Match"), row.Revision)
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		var data any
		switch {
		case r.Method == "PATCH" && r.URL.Path == path:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["title"] != "Updated" || len(body) != 2 {
				t.Errorf("unexpected field patch: %+v", body)
			}
			var labels []string
			for _, item := range body["labels"].([]any) {
				labels = append(labels, item.(string))
			}
			if len(labels) != 2 || labels[0] != "a" || labels[1] != "z" {
				t.Errorf("labels were not canonicalized: %v", labels)
			}
			row.Title, row.Labels, row.Revision = "Updated", labels, "r-2"
			writes["patch"]++
			data = map[string]any{"issue": row}
		case r.Method == "DELETE" && r.URL.Path == path+"/dependencies/dep-1":
			edges, row.Revision = nil, "r-3"
			writes["remove"]++
			data = map[string]any{"removed": true}
		case r.Method == "POST" && r.URL.Path == path+"/dependencies":
			if !failedAdd {
				failedAdd = true
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"temporary"}}`))
				return
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["depends_on"] != newDep {
				t.Errorf("unexpected dependency add: %+v", body)
			}
			edge := models.IssueDependency{ID: "dep-2", IssueID: issueID, DependsOnID: newDep}
			edges, row.Revision = []models.IssueDependency{edge}, "r-4"
			writes["add"]++
			data = map[string]any{"dependency": edge}
		case r.Method == "POST" && r.URL.Path == path+"/status":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["status"] != string(models.StatusInProgress) {
				t.Errorf("unexpected status update: %+v", body)
			}
			row.Status, row.Revision = models.StatusInProgress, "r-5"
			writes["status"]++
			data = map[string]any{"issue": row}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		receipt, _ := json.Marshal(map[string]any{"ok": true, "data": data})
		completed[key] = receipt
		_, _ = w.Write(receipt)
	}))
	defer server.Close()
	client, err := backendstore.NewClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	conn := &backendstore.Connection{Client: client, ProjectID: project, SessionID: session, StateDirectory: t.TempDir()}
	source := NewBackendDataSource(context.Background(), conn, func(context.Context) error { return nil })
	handle := &backendIssueTransition{source: source, writer: conn, observed: observed}
	desired := observed.Issue
	desired.Title, desired.Status, desired.Labels = "Updated", models.StatusInProgress, []string{"z", "a", "z"}
	if err := handle.SaveEdit(desired, []string{newDep}); err == nil || !strings.Contains(err.Error(), "add") {
		t.Fatalf("partial failure was not reported: %v", err)
	}
	if writes["patch"] != 1 || writes["remove"] != 1 || writes["add"] != 0 || writes["status"] != 0 {
		t.Fatalf("wrong partial operation order: %+v", writes)
	}
	if err := handle.SaveEdit(desired, []string{newDep}); err != nil {
		t.Fatalf("frozen form plan did not resume: %v", err)
	}
	if row.Title != desired.Title || row.Status != desired.Status || row.Revision != "r-5" || len(row.Labels) != 2 || row.Labels[0] != "a" || row.Labels[1] != "z" || len(edges) != 1 || edges[0].DependsOnID != newDep {
		t.Fatalf("form save lost fields, relation or status: %+v %+v", row, edges)
	}
	for _, name := range []string{"patch", "remove", "add", "status"} {
		if writes[name] != 1 {
			t.Fatal(fmt.Sprintf("%s mutated %d times", name, writes[name]))
		}
	}
	if err := handle.SaveEdit(desired, []string{newDep}); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("fresh stale submission accepted after completion: %v", err)
	}
}

func TestBackendMonitorFormKeepsHandleForGroupedRetry(t *testing.T) {
	row := backendstore.Issue{Issue: models.Issue{ID: "td-0123456789abcdef", Title: "Draft"}, Revision: "r-1"}
	store := &backendIssueTransition{observed: row}
	m := Model{WorkflowPending: true, WorkflowRequest: 3, FormAutofillRequest: 4, FormOpen: true, FormEditStore: store, FormState: newFormStateForEditWithTheme(&row.Issue, Theme{})}
	updated, _ := m.handleRemoteFormSaved(MonitorFormSavedMsg{IssueID: row.ID, Request: 3, FormRequest: 4, Error: errors.New("temporary failure")})
	m = updated.(Model)
	if !m.FormOpen || m.FormEditStore != store || m.FormSaveError == nil || !strings.Contains(m.StatusMessage, "retry the same form") {
		t.Fatalf("backend retry lost draft or group handle: %+v", m)
	}
}
