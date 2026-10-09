package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cachedSnapshotFixture(t *testing.T) (*Client, *[]string) {
	t.Helper()
	client, calls := snapshotFixture([][]apiIssue{{{Number: 1, State: "open", Title: "Original"}}}, [][]apiComment{{{ID: 1, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: "comment"}}}, nil)
	client.cache = &snapshotCache{root: filepath.Join(t.TempDir(), "td", "gh-issue", "v1"), repo: client.repo, credential: cacheHash("credential-A")}
	return client, calls
}

func TestSnapshotCacheHitScopesFreshnessPermissionsAndCorruption(t *testing.T) {
	c, calls := cachedSnapshotFixture(t)
	ctx := context.Background()
	first, err := c.ReadSnapshot(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.ReadSnapshot(ctx, true)
	if err != nil || len(*calls) != 2 || second.ObservedAt() != first.ObservedAt() {
		t.Fatalf("cache hit: %v %v", *calls, err)
	}
	full := c.cache.path(true)
	for path, mode := range map[string]os.FileMode{filepath.Dir(full): 0700, full: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("permissions: %s %v %v", path, info, err)
		}
	}
	if _, err := c.ReadSnapshot(ctx, false); err != nil || len(*calls) != 3 {
		t.Fatalf("scope isolation: %v %v", *calls, err)
	}
	if _, err := c.ReadSnapshot(WithFreshSnapshot(ctx), true); err != nil || len(*calls) != 5 {
		t.Fatalf("refresh bypass: %v %v", *calls, err)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	var original cacheEnvelope
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		alter func(*cacheEnvelope)
	}{
		{"expired", func(e *cacheEnvelope) { e.CollectedAt = time.Now().Add(-snapshotCacheTTL) }},
		{"future", func(e *cacheEnvelope) { e.CollectedAt = time.Now().Add(time.Hour) }},
		{"version", func(e *cacheEnvelope) { e.Version = 99 }},
		{"credential", func(e *cacheEnvelope) { e.CredentialContext = "other" }},
		{"repo", func(e *cacheEnvelope) { e.Repository = "other/repo" }},
		{"host", func(e *cacheEnvelope) { e.Host = "other.host" }},
		{"incomplete", func(e *cacheEnvelope) { e.Complete = false }},
		{"token", func(e *cacheEnvelope) { e.ChangeToken = "wrong" }},
		{"malformed metadata", func(e *cacheEnvelope) { e.Entries[0].Issue.Body = "<!-- td:issue:v1\ninvalid -->" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var envelope cacheEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatal(err)
			}
			tc.alter(&envelope)
			changed, _ := json.Marshal(envelope)
			if err := os.WriteFile(full, changed, 0600); err != nil {
				t.Fatal(err)
			}
			before := len(*calls)
			if _, err := c.ReadSnapshot(ctx, true); err != nil || len(*calls) != before+2 {
				t.Fatalf("bad cache not recovered: %v %v", *calls, err)
			}
		})
	}
	for _, kind := range []string{"missing", "invalid JSON", "public permissions"} {
		t.Run(kind, func(t *testing.T) {
			switch kind {
			case "missing":
				_ = os.Remove(full)
			case "invalid JSON":
				_ = os.WriteFile(full, []byte("not json"), 0600)
			case "public permissions":
				_ = os.Chmod(full, 0644)
			}
			before := len(*calls)
			if _, err := c.ReadSnapshot(ctx, true); err != nil || len(*calls) != before+2 {
				t.Fatalf("fallback failed: %v %v", *calls, err)
			}
			// Permission errors deliberately use direct reads; restore for the next case.
			_ = os.Chmod(full, 0600)
		})
	}
}

