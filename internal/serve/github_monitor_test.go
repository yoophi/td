package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
	"github.com/marcus/td/internal/session"
	"github.com/marcus/td/pkg/monitor"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type githubMonitorFixture struct {
	githubReadFixture
	activities map[string][]models.Activity
	facts      map[string]*ghstore.MonitorReviewFacts
	calls      map[string]int
	failure    string
	actor      string
}

func (f *githubMonitorFixture) ListIncludingDeleted(context.Context, bool) ([]ghstore.Record, error) {
	if f.failure == "list" {
		return nil, errors.New("list permission denied")
	}
	return f.records, nil
}
func (f *githubMonitorFixture) ListActivityIncludingDeleted(_ context.Context, id string) ([]models.Activity, error) {
	f.calls[id]++
	if f.failure == "activity" {
		return nil, errors.New("activity permission denied")
	}
	return f.activities[id], nil
}
func (f *githubMonitorFixture) ObserveMonitorReview(_ context.Context, r *ghstore.Record, actor string) (*ghstore.MonitorReviewFacts, error) {
	f.actor = actor
	if f.failure == "review" {
		return nil, &ghstore.ConflictError{ID: r.ID}
	}
	return f.facts[r.ID], nil
}

type githubMonitorFocusFixture struct {
	focus   *string
	failure error
}

