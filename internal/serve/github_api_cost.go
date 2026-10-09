package serve

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/marcus/td/internal/ghstore"
)

func (s *Server) githubCostMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.githubRequests == nil || os.Getenv("TD_GH_DEBUG") != "1" {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cost := ghstore.WithAPICost(r.Context())
		r = r.WithContext(ctx)
		recorder := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		start := time.Now()
		defer func() {
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			// Pattern is the mux's registered template, never the URL/query/body.
			fmt.Fprintf(os.Stderr, "gh-http-cost scope=%d route=%q status=%d duration_ms=%d\n", cost.Snapshot().ID, route, recorder.code, time.Since(start).Milliseconds())
			ghstore.LogAPICost(cost)
		}()
		next.ServeHTTP(recorder, r)
	})
}
