package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/ghstore"
)

func TestGitHubJournalCLIDependencyBatchSingleEffect(t *testing.T) {
	for _, verb := range []string{"add", "rm"} {
		for _, failure := range []string{"", "target-lost", "receipt-denied"} {
			t.Run(verb+"/"+failure, func(t *testing.T) {
				_, _, run := journalCLI(t)
				execute := func(args ...string) string {
					t.Helper()
					out, stderr, err := run(append(args, "--json")...)
					if err != nil {
						t.Fatal(args, out, stderr, err)
					}
					return out
				}
				read := func() []byte {
					t.Helper()
					data, err := os.ReadFile(os.Getenv("TD_SCHEDULE_STATE"))
					if err != nil {
						t.Fatal(err)
					}
					return data
				}
				execute("create", "Batch dependency source")
				if verb == "rm" {
					execute("dep", "add", "gh-6", "gh-1", "gh-2")
				}
				before, journalCount := read(), len(fixtureJournals(t))
				t.Setenv("TD_GH_JOURNAL_FAIL", failure)
				out, stderr, err := run("dep", verb, "gh-6", "gh-1", "gh-2", "--json")
				if (err != nil) != (failure != "") {
					t.Fatal(out, stderr, err)
				}
				t.Setenv("TD_GH_JOURNAL_FAIL", "")
				journals := fixtureJournals(t)
				if len(journals) != journalCount+1 {
					t.Fatal("batch must have one complete source effect", len(journals)-journalCount)
				}
				last := journals[len(journals)-1]
				var body struct {
					Details struct {
						Dependencies []string                   `json:"dependencies"`
						Transitions  []ghstore.TransitionRecord `json:"transitions"`
					} `json:"details"`
				}
				if last.UpdateIntent == nil {
					t.Fatal("missing complete input")
				}
				metadata := strings.SplitN(last.UpdateIntent.Target.Body, "<!-- td:issue:v1\n", 2)
				if len(metadata) != 2 || json.Unmarshal([]byte(strings.TrimSuffix(metadata[1], "\n-->")), &body) != nil {
					t.Fatal("bad planned metadata")
				}
				want := []string{"gh-1", "gh-2"}
				if verb == "rm" {
					want = nil
				}
				if !reflect.DeepEqual(body.Details.Dependencies, want) {
					t.Fatal("intent did not freeze the complete dependency delta", body.Details.Dependencies)
				}
				transitions := body.Details.Transitions
				if len(transitions) < 2 || transitions[len(transitions)-1].OperationID != transitions[len(transitions)-2].OperationID {
					t.Fatal("batch delta lost one operation identity", transitions)
				}
				var rows []struct{ ID string }
				if json.Unmarshal([]byte(execute("last", "--n", "1")), &rows) != nil || len(rows) != 1 {
					t.Fatal("missing receipt")
				}
				if failure != "" {
					actual := read()
					execute("undo", "--reconcile", rows[0].ID, "--apply")
					if !bytes.Equal(actual, read()) {
						t.Fatal("recovery replayed the dependency mutation")
					}
				}
				execute("undo")
				var original, restored struct{ Issues map[string]map[string]any }
				if json.Unmarshal(before, &original) != nil || json.Unmarshal(read(), &restored) != nil {
					t.Fatal("invalid fixture data")
				}
				for id, issue := range original.Issues {
					for _, field := range []string{"title", "body", "state", "labels"} {
						if !reflect.DeepEqual(issue[field], restored.Issues[id][field]) {
							t.Fatal("undo failed to restore the whole command", id, field)
						}
					}
				}
				actual := read()
				execute("undo", "--reconcile", rows[0].ID, "--apply")
				if !bytes.Equal(actual, read()) {
					t.Fatal("confirmation replayed the inverse")
				}
			})
		}
	}
}

func TestGitHubJournalCLIDependencyBatchNoopsAndInvalidRemoval(t *testing.T) {
	_, _, run := journalCLI(t)
	execute := func(args ...string) string {
		t.Helper()
		out, stderr, err := run(append(args, "--json")...)
		if err != nil {
			t.Fatal(args, out, stderr, err)
		}
		return out
	}
	read := func(file string) []byte {
		t.Helper()
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	execute("create", "Repeated target fixture")
	args := []string{"dep", "add", "gh-6", "gh-1", "--depends-on", "1,gh-2"}
	out := execute(args...)
	decoder := json.NewDecoder(strings.NewReader(out))
	for i, want := range []bool{false, true, false} {
		var row struct {
			To            string
			AlreadyExists bool `json:"already_exists"`
			Issue         ghstore.Record
		}
		if err := decoder.Decode(&row); err != nil || row.AlreadyExists != want || row.To != []string{"gh-1", "gh-1", "gh-2"}[i] || !reflect.DeepEqual(row.Issue.Details.Dependencies, []string{"gh-1", "gh-2"}) {
			t.Fatal("batch output lost argument order or reported intermediate data", row, err)
		}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatal("unexpected output after batch results", err)
	}
	stateFile, journalFile := os.Getenv("TD_SCHEDULE_STATE"), os.Getenv("TD_GH_FIXTURE_JOURNAL")
	state, journal := read(stateFile), read(journalFile)
	execute(args...)
	for _, targets := range [][]string{{"gh-1", "gh-99"}, {"gh-1", "1"}} {
		args := append([]string{"dep", "remove", "gh-6"}, targets...)
		if out, stderr, err := run(append(args, "--json")...); err == nil || !strings.Contains(out+stderr, "not found") {
			t.Fatal("invalid later removal accepted", out, stderr, err)
		}
	}
	if !bytes.Equal(state, read(stateFile)) || !bytes.Equal(journal, read(journalFile)) {
		t.Fatal("no-op/invalid removal wrote a task or receipt")
	}
}
