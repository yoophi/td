package storetransfer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
)

// ResumeGitHubDestination uses only the immutable original preparation. It
// never recollects a newer source or creates an identity from checkpoint data.
func ResumeGitHubDestination(ctx context.Context, checkpointPath string, maxBytes int64) (*GitHubTransferResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if checkpointPath == "" {
		return nil, fmt.Errorf("resume requires the original checkpoint file")
	}
	path, err := filepath.Abs(checkpointPath)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	checkpoint, err := ReadCheckpoint(file, maxBytes)
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	progress, err := openProgressRecorder(path, checkpoint, maxBytes)
	if err != nil {
		return nil, err
	}
	source, err := checkpoint.Source.decode()
	if err != nil {
		return nil, err
	}
	target, err := checkpoint.Target.decode()
	if err != nil {
		return nil, err
	}
	prepared, err := PrepareGitHubWorkspace(source, &InspectedTarget{Spec: checkpoint.Destination, Observation: target}, checkpoint.Bindings, maxBytes)
	if err != nil {
		return nil, err
	}
	client, err := ghstore.OpenRemote(ctx, checkpoint.Destination.ProjectDir, checkpoint.Destination.Remote)
	if err != nil {
		return nil, err
	}
	if client.Repository() != checkpoint.Destination.Namespace {
		return nil, fmt.Errorf("original checkpoint destination repository changed; no write attempted")
	}
	scope, err := ghcontext.Resolve(ctx, checkpoint.Destination.ProjectDir, client.Repository())
	if err != nil {
		return nil, err
	}
	state, err := scope.ReadExisting(ctx)
	if err != nil {
		return nil, fmt.Errorf("read actual resume actor; no identity created: %w", err)
	}
	if state.Session.ID != checkpoint.ActorSessionID {
		return nil, fmt.Errorf("actual resume session differs from original checkpoint actor; no write attempted")
	}
	return executePreparedGitHub(ctx, client, prepared, checkpoint, path, progress, true, maxBytes)
}
