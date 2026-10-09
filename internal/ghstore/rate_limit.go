package ghstore

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// RateLimitError preserves the diagnostic and observed retry/reset deadline.
// Without usable headers its wait is conservative advice, not evidence that
// the limit has reset.
type RateLimitError struct {
	Cause      error
	RetryAt    time.Time
	WaitSource string
}

func (e *RateLimitError) Error() string {
	if !e.RetryAt.IsZero() {
		return fmt.Sprintf("%v; GitHub rate limit reached; no automatic retry; wait until %s (%s), inspect remote state before retrying writes", e.Cause, e.RetryAt.UTC().Format(time.RFC3339), e.WaitSource)
	}
	return fmt.Sprintf("%v; GitHub rate limit reached; no automatic retry; reset deadline unavailable, inspect 'gh api --include rate_limit' before retrying", e.Cause)
}
func (e *RateLimitError) Unwrap() error              { return e.Cause }
func (e *RateLimitError) MinimumWait() time.Duration { return e.waitAt(time.Now()) }
func (e *RateLimitError) waitAt(now time.Time) time.Duration {
	if e.RetryAt.IsZero() || !e.RetryAt.After(now) {
		return time.Minute
	}
	wait := e.RetryAt.Sub(now)
	// Round upward: truncation must not advise a retry before the deadline.
	seconds := wait / time.Second
	if wait%time.Second != 0 {
		seconds++
	}
	if seconds > time.Duration((1<<63-1)/time.Second) {
		return time.Duration(1<<63 - 1)
	}
	return seconds * time.Second
}

var rateLimitStatus = regexp.MustCompile(`(?i)\bHTTP\s+(403|429)\b`)

func classifyRateLimit(err error) error {
	if err == nil {
		return nil
	}
	var existing *RateLimitError
	if errors.As(err, &existing) {
		return err
	}
	message := err.Error()
	status := rateLimitStatus.FindStringSubmatch(message)
	if len(status) == 0 {
		return err
	}
	lower := strings.ToLower(message)
	if status[1] == "429" || strings.Contains(lower, "rate limit") || strings.Contains(lower, "abuse detection") {
		return &RateLimitError{Cause: err}
	}
	return err
}
