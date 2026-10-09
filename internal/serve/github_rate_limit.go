package serve

import (
	"errors"
	"github.com/marcus/td/internal/ghstore"
	"net/http"
	"strconv"
)

func writeGitHubRateLimit(w http.ResponseWriter, err error) bool {
	var limit *ghstore.RateLimitError
	if !errors.As(err, &limit) {
		return false
	}
	// Use an observed deadline when available, otherwise a minimum wait hint.
	// A write must still be inspected before the caller retries it.
	w.Header().Set("Retry-After", strconv.Itoa(int(limit.MinimumWait().Seconds())))
	WriteError(w, "rate_limited", err.Error(), http.StatusTooManyRequests)
	return true
}
