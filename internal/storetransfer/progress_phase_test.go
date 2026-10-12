package storetransfer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func phaseFixtureMappings(t *testing.T, c *Checkpoint) (map[string]string, map[string]string) {
	t.Helper()
	source, err := c.Source.decode()
	if err != nil {
		t.Fatal(err)
	}
	preview, err := source.Preview()
	if err != nil {
		t.Fatal(err)
	}
	tasks, all := map[string]string{}, map[string]string{}
	for n, identity := range preview.Identities {
		prefix := map[string]string{"task": "gh-", "note": "nt-gh-", "board": "bd-gh-"}[identity.Kind]
		all[identity.Key] = fmt.Sprintf("%s%d", prefix, n+1)
		if identity.Kind == "task" {
			tasks[identity.Key] = all[identity.Key]
		}
	}
	return tasks, all
}

func TestPhaseReceiptsAppendPrivatelyWithoutRewritingMutationOrCheckpoint(t *testing.T) {
	c, path, recorder := progressFixture(t)
	tasks, all := phaseFixtureMappings(t, c)
	before, _ := os.ReadFile(path)
	sequence, err := recorder.BeginMutation(progressRequest())
	if err != nil {
		t.Fatal(err)
	}
	phases := []PhaseReceipt{
		{Name: "source_archive", NativeRootID: "ghc-101", Mappings: c.Bindings},
		{Name: "tasks", Mappings: tasks},
		{Name: "entities", Mappings: all},
		{Name: "completion_archive", NativeRootID: "ghc-202", Mappings: all},
	}
	for _, phase := range phases {
		if err := recorder.recordPhase(phase); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(path + ".progress.json")
	stored, err := ReadProgress(bytes.NewReader(data), c, 0)
	if err != nil || len(stored.Phases) != 4 || stored.Mutations[sequence-1].State != "started" {
		t.Fatal("phase invented response receipt", stored, err)
	}
	for _, phase := range phases {
		if err := recorder.recordPhase(phase); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := os.ReadFile(path + ".progress.json")
	checkpointAfter, _ := os.ReadFile(path)
	if !bytes.Equal(data, after) || !bytes.Equal(before, checkpointAfter) {
		t.Fatal("repeat rewrote durable evidence")
	}
	if err := recorder.ReceiveMutation(sequence, []byte(`{"id":12345}`)); err != nil {
		t.Fatal("phase blocked actual original transport receipt", err)
	}
	all["foreign-key"] = "gh-999"
	if _, present := recorder.current.Phases[2].Mappings["foreign-key"]; present {
		t.Fatal("caller mutated frozen phase mappings")
	}
}

func TestPhaseReceiptDiskFullPreservesStopPointAndResumesWithoutInventingResponse(t *testing.T) {
	c, path, recorder := progressFixture(t)
	if _, err := recorder.BeginMutation(progressRequest()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path + ".progress.json")
	persist := recorder.persist
	recorder.persist = func(*Progress, string) error { return syscall.ENOSPC }
	phase := PhaseReceipt{Name: "source_archive", NativeRootID: "ghc-101", Mappings: c.Bindings}
	if err := recorder.recordPhase(phase); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal("original capacity error lost", err)
	}
	after, _ := os.ReadFile(path + ".progress.json")
	if !bytes.Equal(before, after) || len(recorder.current.Phases) != 0 {
		t.Fatal("failed phase publication advanced stop point")
	}
	recorder.persist = persist
	reopened, err := openProgressRecorder(path, c, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.recordPhase(phase); err != nil {
		t.Fatal(err)
	}
	if reopened.current.Mutations[0].State != "started" {
		t.Fatal("observed phase upgraded uncertain transport response")
	}
	if _, err := reopened.BeginMutation(progressRequest()); err == nil {
		t.Fatal("phase allowed request replay")
	}
}

func TestPhaseReceiptsRejectOrderChangedMappingsProofAndCombinedTransitions(t *testing.T) {
	c, _, r := progressFixture(t)
	tasks, _ := phaseFixtureMappings(t, c)
	if err := r.recordPhase(PhaseReceipt{Name: "tasks", Mappings: tasks}); err == nil {
		t.Fatal("out-of-order phase accepted")
	}
	first := PhaseReceipt{Name: "source_archive", NativeRootID: "ghc-101", Mappings: c.Bindings}
	if err := r.recordPhase(first); err != nil {
		t.Fatal(err)
	}
	first.NativeRootID = "ghc-102"
	if err := r.recordPhase(first); err == nil {
		t.Fatal("original phase overwritten")
	}
	for _, phase := range []PhaseReceipt{
		{Name: "tasks", Mappings: map[string]string{"foreign-key": "gh-1"}},
		{Name: "tasks", Mappings: map[string]string{"": "gh-1"}},
		{Name: "tasks", Mappings: tasks, EntityProofs: map[string]string{"foreign-op": "verified"}},
	} {
		if err := r.recordPhase(phase); err == nil {
			t.Fatal("invalid phase accepted", phase)
		}
	}
	next, err := r.next()
	if err != nil {
		t.Fatal(err)
	}
	next.Phases = append(next.Phases, PhaseReceipt{Name: "tasks", Mappings: tasks})
	next.Mutations = append(next.Mutations, MutationReceipt{Sequence: 1, Request: progressRequest(), State: "started"})
	if err := validateProgressTransition(r.current, next); err == nil {
		t.Fatal("phase publication combined with uncertain request transition")
	}
}
