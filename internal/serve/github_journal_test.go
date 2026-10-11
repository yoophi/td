package serve

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/testutil"
)

func journalHTTP(t *testing.T) (testutil.GitHubJournalFixture, string, func(string, string, string, int) string) {
	t.Helper()
	f := testutil.NewGitHubJournalFixture(t)
	scope, err := ghcontext.ResolveWeb(context.Background(), f.Directory, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	state, err := scope.Update(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := NewGitHubServer(f.Directory, state.Session.ID, "owner/repo", ServeConfig{})
	store := NewGitHubWriteStore(f.Directory, models.GitHubStoreConfig{Remote: "origin", Repo: "owner/repo"}, scope, state.Session.ID)
	s.EnableGitHubWrites(store)
	s.EnableGitHubComments(store)
	s.EnableGitHubWorkflow(store)
	s.EnableGitHubDeletion(store)
	s.EnableGitHubBoardWrites(store)
	s.EnableGitHubBoardPositions(store)
	request := func(method, path, body string, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	return f, state.Session.ID, request
}

func TestGitHubHTTPProductionFactoryJournalsIssueAndActivityWrites(t *testing.T) {
	f, actor, request := journalHTTP(t)
	request("POST", "/v1/issues", `{"title":"HTTP created journal fixture"}`, 201)
	request("PATCH", "/v1/issues/gh-2", `{"title":"HTTP updated journal fixture"}`, 200)
	request("POST", "/v1/issues/gh-2/comments", `{"text":"HTTP native activity"}`, 201)
	request("POST", "/v1/issues/gh-2/start", `{}`, 200)
	request("POST", "/v1/issues/gh-2/review", `{}`, 200)
	request("POST", "/v1/issues/gh-2/approve", `{"self_review":true,"reason":"Fixture implementation self-review"}`, 200)
	actions := f.Journals(t)
	if len(actions) != 8 {
		t.Fatal(actions)
	}
	for i, a := range actions {
		if a.Phase != "applied" || a.SessionID != actor || a.CarrierID != "gh-999" {
			t.Fatal(i, a)
		}
	}
	if actions[0].Action != "create" || actions[1].Action != "update" || actions[2].Action != "comment_create" || actions[2].CommentAfter.Author != "fixture-author" {
		t.Fatal(actions)
	}
	if actions[4].Group == nil || actions[4].Group.Command != "workflow-update" || actions[5].Action != "comment_create" || actions[6].Action != "update" || actions[5].GroupOperationID != actions[4].OperationID || actions[6].GroupOperationID != actions[4].OperationID {
		t.Fatal("HTTP review handoff and task write are not one command", actions)
	}
}

func TestGitHubHTTPJournalFailuresPreventFalseSuccess(t *testing.T) {
	for _, mode := range []string{"intent-denied", "receipt-denied", "target-denied", "comment-lost"} {
		t.Run(mode, func(t *testing.T) {
			f, _, request := journalHTTP(t)
			t.Setenv("TD_JOURNAL_FAIL", mode)
			var out string
			if mode == "comment-lost" {
				out = request("POST", "/v1/issues/gh-1/comments", `{"text":"Uncertain native activity"}`, 502)
			} else {
				out = request("PATCH", "/v1/issues/gh-1", `{"title":"HTTP attempted journal fixture"}`, 502)
			}
			raw := "HTTP 403"
			if mode == "comment-lost" {
				raw = "connection lost after native comment"
			}
			if !strings.Contains(out, raw) {
				t.Fatal("raw backend error lost", out)
			}
			actions := f.Journals(t)
			want := 1
			if mode == "intent-denied" {
				want = 0
			}
			if len(actions) != want {
				t.Fatal("missing prepared outcome", actions)
			}
			for _, a := range f.Journals(t) {
				if a.Phase != "prepared" {
					t.Fatal("unverified write became applied", a)
				}
			}
		})
	}
}

func TestGitHubHTTPFactoryRejectsRetiredWebSession(t *testing.T) {
	f, _, request := journalHTTP(t)
	scope, err := ghcontext.ResolveWeb(context.Background(), f.Directory, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.Update(context.Background(), func(state *ghcontext.State) error { scope.NewSession(state); return nil }); err != nil {
		t.Fatal(err)
	}
	request("PATCH", "/v1/issues/gh-1", `{"title":"Retired web actor must not write"}`, 409)
	if len(f.Journals(t)) != 0 {
		t.Fatal("retired actor wrote journal")
	}
}

func TestGitHubHTTPProductionFactoryJournalsBoardChanges(t *testing.T) {
	f, actor, request := journalHTTP(t)
	request("POST", "/v1/boards", `{"name":"HTTP journal board","query":"status = open"}`, 201)
	request("PATCH", "/v1/boards/bd-gh-2", `{"name":"HTTP renamed journal board"}`, 200)
	request("DELETE", "/v1/boards/bd-gh-2", `{}`, 200)
	actions := f.Journals(t)
	if len(actions) != 3 {
		t.Fatal(actions)
	}
	for _, a := range actions {
		if a.Phase != "applied" || a.SessionID != actor {
			t.Fatal(a)
		}
	}
	if actions[0].Action != "create" || actions[1].Action != "board" || actions[2].Action != "board" {
		t.Fatal(actions)
	}
}

func TestGitHubHTTPInitialBuiltinSettingsUseOneCreation(t *testing.T) {
	for _, path := range []string{"issues", "move"} {
		t.Run(path, func(t *testing.T) {
			f, actor, request := journalHTTP(t)
			request("POST", "/v1/boards/bd-all-issues/"+path, `{"issue_id":"gh-1"}`, 200)
			actions := f.Journals(t)
			if len(actions) != 1 || actions[0].Action != "create" || actions[0].Phase != "applied" || actions[0].SessionID != actor || actions[0].CreateInput.Body != actions[0].After.Body || !strings.Contains(actions[0].After.Body, `"positions":[{"issue_id":"gh-1"`) {
				t.Fatal("initial board position did not belong to its creation", actions)
			}
			requests, err := os.ReadFile(f.RequestsPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(requests)), "\n") {
				var request struct{ Method, Path string }
				if err := json.Unmarshal([]byte(line), &request); err != nil {
					t.Fatal(err)
				}
				if request.Method == "PATCH" && request.Path == "repos/owner/repo/issues/2" {
					t.Fatal("HTTP initial configuration required a second carrier write")
				}
			}
		})
	}
}
