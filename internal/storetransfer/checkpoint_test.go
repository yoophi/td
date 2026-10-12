package storetransfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func checkpointFixture(t *testing.T) *Checkpoint {
	t.Helper()
	source, _ := githubEntitySourceFixture(t)
	target := gitHubPrepareTarget(t, &ghstore.WorkspaceExport{Repository: "owner/repo"})
	c, err := NewCheckpoint(source, target, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	source.Envelope.Payload[0] = '!'
	target.Observation.Envelope.Payload[0] = '!'
	return c
}

func TestCheckpointRoundTripAndRejectsInvalidInputs(t *testing.T) {
	c := checkpointFixture(t)
	data, _ := json.Marshal(c)
	read, err := ReadCheckpoint(bytes.NewReader(data), 0)
	if err != nil || read.OperationID != c.OperationID {
		t.Fatal(read, err)
	}
	for _, test := range []struct {
		name   string
		change func(*Checkpoint)
	}{
		{"checksum", func(c *Checkpoint) { c.SHA256 = "changed" }},
		{"version", func(c *Checkpoint) { c.Version++; c.SHA256 = c.digest() }},
		{"operation", func(c *Checkpoint) { c.OperationID = "../foreign"; c.SHA256 = c.digest() }},
		{"destination", func(c *Checkpoint) { c.Destination.Namespace = "other/repo"; c.SHA256 = c.digest() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := *c
			test.change(&copy)
			input, _ := json.Marshal(copy)
			if result, err := ReadCheckpoint(bytes.NewReader(input), 0); err == nil || result != nil {
				t.Fatal("invalid checkpoint accepted")
			}
		})
	}
	for _, input := range [][]byte{append(append([]byte{}, data...), []byte(" {}")...), append([]byte(`{"unknown":true,`), data[1:]...)} {
		if _, err := ReadCheckpoint(bytes.NewReader(input), 0); err == nil {
			t.Fatal("extra JSON accepted")
		}
	}
	if _, err := ReadCheckpoint(bytes.NewReader(data), int64(len(data)-1)); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
}

func TestCheckpointPublishesPrivatelyWithoutOverwrite(t *testing.T) {
	c := checkpointFixture(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	if err := SaveCheckpointNew(path, c, 1); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("capacity failure created files")
	}
	if err := SaveCheckpointNew(path, c, 0); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if _, err := ReadCheckpoint(bytes.NewReader(before), 0); err != nil {
		t.Fatal(err)
	}
	if err := SaveCheckpointNew(path, c, 0); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("existing checkpoint changed")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("private staging file leaked")
	}
	if err := SaveCheckpointNew(filepath.Join(dir, "missing", "checkpoint.json"), c, 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestCheckpointEntityOperationsRemainStableAndQualified(t *testing.T) {
	c := checkpointFixture(t)
	source, err := c.Source.decode()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := source.Preview()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(c)
	restored, err := ReadCheckpoint(bytes.NewReader(data), 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, identity := range preview.Identities {
		op, err := c.EntityOperationID(identity.Key)
		if identity.Kind == "task" {
			if err == nil {
				t.Fatal("task bypassed existing import identity")
			}
			continue
		}
		if err != nil || seen[op] || len(op) > 128 {
			t.Fatal(op, err)
		}
		seen[op] = true
		again, err := restored.EntityOperationID(identity.Key)
		if err != nil || again != op {
			t.Fatal("read changed original operation", again, op, err)
		}
	}
	if len(seen) < 2 {
		t.Fatal("fixture omitted notes or boards")
	}
	if _, err := c.EntityOperationID("unknown"); err == nil {
		t.Fatal("unknown source key accepted")
	}
	c.Source.Namespace = "changed"
	if _, err := c.EntityOperationID(preview.Identities[0].Key); err == nil {
		t.Fatal("changed checkpoint accepted")
	}
}
