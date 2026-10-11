package ghstore

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/marcus/td/internal/config"
)

// Opt-in GET-only integration: no repository, task, receipt or access writes.
// Synthetic proof inputs are compared with real native event collections;
// this is not an assertion that a native compound command was executed.
func TestNativeEventObservationLiveReadOnly(t *testing.T) {
	dir := os.Getenv("TD_GH73_NATIVE_DIR")
	if dir == "" {
		t.Skip("explicit native sample directory required")
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHub == nil || cfg.GitHub.Repo != "yoophi/td-sample" {
		t.Fatal("requires the explicitly named sample repository")
	}
	ctx := context.Background()
	client, err := Open(ctx, dir, cfg.GitHub)
	if err != nil {
		t.Fatal(err)
	}
	run := client.run
	calls := 0
	client.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		i := slices.Index(args, "--method")
		if i < 0 || i+1 >= len(args) || args[i+1] != "GET" || len(payload) != 0 {
			t.Fatal("native test forbids remote writes", args)
		}
		calls++
		return run(ctx, dir, payload, args...)
	}
	root := &ActionJournalRecord{ActionJournal: ActionJournal{Group: &ActionGroup{Command: "update-comments", UpdateComments: &ActionUpdateComments{}}}}
	for _, id := range []string{"gh-135", "gh-138"} {
		events, err := client.readStateEvents(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		root.Group.Members = append(root.Group.Members, ActionGroupMember{IssueID: id})
		root.Group.UpdateComments.Inputs = append(root.Group.UpdateComments.Inputs, ActionUpdateActivity{Update: &ActionUpdateIntent{NativeEvents: &events}})
	}
	verify := func(ctx context.Context) {
		t.Helper()
		if err := client.verifyPairedGroupEvents(ctx, root); err != nil {
			t.Fatal(err)
		}
	}
	calls = 0
	verify(ctx)
	verify(ctx)
	before := calls
	observation := withNativeEventObservation(ctx)
	calls = 0
	verify(observation)
	verify(observation)
	after := calls
	calls = 0
	verify(withNativeEventObservation(observation))
	next := calls
	if before != 4 || after != 2 || next != 2 {
		t.Fatal("unexpected live boundary counts", before, after, next)
	}
	wrong := append(slices.Clone(*root.Group.UpdateComments.Inputs[0].Update.NativeEvents), nativeStateEvent{ID: 9223372036854775807, Event: "closed"})
	root.Group.UpdateComments.Inputs[0].Update.NativeEvents = &wrong
	if err := client.verifyPairedGroupEvents(observation, root); err == nil {
		t.Fatal("memo skipped independent mismatched proof comparison")
	}
	t.Logf("GET-only native proof reads: before=%d after=%d next-boundary=%d; differing proof rejected; remote writes=0", before, after, next)
}
