package backendstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
)

var operationPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type InitIntent struct {
	URL         string            `json:"url"`
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	SessionKeys map[string]string `json:"session_keys"`
}

func operationKey() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 0x40
	b[8] = (b[8] & 63) | 0x80
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func intentPath(base, endpoint string) string {
	hash := sha256.Sum256([]byte(endpoint))
	return filepath.Join(base, ".todos", "backend-init", fmt.Sprintf("%x.json", hash))
}
func loadIntent(base, endpoint string) (InitIntent, error) {
	var out InitIntent
	data, e := os.ReadFile(intentPath(base, endpoint))
	if os.IsNotExist(e) {
		out = InitIntent{URL: endpoint, Name: filepath.Base(base), SessionKeys: map[string]string{}}
		out.Key, e = operationKey()
		return out, e
	}
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(data, &out); e != nil {
		return out, e
	}
	if out.URL != endpoint || !operationPattern.MatchString(out.Key) || out.Name == "" {
		return out, fmt.Errorf("invalid persisted backend init intent")
	}
	if out.SessionKeys == nil {
		out.SessionKeys = map[string]string{}
	}
	for project, key := range out.SessionKeys {
		if !config.ValidBackendProjectID(project) || !operationPattern.MatchString(key) {
			return out, fmt.Errorf("invalid persisted backend session intent")
		}
	}
	return out, nil
}
func saveIntent(base string, intent InitIntent) error {
	path := intentPath(base, intent.URL)
	dir := filepath.Dir(path)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	data, e := json.MarshalIndent(intent, "", "  ")
	if e != nil {
		return e
	}
	file, e := os.CreateTemp(dir, "init-*.tmp")
	if e != nil {
		return e
	}
	name := file.Name()
	defer os.Remove(name)
	if _, e = file.Write(data); e != nil {
		file.Close()
		return e
	}
	if e = file.Sync(); e != nil {
		file.Close()
		return e
	}
	if e = file.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}

// Initialize performs network validation outside the local config lock. The
// durable intent precedes POST, so retries recover even after response/save loss.
func (c *Client) Initialize(ctx context.Context, base, requestedID string) (models.BackendStoreConfig, error) {
	var empty models.BackendStoreConfig
	cfg, e := config.Load(base)
	if e != nil {
		return empty, e
	}
	expected, e := config.Store(cfg)
	if e != nil {
		return empty, e
	}
	if requestedID != "" && !config.ValidBackendProjectID(requestedID) {
		return empty, fmt.Errorf("invalid backend project ID: expected prj_ UUIDv7")
	}
	if cfg.Backend != nil {
		endpoint, e := config.NormalizeBackendURL(cfg.Backend.URL)
		if e != nil {
			return empty, e
		}
		if endpoint != c.URL || (requestedID != "" && requestedID != cfg.Backend.ProjectID) {
			return empty, fmt.Errorf("existing backend association differs; refusing to overwrite")
		}
		requestedID = cfg.Backend.ProjectID
		if !config.ValidBackendProjectID(requestedID) {
			return empty, fmt.Errorf("invalid saved backend project ID")
		}
	}
	if e = c.Check(ctx); e != nil {
		return empty, e
	}
	var intent InitIntent
	e = config.WithLocalStateLock(base, func() error {
		var e error
		intent, e = loadIntent(base, c.URL)
		if e != nil {
			return e
		}
		return saveIntent(base, intent)
	})
	if e != nil {
		return empty, e
	}
	projectID := requestedID
	if projectID == "" {
		project, e := c.CreateProject(ctx, intent.Key, intent.Name)
		if e != nil {
			return empty, e
		}
		projectID = project.ID
	}
	var sessionKey string
	e = config.WithLocalStateLock(base, func() error {
		latest, e := loadIntent(base, c.URL)
		if e != nil {
			return e
		}
		sessionKey = latest.SessionKeys[projectID]
		if sessionKey == "" {
			sessionKey, e = operationKey()
			if e != nil {
				return e
			}
			latest.SessionKeys[projectID] = sessionKey
		}
		return saveIntent(base, latest)
	})
	if e != nil {
		return empty, e
	}
	session, e := c.RegisterInitSession(ctx, projectID, sessionKey)
	if e != nil {
		return empty, e
	}
	if _, e = c.GetProject(ctx, projectID, session); e != nil {
		return empty, e
	}
	result := models.BackendStoreConfig{URL: c.URL, ProjectID: projectID}
	if e = config.SetBackend(base, result, expected); e != nil {
		return empty, e
	}
	return result, nil
}
