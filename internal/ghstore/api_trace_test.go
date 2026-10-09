package ghstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAPITraceCountsObservedPagesWithoutSensitiveData(t *testing.T) {
	bin := t.TempDir()
	fixture := filepath.Join(bin, "response.txt")
	if err := os.WriteFile(fixture, []byte("[HTTP/2.0 200 OK\r\nX-Ratelimit-Remaining: 42\r\nSet-Cookie: private-cookie\r\n\r\n[{\"private-body\":true}]\n,HTTP/2.0 200 OK\r\nX-Ratelimit-Remaining: 41\r\n\r\n[]]"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\ncat \"$TD_TRACE_RESPONSE\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_TRACE_RESPONSE", fixture)
	t.Setenv("TD_GH_DEBUG", "1")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	capturePath := filepath.Join(bin, "trace.txt")
	capture, err := os.Create(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = capture
	t.Cleanup(func() { os.Stderr = previous; _ = capture.Close() })
	beforeCalls, beforeResponses := apiInvocations.Load(), observedHTTPResponses.Load()
	if _, err := runAPI(context.Background(), bin, nil, "api", "--paginate", "--slurp", "repos/owner/repo/issues"); err != nil {
		t.Fatal(err)
	}
	trace, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{fmt.Sprintf("invocation=%d", beforeCalls+1), "observed_http_responses=2", fmt.Sprintf("total_observed_http_responses=%d", beforeResponses+2), "last_status=200 remaining=41"} {
		if !strings.Contains(string(trace), want) {
			t.Fatal(want, string(trace))
		}
	}
	if strings.Contains(string(trace), "private") || strings.Contains(string(trace), "Cookie") {
		t.Fatal("trace leaked body or headers", string(trace))
	}
}
