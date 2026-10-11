package db

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// InitializeSQLiteTransferDestination publishes a fully initialized new DB
// without overwriting an existing file. Schema work happens in a private sibling
// staging directory; an atomic hard link publishes only when the target is absent.
// It never selects a backend, opens an existing target, or migrates user data.
func InitializeSQLiteTransferDestination(ctx context.Context, baseDir string) (*SQLiteWorkspaceExport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(ResolveBaseDir(baseDir))
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite transfer destination: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("SQLite transfer destination must be an existing project directory")
	}
	destination := filepath.Join(root, dbFile)
	if _, err := os.Lstat(destination); err == nil {
		return nil, fmt.Errorf("SQLite transfer destination already exists; no initialization attempted")
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect SQLite transfer destination: %w", err)
	}
	todos := filepath.Dir(destination)
	if err := os.MkdirAll(todos, 0755); err != nil {
		return nil, fmt.Errorf("create SQLite transfer directory: %w", err)
	}
	stage, err := os.MkdirTemp(todos, ".td-transfer-init-")
	if err != nil {
		return nil, fmt.Errorf("create private SQLite transfer staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	path := filepath.Join(stage, dbFile)
	if err := os.Mkdir(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	conn, err := OpenSQLite(dsn, OpenOptions{MaxOpenConns: 1})
	if err != nil {
		return nil, fmt.Errorf("open private SQLite transfer staging database: %w", err)
	}
	database := &DB{conn: conn, baseDir: stage}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, schema); err != nil {
		return nil, fmt.Errorf("initialize private SQLite transfer schema: %w", err)
	}
	if _, err := database.RunMigrations(); err != nil {
		return nil, fmt.Errorf("initialize private SQLite transfer migrations: %w", err)
	}
	baseline, err := database.readWorkspace(ctx, SchemaVersion)
	if err != nil {
		return nil, fmt.Errorf("observe private initialized SQLite destination: %w", err)
	}
	if err := database.Close(); err != nil {
		return nil, fmt.Errorf("close private SQLite transfer database: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return nil, fmt.Errorf("sync private SQLite transfer database: %w", err)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Same filesystem by construction. Link fails with EEXIST if another writer
	// creates the destination after inspection; rename would overwrite that file.
	if err := os.Link(path, destination); err != nil {
		return nil, fmt.Errorf("publish new SQLite transfer destination without overwrite: %w", err)
	}
	dir, err := os.Open(todos)
	if err != nil {
		return nil, fmt.Errorf("SQLite destination published; directory open failed; inspect before retry: %w", err)
	}
	err = dir.Sync()
	closeErr = dir.Close()
	if err != nil {
		return nil, fmt.Errorf("SQLite destination published; directory sync failed; inspect before retry: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("SQLite destination published; directory close failed; inspect before retry: %w", closeErr)
	}
	return baseline, nil
}
