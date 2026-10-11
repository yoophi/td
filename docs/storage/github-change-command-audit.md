# Change command and option audit (#68)

Scope: main `c609f64`, following #65/#67/#66/#83, on 2026-10-11,
plus the scope-message corrections made during this audit.
This audit covers GitHub task, activity, board and note history/undo for #9.
The general command support audit remains #31; GUI interaction acceptance remains
#55. A command having a route does not prove every combination of options works.

## Captured surface

The table records every runnable canonical command in these families from
`cmd/testdata/cli-inventory-sync-true.json`. Aliases invoke the same Cobra handlers.
Shared flags `--help`, `--json`, `--refresh`, `--work-dir` are omitted: they affect
help/output/read freshness/project scope, rather than the target payload.
Inventory is an enumeration; behavioral evidence appears below.

| Command | Command options | Aliases |
| --- | --- | --- |
| `td approve` | `--all`, `--comment`, `--decision`, `--message`, `--note`, `--notes`, `--reason`, `--record-only`, `--reviewed-by`, `--self-review` | none |
| `td block` | `--reason` | none |
| `td board create` | `--query` | none |
| `td board delete` | none | none |
| `td board edit` | `--name`, `--query`, `--view-mode` | none |
| `td board list` | none | none |
| `td board move` | none | none |
| `td board show` | `--status` | none |
| `td board unposition` | none | none |
| `td close` | `--admin`, `--comment`, `--message`, `--note`, `--notes`, `--reason`, `--self-close-exception` | `complete`, `done` |
| `td comment` | none | none |
| `td comments` | none | none |
| `td comments add` | none | none |
| `td config sync-priority-labels` | `--apply`, `--id` | none |
| `td config sync-state-labels` | none | none |
| `td create` | `--acceptance`, `--acceptance-file`, `--blocks`, `--body`, `--defer`, `--depends-on`, `--desc`, `--description`, `--description-file`, `--due`, `--epic`, `--label`, `--labels`, `--minor`, `--notes`, `--parent`, `--points`, `--priority`, `--tag`, `--tags`, `--title`, `--type` | `add`, `new` |
| `td defer` | `--clear` | none |
| `td delete` | `--force`, `--reason`, `--yes` | none |
| `td dep` | `--blocking` | none |
| `td dep add` | `--depends-on` | none |
| `td dep rm` | none | `remove` |
| `td due` | `--clear` | none |
| `td export` | `--all`, `--format`, `--output`, `--render-markdown` | none |
| `td handoff` | `--decision`, `--done`, `--message`, `--note`, `--remaining`, `--uncertain` | none |
| `td import` | `--dry-run`, `--force`, `--format`, `--mapping`, `--source` | none |
| `td last` | `--n` | none |
| `td link` | `--depends-on`, `--recursive`, `--role` | none |
| `td log` | `--blocker`, `--decision`, `--hypothesis`, `--issue`, `--result`, `--task`, `--tried`, `--type` | none |
| `td note add` | `--content` | none |
| `td note archive` | none | none |
| `td note delete` | none | none |
| `td note edit` | `--content`, `--title` | none |
| `td note list` | `--all`, `--archived`, `--deleted`, `--limit`, `--output`, `--pinned`, `--search` | `ls` |
| `td note pin` | none | none |
| `td note restore` | none | none |
| `td note show` | `--include-deleted` | none |
| `td note unarchive` | none | none |
| `td note unpin` | none | none |
| `td reject` | `--comment`, `--message`, `--note`, `--notes`, `--reason` | none |
| `td reopen` | `--reason` | none |
| `td restore` | `--reason` | none |
| `td review` | `--comment`, `--message`, `--minor`, `--note`, `--notes`, `--reason` | `finish`, `submit` |
| `td start` | `--force`, `--reason` | `begin` |
| `td unblock` | `--reason` | none |
| `td undo` | `--apply`, `--list`, `--reconcile` | none |
| `td unlink` | none | none |
| `td unstart` | `--force`, `--reason`, `--session`, `--stale` | `stop` |
| `td update` | `--acceptance`, `--acceptance-file`, `--append`, `--blocks`, `--body`, `--comment`, `--defer`, `--depends-on`, `--desc`, `--description`, `--description-file`, `--due`, `--labels`, `--note`, `--parent`, `--points`, `--priority`, `--sprint`, `--status`, `--title`, `--type` | `edit` |
| `td ws current` | none | none |
| `td ws end` | none | none |
| `td ws handoff` | `--continue`, `--decision`, `--done`, `--remaining`, `--review`, `--uncertain` | none |
| `td ws list` | none | none |
| `td ws log` | `--blocker`, `--decision`, `--hypothesis`, `--only`, `--result`, `--tried` | none |
| `td ws show` | `--full` | none |
| `td ws start` | none | none |
| `td ws tag` | `--no-start` | none |
| `td ws untag` | none | none |
| `td epic create` | `--acceptance`, `--acceptance-file`, `--blocks`, `--body`, `--defer`, `--depends-on`, `--desc`, `--description`, `--description-file`, `--due`, `--epic`, `--label`, `--labels`, `--minor`, `--notes`, `--parent`, `--points`, `--priority`, `--tag`, `--tags`, `--title`, `--type` | none |
| `td task create` | `--acceptance`, `--acceptance-file`, `--blocks`, `--body`, `--defer`, `--depends-on`, `--desc`, `--description`, `--description-file`, `--due`, `--epic`, `--label`, `--labels`, `--minor`, `--notes`, `--parent`, `--points`, `--priority`, `--tag`, `--tags`, `--title`, `--type` | none |

