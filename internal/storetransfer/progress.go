package storetransfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	"github.com/marcus/td/internal/ghstore"
)

// Progress is local controller evidence, not authorization or native proof.
// The original checkpoint never changes. A started request without a received
// response is uncertain; absence from a later listing cannot authorize replay.
type Progress struct {
	Version        int               `json:"version"`
	CheckpointSHA  string            `json:"checkpoint_sha256"`
	OperationID    string            `json:"operation_id"`
	ActorSessionID string            `json:"actor_session_id"`
	Mutations      []MutationReceipt `json:"mutations"`
	SHA256         string            `json:"sha256"`
}

type MutationReceipt struct {
	Sequence       int                     `json:"sequence"`
	Request        ghstore.MutationRequest `json:"request"`
	State          string                  `json:"state"`
	ResponseSHA256 string                  `json:"response_sha256,omitempty"`
	ResponseID     int64                   `json:"response_id,omitempty"`
	ResponseNumber int                     `json:"response_number,omitempty"`
}

func (p Progress) digest() string {
	p.SHA256 = ""
	data, _ := json.Marshal(p)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validProgressHash(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == sha256.Size && hex.EncodeToString(data) == value
}

func (p *Progress) validate(c *Checkpoint) error {
	if p == nil || c == nil || c.SHA256 != c.digest() || !validCheckpointActor(c.ActorSessionID) || p.Version != 1 || p.CheckpointSHA != c.SHA256 || p.OperationID != c.OperationID || p.ActorSessionID != c.ActorSessionID || p.Mutations == nil || p.SHA256 != p.digest() {
		return fmt.Errorf("invalid transfer progress checkpoint/actor/checksum")
	}
	for n, mutation := range p.Mutations {
		request := mutation.Request
		if mutation.Sequence != n+1 || !strings.HasPrefix(request.Path, "repos/"+c.Destination.Namespace+"/") || len(request.Path) > 4096 || strings.ContainsAny(request.Path, "\r\n\x00") || !validProgressHash(request.PayloadSHA256) || mutation.ResponseID < 0 || mutation.ResponseNumber < 0 {
			return fmt.Errorf("invalid transfer mutation %d identity/request", n+1)
		}
		switch request.Method {
		case "POST", "PATCH", "PUT", "DELETE":
		default:
			return fmt.Errorf("invalid transfer mutation method %q", request.Method)
		}
		switch mutation.State {
		case "started":
			if mutation.ResponseSHA256 != "" || mutation.ResponseID != 0 || mutation.ResponseNumber != 0 {
				return fmt.Errorf("started mutation cannot invent a received response")
			}
		case "response_received":
			if !validProgressHash(mutation.ResponseSHA256) {
				return fmt.Errorf("received mutation requires its actual response hash")
			}
		default:
			return fmt.Errorf("invalid transfer mutation state %q", mutation.State)
		}
	}
	return nil
}

// ReadProgress validates metadata against the immutable original checkpoint.
// It never accepts local response hashes as native outcome or review authority.
func ReadProgress(reader io.Reader, checkpoint *Checkpoint, maxBytes int64) (*Progress, error) {
	limit, err := byteLimit(maxBytes)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read transfer progress: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, ErrCapacity
	}
	var p Progress
	if err := strictJSON(data, &p); err != nil {
		return nil, fmt.Errorf("decode transfer progress: %w", err)
	}
	if err := p.validate(checkpoint); err != nil {
		return nil, err
	}
	return &p, nil
}

// Only append a started request, or acknowledge one exact started request.
// Existing input/response evidence can never be rewritten or removed.
func validateProgressTransition(before, after *Progress) error {
	if before.CheckpointSHA != after.CheckpointSHA || before.OperationID != after.OperationID || before.ActorSessionID != after.ActorSessionID || before.Version != after.Version {
		return fmt.Errorf("progress cannot replace its original checkpoint or actor")
	}
	if len(after.Mutations) == len(before.Mutations)+1 && reflect.DeepEqual(before.Mutations, after.Mutations[:len(before.Mutations)]) && after.Mutations[len(before.Mutations)].State == "started" {
		return nil
	}
	if len(after.Mutations) == len(before.Mutations) {
		changed := 0
		for i, previous := range before.Mutations {
			next := after.Mutations[i]
			if reflect.DeepEqual(previous, next) {
				continue
			}
			if previous.State != "started" || next.State != "response_received" || previous.Sequence != next.Sequence || previous.Request != next.Request {
				return fmt.Errorf("progress cannot rewrite mutation %d", i+1)
			}
			changed++
		}
		if changed == 1 {
			return nil
		}
	}
	return fmt.Errorf("progress requires one immutable intent or received response transition")
}

type progressRecorder struct {
	mu         sync.Mutex
	checkpoint *Checkpoint
	current    *Progress
	maxBytes   int64
	persist    func(*Progress, string) error
}

func newProgressRecorder(checkpointPath string, c *Checkpoint, maxBytes int64) (*progressRecorder, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	frozen, err := ReadCheckpoint(bytes.NewReader(data), maxBytes)
	if err != nil {
		return nil, err
	}
	p := &Progress{Version: 1, CheckpointSHA: frozen.SHA256, OperationID: frozen.OperationID, ActorSessionID: frozen.ActorSessionID, Mutations: []MutationReceipt{}}
	p.SHA256 = p.digest()
	r := &progressRecorder{checkpoint: frozen, current: p, maxBytes: maxBytes}
	r.persist = func(next *Progress, previousSHA string) error {
		return saveProgress(checkpointPath+".progress.json", next, previousSHA, frozen, maxBytes)
	}
	if err := r.persist(p, ""); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *progressRecorder) next() (*Progress, error) {
	data, err := json.Marshal(r.current)
	if err != nil {
		return nil, err
	}
	return ReadProgress(bytes.NewReader(data), r.checkpoint, r.maxBytes)
}

func (r *progressRecorder) save(next *Progress) error {
	next.SHA256 = next.digest()
	if err := r.persist(next, r.current.SHA256); err != nil {
		return err
	}
	r.current = next
	return nil
}

func (r *progressRecorder) BeginMutation(request ghstore.MutationRequest) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next, err := r.next()
	if err != nil {
		return 0, err
	}
	sequence := len(next.Mutations) + 1
	next.Mutations = append(next.Mutations, MutationReceipt{Sequence: sequence, Request: request, State: "started"})
	if err := r.save(next); err != nil {
		return 0, err
	}
	return sequence, nil
}

func (r *progressRecorder) ReceiveMutation(sequence int, response []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next, err := r.next()
	if err != nil {
		return err
	}
	if sequence <= 0 || sequence > len(next.Mutations) || next.Mutations[sequence-1].State != "started" {
		return fmt.Errorf("received result requires its exact original started mutation")
	}
	digest := sha256.Sum256(response)
	mutation := &next.Mutations[sequence-1]
	mutation.State, mutation.ResponseSHA256 = "response_received", hex.EncodeToString(digest[:])
	var identity struct {
		ID     int64 `json:"id"`
		Number int   `json:"number"`
	}
	// Malformed/native-body-less responses retain their bytes' hash, not an
	// invented ID. The ordinary higher-level decoder decides logical validity.
	if json.Unmarshal(response, &identity) == nil && identity.ID >= 0 && identity.Number >= 0 {
		mutation.ResponseID, mutation.ResponseNumber = identity.ID, identity.Number
	}
	return r.save(next)
}
