package ghstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const snapshotCacheTTL = 30 * time.Second
const snapshotCacheMaxFile = 32 << 20
const snapshotCacheMaxTotal = 256 << 20
const snapshotCacheMaxAge = 7 * 24 * time.Hour

var snapshotFlights singleflight.Group

type freshSnapshotKey struct{}

// WithFreshSnapshot bypasses reusable observations. It does not retry failed writes.
func WithFreshSnapshot(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshSnapshotKey{}, true)
}

type snapshotCache struct {
	root, repo, credential string
	warning                sync.Once
}

type cacheEnvelope struct {
	Version           int             `json:"version"`
	Host              string          `json:"host"`
	Repository        string          `json:"repository"`
	CredentialContext string          `json:"credential_context"`
	Scope             string          `json:"scope"`
	CollectedAt       time.Time       `json:"collected_at"`
	Complete          bool            `json:"complete"`
	Generation        string          `json:"generation"`
	ChangeToken       string          `json:"change_token,omitempty"`
	Entries           []snapshotEntry `json:"entries"`
	// Full reconciliation is used initially; no incremental cursor is implied.
	Cursor string `json:"cursor,omitempty"`
}

func cacheHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func newSnapshotCache(repo string, token []byte) *snapshotCache {
	// gh auth token resolves environment overrides and the actual active credential.
	// Only its digest survives this function; token output/errors are never logged.
	if os.Getenv("TD_GH_CACHE") == "off" || len(strings.TrimSpace(string(token))) == 0 {
		return nil
	}
	root, err := os.UserCacheDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Warning: GitHub snapshot cache unavailable; using remote reads")
		return nil
	}
	return &snapshotCache{root: filepath.Join(root, "td", "gh-issue", "v1"), repo: strings.ToLower(repo), credential: cacheHash("td-credential-v1:" + strings.TrimSpace(string(token)))}
}

func (s *snapshotCache) scope(history bool) string {
	if history {
		return "full-history"
	}
	return "issues-only"
}
func (s *snapshotCache) path(history bool) string {
	key := cacheHash("1\ngithub.com\n" + s.repo + "\n" + s.credential + "\n" + s.scope(history))
	return filepath.Join(s.root, key, "snapshot.json")
}
func (s *snapshotCache) warn(err error) {
	if err != nil {
		s.warning.Do(func() {
			fmt.Fprintln(os.Stderr, "Warning: GitHub snapshot cache unavailable; using remote reads:", err)
		})
	}
}

func privateCacheDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cache path is not a private directory")
	}
	return os.Chmod(path, 0700)
}

