package ghstore

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type apiResponseHeaders struct {
	Status int
	Rate   http.Header
}

var responseStatus = regexp.MustCompile(`^HTTP/[0-9]+(?:\.[0-9]+)? ([0-9]{3})(?: .*)?$`)

// gh --slurp writes '[' / ',' before each response's headers. Remove only
// these header blocks and preserve their JSON delimiters and body bytes.
// Retain only rate-limit headers; never propagate other response headers.
func splitAPIHeaders(output []byte) ([]byte, []apiResponseHeaders, error) {
	var body bytes.Buffer
	headers := []apiResponseHeaders{}
	inside := false
	for _, line := range bytes.SplitAfter(output, []byte("\n")) {
		text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
		if inside {
			if text == "" {
				inside = false
				continue
			}
			name, value, ok := strings.Cut(text, ":")
			if !ok || strings.TrimSpace(name) == "" {
				return nil, headers, errors.New("invalid gh response header block")
			}
			name = http.CanonicalHeaderKey(strings.TrimSpace(name))
			switch name {
			case "Retry-After", "X-Ratelimit-Remaining", "X-Ratelimit-Reset":
				headers[len(headers)-1].Rate.Add(name, strings.TrimSpace(value))
			}
			continue
		}
		prefix := ""
		candidate := text
		if strings.HasPrefix(candidate, "[") || strings.HasPrefix(candidate, ",") {
			prefix = candidate[:1]
			candidate = candidate[1:]
		}
		match := responseStatus.FindStringSubmatch(candidate)
		if match != nil {
			status, _ := strconv.Atoi(match[1])
			headers = append(headers, apiResponseHeaders{Status: status, Rate: http.Header{}})
			body.WriteString(prefix)
			inside = true
			continue
		}
		body.Write(line)
	}
	if inside {
		return nil, headers, errors.New("truncated gh response headers")
	}
	return body.Bytes(), headers, nil
}
func rateLimitFromHeaders(cause error, headers []apiResponseHeaders, now time.Time) error {
	var response *apiResponseHeaders
	for i := range headers {
		if headers[i].Status >= 400 {
			response = &headers[i]
		}
	}
	if response == nil {
		return classifyRateLimit(cause)
	}
	classified := classifyRateLimit(cause)
	var existing *RateLimitError
	limited := errors.As(classified, &existing)
	if response.Status == 429 || (response.Status == 403 && response.Rate.Get("X-Ratelimit-Remaining") == "0") {
		limited = true
	}
	if !limited {
		return cause
	}
	result := &RateLimitError{Cause: cause}
	// Multiple conflicting values are not a reliable reset observation.
	if values := response.Rate.Values("Retry-After"); len(values) == 1 {
		value := values[0]
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
			result.RetryAt = now.Add(time.Duration(seconds) * time.Second)
			result.WaitSource = "retry-after"
		} else if date, err := http.ParseTime(value); err == nil {
			result.RetryAt = date
			result.WaitSource = "retry-after"
		}
	}
	if result.RetryAt.IsZero() && response.Rate.Get("X-Ratelimit-Remaining") == "0" {
		if values := response.Rate.Values("X-Ratelimit-Reset"); len(values) == 1 {
			epoch, err := strconv.ParseInt(values[0], 10, 64)
			if err == nil && epoch > 0 && epoch <= 253402300799 {
				result.RetryAt = time.Unix(epoch, 0)
				result.WaitSource = "x-ratelimit-reset"
			}
		}
	}
	return result
}
func requireAPIHeaders(headers []apiResponseHeaders) error {
	if len(headers) == 0 {
		return fmt.Errorf("gh api --include returned no response headers")
	}
	return nil
}
