package config

import (
	"fmt"

	"github.com/marcus/td/internal/models"
)

const (
	StoreSQLite = "sqlite"
	StoreGitHub = "gh-issue"
)

// Store returns the project's effective store, defaulting legacy projects to SQLite.
func Store(cfg *models.Config) (string, error) {
	switch cfg.Store {
	case "", StoreSQLite:
		return StoreSQLite, nil
	case StoreGitHub:
		return StoreGitHub, nil
	default:
		return "", fmt.Errorf("invalid store %q (use sqlite or gh-issue)", cfg.Store)
	}
}

// SetStore preserves unrelated settings and serializes writes with other config updates.
func SetStore(baseDir, store string, github *models.GitHubStoreConfig) error {
	if store != StoreSQLite && store != StoreGitHub {
		return fmt.Errorf("invalid store %q (use sqlite or gh-issue)", store)
	}
	if store == StoreGitHub && (github == nil || github.Remote == "" || github.Repo == "") {
		return fmt.Errorf("gh-issue requires a validated GitHub remote and repository")
	}
	return withConfigLock(baseDir, func() error {
		cfg, err := Load(baseDir)
		if err != nil {
			return err
		}
		cfg.Store = store
		cfg.GitHub = nil
		if store == StoreGitHub {
			cfg.GitHub = github
		}
		return Save(baseDir, cfg)
	})
}
