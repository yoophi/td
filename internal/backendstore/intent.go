package backendstore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcus/td/internal/config"
)

// MutationRequest is immutable once persisted. Prepare may read remote state,
// but is called only when a new invocation needs a new observed request.
type MutationRequest struct {
	Method   string          `json:"method"`
	Resource string          `json:"resource"`
	Revision string          `json:"revision,omitempty"`
	Body     json.RawMessage `json:"body"`
}
type mutationIntent struct {
	Version    int             `json:"version"`
	URL        string          `json:"url"`
	Project    string          `json:"project"`
	Session    string          `json:"session"`
	InputHash  string          `json:"input_hash"`
	Key        string          `json:"key"`
	Request    MutationRequest `json:"request"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// RunIntent records a request before sending it. Pending retries reuse the exact
// payload/revision/key, including read-modify-write payloads. A later invocation
// after completion prepares a fresh request. Concurrent invocations begun before
// completion recover the original receipt. No network runs under the file lock.
// verify must validate any required response fields before completion is saved.
func (c *Connection) RunIntent(ctx context.Context, input any, prepare func() (MutationRequest, error), out any, verify func() error) error {
	if _, e := scopedPath(c.ProjectID, c.SessionID); e != nil {
		return e
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	started := time.Now().UTC()
	raw, e := json.Marshal(input)
	if e != nil {
		return e
	}
	digest := sha256.Sum256(raw)
	hash := fmt.Sprintf("%x", digest)
	root := c.StateDirectory
	if root == "" {
		root, e = backendContextRoot()
		if e != nil {
			return e
		}
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("backend operation state directory must be absolute")
	}
	directory := filepath.Join(root, "operations", c.SessionID)
	path := filepath.Join(directory, hash+".json")
	var intent mutationIntent
	reusable := func(i mutationIntent) bool { return i.FinishedAt == nil || !i.FinishedAt.Before(started) }
	load := func() (mutationIntent, error) {
		i, e := readMutationIntent(path)
		if e != nil {
			return i, e
		}
		if i.URL != c.Client.URL || i.Project != c.ProjectID || i.Session != c.SessionID || i.InputHash != hash {
			return i, fmt.Errorf("backend operation intent scope differs")
		}
		return i, nil
	}
	useExisting := false
	e = config.WithLocalStateLock(directory, func() error {
		i, e := load()
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if reusable(i) {
			intent = i
			useExisting = true
		}
		return nil
	})
	if e != nil {
		return e
	}
	if !useExisting {
		request, e := prepare()
		if e != nil {
			return e
		}
		request.Body = append(json.RawMessage(nil), request.Body...)
		if e = validateMutationRequest(request); e != nil {
			return e
		}
		e = config.WithLocalStateLock(directory, func() error {
			latest, e := load()
			if e == nil && reusable(latest) {
				intent = latest
				return nil
			}
			if e != nil && !os.IsNotExist(e) {
				return e
			}
			key, e := operationKey()
			if e != nil {
				return e
			}
			intent = mutationIntent{Version: 1, URL: c.Client.URL, Project: c.ProjectID, Session: c.SessionID, InputHash: hash, Key: key, Request: request}
			return writeJSONIntent(path, intent)
		})
		if e != nil {
			return e
		}
	}
	request := intent.Request
	result := c.Client.Mutate(ctx, request.Method, c.ProjectID, c.SessionID, request.Resource, intent.Key, request.Revision, request.Body, out)
	if result == nil && verify != nil {
		if e := verify(); e != nil {
			return &UncertainWriteError{Key: intent.Key, Cause: e}
		}
	}
	var uncertain *UncertainWriteError
	if errors.As(result, &uncertain) {
		return result
	}
	// Transport/decode failures retain pending state. Definite 4xx rejection and
	// verified success terminate this invocation; only an explicit later invocation
	// observes fresh state. Redirects are not a definite command outcome.
	definite := result == nil
	var api *APIError
	if errors.As(result, &api) && api.StatusCode >= 400 && api.StatusCode < 500 {
		definite = true
	}
	if !definite {
		return result
	}
	saveErr := config.WithLocalStateLock(directory, func() error {
		latest, e := load()
		if e != nil {
			return e
		}
		// An independently started later invocation may already own this slot.
		if latest.Key != intent.Key || latest.FinishedAt != nil {
			return nil
		}
		finished := time.Now().UTC()
		latest.FinishedAt = &finished
		return writeJSONIntent(path, latest)
	})
	if saveErr != nil {
		if result == nil {
			return &UncertainWriteError{Key: intent.Key, Cause: fmt.Errorf("backend reply received but operation completion could not be saved: %w", saveErr)}
		}
		return fmt.Errorf("%w; operation state save failed: %v", result, saveErr)
	}
	return result
}
func validateMutationRequest(r MutationRequest) error {
	if r.Method != http.MethodPost && r.Method != http.MethodPatch && r.Method != http.MethodDelete && r.Method != http.MethodPut {
		return fmt.Errorf("invalid backend mutation intent method")
	}
	if !validMutationResource(r.Resource) {
		return fmt.Errorf("invalid backend mutation intent resource")
	}
	if r.Revision != "" && !revisionPattern.MatchString(r.Revision) {
		return fmt.Errorf("invalid backend mutation intent revision")
	}
	if (r.Method == http.MethodPatch || r.Method == http.MethodDelete) && r.Revision == "" {
		return fmt.Errorf("observed revision required")
	}
	if len(r.Body) == 0 && (r.Method == http.MethodDelete || (r.Method == http.MethodPost && strings.HasSuffix(r.Resource, "/restore"))) {
		return nil
	}
	if !json.Valid(r.Body) {
		return fmt.Errorf("invalid backend mutation intent body")
	}
	return nil
}
func readMutationIntent(path string) (mutationIntent, error) {
	var out mutationIntent
	raw, e := os.ReadFile(path)
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(raw, &out); e != nil {
		return out, fmt.Errorf("invalid persisted backend mutation intent")
	}
	if out.Version != 1 || !operationPattern.MatchString(out.Key) {
		return out, fmt.Errorf("invalid persisted backend mutation intent")
	}
	if e = validateMutationRequest(out.Request); e != nil {
		return out, e
	}
	return out, nil
}
