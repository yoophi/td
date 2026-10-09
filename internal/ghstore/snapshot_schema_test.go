package ghstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotSchemaSeparatesIncrementalDataFromLegacyReaders(t *testing.T) {
	t.Setenv("TD_GH_CACHE", "off")
	cache := newSnapshotCacheIdentity("owner/repo", []byte(t.Name()))
	if cache == nil || filepath.Base(cache.root) != "v2" {
		t.Fatal("new schema reused legacy directory")
	}
	if newSnapshotCache("owner/repo", []byte(t.Name())) != nil {
		t.Fatal("read opt-out ignored")
	}
	c, _ := cachedSnapshotFixture(t)
	ctx := context.Background()
	if _, err := c.ReadSnapshot(ctx, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c.cache.path(true))
	if err != nil {
		t.Fatal(err)
	}
	var e cacheEnvelope
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	if e.Version != 2 {
		t.Fatal("incremental-capable envelope claims legacy schema")
	}
	e.Version = 1
	data, err = json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.cache.path(true), data, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := c.cache.load(c, true, e.Generation, time.Now()); err != nil || s != nil {
		t.Fatal("legacy envelope reused", err)
	}
}

func TestCompatibleInvalidationFencesLegacyPublishers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "td", "gh-issue")
	current := &snapshotCache{root: filepath.Join(root, "v2"), repo: "owner/repo", credential: cacheHash(t.Name())}
	legacy := &snapshotCache{root: filepath.Join(root, "v1"), repo: current.repo, credential: current.credential}
	ctx := context.Background()
	// Seed an envelope under the old schema's actual path/key. Invalidation
	// must identify it by repository, not assume current schema path hashes.
	path := filepath.Join(legacy.root, cacheHash("legacy-path"), "snapshot.json")
	data, _ := json.Marshal(cacheEnvelope{Version: 1, Repository: current.repo, Generation: "0:0"})
	if err := atomicCacheFile(path, data); err != nil {
		t.Fatal(err)
	}
	before, err := legacy.generation()
	if err != nil {
		t.Fatal(err)
	}
	if err := current.invalidateCompatible(ctx, false); err != nil {
		t.Fatal(err)
	}
	after, err := legacy.generation()
	if err != nil || after == before {
		t.Fatal("legacy generation unchanged", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("legacy snapshot survived write invalidation", err)
	}
	// The regular generation fence also prevents pre-invalidation publishers
	// from writing a regenerated observation into the legacy directory.
	c, _ := cachedSnapshotFixture(t)
	s, err := c.readSnapshotRemote(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.publish(ctx, s, before, s.ObservedAt()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy.path(true)); !os.IsNotExist(err) {
		t.Fatal("old generation republished", err)
	}
}
