package storetransfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
)

var ErrGitHubTaskAdoption = errors.New("GitHub task adoption requires a checked importer source-key contract; no write attempted")

// GitHubTransferResult reports individually confirmed stages. GitHub is not a
// transaction; an error may leave native intents/entities and the source archive.
type GitHubTransferResult struct {
	SourceStore         string                `json:"source_store"`
	TargetStore         string                `json:"target_store"`
	Completed           bool                  `json:"completed"`
	OperationID         string                `json:"operation_id"`
	Checkpoint          string                `json:"checkpoint"`
	ActorSessionID      string                `json:"actor_session_id"`
	SourceSHA256        string                `json:"source_sha256"`
	TargetNamespace     string                `json:"target_namespace"`
	SourceArchiveID     int64                 `json:"source_archive_id"`
	CompletionArchiveID int64                 `json:"completion_archive_id"`
	TaskImport          *ghstore.ImportResult `json:"task_import,omitempty"`
	Notes               int                   `json:"created_notes"`
	Boards              int                   `json:"created_boards"`
	BuiltinPositions    int                   `json:"builtin_positions"`
	Mappings            map[string]string     `json:"mappings"`
	Preservation        []Disclosure          `json:"preservation"`
}

// ExecuteGitHubDestination freezes and saves the exact input before any GitHub
// mutation. It reuses native import/entity journals and source capsule transport.
// Reconciliation/resume is a separate boundary; no write is automatically retried.
func ExecuteGitHubDestination(ctx context.Context, source *Source, target *InspectedTarget, bindings map[string]string, checkpointPath string, maxBytes int64) (*GitHubTransferResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if checkpointPath == "" {
		return nil, fmt.Errorf("GitHub execution requires an explicit new checkpoint file")
	}
	checkpointPath, err := filepath.Abs(checkpointPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(checkpointPath); err == nil {
		return nil, fmt.Errorf("checkpoint already exists; inspect it before explicit resume: %w", os.ErrExist)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	prepared, err := PrepareGitHubWorkspace(source, target, bindings, maxBytes)
	if err != nil {
		return nil, err
	}
	for _, row := range prepared.identities.Assignments {
		if row.Kind == "task" && row.Action == "adopt" {
			return nil, ErrGitHubTaskAdoption
		}
	}
	spec := target.Spec
	frozenSource, err := Read(bytes.NewReader(prepared.OriginalArchive()), maxBytes)
	if err != nil {
		return nil, err
	}
	prepared.source = frozenSource
	client, err := ghstore.OpenRemote(ctx, spec.ProjectDir, spec.Remote)
	if err != nil {
		return nil, err
	}
	if client.Repository() != prepared.target.Envelope.Namespace {
		return nil, fmt.Errorf("GitHub destination repository changed before execution")
	}
	fresh, err := client.ExportWorkspaceLimited(ctx, maxBytes)
	if err != nil {
		return nil, err
	}
	observed, err := NewGitHub(fresh, maxBytes)
	if err != nil {
		return nil, err
	}
	if err := prepared.CheckTarget(observed); err != nil {
		return nil, err
	}
	// Previously archived source identities must be inspected/resumed instead
	// of creating another workspace operation with duplicate notes/boards.
	keys := map[string]bool{}
	for _, row := range prepared.identities.Assignments {
		keys[row.SourceKey] = true
	}
	for _, archive := range fresh.Archives {
		prior, err := Read(bytes.NewReader(archive.SourceEnvelope), maxBytes)
		if err != nil {
			return nil, fmt.Errorf("inspect existing transfer archive: %w", err)
		}
		if prior.Envelope.Store != prepared.source.Envelope.Store || prior.Envelope.Namespace != prepared.source.Envelope.Namespace {
			continue
		}
		preview, err := prior.Preview()
		if err != nil {
			return nil, err
		}
		for _, identity := range preview.Identities {
			if keys[identity.Key] {
				return nil, fmt.Errorf("source %s already has workspace archive operation %s; inspect the original checkpoint before explicit resume; no write attempted", identity.Key, archive.OperationID)
			}
		}
	}
	scope, err := ghcontext.Resolve(ctx, spec.ProjectDir, client.Repository())
	if err != nil {
		return nil, err
	}
	state, err := scope.Update(ctx, nil)
	if err != nil {
		return nil, err
	}
	checkpoint, err := NewCheckpoint(prepared.source, &InspectedTarget{Spec: spec, Observation: prepared.target}, prepared.ReusedBindings(), maxBytes)
	if err != nil {
		return nil, err
	}
	if err := checkpoint.BindActor(state.Session.ID); err != nil {
		return nil, err
	}
	if err := SaveCheckpointNew(checkpointPath, checkpoint, maxBytes); err != nil {
		return nil, err
	}
	result := &GitHubTransferResult{SourceStore: "sqlite", TargetStore: "gh-issue", OperationID: checkpoint.OperationID, Checkpoint: checkpointPath, ActorSessionID: state.Session.ID, SourceSHA256: prepared.SourceSHA256(), TargetNamespace: client.Repository(), Mappings: prepared.ReusedBindings()}
	fail := func(stage string, err error) (*GitHubTransferResult, error) {
		return result, fmt.Errorf("GitHub transfer %s failed (operation=%s, checkpoint=%s, source_sha256=%s); earlier writes may remain; inspect native state and the original checkpoint before explicit resume; no automatic retry: %w", stage, result.OperationID, checkpointPath, result.SourceSHA256, err)
	}
	if err := ctx.Err(); err != nil {
		return fail("before publication", err)
	}
	if err := client.TrackActions(state.Session.ID); err != nil {
		return fail("actor", err)
	}
	archive, err := client.PublishWorkspaceArchiveData(ctx, checkpoint.OperationID, prepared.OriginalArchive(), result.Mappings, maxBytes)
	if err != nil {
		return fail("source archive", err)
	}
	result.SourceArchiveID = archive.ID
	input, err := prepared.TaskInputs()
	if err != nil {
		return fail("task inputs", err)
	}
	if len(input.Items) > 0 {
		result.TaskImport, err = client.Import(ctx, input.Items, false)
		if err != nil {
			return fail("tasks", err)
		}
	}
	completed, err := client.CompletedImportMappings(ctx, input.Items)
	if err != nil {
		return fail("task receipts", err)
	}
	taskIDs, err := BindGitHubTaskIDs(prepared.source, completed)
	if err != nil {
		return fail("task bindings", err)
	}
	for key, id := range taskIDs {
		result.Mappings[key] = id
	}
	entities, err := BuildGitHubEntityInputs(prepared.source, taskIDs, prepared.reused)
	if err != nil {
		return fail("entity inputs", err)
	}
	operations := map[string]string{}
	operationKeys := map[string]string{}
	for _, note := range entities.Notes {
		op, err := checkpoint.EntityOperationID(note.SourceKey)
		if err != nil {
			return fail("note identity", err)
		}
		note.Options.OperationID = op
		created, err := client.CreateNoteWithOptions(ctx, note.Title, note.Content, state.Session.ID, note.Options)
		if err != nil {
			return fail("note "+note.SourceKey, err)
		}
		result.Notes++
		result.Mappings[note.SourceKey] = created.ID
		operations[op] = "note"
		operationKeys[op] = note.SourceKey
	}
	for _, board := range entities.Boards {
		op, err := checkpoint.EntityOperationID(board.SourceKey)
		if err != nil {
			return fail("board identity", err)
		}
		board.Options.OperationID = op
		var created *ghstore.BoardRecord
		if board.Builtin {
			// Recheck the original retained builtin before changing its positions.
			fresh, err := client.ExportWorkspaceLimited(ctx, maxBytes)
			if err != nil {
				return fail("builtin observation", err)
			}
			if err := prepared.checkRetainedNative(fresh, true); err != nil {
				return fail("builtin basis", err)
			}
			observed, err := client.GetBoard(ctx, "bd-all-issues")
			if err != nil {
				return fail("builtin read", err)
			}
			expectedNumber := 0
			for _, before := range prepared.target.GitHub.Boards {
				if before.IsBuiltin {
					expectedNumber = before.Number
				}
			}
			if observed.Number != expectedNumber {
				return fail("builtin baseline", fmt.Errorf("builtin carrier changed since preparation"))
			}
			if expectedNumber > 0 {
				for _, native := range prepared.target.GitHub.Native {
					if native.Number == expectedNumber {
						if err := observed.CheckNativeBaseline(native); err != nil {
							return fail("builtin baseline", err)
						}
					}
				}
			}
			created, err = client.ApplyBuiltinBoardTransferObserved(ctx, observed, state.Session.ID, board.Options)
			if err != nil {
				return fail("builtin positions", err)
			}
			if observed.Number == 0 || len(board.Options.Positions) > 0 {
				operations[op] = "board"
				operationKeys[op] = board.SourceKey
			}
			result.BuiltinPositions += len(board.Options.Positions)
		} else {
			created, err = client.CreateBoardWithOptions(ctx, board.Name, board.Query, state.Session.ID, board.Options)
			if err != nil {
				return fail("board "+board.SourceKey, err)
			}
			result.Boards++
			operations[op] = "board"
			operationKeys[op] = board.SourceKey
		}
		result.Mappings[board.SourceKey] = created.ID
	}
	verified, err := client.VerifiedEntityOperationMappings(ctx, operations)
	if err != nil {
		return fail("entity receipts", err)
	}
	for op, id := range verified {
		if result.Mappings[operationKeys[op]] != id {
			return fail("entity binding", fmt.Errorf("confirmed entity identity differs from receipt"))
		}
	}
	// Fresh task receipts reject edits that happened during auxiliary creation.
	finalTasks, err := client.CompletedImportMappings(ctx, input.Items)
	if err != nil {
		return fail("final task receipts", err)
	}
	if !reflect.DeepEqual(completed, finalTasks) {
		return fail("final task mapping", fmt.Errorf("task mapping changed during entity writes"))
	}
	finalWorkspace, err := client.ExportWorkspaceLimited(ctx, maxBytes)
	if err != nil {
		return fail("final workspace observation", err)
	}
	if err := prepared.checkRetainedNative(finalWorkspace, false); err != nil {
		return fail("retained destinations", err)
	}
	if len(result.Mappings) != len(prepared.identities.Assignments) {
		return fail("complete mapping", fmt.Errorf("workspace mapping does not cover source identities"))
	}
	completion, err := client.PublishWorkspaceArchiveData(ctx, checkpoint.OperationID+"-complete", prepared.OriginalArchive(), result.Mappings, maxBytes)
	if err != nil {
		return fail("completion archive", err)
	}
	result.CompletionArchiveID = completion.ID
	result.Completed = true
	plan, err := BuildDataPlan(prepared.source, prepared.target, prepared.ReusedBindings())
	if err != nil {
		return fail("preservation report", err)
	}
	result.Preservation = plan.Disclosures
	return result, nil
}

// Compare only retained entities. Own task/note/board writes and journal
// additions are expected; imported source data never authorizes overwriting an
// explicitly adopted destination. The check is best-effort, not a GitHub CAS.
func (p *PreparedGitHubWorkspace) checkRetainedNative(current *ghstore.WorkspaceExport, includeBuiltin bool) error {
	if current == nil || current.Repository != p.target.Envelope.Namespace {
		return fmt.Errorf("retained destination repository changed")
	}
	numbers := map[int]bool{}
	for _, row := range p.identities.Assignments {
		if row.Action == "adopt" {
			for _, native := range p.target.GitHub.Native {
				prefix := map[string]string{"note": "nt-gh-", "board": "bd-gh-", "task": "gh-"}[row.Kind]
				if row.TargetID == fmt.Sprintf("%s%d", prefix, native.Number) {
					numbers[native.Number] = true
				}
			}
		}
		if includeBuiltin && row.Action == "builtin" {
			for _, board := range p.target.GitHub.Boards {
				if board.IsBuiltin && board.Number > 0 {
					numbers[board.Number] = true
				}
			}
		}
	}
	for number := range numbers {
		var before, after *ghstore.NativeExport
		for i := range p.target.GitHub.Native {
			if p.target.GitHub.Native[i].Number == number {
				before = &p.target.GitHub.Native[i]
			}
		}
		for i := range current.Native {
			if current.Native[i].Number == number {
				after = &current.Native[i]
			}
		}
		if before == nil || after == nil || !reflect.DeepEqual(before, after) {
			return fmt.Errorf("retained destination gh-%d changed since preparation; inspect before proceeding", number)
		}
	}
	return nil
}