// withLock holds a short publication lock. No network operation runs in fn.
func (s *snapshotCache) withLock(ctx context.Context, fn func() error) error {
	for _, dir := range []string{filepath.Dir(filepath.Dir(s.root)), filepath.Dir(s.root), s.root} {
		if err := privateCacheDir(dir); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(filepath.Join(s.root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := os.Lstat(file.Name())
	if err != nil {
		return err
	}
	actual, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !os.SameFile(info, actual) {
		return fmt.Errorf("invalid cache lock file")
	}
	if err := lockSnapshotFile(ctx, file); err != nil {
		return err
	}
	defer unlockSnapshotFile(file)
	return fn()
}

func atomicCacheFile(path string, data []byte) error {
	if err := privateCacheDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (s *snapshotCache) generation() (string, error) {
	values := []string{}
	for _, path := range []string{filepath.Join(s.root, ".generation"), filepath.Join(s.root, "repositories", cacheHash(s.repo)+".generation")} {
		value, err := readCacheFile(path)
		if errors.Is(err, os.ErrNotExist) {
			values = append(values, "0")
			continue
		}
		if err != nil {
			return "", err
		}
		values = append(values, string(value))
	}
	return strings.Join(values, ":"), nil
}

func (s *snapshotCache) invalidate(ctx context.Context, all bool) error {
	return s.withLock(ctx, func() error {
		path := filepath.Join(s.root, "repositories", cacheHash(s.repo)+".generation")
		if all {
			path = filepath.Join(s.root, ".generation")
		}
		if err := atomicCacheFile(path, []byte(rand.Text())); err != nil {
			return err
		}
		// Physical removal is cleanup; the generation change is the correctness gate.
		entries, err := os.ReadDir(s.root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || !validCacheKey(entry.Name()) {
				continue
			}
			file := filepath.Join(s.root, entry.Name(), "snapshot.json")
			if all {
				_ = os.Remove(file)
				continue
			}
			data, err := readCacheFile(file)
			if err != nil {
				continue
			}
			var envelope cacheEnvelope
			if json.Unmarshal(data, &envelope) == nil && envelope.Repository == s.repo {
				_ = os.Remove(file)
			}
		}
		return nil
	})
}

func validCacheKey(name string) bool {
	if len(name) != 64 {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}

func readCacheFile(path string) ([]byte, error) {
	directory, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if !directory.IsDir() || directory.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("invalid cache directory permissions or type")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > snapshotCacheMaxFile {
		return nil, fmt.Errorf("invalid cache file permissions, type or size")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	actual, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, actual) {
		return nil, fmt.Errorf("cache file changed during open")
	}
	data, err := io.ReadAll(io.LimitReader(f, snapshotCacheMaxFile+1))
	if err == nil && len(data) > snapshotCacheMaxFile {
		return nil, fmt.Errorf("cache file exceeds size limit")
	}
	return data, err
}

func (s *snapshotCache) load(c *Client, history bool, generation string, now time.Time) (*Snapshot, error) {
	data, err := readCacheFile(s.path(history))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var envelope cacheEnvelope
	if json.Unmarshal(data, &envelope) != nil {
		return nil, nil
	}
	if envelope.Version != 1 || envelope.Host != "github.com" || envelope.Repository != s.repo || envelope.CredentialContext != s.credential || envelope.Scope != s.scope(history) || !envelope.Complete || envelope.Generation != generation || envelope.CollectedAt.IsZero() || envelope.CollectedAt.After(now) || now.Sub(envelope.CollectedAt) >= snapshotCacheTTL || envelope.Entries == nil {
		return nil, nil
	}
	issues, comments := []apiIssue{}, []apiComment{}
	for _, entry := range envelope.Entries {
		issues = append(issues, entry.Issue)
		comments = append(comments, entry.Comments...)
	}
	snapshot, err := c.assembleSnapshot([][]apiIssue{issues}, [][]apiComment{comments}, history)
	if err != nil {
		return nil, nil
	}
	snapshot.observedAt = envelope.CollectedAt
	if history {
		token, err := snapshot.ChangeToken()
		if err != nil || token != envelope.ChangeToken {
			return nil, nil
		}
	}
	_ = os.Chtimes(s.path(history), now, now)
	return snapshot, nil
}

func (s *snapshotCache) publish(ctx context.Context, snapshot *Snapshot, generation string, collectedAt time.Time) error {
	envelope := cacheEnvelope{Version: 1, Host: "github.com", Repository: s.repo, CredentialContext: s.credential, Scope: s.scope(snapshot.fullHistory), Complete: true, Generation: generation, CollectedAt: collectedAt, Entries: snapshot.entries}
	if snapshot.fullHistory {
		token, err := snapshot.ChangeToken()
		if err != nil {
			return err
		}
		envelope.ChangeToken = token
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if len(data) > snapshotCacheMaxFile {
		return nil
	}
	return s.withLock(ctx, func() error {
		current, err := s.generation()
		if err != nil {
			return err
		}
		if current != generation {
			return nil
		}
		// A slower process must not replace an observation collected later.
		if old, err := readCacheFile(s.path(snapshot.fullHistory)); err == nil {
			var previous cacheEnvelope
			if json.Unmarshal(old, &previous) == nil && previous.Generation == generation && previous.CollectedAt.After(collectedAt) {
				return nil
			}
		}
		if err := atomicCacheFile(s.path(snapshot.fullHistory), data); err != nil {
			return err
		}
		return s.cleanup(time.Now())
	})
}

func (s *snapshotCache) cleanup(now time.Time) error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	type cachedFile struct {
		path string
		info os.FileInfo
	}
	files := []cachedFile{}
	var total int64
	for _, entry := range entries {
		if !entry.IsDir() || !validCacheKey(entry.Name()) {
			continue
		}
		path := filepath.Join(s.root, entry.Name(), "snapshot.json")
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if now.Sub(info.ModTime()) > snapshotCacheMaxAge {
			_ = os.Remove(path)
			continue
		}
		total += info.Size()
		files = append(files, cachedFile{path, info})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].info.ModTime().Before(files[j].info.ModTime()) })
	for _, file := range files {
		if total <= snapshotCacheMaxTotal {
			break
		}
		if err := os.Remove(file.path); err == nil {
			total -= file.info.Size()
		}
	}
	return nil
}

// ReadSnapshot reuses only complete, validated observations within a 30-second
// TTL. Repository/auth preflight still happens in Open; mutations use fresh GETs.
func (c *Client) ReadSnapshot(ctx context.Context, history bool) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.cache == nil {
		return c.readSnapshotRemote(ctx, history)
	}
	cache := c.cache
	generation := ""
	var cached *Snapshot
	err := cache.withLock(ctx, func() error {
		var err error
		generation, err = cache.generation()
		if err != nil {
			return err
		}
		if ctx.Value(freshSnapshotKey{}) != true {
			cached, err = cache.load(c, history, generation, time.Now())
		}
		return err
	})
	if err != nil {
		cache.warn(err)
		return c.readSnapshotRemote(ctx, history)
	}
	if cached != nil {
		return cached, nil
	}
	// Generation and refresh intent separate in-flight reads across invalidation.
	key := cache.path(history) + ":" + generation
	if ctx.Value(freshSnapshotKey{}) == true {
		key += ":fresh"
	}
	result := snapshotFlights.DoChan(key, func() (any, error) {
		snapshot, err := c.readSnapshotRemote(ctx, history)
		if err != nil {
			return nil, err
		}
		cache.warn(cache.publish(ctx, snapshot, generation, snapshot.ObservedAt()))
		return snapshot, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return nil, value.Err
		}
		return value.Val.(*Snapshot), nil
	}
}

// CacheStatus describes reusable observations without exposing credentials or bodies.
type CacheStatus struct {
	Enabled    bool               `json:"enabled"`
	Repository string             `json:"repository"`
	TTLSeconds int                `json:"ttl_seconds"`
	Snapshots  []CacheScopeStatus `json:"snapshots"`
}
type CacheScopeStatus struct {
	Scope       string     `json:"scope"`
	Path        string     `json:"path"`
	State       string     `json:"state"`
	CollectedAt *time.Time `json:"collected_at,omitempty"`
	Bytes       int        `json:"bytes"`
}

func (c *Client) CacheStatus(ctx context.Context) (*CacheStatus, error) {
	status := &CacheStatus{Enabled: c.cache != nil, Repository: c.repo, TTLSeconds: int(snapshotCacheTTL / time.Second), Snapshots: []CacheScopeStatus{}}
	if c.cache == nil {
		return status, nil
	}
	cache := c.cache
	err := cache.withLock(ctx, func() error {
		generation, err := cache.generation()
		if err != nil {
			return err
		}
		for _, history := range []bool{false, true} {
			item := CacheScopeStatus{Scope: cache.scope(history), Path: cache.path(history), State: "missing"}
			data, err := readCacheFile(item.Path)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				item.State = "invalid"
			}
			if err == nil {
				item.State = "expired-or-invalid"
				item.Bytes = len(data)
				var envelope cacheEnvelope
				if json.Unmarshal(data, &envelope) == nil && !envelope.CollectedAt.IsZero() {
					item.CollectedAt = &envelope.CollectedAt
				}
				snapshot, err := cache.load(c, history, generation, time.Now())
				if err == nil && snapshot != nil {
					item.State = "fresh"
				}
			}
			status.Snapshots = append(status.Snapshots, item)
		}
		return nil
	})
	return status, err
}

// ClearCache invalidates all credential/scope snapshots for this repository.
func (c *Client) ClearCache(ctx context.Context) error {
	if c.cache == nil {
		return fmt.Errorf("GitHub snapshot cache is disabled for this authentication context")
	}
	return c.cache.invalidate(ctx, false)
}

// ClearAllSnapshotCaches deletes regenerable snapshots only, retaining generation
// fences so a pre-clear in-flight collector cannot republish deleted data.
func ClearAllSnapshotCaches(ctx context.Context) error {
	root, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	cache := &snapshotCache{root: filepath.Join(root, "td", "gh-issue", "v1")}
	return cache.invalidate(ctx, true)
}
