package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/td/internal/db"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/storetransfer"
)

func TestStoreTransferPhasePublicationFailureStopsAndResumesWithoutDuplicateArchive(t *testing.T) {
	target, _, run := journalCLIWithTimeout(t, 2*time.Minute)
	useNativeCommentOrderFixture(t)
	useHandoffReviewFixture(t)
	t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
	configureImportJournalFixture(t)
	if out, stderr, err := run("create", "baseline seed", "--minor", "--json"); err != nil {
		t.Fatal(out, stderr, err)
	}
	source := t.TempDir()
	database, err := db.Initialize(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateIssue(&models.Issue{Title: "task after source phase"}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(t.TempDir(), "original.json")
	t.Setenv("TD_WORKSPACE_PHASE_FAIL", checkpoint+".progress.json")
	path, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seam := "def with_journal_observation(path,data):\n"
	if !bytes.Contains(script, []byte(seam)) {
		t.Fatal("missing collection readback seam")
	}
	hook := `def with_journal_observation(path,data):
 phase_path=os.environ.get('TD_WORKSPACE_PHASE_FAIL')
 if phase_path and pathlib.Path(phase_path).exists() and method=='GET' and '/comments?' in path:
  progress=json.loads(pathlib.Path(phase_path).read_text())
  latest=progress['mutations'][-1] if progress['mutations'] else {}
  roots=[c for c in journal['comments'] if '<!-- td:workspace-archive:v1' in c['body'] and c['id']==latest.get('response_id')]
  if not progress.get('phases') and latest.get('state')=='response_received' and roots:
   pathlib.Path(phase_path+'.lock').touch(exist_ok=True)
`
	script = bytes.Replace(script, []byte(seam), []byte(hook), 1)
	if err := os.WriteFile(path, script, 0755); err != nil {
		t.Fatal(err)
	}
	before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
	out, stderr, err := run("--work-dir", source, "store", "transfer", "--execute", "--to", "gh-issue", "--destination", target, "--remote", "origin", "--source-namespace", "phase-failure-fixture", "--checkpoint", checkpoint, "--json")
	if err == nil || !strings.Contains(out+stderr, "source archive phase receipt") || !strings.Contains(out+stderr, "lock transfer progress") || !strings.Contains(out+stderr, "no automatic retry") {
		t.Fatal("phase failure hidden", out, stderr, err)
	}
	if !bytes.Equal(before, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
		t.Fatal("phase failure allowed later entity writes")
	}
	data, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := storetransfer.ReadCheckpoint(bytes.NewReader(data), 0)
	if err != nil {
		t.Fatal(err)
	}
	progressData, err := os.ReadFile(checkpoint + ".progress.json")
	if err != nil {
		t.Fatal(err)
	}
	progress, err := storetransfer.ReadProgress(bytes.NewReader(progressData), cp, 0)
	if err != nil || len(progress.Phases) != 0 || progress.Mutations[len(progress.Mutations)-1].State != "response_received" {
		t.Fatal("failed local phase changed transport evidence", progress, err)
	}
	t.Setenv("TD_WORKSPACE_PHASE_FAIL", "")
	if err := os.Remove(checkpoint + ".progress.json.lock"); err != nil {
		t.Fatal(err)
	}
	out, stderr, err = run("store", "transfer", "--resume", "--checkpoint", checkpoint, "--json")
	var result storetransfer.GitHubTransferResult
	if err != nil || json.Unmarshal([]byte(out), &result) != nil || !result.Completed {
		t.Fatal("phase recovery failed", out, stderr, err)
	}
	var journal struct{ Comments []struct{ Body string } }
	if err := json.Unmarshal(relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL"), &journal); err != nil {
		t.Fatal(err)
	}
	roots := 0
	for _, comment := range journal.Comments {
		if strings.HasPrefix(comment.Body, "td workspace source archive "+cp.OperationID+"\n") {
			roots++
		}
	}
	if roots != 1 {
		t.Fatal("source archive duplicated", roots)
	}
}

func TestStoreTransferResumeBeforeFirstDispatchAndRefusesChangedBaseline(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unattempted", true: "changed_destination"}[changed], func(t *testing.T) {
			target, actor, run := journalCLIWithTimeout(t, 2*time.Minute)
			useNativeCommentOrderFixture(t)
			useHandoffReviewFixture(t)
			t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
			configureImportJournalFixture(t)
			if out, stderr, err := run("create", "baseline seed", "--minor", "--json"); err != nil {
				t.Fatal(out, stderr, err)
			}
			source := t.TempDir()
			database, err := db.Initialize(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.CreateIssue(&models.Issue{Title: "first dispatch task"}); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			collected, err := storetransfer.Collect(context.Background(), source, "first-dispatch-fixture", 0)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := storetransfer.InspectTarget(context.Background(), storetransfer.TargetSpec{ProjectDir: target, Store: "gh-issue", Remote: "origin"}, 0)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, err := storetransfer.NewCheckpoint(collected.Source, observed, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := checkpoint.BindActor(actor); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "original.json")
			if err := storetransfer.SaveCheckpointNew(path, checkpoint, 0); err != nil {
				t.Fatal(err)
			}
			progress := storetransfer.Progress{Version: 1, CheckpointSHA: checkpoint.SHA256, OperationID: checkpoint.OperationID, ActorSessionID: actor, Mutations: []storetransfer.MutationReceipt{}}
			data, _ := json.Marshal(progress)
			digest := sha256.Sum256(data)
			progress.SHA256 = fmt.Sprintf("%x", digest)
			data, _ = json.Marshal(progress)
			if _, err := storetransfer.ReadProgress(bytes.NewReader(data), checkpoint, 0); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".progress.json", data, 0600); err != nil {
				t.Fatal(err)
			}
			if changed {
				if out, stderr, err := run("create", "external new task", "--minor", "--json"); err != nil {
					t.Fatal(out, stderr, err)
				}
			}
			before := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
			out, stderr, err := run("store", "transfer", "--resume", "--checkpoint", path, "--json")
			if changed {
				if err == nil || !strings.Contains(out+stderr, "destination changed") || !bytes.Equal(before, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) {
					t.Fatal("changed baseline dispatched writes", out, stderr, err)
				}
			} else {
				var result storetransfer.GitHubTransferResult
				if err != nil || json.Unmarshal([]byte(out), &result) != nil || !result.Completed {
					t.Fatal("unattempted resume failed", out, stderr, err)
				}
			}
		})
	}
}

