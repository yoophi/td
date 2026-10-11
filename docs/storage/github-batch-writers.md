# GitHub compound writer coverage (#65)

A command group fixes the selected task membership and observed revisions before
its first task write. Each attempted effect stores its complete input before
that effect's write. Recovery observes actual outcomes without replaying writes;
undo uses the existing group conflict checks and preserves uncertain provenance.
This is not an atomic GitHub transaction or a rollback of device-local context.

## Connected transition writers

| Writer | Group behavior | Local state |
|---|---|---|
| Multi-ID start/unstart/block/unblock | Basic transition group | Existing focus rules |
| Single/multi-ID review and review hierarchy | v29/v30 workflow group includes automatic handoffs and actual hierarchy effects | Existing focus rules |
| Close/approve/reopen and closure cascades | Ordered v14/v15 effects (#64) | Existing focus rules |
| Forced `unstart --session` / `--stale` with multiple selected claims | Basic unstart group, bound to original selected revisions | Local liveness rechecked; focus cleared on actual release |
| `ws tag` auto-starting multiple open tasks | Basic start group, bound to original selected revisions; one captured Git snapshot | Tags remain device-local, including after remote undo |
| Multi-ID `update` with scalar fields only | v16 group with every member's complete input fixed before the first write; existing v11 effect receipts | No focus change |
| Multi-ID `reject` | v17 planned independent task group; existing review and inverse checkpoint checks | Focus cleared after actual rejection/no-op, as before |
| Multi-ID `delete` / `restore` | v17 planned independent task group; native open/closed state retained; deleted tasks included in selection | Existing device-local context |
| Multi-target `ws log` / `ws handoff` without `--review` | v19 activity group fixes all complete inputs; partial recovery observes comments without replay; undo checks and removes actual results | Tags, history membership and end state remain local |
| `config sync-state-labels` / `config sync-priority-labels --apply` with multiple repairs | v17 planned scalar `update` group fixes every repair's complete labels and original revision, including closed/deleted tasks | No local context change |
| Multi-edge `dep add` / `dep rm` (`remove`) | One source PATCH and one existing update receipt for the full prevalidated delta; shared reachable targets are observed once per fence | No local context change |
| File `link` / `unlink`, `due` / `defer` | v21 update/activity group freezes both complete inputs before writes; v22 members bind the actual field result to the following activity | File commands retain their local path/hash checks; schedule activities retain work-session attribution |
| `ws handoff --review` | v23 command fixes all handoffs and review templates; v24 ordered effects bind each review to its actual preceding handoff | Tags, history membership and work-session end state remain device-local |
| `update` with scalar fields and `--comment` / `--note`; multi-ID comment-only update | v25 command fixes every selected task's optional field input and complete comment; v26 effects bind comments to actual preceding field results | No local context change |
| `update` with parent/dependency changes, optional scalar fields and inline comments, without `--status` | v27 command includes selected tasks and shared reverse-edge targets; v28 effects bind graph observations to actual earlier results | No local context change |
| `update` with `--status`, optional fields/relationships/inline comments | v29 freezes complete requested inputs and original graph membership; v30 receipts bind actual field, workflow/cascade and comment results | Focus changes only for completed selected roots |
| Monitor edit form | One observed command for all fields, dependencies and an actually changed status; stale forms fail before writes | Draft and error handling remain local |
| CLI create with `--depends-on` / `--blocks`; monitor creation with dependencies | v31 creation command freezes complete input and original graph before POST; actual allocation owns an optional v32 reverse-edge command | Monitor retains its draft, allocated ID and no-repeat guard on partial failure |

Claim preview and `ws tag --no-start` do not create a mutation group. A changed
selection is rejected before journal bootstrap or task writes. Single-target
transition paths retain the existing standalone journal when no cascade is
needed. Update/activity commands use their two-effect group even for one task.
A later failure leaves earlier
actual effects and the group intent available for explicit reconciliation.

Scalar update batches select tasks from one fresh bulk issue read and prepare
all payloads before journal bootstrap, label creation or task writes. They
support open and closed tasks, including per-task `--append` results. Missing,
deleted, duplicate or stale selections fail before any write. The group and
each member are sized before the first write. The immutable group, watched
task revisions and required native event baselines are rechecked after each
member intent, before PATCH and before completion. A partial result identifies
the original group receipt; reconciliation never replays an update.

The v16 inputs are requested contents, not fabricated future observations.
Existing monitors/servers must run a v16-capable build before reading a
repository with these new records. Activating a new executable does not replace
the decoder already loaded by a running process.

The v17 groups also declare which members already satisfy the command. These
no-ops retain their exact native revisions and never acquire effect receipts.
An invalid later target prevents the entire command from starting. Unknown or
changed inputs, changed selected tasks and replaced review evidence stop the
operation. Partial recovery observes a changed target without replay; undo
retains the original review/native-event checks and watches unchanged members.
Reject/delete/restore use the same preparation for standalone and batch paths.
Deletion permits native tasks with missing or older detail metadata and uses
effective native state for the visibility transition and labels. Inverse review
validation still rejects restoration of invalid historical review evidence.

Label repair batches use the existing v17 `update` primitive, without a new
journal schema. Selection and every per-task payload are prepared before label
setup, journal bootstrap or task writes. The priority preview and explicit-ID
validation remain read-only; already-correct tasks create no receipt. Both
mirrors follow authoritative metadata/native state, and custom labels remain
intact. A failed batch reports the verified repair count, original cause and
group receipt for reconciliation. Recovery observes actual results without
replaying repairs, and undo restores the original labels (including missing or
incorrect mirrors) without changing task metadata, native state or deletion.
Repository label definitions created during setup are not removed by undo.

Dependency batches validate all requested edges before journal or source writes.
A missing or invalid later target leaves earlier edges unchanged. One operation
ID and timestamp cover the actual transition delta. A lost response is reconciled
without replay, and one undo restores the entire command subject to the existing
source, graph and historical-review checks. This uses the existing relation
receipt format and does not introduce a journal schema. GitHub PATCH remains
optimistic; it is not a graph transaction or conditional-write guarantee.

Duplicate additions keep their per-argument `already_exists` results, including
the `--depends-on` form, without extra writes. Each successful JSON result refers
to the final source state, not a fabricated intermediate state. A completely
unchanged command creates no receipt. Removal can clean edges to missing/deleted
tasks; restoring such an invalid historical graph through undo is still refused.
GitHub `dep rm`/`dep remove` now accept multiple targets. SQLite retains its
existing exactly-one-target removal contract, documented in command help and
the captured CLI inventory.

Isolated `yoophi/td-sample` acceptance on 2026-10-11 verified two-edge additions
and removals with exactly one source PATCH per command, whole-delta undo, and
read-only duplicate additions/completed inverse confirmation. Before/after
native title, body, state and labels were compared independently. Request logging
captured only methods and endpoints. Lost-response recovery was exercised in CLI
fixtures, not by injecting a GitHub service failure. Stage evidence is recorded
on #65; this does not complete the full command/flag audit.

Update/activity commands record one original task and two ordered effects. The
first effect updates fields; only its verified actual result can establish the
comment's starting revision. No future GitHub clock or allocated comment ID is
invented in the plan. Group completion freezes digests of the original effect
identities, inputs, observations and results. `last` and `undo --list` collapse
the two effects into one command; direct member undo is refused.

Reconciliation observes uncertain field/comment outcomes and writes receipts
only. Undo first validates the complete field inverse, deletes only its exact
companion activity, then restores the fields. This command's observed comment
inverse may bridge its own field revision; it never becomes a causal witness
for unrelated earlier commands. Source, original receipts, native activity and
required review/native-event evidence are fenced. A partial inverse keeps its
original group/undo identity; completed effects are not replayed. Observed
outcomes remain weaker evidence even after later effects complete normally.

Native comment size is checked before any target or journal write. File no-ops
still create no activity or group. The v21/v22 semantic formats preserve the
existing v20 multipart manifest and bounded v9 compressed transport. Running
monitors/servers must be restarted with a build that understands v21/v22 before
reading repositories containing these records. Fixture tests cover complete and
partial forward/inverse paths, read-only confirmation, invalid activity input,
and foreign source/comment/original-receipt changes. Native acceptance and the
full command/flag audit remain separate verification gates.

`ws handoff --review` now preflights all handoff bodies and review inputs before
the first comment or journal write. It freezes review selection from the
original tagged-task observations, including tasks that receive only a handoff.
Review inputs fix attribution, transition identity, contents and native event
baselines. The new handoff's real comment ID and timestamp are bound only after
its journaled publication has been verified; a later handoff is not substituted.
Changed source revisions, edited/deleted handoffs and changed native state
events stop submission. A failed or uncertain effect is never replayed by the
same in-memory plan, and the work session stays active on partial failure.
Capacity probes reuse the captured event baseline without additional API calls;
execution still reads fresh evidence before a review write.
The v23 command persists these inputs before the first effect, including when
only one task is tagged. All handoffs precede all selected reviews. Each v24
review receipt must reference its own verified handoff receipt, actual native
comment ID and timestamp; another handoff cannot establish that edge. `last`
and `undo --list` collapse effects into one command. Direct member undo is refused.

Recovery observes uncertain results and writes receipts only; it never repeats
an issue PATCH, handoff POST or comment DELETE. Whole-command undo first checks
the remaining inverses, restores review states in reverse order, then deletes
only the command's exact handoff comments. Foreign task/comment changes,
replaced original receipts and changed native state events stop the operation.
A recovered review inverse can bridge only its own handoff clock; it cannot
serve as a causal witness for an unrelated command. Observed evidence remains
weaker than a verified target write. Device-local work-session state is not
restored by remote undo.

The v23/v24 semantics use the existing bounded v9 compression and v20 multipart
transport. Upgrade and restart every monitor/server reading this repository
before writing these formats; replacing the installed executable does not
upgrade an already-running process.

## Scalar updates with inline comments

Scalar fields with inline comments now use one command group, including a
single target. Comment-only updates use this group for multiple targets; a
single comment retains its standalone activity receipt. Each target's optional
field change precedes its own comment. Comment-only batches never issue a
synthetic field PATCH. Complete per-target inputs and archive sizes are checked
before the first write. `last` and `undo --list` expose the command as one entry.

Recovery records observed outcomes without repeating target writes. Undo
checks all remaining effects, then removes each exact companion comment and
restores fields in reverse command order. The command's own comment deletion
can bridge only its companion field revision. Foreign task/comment changes or
replaced original receipts stop the inverse. An observed recovery stays weaker
than a verified write, and completed confirmation is read-only. The v25/v26
formats use bounded v9 compression and v20 multipart transport. Upgrade and
restart readers before publishing these records.

## Relationship updates with optional inline comments

Relationship updates without `--status` use one command even for a single
selected task. The group includes all selected tasks and every reverse-edge
target whose dependencies change. It freezes the complete final delta and
writes each target at most once, combining its field and relationship changes.
Execution can differ from selection order to avoid temporary dependency cycles;
successful output retains the user's selection order. Inline comments belong
only to selected tasks and follow their own field change, when present.

All input, graph and archive checks run before target writes. The command keeps
original graph observations and reverse membership; a member may reference only
an original external target or an actual earlier result of this command. It
does not persist projected revisions. External watched revisions and new or
removed outside dependents remain conflicts. Unrelated tasks may change.

Undo preflights the entire reverse graph before deleting comments, then restores
actual effects in reverse order with fresh per-effect checks. Recovery observes
uncertain outcomes without repeating target writes. Original receipts and weak
observer provenance remain immutable. An unchanged relation-only request
does not create a journal or synthetic task write. The v27/v28 semantics retain
bounded v9 compression and v20 multipart transport; upgrade and restart running
readers before publishing them.

## Mixed workflow updates

Mixed `update` commands preflight all distinct live selected tasks, field payloads,
status requests (including blocked/foreign claim restrictions), individual
relationship requests and comment publication sizes before their first write.
Selection uses one fresh paginated issue collection rather than one GET per
selected task; relationship validation may require additional graph reads.
Parent-only batches use the relationship group above. Single-task inline
comments also receive the size preflight.

The v29/v30 workflow command persists complete fields, relationships, requested
status, transition options and inline comments before the first task write.
Its membership includes both original and requested hierarchies, shared reverse
dependency targets, potential cascade targets and their graph observations.
Requested graph projections never become observed revisions or future results.
Each effect binds to an actual earlier result of this command.

Fields and relationships are saved before workflow policy is evaluated. An
approval of older contents cannot close newly edited contents. A later policy
failure leaves a prepared command and any already-saved effects; the error names
the command receipt and partial result. Recovery records actual outcomes without
repeating writes. It does not finish the requested transition on the user's behalf.

Review commands include their exact existing handoff or a frozen automatic
handoff input. Undo may delete only an automatic handoff actually created by this
command, with its original native identity, contents and clock. Existing
handoffs survive. Ordered compensation restores task fields and workflow state,
preserves audit history and invalidates old approval/review authority. It checks
the complete inverse before the first write and rechecks each actual effect.
Foreign watched tasks, native comments, original receipts and close/reopen
events stop the inverse. Completed confirmation is read-only; observed recovery
never becomes proof of a verified write.

Tracked standalone review, HTTP review and monitor review use this same command
boundary. Monitor edits pin the form's original observation and omit an unchanged
status, so editing an in-review task does not silently resubmit it. Unchanged
basic status requests without field/activity effects do not create journals.

The v29/v30 semantics use bounded v9 compression and v20 multipart transport.
Upgrade and restart readers before publishing these records. Focused CLI tests
cover compound transitions, automatic/existing handoffs, parent/dependent
cascades, policy failures, uncertain writes and external conflicts; full release
checks and native acceptance are separate gates, not implied by this document.

## Creation with relationships

CLI creation with relationship flags and monitor creation with dependencies now
use the same command. Preparation captures the original graph in one paginated
issue collection, without one issue GET per dependency. Initial dependencies are
part of the creation POST. Only the actual allocated GitHub issue number can be
bound into reverse-edge inputs; original external observations remain unchanged.
The optional relationship child cannot be recovered or undone independently.
`last` and `undo --list` collapse the creation and its child into one entry.

Forward recovery may record a partial creation with unattempted reverse edges.
It never POSTs another task or adds missing edges. Known allocated IDs accompany
errors. Verified allocation evidence remains distinct from an observed outcome.

Whole-command undo first checks the source, original graph, native activity,
close/reopen events and every remaining relationship inverse. It restores actual
reverse-edge effects through the existing group protocol, records their real
completed receipts, then logically deletes the allocated task. GitHub identity,
native state and history survive. The original creation snapshots are immutable;
the inverse does not fabricate a before-state for an issue that did not exist.

Receipt recovery after a lost deletion response writes receipts only and retains
observed provenance. A pending child inverse can be reconciled before an explicit
`undo` continues unattempted effects. Once the deletion checkpoint was published,
an uncertain deletion is never automatically retried. If the task still has its
original live state, recovery reports an unverified outcome rather than claiming
completion or resending PATCH. Inspect that case manually. Completed confirmation
checks the source and saved external results without writes, including after the
source is hidden. Later task, comment, receipt or graph changes remain conflicts.

The v31/v32 formats use the existing bounded compression and multipart transport.
Upgrade and restart readers before publishing them. Fixture coverage includes
whole-command compensation, partial forward outcomes, uncertain child/deletion/
parent receipts, conflicts, and the monitor production client. Native acceptance
and the full release gates remain separate checks.

## Remaining acceptance and adapter audit

The import executor remains an incomplete batch writer in #65. The local CLI now
binds its existing capture, provenance, intent and completion protocol to one
current-session action command, with original-root resume, completed results and
receipt-only reconciliation. Whole-command compensation and same-ID reimport
now pass local CLI acceptance. Proven pre-PATCH failures retain cancellation
records and permit explicit resume; uncertain target writes are never replayed.
Inverse plans/results reuse immutable document parts across progress updates.
Portable imported history (#66) does not cover
journaling the import command's own allocations, destination overwrites, graph
detachment and native provenance comments. Do not close #65 by excluding this
path. Both import execution and preview now freeze and validate complete source
envelopes before the first remote observation; allocation, replanning, graph
projection and provenance retain those same inputs. Mixed overwrite/create and
graph-detachment failure runtime checks, the #65 writer audit and native
acceptance remain release gates, tracked as separate tasks under release
preparation epic #74. Capacity errors may stop with explicit original errors and
partial-effect disclosure; worst-case future result sizing is not a release
gate. See [the import command contract](github-import-command-journal.md).

HTTP creation currently accepts a parent but no dependency/block list, so its
existing single creation POST remains standalone. Legacy standalone creation
receipts retain their explicit logical-delete alternative. HTTP/monitor adapter
coverage and the complete command/flag inventory must still be checked against
#31 before closing #65/#68.

Individual uncertain activity creations support receipt-only reconciliation
with explicit v18 observer provenance. Work-session activity groups carry the
same observer provenance in v19 members. Mixed writers above still need their
aggregate membership, execution and inverse checks.

Board/note operations currently mutate one entity per command; rendering and
filter loops do not constitute batch writes. Session cleanup and work-session
start/end/tag removal change device-local JSON; remote journal undo must not
claim to restore those local records. Portable history remains #66. Multipart
storage (#67) now publishes large journals across verified part comments and a
manifest, including large closure watch sets; it does not complete the remaining
writer integrations above.