func (f *githubMonitorFocusFixture) ReadFocus(context.Context) (*string, error) {
	return f.focus, f.failure
}
func (f *githubMonitorFocusFixture) ListSessions(context.Context) ([]session.Session, error) {
	return nil, errors.New("unexpected session-list read")
}
func (f *githubMonitorFocusFixture) SetFocus(context.Context, *string) (*string, error) {
	return nil, errors.New("unexpected focus write")
}
func monitorFixture(now time.Time) *githubMonitorFixture {
	f := &githubMonitorFixture{activities: map[string][]models.Activity{}, facts: map[string]*ghstore.MonitorReviewFacts{}, calls: map[string]int{}}
	states := []models.Status{models.StatusOpen, models.StatusOpen, models.StatusBlocked, models.StatusInProgress, models.StatusInProgress, models.StatusInReview, models.StatusClosed, models.StatusOpen, models.StatusInReview, models.StatusInReview}
	for i, state := range states {
		n := i + 1
		f.records = append(f.records, ghstore.Record{Number: n, Issue: models.Issue{ID: fmt.Sprintf("gh-%d", n), Title: fmt.Sprintf("Task %d", n), Status: state, Type: models.TypeTask, Priority: models.PriorityP2, CreatedAt: now.Add(-time.Duration(n) * time.Hour)}})
	}
	f.records[0].Details = &ghstore.IssueDetails{Dependencies: []string{"gh-7"}}
	f.records[1].Details = &ghstore.IssueDetails{Dependencies: []string{"gh-4"}}
	f.records[4].Details = &ghstore.IssueDetails{Transitions: []ghstore.TransitionRecord{{Action: "reject", OperationID: "reject5", At: now.Add(-time.Minute), SessionID: "reviewer"}}}
	f.records[7].DeletedAt = &now
	f.facts["gh-6"] = &ghstore.MonitorReviewFacts{Fresh: true}
	f.facts["gh-9"] = &ghstore.MonitorReviewFacts{Fresh: false, ActiveApproval: false}
	f.facts["gh-10"] = &ghstore.MonitorReviewFacts{Fresh: true, ActiveApproval: true}
	f.activities["gh-1"] = []models.Activity{{ID: "ghc-1", Kind: "log", SessionID: "worker", LogType: models.LogTypeProgress, Message: "build passes", CreatedAt: now.Add(-time.Minute)}}
	f.activities["gh-8"] = []models.Activity{{ID: "ghc-8", Kind: "comment", Native: true, Message: "native comment", CreatedAt: now}, {ID: "ghc-9", Kind: "handoff", SessionID: "worker", Done: []string{"done"}, CreatedAt: now.Add(-2 * time.Hour)}}
	return f
}
func TestGitHubMonitorCategoriesFocusActivityAndTDQCache(t *testing.T) {
	now := time.Now()
	f := monitorFixture(now)
	focus := "gh-4"
	msg, err := fetchGitHubMonitor(context.Background(), f, "web", reviewpolicy.ModeTrusted, &focus, "", "auto", true, monitor.SortByPriority, now)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(issues []models.Issue) []string {
		result := []string{}
		for _, issue := range issues {
			result = append(result, issue.ID)
		}
		return result
	}
	if !slices.Equal(ids(msg.TaskList.Ready), []string{"gh-1"}) || len(msg.TaskList.Blocked) != 2 || !slices.Equal(ids(msg.TaskList.InProgress), []string{"gh-4"}) || !slices.Equal(ids(msg.TaskList.NeedsRework), []string{"gh-5"}) || !slices.Equal(ids(msg.TaskList.Reviewable), []string{"gh-6"}) || !slices.Equal(ids(msg.TaskList.PendingOther), []string{"gh-9"}) || !slices.Equal(ids(msg.TaskList.ReadyToClose), []string{"gh-10"}) || !slices.Equal(ids(msg.TaskList.Closed), []string{"gh-7"}) {
		t.Fatalf("queues: %+v", msg.TaskList)
	}
	if msg.FocusedIssue == nil || msg.FocusedIssue.ID != focus || len(msg.InProgress) != 2 || len(msg.Activity) != 4 || len(msg.RecentHandoffs) != 1 || msg.RecentHandoffs[0].IssueID != "gh-8" || !slices.Equal(msg.ActiveSessions, []string{"worker"}) || f.actor != "web" {
		t.Fatalf("%+v actor=%s", msg, f.actor)
	}
	f.calls = map[string]int{}
	msg, err = fetchGitHubMonitor(context.Background(), f, "web", reviewpolicy.ModeTrusted, &focus, `log.message ~ "build"`, "tdq", false, monitor.SortByPriority, now)
	if err != nil || len(msg.TaskList.Ready) != 1 || len(msg.TaskList.Blocked) != 0 || len(msg.Activity) != 4 || msg.FocusedIssue == nil {
		t.Fatalf("TDQ: %+v %v", msg, err)
	}
	for id, count := range f.calls {
		if count != 1 {
			t.Fatalf("activity fetched repeatedly for %s: %d", id, count)
		}
	}
}
func TestGitHubMonitorFailureBoundariesAndTimestampBasedRework(t *testing.T) {
	now := time.Now()
	for _, kind := range []string{"list", "activity", "review", "duplicate-issue", "duplicate-activity", "missing-dependency"} {
		t.Run(kind, func(t *testing.T) {
			f := monitorFixture(now)
			f.failure = kind
			switch kind {
			case "duplicate-issue":
				f.records = append(f.records, f.records[0])
			case "duplicate-activity":
				f.activities["gh-2"] = f.activities["gh-1"]
			case "missing-dependency":
				f.records[0].Details.Dependencies = []string{"gh-404"}
			}
			if msg, err := fetchGitHubMonitor(context.Background(), f, "web", reviewpolicy.ModeTrusted, nil, "", "auto", false, monitor.SortByPriority, now); err == nil || msg != nil {
				t.Fatalf("partial success %+v %v", msg, err)
			}
		})
	}
	f := monitorFixture(now)
	f.records[4].Details.Transitions = append([]ghstore.TransitionRecord{{Action: "review", At: now, OperationID: "review5"}}, f.records[4].Details.Transitions...)
	msg, err := fetchGitHubMonitor(context.Background(), f, "web", reviewpolicy.ModeTrusted, nil, "Task 5", "text", false, monitor.SortByPriority, now)
	if err != nil || len(msg.TaskList.InProgress) != 1 || len(msg.TaskList.NeedsRework) != 0 {
		t.Fatalf("array order overrode timestamps: %+v %v", msg, err)
	}
	deleted := "gh-8"
	msg, err = fetchGitHubMonitor(context.Background(), f, "web", reviewpolicy.ModeTrusted, &deleted, "", "text", false, monitor.SortByPriority, now)
	if err != nil || msg.FocusedIssue != nil {
		t.Fatal("deleted focus leaked")
	}
}
func TestGitHubMonitorHTTPInputContractsAndErrors(t *testing.T) {
	f := monitorFixture(time.Now())
	focus := "gh-4"
	focusStore := &githubMonitorFocusFixture{focus: &focus}
	opens := 0
	openError := error(nil)
	srv := NewGitHubServer(t.TempDir(), "web", "owner/repo", ServeConfig{})
	srv.EnableGitHubMonitor(&GitHubReadStore{open: func(context.Context) (githubReadClient, error) {
		opens++
		if openError != nil {
			return nil, openError
		}
		return f, nil
	}}, focusStore)
	request := func(path string, want int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	srv.githubEvents = newGitHubEventHub(func(context.Context) (string, error) { return "gh-current", nil }, 30*time.Second)
	srv.githubEvents.observe(context.Background())
	body := request("/v1/monitor?include_closed=true", 200)["data"].(map[string]any)
	if body["session_id"] != "web" || body["change_token"] != "gh-current" || body["monitor"].(map[string]any)["focused_issue"].(map[string]any)["id"] != "gh-4" {
		t.Fatalf("%v", body)
	}
	for _, path := range []string{"?session_id=forged", "?sort=invalid", "?include_closed=1", "?include_closed=true&include_closed=false", "?search_mode=invalid", "?search_mode=tdq&search=status%20%3D"} {
		before := opens
		request("/v1/monitor"+path, 400)
		if opens != before {
			t.Fatal("invalid query opened backend")
		}
	}
	openError = errors.New("gh missing")
	request("/v1/monitor", 502)
	openError = &ghstore.RateLimitError{Cause: errors.New("HTTP 429")}
	request("/v1/monitor", 429)
	openError = nil
	focusStore.failure = errWebSessionChanged
	request("/v1/monitor", 409)
	focusStore.failure = nil
	f.failure = "review"
	request("/v1/monitor", 409)
	f.failure = "activity"
	request("/v1/monitor", 502)
	f.failure = ""
	project := request("/v1/project", 200)["data"].(map[string]any)
	if !strings.Contains(fmt.Sprint(project["capabilities"]), "monitor") || !strings.Contains(fmt.Sprint(project["supported_endpoints"]), "GET /v1/monitor") {
		t.Fatal("support matrix missing")
	}
}

func TestGitHubMonitorReviewModesLimitsEmptyAndSorting(t *testing.T) {
	now := time.Now()
	for _, mode := range []reviewpolicy.Mode{reviewpolicy.ModeTrusted, reviewpolicy.ModeDelegated, reviewpolicy.ModeStrict} {
		f := monitorFixture(now)
		f.records[5].ImplementerSession = "web"
		f.facts["gh-6"].ImplementationInvolved = true
		f.facts["gh-6"].AnyInvolved = true
		msg, err := fetchGitHubMonitor(context.Background(), f, "web", mode, nil, "Task 6", "text", false, monitor.SortByCreatedDesc, now)
		if err != nil {
			t.Fatal(err)
		}
		if mode == reviewpolicy.ModeTrusted {
			if len(msg.TaskList.Reviewable) != 1 {
				t.Fatal("trusted acknowledgement path hidden")
			}
		} else if len(msg.TaskList.PendingReview) != 1 || len(msg.TaskList.Reviewable) != 0 {
			t.Fatal("own implementation presented as independent review")
		}
	}
	f := monitorFixture(now)
	for i := 0; i < 60; i++ {
		f.activities["gh-1"] = append(f.activities["gh-1"], models.Activity{ID: fmt.Sprintf("ghc-%d", 100+i), Kind: "comment", CreatedAt: now.Add(-time.Duration(i) * time.Second)})
	}
	for i := 0; i < 12; i++ {
		f.activities["gh-8"] = append(f.activities["gh-8"], models.Activity{ID: fmt.Sprintf("ghc-%d", 200+i), Kind: "handoff", SessionID: "worker", CreatedAt: now.Add(-time.Duration(i) * time.Minute)})
	}
	msg, err := fetchGitHubMonitor(context.Background(), f, "web", reviewpolicy.ModeTrusted, nil, "", "text", false, monitor.SortByCreatedDesc, now)
	if err != nil || len(msg.Activity) != 50 || len(msg.RecentHandoffs) != 10 || len(msg.TaskList.Closed) != 0 {
		t.Fatalf("limits: %+v %v", msg, err)
	}
	for i := 1; i < len(msg.Activity); i++ {
		if msg.Activity[i].Timestamp.After(msg.Activity[i-1].Timestamp) {
			t.Fatal("activity not sorted")
		}
	}
	f.records = nil
	msg, err = fetchGitHubMonitor(context.Background(), f, "web", reviewpolicy.ModeTrusted, nil, "", "text", false, monitor.SortByPriority, now)
	if err != nil || msg.HasIssues {
		t.Fatal("empty repository failed")
	}
	dto := MonitorDataToDTO(msg)
	if dto.Activity == nil || dto.TaskList.Ready == nil || dto.TaskList.ReadyToClose == nil || dto.ActiveSessions == nil {
		t.Fatal("empty repository produced null arrays")
	}
}

func TestGitHubMonitorUnsupportedProviderAndNoSQLiteFallback(t *testing.T) {
	for _, missingFocus := range []bool{false, true} {
		dir := t.TempDir()
		srv := NewGitHubServer(dir, "web", "owner/repo", ServeConfig{})
		var focus SessionStore = &githubMonitorFocusFixture{}
		if missingFocus {
			focus = nil
		}
		srv.EnableGitHubMonitor(&GitHubReadStore{open: func(context.Context) (githubReadClient, error) { return &githubReadFixture{}, nil }}, focus)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/v1/monitor", nil))
		if w.Code != 501 {
			t.Fatalf("missing provider did not fail explicitly: %d %s", w.Code, w.Body.String())
		}
		if _, err := os.Stat(filepath.Join(dir, ".todos", "issues.db")); !os.IsNotExist(err) {
			t.Fatal("unsupported provider fell back to SQLite")
		}
	}
}