func TestStoreTransferResumeAfterLostTaskOrNoteResponse(t *testing.T) {
	for _, failure := range []string{"root-lost", "allocation-lost", "note-lost", "board-lost"} {
		t.Run(failure, func(t *testing.T) {
			target, _, run := journalCLIWithTimeout(t, 2*time.Minute)
			useNativeCommentOrderFixture(t)
			useHandoffReviewFixture(t)
			t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
			configureImportJournalFixture(t)
			path, err := exec.LookPath("gh")
			if err != nil {
				t.Fatal(err)
			}
			script, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			seam := "if os.environ.get('TD_GH_IMPORT_FAIL')=='allocation-lost':"
			if !bytes.Contains(script, []byte(seam)) {
				t.Fatal("missing allocation seam")
			}
			script = bytes.Replace(script, []byte(seam), []byte("if os.environ.get('TD_GH_IMPORT_FAIL') in ('note-lost','board-lost') and ('\"entity_kind\":\"'+('note' if os.environ.get('TD_GH_IMPORT_FAIL')=='note-lost' else 'board')+'\"') in patch.get('body',''):sys.stderr.write('connection lost after workspace entity');sys.exit(1)\n "+seam), 1)
			if err := os.WriteFile(path, script, 0755); err != nil {
				t.Fatal(err)
			}
			source := t.TempDir()
			database, err := db.Initialize(source)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.CreateIssue(&models.Issue{Title: "resume task"}); err != nil {
				t.Fatal(err)
			}
			if _, err := database.CreateNote("resume note", "frozen content"); err != nil {
				t.Fatal(err)
			}
			if _, err := database.CreateBoard("resume board", ""); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			checkpoint := filepath.Join(t.TempDir(), "original.json")
			t.Setenv("TD_GH_IMPORT_FAIL", failure)
			out, stderr, err := run("--work-dir", source, "store", "transfer", "--execute", "--to", "gh-issue", "--destination", target, "--remote", "origin", "--source-namespace", "resume-fixture", "--checkpoint", checkpoint, "--json")
			if err == nil || !strings.Contains(out+stderr, "connection lost") || !strings.Contains(out+stderr, "no automatic retry") {
				t.Fatal("lost response hidden", out, stderr, err)
			}
			t.Setenv("TD_GH_IMPORT_FAIL", "")
			original, err := os.ReadFile(checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(source); err != nil {
				t.Fatal(err)
			}
			out, stderr, err = run("store", "transfer", "--resume", "--checkpoint", checkpoint, "--json")
			var result storetransfer.GitHubTransferResult
			if err != nil || json.Unmarshal([]byte(out), &result) != nil || !result.Completed || len(result.Mappings) != 4 {
				t.Fatal("partial resume failed", out, stderr, err)
			}
			if failure == "note-lost" || failure == "board-lost" {
				observed := false
				for _, proof := range result.EntityProofs {
					observed = observed || proof == "observed"
				}
				if !observed || failure == "note-lost" && result.Notes != 0 {
					t.Fatal("lost note duplicated or proof upgraded", result)
				}
			}
			var native struct{ Issues map[string]json.RawMessage }
			if err := json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &native); err != nil || len(native.Issues) != 9 {
				t.Fatal("duplicate or missing native entity", len(native.Issues), err)
			}
			before, journal := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
			out, stderr, err = run("store", "transfer", "--resume", "--checkpoint", checkpoint, "--json")
			if err != nil || !bytes.Equal(before, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journal, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
				t.Fatal("repeat resume wrote", out, stderr, err)
			}
			after, _ := os.ReadFile(checkpoint)
			if !bytes.Equal(original, after) {
				t.Fatal("resume replaced original preparation")
			}
		})
	}
}

