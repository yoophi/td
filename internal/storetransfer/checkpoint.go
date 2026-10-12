package storetransfer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// Checkpoint preserves the exact source/target preparation before remote writes.
// Stored mappings are observations, not permission to skip native verification.
// This file is local controller state and must not be published as provenance.
type Checkpoint struct {
	Version        int               `json:"version"`
	OperationID    string            `json:"operation_id"`
	ActorSessionID string            `json:"actor_session_id,omitempty"`
	Source         Envelope          `json:"source"`
	Target         Envelope          `json:"target"`
	Destination    TargetSpec        `json:"destination"`
	Bindings       map[string]string `json:"bindings"`
	SHA256         string            `json:"sha256"`
}

// BindActor records the actual device-local execution identity before initial
// publication. The controller never derives this from source actors/history.
func (c *Checkpoint) BindActor(actor string) error {
	if c == nil || c.SHA256 != c.digest() || c.ActorSessionID != "" || !validCheckpointActor(actor) {
		return fmt.Errorf("immutable unbound checkpoint and actual actor required")
	}

	c.ActorSessionID = actor
	c.SHA256 = c.digest()
	return nil
}

func validCheckpointActor(actor string) bool {
	if actor == "" || len(actor) > 128 {
		return false
	}
	for _, char := range actor {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_'
		if !allowed {
			return false
		}
	}
	return true
}

func NewCheckpoint(source *Source, target *InspectedTarget, bindings map[string]string, maxBytes int64) (*Checkpoint, error) {
	if _, err := PrepareGitHubWorkspace(source, target, bindings, maxBytes); err != nil {
		return nil, err
	}
	c := &Checkpoint{Version: 1, OperationID: "td-workspace-" + rand.Text(), Source: source.Envelope, Target: target.Observation.Envelope, Destination: target.Spec, Bindings: map[string]string{}}
	for key, id := range bindings {
		c.Bindings[key] = id
	}
	c.SHA256 = c.digest()
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	return ReadCheckpoint(bytes.NewReader(data), maxBytes)
}

func (c Checkpoint) digest() string {
	c.SHA256 = ""
	data, _ := json.Marshal(c)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// ReadCheckpoint validates immutable input checksums and repeats pure preflight.
// It neither opens a destination nor trusts a stored claimed-success result.
func ReadCheckpoint(reader io.Reader, maxBytes int64) (*Checkpoint, error) {
	limit, err := byteLimit(maxBytes)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read checkpoint: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w (%d bytes)", ErrCapacity, limit)
	}
	var c Checkpoint
	if err := strictJSON(data, &c); err != nil {
		return nil, fmt.Errorf("decode store-transfer checkpoint: %w", err)
	}
	if c.Version != 1 || !checkpointOperationID.MatchString(c.OperationID) || c.ActorSessionID != "" && !validCheckpointActor(c.ActorSessionID) || c.SHA256 != c.digest() {
		return nil, fmt.Errorf("invalid store-transfer checkpoint version/identity/checksum")
	}
	source, err := c.Source.decode()
	if err != nil {
		return nil, err
	}
	target, err := c.Target.decode()
	if err != nil {
		return nil, err
	}
	if _, err := PrepareGitHubWorkspace(source, &InspectedTarget{Spec: c.Destination, Observation: target}, c.Bindings, maxBytes); err != nil {
		return nil, err
	}
	return &c, nil
}

var checkpointOperationID = regexp.MustCompile(`^td-workspace-[A-Z2-7]{26}$`)

func (c *Checkpoint) TaskOperationID() (string, error) {
	if c == nil || !checkpointOperationID.MatchString(c.OperationID) || c.SHA256 != c.digest() {
		return "", fmt.Errorf("valid immutable checkpoint required")
	}
	return c.OperationID + "-tasks", nil
}

// EntityOperationID derives a stable note/board creation identity from this
// immutable execution and an exact qualified source key. It is not a receipt.
func (c *Checkpoint) EntityOperationID(sourceKey string) (string, error) {
	if c == nil || !checkpointOperationID.MatchString(c.OperationID) || c.SHA256 != c.digest() {
		return "", fmt.Errorf("valid immutable checkpoint required")
	}
	source, err := c.Source.decode()
	if err != nil {
		return "", err
	}
	preview, err := source.Preview()
	if err != nil {
		return "", err
	}
	for _, identity := range preview.Identities {
		if identity.Key == sourceKey && (identity.Kind == "note" || identity.Kind == "board") {
			hash := sha256.Sum256([]byte(identity.Kind + "\x00" + sourceKey))
			return c.OperationID + "-" + hex.EncodeToString(hash[:]), nil
		}
	}
	return "", fmt.Errorf("checkpoint source key %q is not a note or board", sourceKey)
}

// SaveCheckpointNew syncs a private sibling file and publishes with a hard link.
// An existing checkpoint is never overwritten; ENOSPC and IO errors retain their
// original cause. Directory-sync errors explicitly identify possible publication.
func SaveCheckpointNew(path string, c *Checkpoint, maxBytes int64) error {
	if c == nil {
		return fmt.Errorf("checkpoint required")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err := ReadCheckpoint(bytes.NewReader(data), maxBytes); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".td-transfer-checkpoint-*")
	if err != nil {
		return fmt.Errorf("create private checkpoint: %w", err)
	}
	name := file.Name()
	defer func() { _ = os.Remove(name); _ = file.Close() }()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync checkpoint: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(name, path); err != nil {
		return fmt.Errorf("publish checkpoint without overwrite: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("checkpoint published; inspect before retry: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("checkpoint may be published; directory sync failed; inspect before retry: %w", err)
	}
	return nil
}
