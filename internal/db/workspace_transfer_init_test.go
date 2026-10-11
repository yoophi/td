package db

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestInitializeSQLiteTransferDestinationNewLiteralPathAndNoOverwrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "literal?mode=ro#project")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	baseline, err := InitializeSQLiteTransferDestination(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.SchemaVersion != SchemaVersion || len(baseline.Tasks) != 0 || len(baseline.Boards) != 1 || baseline.Boards[0].Board.ID != "bd-all-issues" {
		t.Fatal("incomplete published schema/default board")
	}
	before, err := os.ReadFile(filepath.Join(root, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	if baseline, err := InitializeSQLiteTransferDestination(context.Background(), root); err == nil || baseline != nil {
		t.Fatal("existing target initialized again")
	}
	after, err := os.ReadFile(filepath.Join(root, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("existing target changed")
	}
	actual, err := ReadSQLiteTransferDestination(context.Background(), root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if actual.SchemaVersion != SchemaVersion || len(actual.Boards) != 1 {
		t.Fatal("published DB unreadable")
	}
	stages, err := filepath.Glob(filepath.Join(root, ".todos", ".td-transfer-init-*"))
	if err != nil || len(stages) != 0 {
		t.Fatal("private staging leaked", stages, err)
	}
}

func TestInitializeSQLiteTransferDestinationConcurrentPublishHasOneWinner(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := InitializeSQLiteTransferDestination(context.Background(), root); results <- err })
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("expected one no-overwrite publisher", winners)
	}
	actual, err := ReadSQLiteTransferDestination(context.Background(), root, 0)
	if err != nil || len(actual.Boards) != 1 || actual.SchemaVersion != SchemaVersion {
		t.Fatal("concurrent publish corrupted destination", err)
	}
}

func TestInitializeSQLiteTransferDestinationCancellationAndDanglingFile(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if baseline, err := InitializeSQLiteTransferDestination(ctx, root); err == nil || baseline != nil {
		t.Fatal("cancelled initialization succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, ".todos")); !os.IsNotExist(err) {
		t.Fatal("cancel created directories", err)
	}
	if err := os.Mkdir(filepath.Join(root, ".todos"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, dbFile)
	if err := os.Symlink(filepath.Join(root, "absent"), path); err != nil {
		t.Fatal(err)
	}
	if baseline, err := InitializeSQLiteTransferDestination(context.Background(), root); err == nil || baseline != nil {
		t.Fatal("dangling symlink overwritten")
	}
	if _, err := os.Readlink(path); err != nil {
		t.Fatal("existing symlink removed", err)
	}
}
