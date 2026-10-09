package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/reviewpolicy"
)

type githubWorkflowFixture struct {
	githubWriteFixture
	records           []ghstore.Record
	options           ghstore.TransitionOptions
	action            string
	calls             int
	afterChild        bool
	availabilityError error
}

func (f *githubWorkflowFixture) AvailableTransitions(_ context.Context, _ *ghstore.Record, session string, mode reviewpolicy.Mode) ([]string, error) {
	if session != "actual-web-fixture" {
		return nil, errors.New("availability used wrong session")
	}
	return []string{"reject"}, f.availabilityError
}

func (f *githubWorkflowFixture) List(context.Context, bool) ([]ghstore.Record, error) {
	if f.afterChild && f.calls > 0 {
		return []ghstore.Record{{Issue: models.Issue{ID: "gh-2", ParentID: "gh-1"}}}, nil
	}
	return f.records, nil
}
func (f *githubWorkflowFixture) TransitionObservedWithCascades(_ context.Context, r *ghstore.Record, action string, o ghstore.TransitionOptions) (*ghstore.Record, bool, error) {
	f.calls++
	f.action = action
	f.options = o
	if f.failure != nil {
		return nil, false, f.failure
	}
	result := *r
	result.Status = models.StatusInReview
	if o.RecordOnly {
		now := time.Now()
		result.ReviewerSession = o.SessionID
		result.ReviewedAt = &now
		result.Details = &ghstore.IssueDetails{Reviews: []models.IssueReview{{ID: "fixture-review", ReviewerSession: o.SessionID, Decision: "approved", Summary: o.Reason, SelfReview: o.SelfReview, CreatedAt: now}}}
	}
	return &result, false, nil
}
func TestGitHubHTTPWorkflowPolicyWiringAndCascadeBoundary(t *testing.T) {
	f := &githubWorkflowFixture{githubWriteFixture: githubWriteFixture{record: ghstore.Record{Issue: models.Issue{ID: "gh-1", Title: "Workflow fixture", Status: models.StatusOpen}}}}
	store := &GitHubWriteStore{baseDir: t.TempDir(), sessionID: "actual-web-fixture", open: func(context.Context) (githubIssueWriter, error) { return f, nil }}
	srv := NewGitHubServer(store.baseDir, store.sessionID, "owner/repo", ServeConfig{})
	srv.EnableGitHubWorkflow(store)
	request := func(action, body, revision string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/v1/issues/gh-1/"+action, strings.NewReader(body))
		if revision != "" {
			r.Header.Set("If-Match", revision)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", action, w.Code, w.Body.String())
		}
	}
	request("start", "", "", 200)
	if f.options.SessionID != store.sessionID || f.action != "start" {
		t.Fatalf("%+v", f.options)
	}
	before := f.calls
	request("start", `{"session_id":"forged"}`, "", 400)
	request("start", `{"self_review":true,"reason":"forged"}`, "", 400)
	request("review", `{}`, "stale", 409)
	if f.calls != before {
		t.Fatal("invalid or stale request transitioned")
	}
	request("reviews", `{"decision":"approved","summary":"Fixture self-review","self_review":true}`, "", 201)
	if f.action != "approve" || !f.options.RecordOnly || !f.options.SelfReview || f.options.Reason != "Fixture self-review" {
		t.Fatalf("%+v", f.options)
	}
	f.failure = &ghstore.WorkflowInputError{Reason: "closer must supply reason"}
	request("close", `{}`, "", 400)
	f.failure = &ghstore.PolicyError{Reason: "review denied"}
	request("approve", `{}`, "", 403)
	f.failure = &ghstore.WorkflowStateError{Reason: "stale review"}
	request("review", `{}`, "", 409)
	f.failure = &ghstore.ConflictError{ID: "gh-1"}
	request("review", `{}`, "", 409)
	f.failure = nil
	f.record.ParentID = "gh-9"
	request("review", `{}`, "", 200)
	f.record.ParentID = ""
	f.records = []ghstore.Record{{Issue: models.Issue{ID: "gh-2", ParentID: "gh-1"}}}
	request("review", `{}`, "", 200)
	f.records = []ghstore.Record{{Issue: models.Issue{ID: "gh-2"}, Details: &ghstore.IssueDetails{Dependencies: []string{"gh-1"}}}}
	request("approve", `{}`, "", 200)
	f.records = nil
	f.calls = 0
	f.afterChild = true
	f.failure = &ghstore.ConflictError{ID: "gh-2", AfterWrite: true}
	request("review", `{}`, "", 409)
	if f.calls != 1 {
		t.Fatal("partial result retried")
	}
	f.failure = nil
	f.availabilityError = errors.New("fixture availability permission denied")
	f.calls = 0
	request("start", `{}`, "", 502)
	if f.calls != 1 {
		t.Fatal("saved transition retried on availability failure")
	}
	f.availabilityError = nil
	r := httptest.NewRequest("POST", "/v1/issues/gh-1/start", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	var body struct {
		Data struct {
			Issue struct {
				Actions []string `json:"available_transitions"`
			} `json:"issue"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Data.Issue.Actions) != 1 || body.Data.Issue.Actions[0] != "reject" {
		t.Fatalf("availability absent from mutation response: %s %v", w.Body.String(), err)
	}
}

func TestGitHubWorkflowModesAndStrictInputContract(t *testing.T) {
	for _, mode := range []string{"strict", "balanced", "delegated", "trusted"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TD_FEATURE_REVIEW_POLICY_MODE", mode)
			t.Setenv("TD_FEATURE_BALANCED_REVIEW_POLICY", "")
			f := &githubWorkflowFixture{githubWriteFixture: githubWriteFixture{record: ghstore.Record{Issue: models.Issue{ID: "gh-1", Status: models.StatusOpen}}}}
			store := &GitHubWriteStore{baseDir: t.TempDir(), sessionID: "actual-web-fixture", open: func(context.Context) (githubIssueWriter, error) { return f, nil }}
			srv := NewGitHubServer(store.baseDir, store.sessionID, "owner/repo", ServeConfig{})
			srv.EnableGitHubWorkflow(store)
			request := func(action, body string, want int) {
				t.Helper()
				w := httptest.NewRecorder()
				srv.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/issues/gh-1/"+action, strings.NewReader(body)))
				if w.Code != want {
					t.Fatalf("%s %s: %d %s", mode, action, w.Code, w.Body.String())
				}
			}
			request("start", `{"force":true}`, 200)
			if string(f.options.Mode) != mode || f.options.AgentType != "web" || f.options.SessionID != store.sessionID || !f.options.Force {
				t.Fatalf("actual web policy context lost: %+v", f.options)
			}
			before := f.calls
			for _, bad := range []struct{ action, body string }{
				{"review", `{"force":true}`}, {"approve", `{"minor":true}`},
				{"approve", `{"decision":"changes_requested"}`}, {"start", `{"admin":"override"}`},
				{"approve", `{"summary":"wrong endpoint"}`}, {"reviews", `{"reason":"one","summary":"two"}`},
				{"approve", `{"session_id":"forged"}`}, {"close", `{"admin":"   "}`},
				{"review", `{} {}`}, {"start", `{"reason":17}`},
			} {
				request(bad.action, bad.body, 400)
			}
			if mode != "trusted" {
				request("approve", `{"self_review":true,"reason":"fixture ack"}`, 400)
			}
			if mode == "strict" || mode == "balanced" {
				request("reviews", `{"reason":"fixture record-only"}`, 400)
			}
			if f.calls != before {
				t.Fatal("invalid input reached mutation")
			}
			t.Setenv("TD_FEATURE_REVIEW_POLICY_MODE", "invalid-fixture-mode")
			request("start", `{}`, 502)
			if f.calls != before {
				t.Fatal("invalid policy silently fell back and wrote")
			}
		})
	}
}

func TestGitHubRecordedReviewResponseContract(t *testing.T) {
	t.Setenv("TD_FEATURE_REVIEW_POLICY_MODE", "trusted")
	f := &githubWorkflowFixture{githubWriteFixture: githubWriteFixture{record: ghstore.Record{Issue: models.Issue{ID: "gh-1", Status: models.StatusInReview}}}}
	store := &GitHubWriteStore{baseDir: t.TempDir(), sessionID: "actual-web-fixture", open: func(context.Context) (githubIssueWriter, error) { return f, nil }}
	srv := NewGitHubServer(store.baseDir, store.sessionID, "owner/repo", ServeConfig{})
	srv.EnableGitHubWorkflow(store)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/issues/gh-1/reviews", strings.NewReader(`{"summary":"Fixture honest self-review","self_review":true}`)))
	var response struct {
		Data struct {
			Review       IssueReviewDTO      `json:"review"`
			ActiveReview *IssueReviewSummary `json:"active_review"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 201 || response.Data.Review.ID != "fixture-review" || response.Data.Review.Summary != "Fixture honest self-review" || !response.Data.Review.SelfReview || response.Data.ActiveReview == nil || response.Data.ActiveReview.ID != response.Data.Review.ID {
		t.Fatalf("recorded review contract: %d %s (%v)", w.Code, w.Body.String(), err)
	}
}
