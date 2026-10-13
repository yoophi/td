package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/marcus/td/internal/models"
)

var backendProjectPattern = regexp.MustCompile(`^prj_[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func ValidBackendProjectID(id string) bool { return backendProjectPattern.MatchString(id) }

// ParseStore splits only the first comma, preserving a comma in an API path.
func ParseStore(value string) (kind, endpoint string, err error) {
	kind, endpoint, hasEndpoint := strings.Cut(value, ",")
	kind = strings.TrimSpace(kind)
	switch kind {
	case StoreSQLite, StoreGitHub:
		if hasEndpoint {
			return "", "", fmt.Errorf("only td-backend accepts an API URL in --store")
		}
		return kind, "", nil
	case StoreBackend:
		if !hasEndpoint {
			return kind, "", nil
		}
		endpoint, err = NormalizeBackendURL(strings.TrimSpace(endpoint))
		return kind, endpoint, err
	default:
		return "", "", fmt.Errorf("invalid store (use sqlite, gh-issue or td-backend,<URL>)")
	}
}
func NormalizeBackendURL(value string) (string, error) {
	u, e := url.Parse(value)
	if e != nil {
		return "", fmt.Errorf("invalid backend API URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" || u.Opaque != "" {
		return "", fmt.Errorf("backend URL requires http(s) scheme and host")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("backend URL cannot contain credentials, query or fragment")
	}
	return strings.TrimRight(u.String(), "/"), nil
}
func SetBackend(baseDir string, b models.BackendStoreConfig, expectedStore string) error {
	endpoint, e := NormalizeBackendURL(b.URL)
	if e != nil {
		return e
	}
	b.URL = endpoint
	if !ValidBackendProjectID(b.ProjectID) {
		return fmt.Errorf("invalid backend project ID: expected prj_ UUIDv7")
	}
	return withConfigLock(baseDir, func() error {
		cfg, e := Load(baseDir)
		if e != nil {
			return e
		}
		current, e := Store(cfg)
		if e != nil {
			return e
		}
		if cfg.Backend != nil {
			endpoint, e := NormalizeBackendURL(cfg.Backend.URL)
			if e != nil {
				return e
			}
			cfg.Backend.URL = endpoint
		}
		if cfg.Backend != nil && (cfg.Backend.URL != b.URL || cfg.Backend.ProjectID != b.ProjectID) {
			return fmt.Errorf("existing backend association differs; refusing to overwrite")
		}
		if current != expectedStore && !(current == StoreBackend && cfg.Backend != nil && *cfg.Backend == b) {
			return fmt.Errorf("project store changed during backend initialization")
		}
		cfg.Store = StoreBackend
		cfg.Backend = &b
		cfg.GitHub = nil
		return Save(baseDir, cfg)
	})
}
