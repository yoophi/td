package ghstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRateLimitClassificationAndSingleWriteAttempt(t *testing.T) {
	for _, tc := range []struct {
		text    string
		limited bool
	}{
		{"HTTP 429: too many requests", true}, {"gh: API rate limit exceeded (HTTP 403)", true}, {"HTTP 403: secondary rate limit exceeded", true}, {"HTTP 403: abuse detection mechanism", true},
		{"HTTP 403: Resource not accessible", false}, {"HTTP 401: API rate limit exceeded", false}, {"HTTP 4290: strange", false}, {"rate limit exceeded without HTTP status", false}, {"context deadline exceeded", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			cause := errors.New(tc.text)
			calls := 0
			c := &Client{repo: "owner/repo", run: func(context.Context, string, []byte, ...string) ([]byte, error) { calls++; return nil, cause }}
			_, err := c.request(context.Background(), "PATCH", "/issues/1", map[string]string{"title": "updated"}, false)
			var limited *RateLimitError
			if errors.As(err, &limited) != tc.limited || !errors.Is(err, cause) || calls != 1 || !strings.Contains(err.Error(), "write may have reached GitHub") {
				t.Fatalf("%v calls=%d", err, calls)
			}
			if tc.limited {
				if limited.MinimumWait() != time.Minute || !strings.Contains(err.Error(), "reset deadline unavailable") {
					t.Fatal("invented reset deadline")
				}
				if classifyRateLimit(err) != err {
					t.Fatal("typed error wrapped repeatedly")
				}
			}
		})
	}
}
func TestRateLimitActualCLIOutputAndRepositoryRunner(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' 'gh: secondary rate limit exceeded (HTTP 403)' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, run := range []func() error{
		func() error { _, err := runAPI(context.Background(), dir, nil, "api"); return err },
		func() error { _, err := runCommand(context.Background(), dir, "gh", "api"); return err },
	} {
		err := run()
		var limited *RateLimitError
		if !errors.As(err, &limited) || !strings.Contains(err.Error(), "secondary rate limit") {
			t.Fatalf("CLI stderr lost: %v", err)
		}
	}
	cause := &RateLimitError{Cause: errors.New("HTTP 429")}
	wrapped := fmt.Errorf("read failed: %w", cause)
	if classifyRateLimit(wrapped) != wrapped {
		t.Fatal("typed wrapping changed")
	}
}
