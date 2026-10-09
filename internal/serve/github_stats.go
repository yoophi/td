package serve

import (
	"context"
	"github.com/marcus/td/internal/models"
	"net/http"
	"time"
)

type githubStatisticsClient interface {
	ExtendedStats(context.Context, time.Time) (*models.ExtendedStats, error)
	DistinctLabels(context.Context) ([]string, error)
}

func (s *Server) EnableGitHubStats(store *GitHubReadStore) {
	s.githubCapabilities = append(s.githubCapabilities, "statistics", "labels")
	s.githubEndpoints = append(s.githubEndpoints, "GET /v1/stats", "GET /v1/labels")
	for _, route := range []string{"GET /v1/stats", "GET /v1/labels"} {
		s.mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) { store.statistics(w, r, route == "GET /v1/stats") })
	}
}
func (s *GitHubReadStore) statistics(w http.ResponseWriter, r *http.Request, stats bool) {
	if r.URL.RawQuery != "" {
		WriteError(w, ErrValidation, "this operation does not support query parameters", 400)
		return
	}
	client, err := s.open(r.Context())
	if err != nil {
		if !writeGitHubRateLimit(w, err) {
			WriteError(w, "store_error", err.Error(), 502)
		}
		return
	}
	provider, ok := client.(githubStatisticsClient)
	if !ok {
		WriteError(w, "unsupported_operation", "statistics store is unavailable", 501)
		return
	}
	if stats {
		result, err := provider.ExtendedStats(r.Context(), time.Now())
		if err != nil {
			if !writeGitHubRateLimit(w, err) {
				WriteError(w, "store_error", err.Error(), 502)
			}
			return
		}
		WriteSuccess(w, StatsToDTO(result), http.StatusOK)
	} else {
		labels, err := provider.DistinctLabels(r.Context())
		if err != nil {
			if !writeGitHubRateLimit(w, err) {
				WriteError(w, "store_error", err.Error(), 502)
			}
			return
		}
		if labels == nil {
			labels = []string{}
		}
		WriteSuccess(w, map[string]any{"labels": labels, "workflows": []any{}, "default_workflow": "standard"}, http.StatusOK)
	}
}
