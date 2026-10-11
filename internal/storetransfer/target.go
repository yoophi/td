package storetransfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
)

type TargetSpec struct {
	ProjectDir string `json:"project_dir"`
	Store      string `json:"store"`
	Namespace  string `json:"namespace,omitempty"`
	Remote     string `json:"remote,omitempty"`
}

// InspectedTarget is a read-only observation, not permission to execute.
// ProjectDir is local controller state; do not publish paths in provenance.
// File permissions, available capacity and remote state can change after it.
type InspectedTarget struct {
	Spec                      TargetSpec
	Observation               *Source
	NeedsSQLiteInitialization bool
}

func InspectTarget(ctx context.Context, spec TargetSpec, maxBytes int64) (*InspectedTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := byteLimit(maxBytes); err != nil {
		return nil, err
	}
	if spec.ProjectDir == "" {
		return nil, fmt.Errorf("explicit destination project directory is required")
	}
	root, err := filepath.Abs(db.ResolveBaseDir(spec.ProjectDir))
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve destination project: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("destination project must be a directory")
	}
	spec.ProjectDir = root
	result := &InspectedTarget{Spec: spec}
	switch spec.Store {
	case config.StoreSQLite:
		if spec.Remote != "" {
			return nil, fmt.Errorf("SQLite destination does not accept a Git remote")
		}
		if spec.Namespace == "" || strings.TrimSpace(spec.Namespace) != spec.Namespace || strings.ContainsAny(spec.Namespace, "\r\n\x00") {
			return nil, fmt.Errorf("SQLite destination requires an explicit stable namespace")
		}
		path := filepath.Join(root, ".todos", "issues.db")
		_, err := os.Lstat(path)
		var data *db.SQLiteWorkspaceExport
		if os.IsNotExist(err) {
			// A dangling database symlink is not an empty destination.
			parentPath := filepath.Dir(path)
			_, perr := os.Lstat(parentPath)
			if perr != nil && !os.IsNotExist(perr) {
				return nil, perr
			}
			if perr == nil {
				parent, perr := os.Stat(parentPath)
				if perr != nil {
					return nil, fmt.Errorf("resolve destination .todos directory: %w", perr)
				}
				if !parent.IsDir() {
					return nil, fmt.Errorf("destination .todos path is not a directory")
				}
			}
			data = &db.SQLiteWorkspaceExport{SchemaVersion: db.SchemaVersion, Tasks: []db.SQLiteExportTask{}, Notes: []models.Note{}, Boards: []db.SQLiteExportBoard{}, Actions: []models.ActionLog{}, ExcludedActions: map[string]int{}}
			result.NeedsSQLiteInitialization = true
		} else if err != nil {
			return nil, fmt.Errorf("inspect SQLite destination: %w", err)
		} else {
			data, err = db.ReadSQLiteTransferDestination(ctx, root, maxBytes)
			if err != nil {
				return nil, fmt.Errorf("inspect SQLite destination: %w", err)
			}
		}
		result.Observation, err = NewSQLite(spec.Namespace, data, maxBytes)
		if err != nil {
			return nil, err
		}
	case config.StoreGitHub:
		if spec.Remote == "" {
			return nil, fmt.Errorf("GitHub destination requires an explicit git remote")
		}
		client, err := ghstore.OpenRemote(ctx, root, spec.Remote)
		if err != nil {
			return nil, err
		}
		if spec.Namespace != "" && spec.Namespace != client.Repository() {
			return nil, fmt.Errorf("GitHub destination namespace differs from resolved repository %s", client.Repository())
		}
		data, err := client.ExportWorkspaceLimited(ctx, maxBytes)
		if err != nil {
			return nil, err
		}
		result.Observation, err = NewGitHub(data, maxBytes)
		if err != nil {
			return nil, err
		}
		result.Spec.Namespace = client.Repository()
	default:
		return nil, fmt.Errorf("destination store must explicitly be sqlite or gh-issue")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
