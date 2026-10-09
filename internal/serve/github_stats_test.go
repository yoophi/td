package serve

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/td/internal/models"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

type githubStatsFixture struct {
	githubReadFixture
	fail  bool
	calls int
}

func (f *githubStatsFixture) ExtendedStats(context.Context, time.Time) (*models.ExtendedStats, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("activity permission denied")
	}
	return &models.ExtendedStats{Total: 4, TotalPoints: 10, AvgPointsPerTask: 2.5, CompletionRate: 0.25, TotalLogs: 7, TotalHandoffs: 2, MostActiveSession: "ses-a", ByStatus: map[models.Status]int{models.StatusClosed: 1}}, nil
}
func (f *githubStatsFixture) DistinctLabels(context.Context) ([]string, error) {
	f.calls++
	if f.fail {
		return nil, errors.New("label permission denied")
	}
	return nil, nil
}
func TestGitHubStatisticsHTTPContractsAndErrors(t *testing.T) {
	f := &githubStatsFixture{}
	opens := 0
	openFailure := false
	unsupported := false
	srv := NewGitHubServer(t.TempDir(), "fixture-web", "owner/repo", ServeConfig{})
	srv.EnableGitHubStats(&GitHubReadStore{open: func(context.Context) (githubReadClient, error) {
		opens++
		if openFailure {
			return nil, errors.New("gh missing")
		}
		if unsupported {
			return &githubReadFixture{}, nil
		}
		return f, nil
	}})
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
	project := request("/v1/project", 200)["data"].(map[string]any)
	caps := []string{}
	for _, value := range project["capabilities"].([]any) {
		caps = append(caps, value.(string))
	}
	endpoints := []string{}
	for _, value := range project["supported_endpoints"].([]any) {
		endpoints = append(endpoints, value.(string))
	}
	if !slices.Contains(caps, "statistics") || !slices.Contains(caps, "labels") || !slices.Contains(endpoints, "GET /v1/stats") || !slices.Contains(endpoints, "GET /v1/labels") {
		t.Fatalf("capability missing: %v", project)
	}
	data := request("/v1/stats", 200)["data"].(map[string]any)
	if data["total"] != float64(4) || data["completion_rate"] != 0.25 || data["total_logs"] != float64(7) || data["total_handoffs"] != float64(2) || data["oldest_open"] != nil || data["newest_task"] != nil || data["last_closed"] != nil || len(data["by_type"].(map[string]any)) != 0 {
		t.Fatalf("%v", data)
	}
	data = request("/v1/labels", 200)["data"].(map[string]any)
	if len(data["labels"].([]any)) != 0 || len(data["workflows"].([]any)) != 0 || data["default_workflow"] != "standard" {
		t.Fatalf("%v", data)
	}
	for _, path := range []string{"/v1/stats", "/v1/labels"} {
		before := opens
		request(path+"?session=forged", 400)
		if opens != before {
			t.Fatal("invalid query opened backend")
		}
		f.fail = true
		request(path, 502)
		f.fail = false
		openFailure = true
		request(path, 502)
		openFailure = false
		unsupported = true
		request(path, 501)
		unsupported = false
	}
}
