package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAPIHeadersPreservesSlurpedPagesAndBodyStrings(t *testing.T) {
	// gh 2.101.0 startPage writes '[' or ',' before processResponse writes
	// headers; response bodies and the trailing outer ']' stay untouched.
	input := "[HTTP/2.0 200 OK\nContent-Type: application/json\r\nX-Ratelimit-Remaining: 42\r\nSet-Cookie: private\r\n\r\n[{\"body\":\"HTTP/2.0 429\\nRetry-After: 9\"}]\n,HTTP/2.0 200 OK\nContent-Type: application/json\r\n\r\n[]]"
	body, headers, err := splitAPIHeaders([]byte(input))
	var pages [][]map[string]any
	if err != nil || json.Unmarshal(body, &pages) != nil || len(pages) != 2 || len(pages[0]) != 1 || len(headers) != 2 || headers[0].Rate.Get("X-Ratelimit-Remaining") != "42" || headers[0].Rate.Get("Set-Cookie") != "" {
		t.Fatalf("body=%s headers=%v err=%v", body, headers, err)
	}
	if pages[0][0]["body"] != "HTTP/2.0 429\nRetry-After: 9" {
		t.Fatal("body string rewritten")
	}
	body, headers, err = splitAPIHeaders([]byte("HTTP/1.1 204 No Content\r\n\r\n"))
	if err != nil || len(body) != 0 || len(headers) != 1 {
		t.Fatal("204 changed")
	}
	for _, bad := range []string{"HTTP/2.0 200 OK\nX-Header: truncated", "HTTP/2.0 200 OK\ninvalid header\n\n{}"} {
		if _, _, err := splitAPIHeaders([]byte(bad)); err == nil {
			t.Fatal("invalid header accepted")
		}
	}
}
func TestRateLimitObservedHeaderPrecedenceAndFallback(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		status  int
		headers http.Header
		want    time.Duration
		source  string
		limited bool
	}{
		{"retry-seconds", 429, http.Header{"Retry-After": {"120"}, "X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1791550800"}}, 2 * time.Minute, "retry-after", true},
		{"retry-date", 429, http.Header{"Retry-After": {now.Add(2 * time.Minute).Format(http.TimeFormat)}}, 2 * time.Minute, "retry-after", true},
		{"primary-reset", 403, http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {"1791550800"}}, time.Hour, "x-ratelimit-reset", true},
		{"permission", 403, http.Header{"X-Ratelimit-Remaining": {"50"}}, 0, "", false},
		{"malformed", 429, http.Header{"Retry-After": {"-1"}}, time.Minute, "", true},
		{"overflow", 429, http.Header{"Retry-After": {"9223372036854775807"}}, time.Minute, "", true},
		{"duplicates", 429, http.Header{"Retry-After": {"10", "20"}}, time.Minute, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := errors.New("CLI failed")
			err := rateLimitFromHeaders(cause, []apiResponseHeaders{{Status: tc.status, Rate: tc.headers}}, now)
			var limit *RateLimitError
			if errors.As(err, &limit) != tc.limited || !errors.Is(err, cause) {
				t.Fatalf("%v", err)
			}
			if tc.limited && (limit.waitAt(now) != tc.want || limit.WaitSource != tc.source) {
				t.Fatalf("wait=%v source=%s date=%v", limit.waitAt(now), limit.WaitSource, limit.RetryAt)
			}
		})
	}
	limit := &RateLimitError{RetryAt: now.Add(1500 * time.Millisecond)}
	if limit.waitAt(now) != 2*time.Second {
		t.Fatal("retry delay rounded down")
	}
}
func TestRunAPIIncludesHeadersAndDropsPartialFailedPages(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "output")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TD_TEST_OUTPUT", output)
	script := "#!/bin/sh\nfound=no\nfor arg in \"$@\"; do if [ \"$arg\" = '--include' ]; then found=yes; fi; done\nif [ \"$found\" != yes ]; then exit 2; fi\n/bin/cat \"$TD_TEST_OUTPUT\"\nexit \"${TD_TEST_EXIT:-0}\"\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(output, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("HTTP/2.0 200 OK\n\n{\"number\":1}")
	body, err := runAPI(context.Background(), dir, nil, "api", "repos/owner/repo/issues/1")
	if err != nil || string(body) != `{"number":1}` {
		t.Fatalf("%s %v", body, err)
	}
	body, err = runCommand(context.Background(), dir, "gh", "api", "repos/owner/repo")
	if err != nil || string(body) != `{"number":1}` {
		t.Fatal("repository API bypassed headers")
	}
	t.Setenv("TD_TEST_EXIT", "1")
	write("[HTTP/2.0 200 OK\n\n[{\"number\":1}]\n,HTTP/2.0 429 Too Many Requests\nRetry-After: 3600\n\n{\"message\":\"limited\"}]")
	body, err = runAPI(context.Background(), dir, nil, "api", "--paginate", "--slurp", "repos/owner/repo/issues")
	var limit *RateLimitError
	if len(body) != 0 || !errors.As(err, &limit) || limit.WaitSource != "retry-after" || limit.MinimumWait() < 3599*time.Second || !strings.Contains(err.Error(), "no automatic retry") {
		t.Fatalf("partial=%s err=%v", body, err)
	}
	t.Setenv("TD_TEST_EXIT", "0")
	write(`{"number":1}`)
	if _, err := runAPI(context.Background(), dir, nil, "api"); err == nil {
		t.Fatal("missing headers accepted")
	}
}