## Dispatch and behavioral evidence

The [compound writer audit](github-writer-audit.md) and
[protocol matrix](github-batch-writers.md) identify the actual production routes,
not just CLI registrations. The following option groups were additionally
compared with their handlers at this revision:

- `gh_issue.go` accepts the captured create/update inputs. Rich text/file aliases
  share input parsing; date/point/priority/type validation precedes execution.
  Scalar-only, comment-only, relation and workflow combinations select their
  complete command protocols. Tests: `TestGitHubMixedUpdatePreflightsAllInputs`,
  `TestGitHubMixedUpdatePreservesValidCombinedEffects`,
  `TestGitHubCreateUpdateRelationsReplacePreflightAndPartialFailures`, and the
  `TestGitHubJournalCLIUpdate*` / `TestGitHubJournalCLICreate*` suites.
  `task.go` and `epic.go` delegate typed creation to the same live `createCmd.RunE`
  after setting the fixed type and rejecting conflicts. Their full create flags
  are included above; `TestGitHubTypeShortcuts` and the shortcut conflicting-type
  tests cover this additional canonical entry point.
- `gh_transitions.go` accepts the captured transition options, including reason
  aliases, `--all`, claim selection, review attribution/record-only and admin
  exceptions. These remain subject to the shared review policy; accepting a flag
  does not grant approval authority. Tests: `TestGitHubTransitionRoutingAndFocus`,
  `TestGitHubJournalWorkflowApprovalPreservesReviewOptions`, grouped transitions,
  native closure, claim sweep and workflow recovery CLI tests.
- `gh_delete.go` accepts reason/force/yes and validates every target before
  planned deletion/restoration. Tests: `TestGitHubJournalCLIPlannedRejectDeleteRestore`
  and `TestGitHubJournalCLIPlannedCommandsValidateAllTargetsBeforeWrites`.
- `gh_relationships.go`, `gh_files.go` and `gh_schedule.go` preserve the captured
  graph/file/date options. `link --depends-on` selects dependency linking and
  rejects file-role/recursive/pattern combinations. Tests:
  `TestGitHubRelationshipCommandsGraphAndBatchPreflight`,
  `TestGitHubFilesGlobRolesStatusAndPartialCommentFailure`,
  `TestGitHubScheduleCommandsFlagsAndPartialFailure`, dependency batch and
  update/activity whole-command tests.