func TestStoreTransferExecuteGitHubWholeWorkspaceAndNoDuplicateReplay(t *testing.T) {
	for _, adopt := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_tasks", true: "adopted_parent"}[adopt], func(t *testing.T) {
			testStoreTransferExecuteGitHubWholeWorkspace(t, adopt)
		})
	}
}

func testStoreTransferExecuteGitHubWholeWorkspace(t *testing.T, adopt bool) {
	t.Helper()
	target, _, run := journalCLIWithTimeout(t, 2*time.Minute)
	useNativeCommentOrderFixture(t)
	useHandoffReviewFixture(t)
	t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
	configureImportJournalFixture(t)
	source := t.TempDir()
	database, err := db.Initialize(source)
	if err != nil {
		t.Fatal(err)
	}
	parent := models.Issue{Title: "transfer parent", Type: models.TypeEpic, Priority: models.PriorityP1}
	if err := database.CreateIssue(&parent); err != nil {
		t.Fatal(err)
	}
	child := models.Issue{Title: "transfer child", ParentID: parent.ID, CreatorSession: "original-source-actor"}
	if err := database.CreateIssue(&child); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateNote("transfer note", "source content"); err != nil {
		t.Fatal(err)
	}
	board, err := database.CreateBoard("transfer custom", `id = "`+child.ID+`"`)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetIssuePosition(board.ID, child.ID, 4); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(source, ".todos", "issues.db"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(t.TempDir(), "checkpoint.json")
	args := []string{"--work-dir", source, "store", "transfer", "--execute", "--to", "gh-issue", "--destination", target, "--remote", "origin", "--source-namespace", "workspace-execute-fixture", "--checkpoint", checkpoint, "--json"}
	var retainedBefore json.RawMessage
	if adopt {
		if out, stderr, err := run("create", "Unrelated fixture seed", "--minor", "--json"); err != nil {
			t.Fatal("initial destination observation", out, stderr, err)
		}
		collected, err := storetransfer.Collect(context.Background(), source, "workspace-execute-fixture", 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, identity := range collected.Preview.Identities {
			if identity.Kind == "task" && identity.ID == parent.ID {
				args = append(args, "--map", identity.Key+"=gh-2")
			}
		}
		var native struct{ Issues map[string]json.RawMessage }
		if err := json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &native); err != nil {
			t.Fatal(err)
		}
		retainedBefore = native.Issues["2"]
	}
	out, stderr, err := run(args...)
	var result storetransfer.GitHubTransferResult
	expectedCompleted := 2
	if adopt {
		expectedCompleted = 1
	}
	if err != nil || json.Unmarshal([]byte(out), &result) != nil || !result.Completed || result.TaskImport == nil || result.TaskImport.Completed != expectedCompleted || result.Notes != 1 || result.Boards != 1 || result.SourceArchiveID == 0 || result.CompletionArchiveID == 0 || len(result.Mappings) != 5 {
		t.Fatal("workspace execution failed", out, stderr, err)
	}
	if adopt {
		var native struct{ Issues map[string]json.RawMessage }
		if err := json.Unmarshal(relationFixtureBytes(t, "TD_SCHEDULE_STATE"), &native); err != nil || !bytes.Equal(retainedBefore, native.Issues["2"]) || result.TaskImport.Skipped != 1 {
			t.Fatal("workspace execution changed adopted destination", err)
		}
	}
	data, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := storetransfer.ReadCheckpoint(bytes.NewReader(data), 0)
	if err != nil || stored.ActorSessionID != result.ActorSessionID || stored.Source.SHA256 != result.SourceSHA256 {
		t.Fatal("checkpoint did not preserve original execution", stored, err)
	}
	progressBytes, err := os.ReadFile(checkpoint + ".progress.json")
	if err != nil {
		t.Fatal("missing durable mutation progress", err)
	}
	progress, err := storetransfer.ReadProgress(bytes.NewReader(progressBytes), stored, 0)
	if err != nil || len(progress.Mutations) == 0 || len(progress.Phases) != 4 {
		t.Fatal("invalid mutation progress", progress, err)
	}
	archiveResponses := map[int64]bool{}
	taskOperation, err := stored.TaskOperationID()
	if err != nil {
		t.Fatal(err)
	}
	taskRootRecorded := false
	for _, mutation := range progress.Mutations {
		if mutation.State != "response_received" {
			t.Fatal("successful fixture lost a mutation response", mutation)
		}
		if strings.HasSuffix(mutation.Request.Path, "/comments") {
			archiveResponses[mutation.ResponseID] = true
			taskRootRecorded = taskRootRecorded || mutation.Request.OperationID == taskOperation
		}
	}
	if !taskRootRecorded || !archiveResponses[result.SourceArchiveID] || !archiveResponses[result.CompletionArchiveID] || bytes.Contains(progressBytes, []byte("source content")) {
		t.Fatal("archive mutation results missing or private body copied into progress")
	}
	after, err := os.ReadFile(filepath.Join(source, ".todos", "issues.db"))
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("source database changed", err)
	}
	native := relationFixtureBytes(t, "TD_SCHEDULE_STATE")
	journal := relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	out, stderr, err = run(args...)
	if err == nil || !strings.Contains(out+stderr, "checkpoint already exists") {
		t.Fatal("same checkpoint replay accepted", stderr, err)
	}
	newArgs := append([]string(nil), args...)
	for i := range newArgs {
		if newArgs[i] == checkpoint {
			newArgs[i] = filepath.Join(t.TempDir(), "new.json")
		}
	}
	sourceBytes, _ := json.Marshal(stored.Source)
	preview, previewErr := storetransfer.Read(bytes.NewReader(sourceBytes), 0)
	if previewErr != nil {
		t.Fatal(previewErr)
	}
	identities, previewErr := preview.Preview()
	if previewErr != nil {
		t.Fatal(previewErr)
	}
	for _, identity := range identities.Identities {
		if identity.ID == board.ID {
			newArgs = append(newArgs, "--map", identity.Key+"="+result.Mappings[identity.Key])
		}
	}
	out, stderr, err = run(newArgs...)
	if err == nil || !strings.Contains(out+stderr, "already has workspace archive") {
		t.Fatal("new checkpoint bypassed original source guard", out, stderr, err)
	}
	if !bytes.Equal(native, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journal, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("replay mutated remote state")
	}
	// Resume must use the frozen preparation even if the original source no
	// longer exists; completed native operations are rechecked, not replayed.
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		out, stderr, err = run("store", "transfer", "--resume", "--checkpoint", checkpoint, "--json")
		var resumed storetransfer.GitHubTransferResult
		if err != nil || json.Unmarshal([]byte(out), &resumed) != nil || !resumed.Completed || resumed.SourceSHA256 != result.SourceSHA256 || resumed.SourceArchiveID != result.SourceArchiveID || resumed.CompletionArchiveID != result.CompletionArchiveID || len(resumed.Mappings) != len(result.Mappings) || resumed.Notes != 0 || resumed.Boards != 0 {
			t.Fatal("completed resume failed", out, stderr, err)
		}
		for key, id := range result.Mappings {
			if resumed.Mappings[key] != id {
				t.Fatal("resume changed original mapping", key, id, resumed.Mappings[key])
			}
		}
		if !bytes.Equal(native, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journal, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
			t.Fatal("completed resume wrote native state")
		}
		checkpointAfter, _ := os.ReadFile(checkpoint)
		progressAfter, _ := os.ReadFile(checkpoint + ".progress.json")
		if !bytes.Equal(data, checkpointAfter) || !bytes.Equal(progressBytes, progressAfter) {
			t.Fatal("completed resume rewrote immutable input or original progress")
		}
	}
	if out, stderr, err := run("usage", "--new-session", "-q"); err != nil {
		t.Fatal("fixture session rotation failed", out, stderr, err)
	}
	out, stderr, err = run("store", "transfer", "--resume", "--checkpoint", checkpoint, "--json")
	if err == nil || !strings.Contains(out+stderr, "actual resume session differs") {
		t.Fatal("rotated actor impersonated original execution", out, stderr, err)
	}
	if !bytes.Equal(native, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journal, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("actor mismatch mutated remote state")
	}
}

