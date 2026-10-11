package storetransfer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcus/td/internal/db"
)

func TestPrepareNewSQLiteDestinationAndApply(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	target, err := InspectTarget(ctx, TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareSQLiteDestination(ctx, githubTaskInputFixture(t), target, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := db.OpenSQLiteTransferDestination(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	if err := prepared.Apply(ctx, writer); err != nil {
		t.Fatal(err)
	}
	actual, err := db.ReadSQLiteTransferDestination(ctx, root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(actual.Tasks) != 2 || len(actual.Boards) != 1 || len(actual.TransferArchives) != 1 {
		t.Fatal("new target incomplete")
	}
}

func TestPrepareNewSQLiteDestinationChecksBeforeInitializationAndRejectsLaterChanges(t *testing.T) {
	for _, stage := range []string{"capacity", "unknown_mapping", "appeared", "after_init"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			target, err := InspectTarget(ctx, TargetSpec{ProjectDir: root, Store: "sqlite", Namespace: "target"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(0)
			var bindings map[string]string
			if stage == "capacity" {
				limit = 1
			}
			if stage == "unknown_mapping" {
				bindings = map[string]string{"unknown": "td-target"}
			}
			if stage == "appeared" {
				if _, err := db.InitializeSQLiteTransferDestination(ctx, root); err != nil {
					t.Fatal(err)
				}
			}
			prepared, err := PrepareSQLiteDestination(ctx, githubTaskInputFixture(t), target, bindings, limit)
			if stage != "after_init" {
				if err == nil || prepared != nil {
					t.Fatal("invalid new target accepted")
				}
				if stage != "appeared" {
					if _, err := os.Stat(filepath.Join(root, ".todos")); !os.IsNotExist(err) {
						t.Fatal("failed preflight initialized target", err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			writer, err := db.OpenSQLiteTransferDestination(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = writer.Close() }()
			if _, err := writer.CreateNote("external", "change"); err != nil {
				t.Fatal(err)
			}
			if err := prepared.Apply(ctx, writer); err == nil {
				t.Fatal("post-publication edits accepted as baseline")
			}
			actual, err := db.ReadSQLiteTransferDestination(ctx, root, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(actual.Tasks) != 0 || len(actual.Notes) != 1 || len(actual.TransferArchives) != 0 {
				t.Fatal("external edits lost or transfer partially committed")
			}
		})
	}
}