- `gh_activity.go` explicitly accepts log target/type flags and handoff sections
  plus message aliases. Native comments and structured log/handoff entries remain
  distinct. Tests: `TestGitHubActivityLogTargetsAndTypes`,
  `TestGitHubCommentsSeparateNativeAndStructuredHistory`,
  `TestGitHubHandoffInputAndSnapshot`.
- `gh_ws.go` and `gh_ws_activity.go` separate local membership/focus/end state
  from remote tagged-task starts/logs/handoff/review. `--no-start` is local only.
  Tests: `TestGitHubWorkSessionCLIStartTagEndAndIsolation`,
  `TestGitHubJournalCLIClaimSweepAndWorkSessionGroups`,
  `TestGitHubJournalCLIWorkSessionActivityGroup`,
  `TestGitHubJournalCLIHandoffReviewWholeCommand`.
- `gh_boards.go` journals shared board create/edit/delete/order changes. `note.go`
  opens `pkg/notes` using the configured store; its GitHub client verifies the
  current actor and enables tracking. Note title/content/delete/restore/pin/archive
  changes use observed native carrier updates. Tests:
  `TestGitHubBoardInvalidDraftsBeforeNetwork`,
  `TestGitHubNoteAllCommandsFiltersAndIsolation`,
  `TestGitHubNoteEditorConflictAndRemoteChange`, entity update/undo and builtin
  initial-settings tests. Monitor view/last-visited settings remain local.
- Label repair uses tracked group updates. State metadata remains authoritative;
  priority repair has explicit preview/apply/ID scope. Tests:
  `TestGitHubJournalCLILabelRepairGroup`,
  `TestGitHubPriorityLabelSyncCLIPreviewApplyAndRepeat`.
- Task import uses its guarded creation/update/relationship executor, including
  force/resume; journal import is a separate passive archive protocol. Dry-run
  does not write. Journal mapping is accepted only for journal import, and
  force/source are rejected for that format. See [import command acceptance](github-import-command-journal.md)
  and [portable journal acceptance](github-portable-journal.md).
- `gh_last.go` uses complete raw `ActionHistory`, collapsing owned effects only
  for CLI presentation. `undo --list` reads; `--reconcile` observes outcomes and
  `--apply` permits receipt publication without replaying uncertain target writes.
  Tests cover latest-action selection, invalid option combinations, current
  session/author fences, native target conflicts and missing/changed receipts.

HTTP/monitor production factories use the same tracked client with their actual
scoped actor. Their package-level race regression checks production creation,
edit, workflow/cascade, comments, dependencies, deletion, board/note mutations,
uncertain outcomes and retired sessions. It does not certify physical GUI drag
acceptance or add HTTP/monitor undo controls.

## Explicit exclusions and alternatives

GitHub's documented [comment API](https://docs.github.com/en/rest/issues/comments)
allows creation with body text, body updates and deletion; it offers no parameters
to restore a deleted comment's original native ID, author or timestamps. td keeps
original identity/body/clocks in the journal and reports exact restoration as
unsupported. Reposting text creates a new comment with new native identity.

