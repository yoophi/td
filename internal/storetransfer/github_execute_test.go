package storetransfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGitHubExecutionTaskAdoptionRequiresGitHubAccessBeforeCheckpoint(t *testing.T) {
	source, _ := githubEntitySourceFixture(t)
	targetSource := githubTaskInputFixture(t)
	target := gitHubPrepareTarget(t, targetSource.GitHub)
	preview, err := source.Preview()
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]string{}
	for _, identity := range preview.Identities {
		if identity.ID == "td-parent" {
			bindings[identity.Key] = "gh-2"
		}
	}
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	t.Setenv("PATH", t.TempDir()) // Valid adoption still requires gh/git access.
	result, err := ExecuteGitHubDestination(context.Background(), source, target, bindings, path, 0)
	if err == nil || result != nil {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inaccessible destination created checkpoint", err)
	}
}

func TestGitHubExecutionRequiresCheckpointAndHonorsCancellationBeforeAccess(t *testing.T) {
	if _, err := ExecuteGitHubDestination(context.Background(), nil, nil, nil, "", 0); err == nil {
		t.Fatal("missing checkpoint accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ExecuteGitHubDestination(ctx, nil, nil, nil, "checkpoint", 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRetainedNativeComparisonRejectsChangedAdoptedBoard(t *testing.T) {
	source, _ := githubEntitySourceFixture(t)
	target := gitHubPrepareTarget(t, githubTaskInputFixture(t).GitHub)
	// The observation is already fully validated; construct a private retained
	// selection to isolate membership/body/clock comparison from title guessing.
	p := &PreparedGitHubWorkspace{source: source, target: target.Observation, identities: &IdentityPlan{Assignments: []Assignment{{Kind: "task", Action: "adopt", TargetID: "gh-2"}}}}
	if err := p.checkRetainedNative(target.Observation.GitHub, false); err != nil {
		t.Fatal(err)
	}
	changed := *target.Observation.GitHub
	changed.Native = append(changed.Native[:0:0], changed.Native...)
	for i := range changed.Native {
		if changed.Native[i].Number == 2 {
			changed.Native[i].Title = "changed"
		}
	}
	if err := p.checkRetainedNative(&changed, false); err == nil {
		t.Fatal("retained destination edit accepted")
	}
}
