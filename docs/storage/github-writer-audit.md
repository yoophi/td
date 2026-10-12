# Compound task writer audit (#79)

Feature-worktree addition for #93: `ApplyBuiltinBoardTransferObserved` uses the
existing configured create/entity update journal, one native carrier mutation
for all selected positions, pure slot/member preflight, and a checkpoint-pinned
operation for explicit writes. A virtual builtin uses the existing initial-create
path; an existing builtin keeps its configuration and records one position delta.
`TestBuiltinTransfer*` covers the complete input, retained settings, conflicts,
replay and partial outcomes. This addition is not evidence of transfer CLI
activation, whole-workspace compensation/resume, or the full #31/#68 audit.

Further #93 execution wiring: `ExecuteGitHubDestination` saves a private original
checkpoint/actual actor, publishes a passive source capsule before task writes,
dispatches the existing native importer and tracked entity writers, re-observes
completion mappings, and publishes a passive completion capsule. Source and
completion capsules do not become active actions or undo authority. A failure
stops with the original checkpoint/operation and native cause; no target write
is automatically retried. `TestStoreTransferExecuteGitHubWholeWorkspaceAndNoDuplicateReplay`
checks the real command dispatch and unchanged SQL bytes. Native evidence is
currently partial allocation/listing-lag failure, not completed workspace proof.
Explicit task adoption uses the native versioned whole-import command and pins
the retained destination observation. Completion and inverse preserve adopted
task fields/history; all-adopted imports journal only their bindings. Verified
mapping observations use bulk collections plus one original-actor check, and
workspace execution rechecks retained entities before auxiliary writes and final
completion. CLI import/repeat/undo and whole-workspace adoption fixtures cover
the public paths. Explicit checkpoint reconciliation and final native/integration
acceptance remain unfinished (#94/#95).

#94 transfer-only request recording now brackets each real gh mutation: persist
its exact input hash before dispatch, then persist returned bytes' hash/native
IDs before proceeding. This captures archive chunks/roots as well as task and
configured-entity journal writes, without copying bodies or credentials. The
immutable checkpoint and existing native logical receipts remain distinct from
these local transport observations. Receipt failure stops the transfer with the
original IO error; no automatic API retry or active-source authority is added.
Ordinary CLI/HTTP/monitor writers keep their existing request path when this
transfer context is absent. Explicit resume and final native acceptance remain
unfinished; this addition does not close #94 or the full #31/#68 audit.

This is the release-preparation audit of #65 at `95141ed`, after native import
acceptance #78. It checks the existing compound task writers and their real
CLI/HTTP/monitor entry points. The complete command/flag contract audit remains
#31/#68; the captured Cobra inventory is not a declaration that every option is
fully supported. The [writer matrix](github-batch-writers.md) describes the
protocols and the [support matrix](github-support-matrix.md) keeps that distinction.

## Entry points and command boundaries

CLI mutations resolve the actual device-local session and call `TrackActions`.
The HTTP production factory `NewGitHubWriteStore` checks the selected repository,
worktree and active web session before enabling the same client protocols.
`NewGitHubDataSource` and the monitor board/notes factories do likewise with the
actual monitor actor. Registering a reader does not bootstrap a journal carrier.

| User operation | Production dispatch and boundary | Existing regression evidence |
| --- | --- | --- |
| Multiple `start`, `unstart`, `block`, `unblock`; `close`, `approve`, `reopen`, including `--all` selection and cascades | `cmd/gh_transitions.go`: `BeginTransitionActionGroup`; shared `TransitionWithCascades` handles singleton cascades as well | `TestGitHubJournalCLIGroupedBasicTransitionsAndPartialRecovery`, `TestGitHubJournalCLINativeClosureGroupAndRecovery` |
| `unstart --session` / `--stale` claim sweep | `cmd/gh_claim_sweep.go`: fixed observed selection, `BeginObservedActionGroup`, explicit group completion | `TestGitHubJournalCLIClaimSweepAndWorkSessionGroups` |
| `ws tag` with automatic starts | `cmd/gh_ws.go`: preselected open tasks share an observed start group; `--no-start` changes local tags only | `TestGitHubJournalCLIClaimSweepAndWorkSessionGroups` |
| Scalar multi-ID `update`, including per-target `--append` | `cmd/gh_issue.go`: `UpdateMany` prepares all complete payloads | `TestGitHubJournalCLIPlannedScalarUpdateGroup` |
| Multiple `reject`, `delete`, `restore` | `cmd/gh_transitions.go`, `cmd/gh_delete.go`: `BeginPlannedCommand` / `ApplyPlannedCommandMember` / completion, including no-op members | `TestGitHubJournalCLIPlannedRejectDeleteRestore`, `TestGitHubJournalCLIPlannedCommandsValidateAllTargetsBeforeWrites` |
| `ws log`, `ws handoff` | `cmd/gh_ws_activity.go`: frozen `BeginActivityGroup`; `--review` uses `PrepareHandoffReview` and `BeginHandoffReviewGroup` | `TestGitHubJournalCLIWorkSessionActivityGroup`, `TestGitHubJournalCLIHandoffReviewWholeCommand` |
| State/priority label repair batches | `cmd/gh_state_labels.go`, `cmd/gh_priority_labels.go`: tracked `SyncStateLabels` / `SyncPriorityLabels`, shared planned updates | `TestGitHubJournalCLILabelRepairGroup` |
| Multi-edge `dep add`, `dep rm` / `remove` | `cmd/gh_relationships.go`: `ChangeDependenciesObserved`, one source PATCH for the complete delta | `TestGitHubRelationshipCommandsGraphAndBatchPreflight` |
| `link`, `unlink`, `due`, `defer` with companion activity | `cmd/gh_files.go`, `cmd/gh_schedule.go`: `ReplaceLinkedFilesWithActivityObserved` / `UpdateWithActivityObserved` | `TestGitHubJournalCLIUpdateActivityWholeCommand` |
| `update --comment` / `--note` with fields, or multiple comment-only targets | `cmd/gh_issue.go`: `UpdateWithComments`; a single comment-only request remains standalone | `TestGitHubJournalCLIUpdateCommentsWholeCommand` |
| `update --parent` / `--depends-on` / `--blocks`, optional fields and inline comment | `cmd/gh_issue.go`: `UpdateWithRelations`, shared reverse targets and graph order | `TestGitHubJournalCLIUpdateRelationsSharedTargetWholeCommand`, `TestGitHubJournalCLIUpdateRelationsRecovery` |
| `update --status`, including fields/relationships/comment; `review` | `cmd/gh_issue.go`, `cmd/gh_transitions.go`: `UpdateWorkflowCommand`, exact requested inputs and automatic handoff/cascade effects | `TestGitHubJournalCLIWorkflowUpdateWholeCommand`, `TestGitHubJournalCLIWorkflowFailureRecoveryDoesNotReplay` |
| `create --depends-on` / `--blocks` | `cmd/gh_issue.go`: `CreateWithRelations`, actual allocation owns optional reverse effects | `TestGitHubJournalCLICreateRelationsWholeCommand`, `TestGitHubJournalCLICreationRecoveryDoesNotReplayMissingRelations` |
| `import`, including `--force` and explicit resume | `cmd/gh_import.go`: tracked `Import`; preview only calls `PlanImport`; original command owns the existing executor and compensation | `TestGitHubJournalCLIImportMixedGraphResumeAndUndo`, `TestGitHubJournalCLIImportInverseReceiptRecovery`, native #78 |
| HTTP workflow routes | `internal/serve/github_workflow.go`: `TransitionObservedWithCascades`; review uses the workflow command including automatic handoff | `TestGitHubHTTPProductionFactoryJournalsIssueAndActivityWrites`, `TestGitHubHTTPWorkflowPolicyWiringAndCascadeBoundary` |
| HTTP issue/comment/dependency/deletion routes | `internal/serve/github_writes.go`, `github_comments.go`, `github_dependencies.go`, `github_delete.go`: tracked standalone writes for their declared single-target inputs | `TestGitHubHTTPProductionFactoryJournalsIssueAndActivityWrites`, `TestGitHubHTTPJournalFailuresPreventFalseSuccess` |
| Monitor create/edit/review/close/delete | `pkg/monitor/github_form_create.go`: `CreateWithRelations` when dependencies exist; `github_form_save.go`: one `UpdateCommandObserved`; transition handles use `TransitionObservedWithCascades` | `TestGitHubMonitorRelatedCreationUsesOneCommandAndWholeUndo`, `TestGitHubMonitorProductionFactoryJournalsCreateEditAndDelete` |

HTTP creation has parent input but no dependency/block-list request. Its current
single creation POST therefore remains standalone. Monitor edits pin the original
form observation and omit unchanged status; partial creation retains its allocated
ID and prevents resubmitting the same form. CLI aliases share the canonical
command handlers. HTTP/monitor do not turn client-side loops into a synthetic
multi-request transaction, and this audit does not add an undo UI/HTTP endpoint.

## Recovery, inverse and exclusions

`Snapshot.ActionHistory` returns the complete uncollapsed source records.
The CLI applies `CollapseActionGroups` to present owned effects as the original
command; portable history keeps the complete records and validates their links.
`ReconcileAction` observes uncertain outcomes and may publish receipts; it does
not repeat target mutations. `UndoAction` dispatches the command-specific inverse
with fresh task, original receipt, native activity/event and graph checks.
Completed confirmation is read-only. Parent/group ownership prevents choosing
an effect independently, and observed outcomes retain weaker attribution.
The existing CLI recovery tests, production-adapter failure tests and #78 native
run provide evidence for these boundaries; they do not promise atomic GitHub
transactions or recovery after arbitrary external changes.

No omitted compound **task** writer was found in the listed CLI/HTTP/monitor
routes. The audit did identify a separate setup boundary:

- [#83](https://github.com/yoophi/td/issues/83) combines a virtual builtin board's
  initial shared mode/position with its creation: one carrier POST and one
  existing create journal. CLI `board edit --view-mode` / `board move`, HTTP
  board position/move and monitor board movement share this ghstore path. No
  empty carrier is created first. The original setting/order request remains
  in the creation event reason and complete intent body. Creation undo retains
  the existing builtin carrier policy; explicit setting resets preserve its
  identity/history. Later settings retain guarded update undo. Monitor
  view/last-visited preferences are device-local and do not materialize a board.
- Existing board/note updates mutate one carrier; rendering/filter loops are
  reads. Device-local focus, tags, work-session membership/end state, preferences
  and session cleanup are not restored by remote journal undo.
- Legacy standalone creation keeps its explicit logical-delete alternative.
  Physically deleted native comment IDs/authors/timestamps and native event
  history cannot be recreated exactly. Supported compensation must not claim
  that identity restoration occurred.
- Portable history is covered by the [#66 archive contract](github-portable-journal.md);
  broad CLI/flag coverage is #31/#68, complete GUI
  interaction coverage is #55, and repeated bulk verification costs are #73.
  Those tasks remain open beyond this intermediate release.

## Verification

On 2026-10-11 with Go 1.27.0, the captured-inventory and support-matrix checks
passed (`cmd`, 0.901 s). Focused production HTTP journal/session/policy tests passed
(`internal/serve`, 17.041 s), and monitor creation/edit/transition/board/notes,
uncertain-result and retired-context tests passed (`pkg/monitor`, 23.240 s).
Both sync feature environment overrides were removed for this run. No production
code changed during this audit. Full release tests, exact-main CI and release
activation remain #80/#81/#82 gates; these focused results do not replace them.