func TestSnapshotCacheGenerationFencesAndNewerPublication(t *testing.T) {
	c, _ := cachedSnapshotFixture(t)
	ctx := context.Background()
	observation, err := c.readSnapshotRemote(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	generation := ""
	if err := c.cache.withLock(ctx, func() error { var err error; generation, err = c.cache.generation(); return err }); err != nil {
		t.Fatal(err)
	}
	if err := c.cache.invalidate(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := c.cache.publish(ctx, observation, generation, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.cache.path(true)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalidated collector republished", err)
	}
	if err := c.cache.withLock(ctx, func() error { var err error; generation, err = c.cache.generation(); return err }); err != nil {
		t.Fatal(err)
	}
	newer := time.Now().Add(-time.Second)
	if err := c.cache.publish(ctx, observation, generation, newer); err != nil {
		t.Fatal(err)
	}
	if err := c.cache.publish(ctx, observation, generation, newer.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c.cache.path(true))
	if err != nil {
		t.Fatal(err)
	}
	var envelope cacheEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || !envelope.CollectedAt.Equal(newer) {
		t.Fatal("older collector replaced new one", err)
	}
	// All-clear also fences collectors for repositories with a previously absent generation.
	if err := c.cache.invalidate(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := c.cache.publish(ctx, observation, generation, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.cache.path(true)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cleared collector republished", err)
	}
}

func TestSnapshotCacheNoPublicationOnPaginationFailureOrStaleSuccess(t *testing.T) {
	c, calls := cachedSnapshotFixture(t)
	ctx := context.Background()
	if _, err := c.ReadSnapshot(ctx, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(c.cache.path(true))
	if err != nil {
		t.Fatal(err)
	}
	var envelope cacheEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.CollectedAt = time.Now().Add(-time.Hour)
	expired, _ := json.Marshal(envelope)
	if err := os.WriteFile(c.cache.path(true), expired, 0600); err != nil {
		t.Fatal(err)
	}
	original := c.run
	failure := errors.New("HTTP 403 rate limit exceeded: original request ID")
	c.run = func(ctx context.Context, dir string, body []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "issues/comments") {
			return nil, failure
		}
		return original(ctx, dir, body, args...)
	}
	s, err := c.ReadSnapshot(ctx, true)
	if s != nil || !strings.Contains(err.Error(), failure.Error()) || len(*calls) != 3 {
		t.Fatalf("stale or partial success: %v %v", s, err)
	}
	after, err := os.ReadFile(c.cache.path(true))
	if err != nil || string(after) != string(expired) {
		t.Fatal("failure published partial data", err)
	}
}

func TestSnapshotCacheSharesConcurrentReadsAndInvalidatesUncertainWrites(t *testing.T) {
	c, _ := cachedSnapshotFixture(t)
	ctx := context.Background()
	original := c.run
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c.run = func(ctx context.Context, dir string, body []byte, args ...string) ([]byte, error) {
		if args[4] != "GET" {
			return nil, errors.New("connection lost after write")
		}
		n := calls.Add(1)
		if n == 1 {
			close(entered)
			<-release
		}
		return original(ctx, dir, body, args...)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 12)
	wg.Go(func() { _, err := c.ReadSnapshot(ctx, true); failures <- err })
	<-entered
	for range 10 {
		wg.Go(func() { _, err := c.ReadSnapshot(ctx, true); failures <- err })
	}
	// Ensure followers can enter the singleflight while the first read is held.
	time.Sleep(30 * time.Millisecond)
	close(release)
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("duplicate bulk requests: %d", calls.Load())
	}
	if _, err := c.request(ctx, "PATCH", "/issues/1", map[string]string{"title": "new"}, false); err == nil {
		t.Fatal("uncertain write succeeded")
	}
	if _, err := os.Stat(c.cache.path(true)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("write kept old snapshot", err)
	}
	if _, err := c.ReadSnapshot(ctx, true); err != nil || calls.Load() != 4 {
		t.Fatalf("post-write cache hit: %d %v", calls.Load(), err)
	}
}

func TestSnapshotCacheCredentialIsolationFallbackAndCleanup(t *testing.T) {
	c, calls := cachedSnapshotFixture(t)
	ctx := context.Background()
	if _, err := c.ReadSnapshot(ctx, true); err != nil {
		t.Fatal(err)
	}
	other := &Client{repo: c.repo, run: c.run, cache: &snapshotCache{root: c.cache.root, repo: c.repo, credential: cacheHash("credential-B")}}
	if _, err := other.ReadSnapshot(ctx, true); err != nil || len(*calls) != 4 {
		t.Fatalf("credential leaked: %v %v", *calls, err)
	}
	if err := c.ClearCache(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other.cache.path(true)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("other credential cache retained", err)
	}
	if _, err := c.ReadSnapshot(ctx, true); err != nil {
		t.Fatal(err)
	}
	file := c.cache.path(true)
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(file, old, old); err != nil {
		t.Fatal(err)
	}
	if err := c.cache.cleanup(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unused cache retained", err)
	}
	// An inaccessible cache root does not turn an otherwise valid remote read into failure.
	broken := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(broken, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	c.cache = &snapshotCache{root: filepath.Join(broken, "gh", "v1"), repo: c.repo, credential: "context"}
	before := len(*calls)
	if _, err := c.ReadSnapshot(ctx, true); err != nil || len(*calls) != before+2 {
		t.Fatalf("cache path failure blocked remote: %v %v", *calls, err)
	}
}

func TestSnapshotCacheHitNeverReplacesIndividualSafetyReads(t *testing.T) {
	c, _ := cachedSnapshotFixture(t)
	if _, err := c.ReadSnapshot(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	c.run = func(_ context.Context, _ string, _ []byte, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("fresh endpoint %s", args[5])
	}
	if _, err := c.Get(context.Background(), "gh-1"); err == nil || !strings.Contains(err.Error(), "/issues/1") {
		t.Fatal("Get used aggregate cache", err)
	}
}

func TestSnapshotCacheProcessHelper(t *testing.T) {
	root := os.Getenv("TD_CACHE_TEST_PROCESS_ROOT")
	if root == "" {
		return
	}
	nanos, err := strconv.ParseInt(os.Getenv("TD_CACHE_TEST_PROCESS_TIME"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{repo: "owner/repo"}
	snapshot, err := client.assembleSnapshot([][]apiIssue{{{Number: 1, State: "open"}}}, [][]apiComment{{}}, true)
	if err != nil {
		t.Fatal(err)
	}
	cache := &snapshotCache{root: root, repo: client.repo, credential: cacheHash("credential-A")}
	if err := cache.publish(context.Background(), snapshot, "0:0", time.Unix(0, nanos).UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotCacheConcurrentProcessesAndClearFence(t *testing.T) {
	c, _ := cachedSnapshotFixture(t)
	older := time.Now().Add(-2 * time.Second).UTC()
	newer := older.Add(time.Second)
	child := func(at time.Time) error {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSnapshotCacheProcessHelper$")
		command.Env = append(os.Environ(), "TD_CACHE_TEST_PROCESS_ROOT="+c.cache.root, "TD_CACHE_TEST_PROCESS_TIME="+strconv.FormatInt(at.UnixNano(), 10))
		out, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}
	failures := make(chan error, 2)
	var wg sync.WaitGroup
	for _, at := range []time.Time{older, newer} {
		wg.Go(func() { failures <- child(at) })
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(c.cache.path(true))
	if err != nil {
		t.Fatal(err)
	}
	var envelope cacheEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || !envelope.CollectedAt.Equal(newer) {
		t.Fatal("cross-process stale publication", err)
	}
	if err := c.cache.invalidate(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := child(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.cache.path(true)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pre-clear process republished", err)
	}
}

func TestSnapshotCacheInvalidationDuringCollection(t *testing.T) {
	c, _ := cachedSnapshotFixture(t)
	original := c.run
	entered, release := make(chan struct{}), make(chan struct{})
	c.run = func(ctx context.Context, dir string, body []byte, args ...string) ([]byte, error) {
		if strings.Contains(args[5], "issues?state") {
			close(entered)
			<-release
		}
		return original(ctx, dir, body, args...)
	}
	done := make(chan error, 1)
	go func() { _, err := c.ReadSnapshot(context.Background(), true); done <- err }()
	<-entered
	if err := c.ClearCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.cache.path(true)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("in-flight collector crossed fence", err)
	}
}

func TestSnapshotCacheCapacityAndOversizeLimits(t *testing.T) {
	c, _ := cachedSnapshotFixture(t)
	for n := range 9 {
		path := filepath.Join(c.cache.root, cacheHash(fmt.Sprint(n)), "snapshot.json")
		if err := privateCacheDir(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(snapshotCacheMaxFile); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		at := time.Now().Add(time.Duration(n-9) * time.Hour)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.cache.cleanup(time.Now()); err != nil {
		t.Fatal(err)
	}
	var total int64
	entries, err := os.ReadDir(c.cache.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(c.cache.root, entry.Name(), "snapshot.json"))
		if err == nil {
			total += info.Size()
		}
	}
	if total > snapshotCacheMaxTotal {
		t.Fatalf("cache exceeds capacity: %d", total)
	}
	oversized := &Snapshot{entries: []snapshotEntry{{Issue: apiIssue{Number: 1, State: "open", Body: strings.Repeat("x", snapshotCacheMaxFile+1)}}}}
	if err := c.cache.publish(context.Background(), oversized, "0:0", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.cache.path(false)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("oversize snapshot published", err)
	}
}
