package backendstore

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Mutate uses the caller's durable key and observed revision. It performs a
// single attempt; callers explicitly recover receipts with the same payload.
func (c *Client) Mutate(ctx context.Context, method, project, session, resource, key, revision string, body, out any) error {
	path, e := scopedPath(project, session)
	if e != nil {
		return e
	}
	if method != http.MethodPost && method != http.MethodPatch && method != http.MethodDelete {
		return fmt.Errorf("unsupported backend mutation method")
	}
	if !operationPattern.MatchString(key) {
		return fmt.Errorf("backend mutation requires UUIDv4 operation key")
	}
	// Resource paths are relative to this project and cannot escape its scope.
	if !strings.HasPrefix(resource, "/") || strings.ContainsAny(resource, "?#%") || strings.Contains(resource, "..") || strings.Contains(resource, "//") {
		return fmt.Errorf("invalid project resource path")
	}
	match := ""
	if revision != "" {
		if !revisionPattern.MatchString(revision) {
			return fmt.Errorf("invalid observed backend revision")
		}
		match = `"` + revision + `"`
	} else if method == http.MethodPatch || method == http.MethodDelete {
		return fmt.Errorf("backend mutation requires observed revision")
	}
	return c.requestWithMatch(ctx, method, path+resource, session, key, match, body, out)
}
