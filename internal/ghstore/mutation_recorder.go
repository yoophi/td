package ghstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// MutationRequest identifies exact gh input without retaining private bodies or
// credentials. It is a controller write intent, not GitHub execution authority.
type MutationRequest struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	PayloadSHA256 string `json:"payload_sha256"`
	OperationID   string `json:"operation_id,omitempty"`
	EntityKind    string `json:"entity_kind,omitempty"`
}

// MutationRecorder persists an intent before gh starts and the received result
// before the caller proceeds. A received response is not a verified logical
// result: native journal readback and conflict checks remain mandatory.
type MutationRecorder interface {
	BeginMutation(MutationRequest) (int, error)
	ReceiveMutation(int, []byte) error
}

// MutationResumeRecorder atomically refuses already attempted requests when
// BeginMutation runs. Ordinary recording alone cannot make absence replay-safe.
type MutationResumeRecorder interface {
	MutationRecorder
	ReplayGuardEnabled() bool
}

type mutationRecorderKey struct{}

// WithMutationRecorder is used by explicit transfer execution only. Ordinary
// commands retain their existing native journals and request behavior.
func WithMutationRecorder(ctx context.Context, recorder MutationRecorder) context.Context {
	return context.WithValue(ctx, mutationRecorderKey{}, recorder)
}

func mutationRequest(method, path string, payload []byte) MutationRequest {
	digest := sha256.Sum256(payload)
	request := MutationRequest{Method: method, Path: path, PayloadSHA256: hex.EncodeToString(digest[:])}
	var input struct{ Body string }
	if json.Unmarshal(payload, &input) != nil || input.Body == "" {
		return request
	}
	if _, meta, managed, err := decodeBody(input.Body); err == nil && managed {
		request.OperationID, request.EntityKind = meta.OperationID, meta.EntityKind
		if request.EntityKind == "" {
			request.EntityKind = "issue"
		}
		return request
	}
	// Native command roots expose their operation even when the immutable
	// input/result is archived. No caller source text becomes authorization.
	if index := strings.Index(input.Body, actionJournalPrefix); index >= 0 {
		block := input.Body[index:]
		if strings.HasPrefix(block, actionCompressedJournalStart) {
			decoded, err := decodeJournalEnvelope(block)
			if err != nil {
				return request
			}
			block = decoded
		}
		if start := strings.IndexByte(block, '\n'); start >= 0 && strings.HasSuffix(block, markerEnd) {
			var header struct {
				OperationID string `json:"operation_id"`
				Journal     struct {
					OperationID string `json:"operation_id"`
				} `json:"journal"`
			}
			if json.Unmarshal([]byte(strings.TrimSuffix(block[start+1:], markerEnd)), &header) == nil {
				request.OperationID = header.OperationID
				if request.OperationID == "" {
					request.OperationID = header.Journal.OperationID
				}
			}
		}
	}
	return request
}
