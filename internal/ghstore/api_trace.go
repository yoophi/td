package ghstore

import (
	"fmt"
	"os"
	"strconv"
	"sync/atomic"
)

var apiInvocations atomic.Int64
var observedHTTPResponses atomic.Int64

// TD_GH_DEBUG traces only numeric request/response facts, never credentials,
// bodies or arbitrary headers. Counts cover this process's gh API calls,
// including repository verification, but not gh auth's internal network calls.
func traceAPIResponses(invocation int64, headers []apiResponseHeaders) {
	total := observedHTTPResponses.Add(int64(len(headers)))
	if os.Getenv("TD_GH_DEBUG") != "1" {
		return
	}
	remaining := "unknown"
	lastStatus := 0
	if len(headers) > 0 {
		last := headers[len(headers)-1]
		lastStatus = last.Status
		if n, err := strconv.Atoi(last.Rate.Get("X-Ratelimit-Remaining")); err == nil && n >= 0 {
			remaining = strconv.Itoa(n)
		}
	}
	fmt.Fprintf(os.Stderr, "gh-api invocation=%d observed_http_responses=%d total_observed_http_responses=%d last_status=%d remaining=%s\n", invocation, len(headers), total, lastStatus, remaining)
}
