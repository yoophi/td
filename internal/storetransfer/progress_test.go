package storetransfer

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func progressFixture(t *testing.T) (*Checkpoint, string, *progressRecorder) {
	t.Helper()
	c := checkpointFixture(t)
	if err := c.BindActor("ses_original"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := SaveCheckpointNew(path, c, 0); err != nil {
		t.Fatal(err)
	}
	r, err := newProgressRecorder(path, c, 0)
	if err != nil {
		t.Fatal(err)
	}
	return c, path, r
}

func progressRequest() ghstore.MutationRequest {
	return ghstore.MutationRequest{Method: "POST", Path: "repos/owner/repo/issues/9/comments", PayloadSHA256: strings.Repeat("a", 64)}
}

func TestProgressAtomicallyKeepsOriginalCheckpointAndMutationIntents(t *testing.T) {
	c, path, recorder := progressFixture(t)
	before, _ := os.ReadFile(path)
	sequence, err := recorder.BeginMutation(progressRequest())
	if err != nil || sequence != 1 {
		t.Fatal(sequence, err)
	}
	data, _ := os.ReadFile(path + ".progress.json")
	started, err := ReadProgress(bytes.NewReader(data), c, 0)
	if err != nil || len(started.Mutations) != 1 || started.Mutations[0].State != "started" {
		t.Fatal("intent not durable before gh", started, err)
	}
	if err := recorder.ReceiveMutation(sequence, []byte(`{"id":12345,"body":"private response"}`)); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path + ".progress.json")
	received, err := ReadProgress(bytes.NewReader(data), c, 0)
	if err != nil || received.Mutations[0].State != "response_received" || received.Mutations[0].ResponseID != 12345 || strings.Contains(string(data), "private response") {
		t.Fatal("actual response lost or private body retained", received, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("progress replaced immutable checkpoint")
	}
	info, _ := os.Stat(path + ".progress.json")
	if info.Mode().Perm() != 0600 {
		t.Fatal("progress is not private", info.Mode())
	}
	if err := recorder.ReceiveMutation(sequence, []byte(`{"id":99999}`)); err == nil {
		t.Fatal("second response rewrote original evidence")
	}
	if _, err := newProgressRecorder(path, c, 0); !errors.Is(err, os.ErrExist) {
		t.Fatal("initialization overwrote existing progress", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 2 {
		t.Fatal("staging or lock file leaked", entries)
	}
}

func TestProgressRejectsStaleReadersRewriteCorruptionAndForeignCheckpoint(t *testing.T) {
	c, path, recorder := progressFixture(t)
	old := *recorder.current
	if _, err := recorder.BeginMutation(progressRequest()); err != nil {
		t.Fatal(err)
	}
	stale := old
	stale.Mutations = append(stale.Mutations, MutationReceipt{Sequence: 1, Request: progressRequest(), State: "started"})
	stale.SHA256 = stale.digest()
	if err := saveProgress(path+".progress.json", &stale, old.SHA256, c, 0); err == nil {
		t.Fatal("stale writer replaced newer state")
	}
	data, _ := os.ReadFile(path + ".progress.json")
	for _, change := range []string{"checksum", "actor", "path", "original_request", "unknown_state", "sequence", "removed_intent"} {
		t.Run(change, func(t *testing.T) {
			p, err := ReadProgress(bytes.NewReader(data), c, 0)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "checksum":
				p.SHA256 = strings.Repeat("b", 64)
			case "actor":
				p.ActorSessionID = "ses_someone_else"
			case "path":
				p.Mutations[0].Request.Path = "repos/foreign/repo/issues"
			case "original_request":
				p.Mutations[0].Request.PayloadSHA256 = strings.Repeat("b", 64)
			case "unknown_state":
				p.Mutations[0].State = "verified"
			case "sequence":
				p.Mutations[0].Sequence++
			case "removed_intent":
				p.Mutations = []MutationReceipt{}
			}
			if change != "checksum" {
				p.SHA256 = p.digest()
			}
			if err := saveProgress(path+".progress.json", p, recorder.current.SHA256, c, 0); err == nil {
				t.Fatal("invalid or rewritten evidence accepted", change)
			}
		})
	}
	foreign := checkpointFixture(t)
	if err := foreign.BindActor("ses_original"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProgress(bytes.NewReader(data), foreign, 0); err == nil {
		t.Fatal("foreign checkpoint accepted progress")
	}
	if _, err := ReadProgress(bytes.NewReader(append(data, []byte(" {}")...)), c, 0); err == nil {
		t.Fatal("trailing data accepted")
	}
	if _, err := ReadProgress(bytes.NewReader(data), c, 1); !errors.Is(err, ErrCapacity) {
		t.Fatal("capacity error lost", err)
	}
}

func TestProgressReceiptSaveFailureKeepsOriginalUncertainIntent(t *testing.T) {
	c, path, recorder := progressFixture(t)
	sequence, err := recorder.BeginMutation(progressRequest())
	if err != nil {
		t.Fatal(err)
	}
	recorder.persist = func(*Progress, string) error { return syscall.ENOSPC }
	if err := recorder.ReceiveMutation(sequence, []byte(`{"id":12345}`)); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal("original disk-full error hidden", err)
	}
	data, _ := os.ReadFile(path + ".progress.json")
	stored, err := ReadProgress(bytes.NewReader(data), c, 0)
	if err != nil || stored.Mutations[0].State != "started" || stored.Mutations[0].ResponseSHA256 != "" {
		t.Fatal("failed receipt became confirmed state", stored, err)
	}
	encoded, _ := json.Marshal(stored)
	if !bytes.Equal(data, encoded) {
		t.Fatal("failed receipt changed published bytes")
	}
}

func TestProgressReceivedBytesDoNotInventNativeIdentityOrLogicalProof(t *testing.T) {
	for _, response := range []string{`not JSON`, `{"id":-1}`, `{"number":-9}`, ``} {
		_, _, recorder := progressFixture(t)
		sequence, err := recorder.BeginMutation(progressRequest())
		if err != nil {
			t.Fatal(err)
		}
		if err := recorder.ReceiveMutation(sequence, []byte(response)); err != nil {
			t.Fatal("received transport data lost before logical decoding", err)
		}
		mutation := recorder.current.Mutations[0]
		if mutation.State != "response_received" || mutation.ResponseID != 0 || mutation.ResponseNumber != 0 || !validProgressHash(mutation.ResponseSHA256) {
			t.Fatal("malformed response gained native identity/verified proof", mutation)
		}
	}
}

func TestReopenedProgressRejectsBothUncertainAndReceivedRequestReplay(t *testing.T) {
	for _, received := range []bool{false, true} {
		c, path, original := progressFixture(t)
		sequence, err := original.BeginMutation(progressRequest())
		if err != nil {
			t.Fatal(err)
		}
		if received {
			if err := original.ReceiveMutation(sequence, []byte(`{"id":12345}`)); err != nil {
				t.Fatal(err)
			}
		}
		before, _ := os.ReadFile(path + ".progress.json")
		reopened, err := openProgressRecorder(path, c, 0)
		if err != nil || !reopened.ReplayGuardEnabled() {
			t.Fatal(reopened, err)
		}
		if _, err := reopened.BeginMutation(progressRequest()); err == nil {
			t.Fatal("reopened recorder repeated attempted request", received)
		}
		after, _ := os.ReadFile(path + ".progress.json")
		if !bytes.Equal(before, after) {
			t.Fatal("refused replay rewrote original evidence")
		}
		fresh := progressRequest()
		fresh.PayloadSHA256 = strings.Repeat("b", 64)
		if sequence, err := reopened.BeginMutation(fresh); err != nil || sequence != 2 {
			t.Fatal("new unattempted request not recorded", sequence, err)
		}
	}
}
