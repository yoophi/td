package storetransfer

import (
	"context"
	"fmt"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
)

type Collected struct {
	Source  *Source
	Preview *Preview
}

// Collect reads the selected source only and returns a validated passive
// preview. It never selects, opens, initializes or writes a destination. A
// SQLite source needs an explicitly retained namespace; GitHub uses its repo.
func Collect(ctx context.Context, baseDir, namespace string, maxBytes int64) (*Collected, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := byteLimit(maxBytes); err != nil {
		return nil, err
	}
	baseDir = db.ResolveBaseDir(baseDir)
	cfg, err := config.Load(baseDir)
	if err != nil {
		return nil, err
	}
	store, err := config.Store(cfg)
	if err != nil {
		return nil, err
	}
	var source *Source
	switch store {
	case config.StoreSQLite:
		if namespace == "" || strings.TrimSpace(namespace) != namespace || strings.ContainsAny(namespace, "\r\n\x00") {
			return nil, fmt.Errorf("SQLite transfer requires an explicit stable source namespace")
		}
		data, err := db.ReadSQLiteWorkspaceLimited(ctx, baseDir, maxBytes)
		if err != nil {
			return nil, err
		}
		source, err = NewSQLite(namespace, data, maxBytes)
		if err != nil {
			return nil, err
		}
	case config.StoreGitHub:
		if namespace != "" && (cfg.GitHub == nil || namespace != cfg.GitHub.Repo) {
			return nil, fmt.Errorf("GitHub source namespace must match the configured repository")
		}
		client, err := ghstore.Open(ctx, baseDir, cfg.GitHub)
		if err != nil {
			return nil, err
		}
		data, err := client.ExportWorkspaceLimited(ctx, maxBytes)
		if err != nil {
			return nil, err
		}
		source, err = NewGitHub(data, maxBytes)
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	preview, err := source.Preview()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &Collected{Source: source, Preview: preview}, nil
}
