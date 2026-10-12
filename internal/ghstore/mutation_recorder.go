package ghstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// MutationRequest identifies exact gh input without retaining private bodies or
// credentials. It is a controller write intent, not GitHub execution authority.
type MutationRequest struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	PayloadSHA256 string `json:"payload_sha256"`
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
	return MutationRequest{Method: method, Path: path, PayloadSHA256: hex.EncodeToString(digest[:])}
}
