package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubJournalCLIRecoversCreatedActivityWithoutReplay(t *testing.T) {
	_, _, run := journalCLI(t)
	useNativeCommentOrderFixture(t)
	execute := func(args ...string) string {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
		return out
	}
	execute("create", "Activity recovery target")
	t.Setenv("TD_GH_JOURNAL_FAIL", "receipt-denied")
	if out, stderr, err := run("log", "--issue", "gh-6", "Saved exactly once", "--json"); err == nil || !strings.Contains(out+stderr, "HTTP 403 journal receipt denied") {
		t.Fatal(out, stderr, err)
	}
	t.Setenv("TD_GH_JOURNAL_FAIL", "")
	var rows []githubActionHistoryRow
	if err := json.Unmarshal([]byte(execute("last", "--n", "1")), &rows); err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	id := rows[0].ID
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	state, journal := os.Getenv("TD_SCHEDULE_STATE"), os.Getenv("TD_GH_FIXTURE_JOURNAL")
	before := read(state)
	var recovered ghstore.ActionReconcileResult
	if err := json.Unmarshal([]byte(execute("undo", "--reconcile", id, "--apply")), &recovered); err != nil || !recovered.Applied || recovered.ForwardOutcome == nil || recovered.ForwardOutcome.TargetWriteVerified {
		t.Fatal(recovered, err)
	}
	if !bytes.Equal(before, read(state)) {
		t.Fatal("receipt recovery replayed a native write")
	}
	if err := json.Unmarshal([]byte(execute("last", "--n", "1")), &rows); err != nil || rows[0].CommentObservation == nil {
		t.Fatal("last hid observer provenance", rows, err)
	}
	saved := read(journal)
	execute("undo", "--reconcile", id, "--apply")
	if !bytes.Equal(before, read(state)) || !bytes.Equal(saved, read(journal)) {
		t.Fatal("completed confirmation wrote remote state")
	}
	execute("undo")
	var remote struct{ Comments []any }
	if err := json.Unmarshal(read(state), &remote); err != nil || len(remote.Comments) != 0 {
		t.Fatal("comment inverse was not applied", remote, err)
	}
}

func useNativeCommentOrderFixture(t *testing.T) {
	t.Helper()
	// Older fixture comments use a separate ID/clock namespace. This recovery
	// test needs native comments allocated after their actual journal intent.
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(gh)
	if err != nil {
		t.Fatal(err)
	}
	needle := "state['comments'].append(data);n="
	replacement := "data.update(id=20000+len(state['comments']),created_at=fixture_tick(journal['comments'][-1]['created_at']),updated_at=fixture_tick(journal['comments'][-1]['created_at']));issues[path.split('/issues/')[1].split('/')[0]]['updated_at']=data['updated_at'];state['comments'].append(data);n="
	if !bytes.Contains(script, []byte(needle)) {
		t.Fatal("native comment fixture seam missing")
	}
	if err := os.WriteFile(gh, bytes.Replace(script, []byte(needle), []byte(replacement), 1), 0755); err != nil {
		t.Fatal(err)
	}
}