func TestStoreTransferProgressInitializationFailureStopsBeforeGitHubWrites(t *testing.T) {
	target, _, run := journalCLIWithTimeout(t, 2*time.Minute)
	useNativeCommentOrderFixture(t)
	useHandoffReviewFixture(t)
	t.Setenv("TD_GH_FIXTURE_NATIVE_EVENTS", "1")
	configureImportJournalFixture(t)
	if out, stderr, err := run("create", "Unrelated fixture seed", "--minor", "--json"); err != nil {
		t.Fatal(out, stderr, err)
	}
	native, journals := relationFixtureBytes(t, "TD_SCHEDULE_STATE"), relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")
	source := t.TempDir()
	database, err := db.Initialize(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateIssue(&models.Issue{Title: "Source must remain unchanged"}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := os.ReadFile(filepath.Join(source, ".todos", "issues.db"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(t.TempDir(), "checkpoint.json")
	oldProgress := []byte("unrelated existing private file")
	if err := os.WriteFile(checkpoint+".progress.json", oldProgress, 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := run("--work-dir", source, "store", "transfer", "--execute", "--to", "gh-issue", "--destination", target, "--remote", "origin", "--source-namespace", "progress-failure-fixture", "--checkpoint", checkpoint, "--json")
	if err == nil || !strings.Contains(out+stderr, "local progress initialization") {
		t.Fatal("receipt initialization failure became success", out, stderr, err)
	}
	if !bytes.Equal(native, relationFixtureBytes(t, "TD_SCHEDULE_STATE")) || !bytes.Equal(journals, relationFixtureBytes(t, "TD_GH_FIXTURE_JOURNAL")) {
		t.Fatal("progress initialization failure reached GitHub mutation")
	}
	sourceAfter, _ := os.ReadFile(filepath.Join(source, ".todos", "issues.db"))
	progressAfter, _ := os.ReadFile(checkpoint + ".progress.json")
	if !bytes.Equal(sourceBefore, sourceAfter) || !bytes.Equal(oldProgress, progressAfter) {
		t.Fatal("failure changed source or existing private progress")
	}
}
