package backendstore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/marcus/td/internal/config"
)

type groupIntent struct {
	Version    int             `json:"version"`
	URL        string          `json:"url"`
	Project    string          `json:"project"`
	Session    string          `json:"session"`
	InputHash  string          `json:"input_hash"`
	Key        string          `json:"key"`
	Plan       json.RawMessage `json:"plan"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// RunGroupIntent freezes a multi-write plan before the first remote mutation.
// An interrupted invocation reuses its plan and group ID; individual writes
// should use that ID in their RunIntent identity. After completion, the same
// command may create a fresh plan and a new group.
func (c *Connection) RunGroupIntent(ctx context.Context, input any, prepare func() (json.RawMessage, error), apply func(string, json.RawMessage) error) error {
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
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	root := c.StateDirectory
	if root == "" {
		root, e = backendContextRoot()
		if e != nil {
			return e
		}
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("backend group state directory must be absolute")
	}
	directory := filepath.Join(root, "operations", "groups", c.SessionID)
	path := filepath.Join(directory, hash+".json")
	load := func() (groupIntent, error) {
		var intent groupIntent
		bytes, e := os.ReadFile(path)
		if e != nil {
			return intent, e
		}
		if e = json.Unmarshal(bytes, &intent); e != nil || intent.Version != 1 || !operationPattern.MatchString(intent.Key) || !json.Valid(intent.Plan) {
			return intent, fmt.Errorf("invalid persisted backend group intent")
		}
		if intent.URL != c.Client.URL || intent.Project != c.ProjectID || intent.Session != c.SessionID || intent.InputHash != hash {
			return intent, fmt.Errorf("backend group intent scope differs")
		}
		return intent, nil
	}
	reusable := func(intent groupIntent) bool { return intent.FinishedAt == nil || !intent.FinishedAt.Before(started) }
	var intent groupIntent
	useExisting := false
	e = config.WithLocalStateLock(directory, func() error {
		prior, e := load()
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		if reusable(prior) {
			intent, useExisting = prior, true
		}
		return nil
	})
	if e != nil {
		return e
	}
	if !useExisting {
		plan, e := prepare()
		if e != nil {
			return e
		}
		if !json.Valid(plan) {
			return fmt.Errorf("invalid backend group plan")
		}
		e = config.WithLocalStateLock(directory, func() error {
			prior, e := load()
			if e == nil && reusable(prior) {
				intent = prior
				return nil
			}
			if e != nil && !os.IsNotExist(e) {
				return e
			}
			key, e := operationKey()
			if e != nil {
				return e
			}
			intent = groupIntent{Version: 1, URL: c.Client.URL, Project: c.ProjectID, Session: c.SessionID, InputHash: hash, Key: key, Plan: append(json.RawMessage(nil), plan...)}
			return writeJSONIntent(path, intent)
		})
		if e != nil {
			return e
		}
	}
	if e = apply(intent.Key, append(json.RawMessage(nil), intent.Plan...)); e != nil {
		return e
	}
	return config.WithLocalStateLock(directory, func() error {
		latest, e := load()
		if e != nil {
			return e
		}
		if latest.Key != intent.Key || latest.FinishedAt != nil {
			return nil
		}
		finished := time.Now().UTC()
		latest.FinishedAt = &finished
		return writeJSONIntent(path, latest)
	})
}