The documented [issue event API](https://docs.github.com/en/rest/issues/events)
provides list/get operations, without an event-deletion endpoint. Compensation
may restore supported current fields but cannot erase native event history. td
retains audit provenance rather than claiming a pristine historical rollback.

Creation undo uses the documented logical-delete alternative; it cannot safely
unallocate the original native issue number. Legacy untracked actions lack
verified before-state and cannot be retroactively treated as executable receipts.
Portable archive restoration preserves source evidence and bindings but does not
make source receipts executable against destination native objects.

Device-local session/focus/worksession membership and preferences, repository
configuration, and GitHub repository/collaborator administration are outside the
remote task journal undo contract. General support of those commands remains in
their own task contracts. SQLite schema migration remains SQLite-specific.

GitHub writes are not atomic multi-object transactions. Conflicts, permissions,
capacity and uncertain outcomes remain explicit errors with actual operation
identity/partial effects. No automatic uncertain mutation replay is authorized.

## Current validation

The full `internal/serve` and `pkg/monitor` race suites passed at c609f64
(255.881 s and 174.127 s). Focused ghstore history/undo race regression passed (23.675 s), and final CLI
history/undo/options race regression passed (60.146 s). Broad command writer
race regression and exact-commit Go CI are still running.

Native acceptance found a concrete remaining defect: two disposable tasks
`gh-208` and `gh-209` were started together, then updated together with fields
and inline comments. Update undo succeeded (269.412 s), restoring the exact
native issue number/title/body/state/labels/creation clock/author observed after
start. The next undo, targeting the earlier start group, failed with a conflict
on `gh-208` despite that exact restored content. It made zero target writes.
The basic group inverse currently rejects causal revision bridging, so the
verified inverse receipt is not sufficient to undo the preceding group.
This is an implementable support gap, not a GitHub API exclusion. Original
observations and journal receipts remain intact for the mandatory bug
fix [#84](https://github.com/yoophi/td/issues/84). No uncertain write was retried, and the fixtures remain logically visible
for diagnosis rather than being silently cleaned up.

The #84 correction verifies a completed exact-restoration group as one causal
revision edge using its immutable root and full effect receipts. It checks
same native author/session/carrier, original effect digests/order, all verified
inverse states, and the final restored native revision. Individual group effects
never independently authorize unrelated inverses. Original snapshots are not
rewritten. Receipt-only observations and native compensations that retain new
audit content do not become exact-restoration edges. Comment deletion and any
recorded native-event/review proofs are checked again at write fences.

Native #84 acceptance resumed the preserved sample `gh-208`/`gh-209` only after
confirming that every field and clock still matched the previous verified update
inverse. The corrected earlier start inverse succeeded (151.439 s), retaining
native identity/creation/author and restoring the exact original title/body/state/
labels. No SQLite file was created. The call used 80 API invocations/224 HTTP
responses, including 8 writes; it read repository collections without per-task
history enumeration. Repeated verification cost remains #73, not a claim of
optimized request volume.

CLI regression includes the original sequential command pattern and refuses
foreign fields/clocks, missing effects, foreign authors and observed-only inverse
proofs without target or journal writes. Native event changes that advance the
issue clock also remain conflicts; review/event baselines, where recorded, keep
their existing stricter event checks. Final-source sequential CLI race regression
passed (155.798 s), the full ghstore race suite passed (203.855 s), and Linux
lint reported zero issues. The original native fixture comments are absent;
both fixtures were logically deleted with their native IDs/history retained.
The broader command-writer race suite and CI are still running as #68 gates.
This record does not declare #68 or #9 complete.

Further native-pattern regression found a separate paired-command gap tracked
in [#85](https://github.com/yoophi/td/issues/85): two successive grouped field/
comment updates could undo the second command, but the first command's observer
still rejected the restored native clock. The real CLI regression failed before
the correction (13.16 s). The paired observer now verifies the same whole-command
receipt chain against its fresh snapshot and rechecks that chain at each inverse
write fence. Only saved-result comparisons use the original expected clocks;
source receipts and current target observations are not rewritten.

The sequential CLI race suite passed (321.827 s), including both the basic-group
and paired-command patterns and their foreign-field/clock, missing-effect,
foreign-author, observed-inverse and native-event rejection cases. The full
ghstore race suite passed (203.673 s), and Linux lint reported zero issues.
The handoff/review cross-command race regression passed (31.650 s). A separate
`due` then field/comment update sequence failed at the earlier command inverse
(13.73 s): scheduling/file-link commands use the distinct update/activity
observer, not the paired-command observer. This implementable gap is registered
as [#86](https://github.com/yoophi/td/issues/86) and blocks #68. Its failing real
CLI regression is preserved for the next task rather than claimed as fixed.

Existing paired-command recovery regressions and native acceptance on disposable
sample tasks `gh-210`/`gh-211` are running. The native harness stopped after a
successful first update because it expected a single JSON object instead of
multiple JSON rows. An explicit continuation freshly verifies that completed
command and resumes at the second update without replaying creates or the first
update. These are pending gates, not claimed successes; #85 remains open.

Sequential inverse recovery also passes the race regression (68.883 s): after
the later command has been undone, an earlier inverse's deleted-comment reply
loss or completion-receipt denial is reconciled without changing native targets
again, and its remaining inverse restores the original fields/comments.
Native acceptance completed the later command inverse (271.229 s), restoring
the exact first-command native fields/identity/creation/author. The earlier
command inverse remains in progress; neither full native acceptance nor #85
completion is claimed yet.

Final native #85 acceptance passed: the earlier field/comment command inverse
succeeded (282.809 s), restoring both sample tasks' original native number,
title/body/state/labels, creation clock and author. Both commands' inline
comments are absent and no SQLite file exists. The two inverses each used
135 API invocations/391 HTTP responses, including 14 writes and zero per-task
comment-list calls; repeated complete verification cost remains #73.
All sequential CLI, paired writer/recovery, cross-command review, full ghstore
race and lint gates listed above passed. Production-code CI run 38153133506
has passed lint/cross-build; its broad test job remains a #68 gate. Required
dedicated update/activity sequential support remains #86, so neither #68 nor
#9 is complete. Disposable sample tasks are being logically deleted while
retaining native IDs and journal history.

The #85 sample tasks were logically deleted in one successful command (66.430 s),
then confirmed with a fresh repository collection read. Its cleanup harness
misread the command's JSON array after the write; corrected read-only validation
confirmed both deletions without replaying them. #85 is closed after genuine
self-review, and its completion notification was sent.

## Dedicated scheduling/file activity sequence (#86)

The real CLI reproduction failed for all four commands `due`, `defer`, `link`
and `unlink` before the correction (34.279 s). Their dedicated update/activity
observer now retains the fresh full history and verifies completed-command
bridges at observation and every inverse write fence. Comment preflight and
member planning permit a revision conflict only through that verified evidence.
Original observations/receipts remain immutable. Completed update/activity
witnesses also validate original forward digests and recorded native events.

All four original CLI sequences passed after the correction (44.521 s). The
reverse ordering, where a scheduling/file activity command follows a field/
comment update, passed the race regression for all four commands (82.014 s).
Sequential partial inverse recovery passed both reply-loss/receipt-denial
scenarios without native mutation replay (59.363 s). Linux lint reported zero
issues. The original scheduling/file sequence and foreign-change/missing-effect/
observed-proof rejection race passed (207.670 s), and full ghstore race passed
(210.459 s). Existing update/activity writer recovery regression and native
acceptance remain pending gates. #86 is still in progress; this record does
not claim #68 or #9 complete.

Final #86 validation passed: existing update/activity writer/partial-recovery
race regression passed (284.009 s). On native sample `gh-212`, `due` followed by
a field/comment update completed both inverses (162.464 s and 168.946 s).
Fresh repository collection reads confirmed exact original native fields,
identity, creation clock and author; both companion activities are absent and
no SQLite file exists. Cleanup logically deletes the disposable task while
preserving its native ID/history. Production-code CI run 38155255139 remains
part of the broader #68 gate rather than a claimed completed test job.

The #68 broad CLI race run exhausted its 60-minute package budget when
`UpdateRelationsRecovery` began. Its unfinished tests were split into groups.
One mixed-workflow test hit its default 30-second child CLI budget both in the
group and in isolation. With the existing workflow-fixture budget of two minutes,
its review update completed in 32.331 s and all original assertions passed
(race suite 81.471 s). Only this test's budget/logging changed; production
behavior and semantic assertions were retained. The remaining group and final
source CI are still pending #68 gates, not broad claimed successes.
