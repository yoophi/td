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
| Multi-ID review and review hierarchy | Review group | Existing focus rules |
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

## Remaining writer integration

Mixed `update` commands preflight all distinct live selected tasks, field payloads,
status requests (including blocked/foreign claim restrictions), individual
relationship requests and comment publication sizes before their first write.
Selection uses one fresh paginated issue collection rather than one GET per
selected task; relationship validation may require additional graph reads.
Parent-only batches use the relationship group above. Single-task inline
comments also receive the size preflight.

For updates involving status, this preparation does not persist a mixed command plan or predict cascade,
review-policy or native-write results. Execution still reads and fences actual
states, applies relationship changes before evaluating review policy, and
reports partial results on later operational/policy failures. Shared reverse
dependency targets retain actual earlier effects. Complete command recovery and
undo for these paths remain unfinished, as listed below.

| Writer | Current effects requiring an aggregate |
|---|---|
| Update involving status, with or without relationships/comments | Mixed task, relationship, workflow and optional activity effects; field/comment and relationship groups are connected above |
| Create with relationships | Creation followed by edits to the new task and related tasks; new GitHub ID must be bound to its actual creation result |

These remaining paths need a shared mixed-effect contract, including complete
command inputs fixed before the first write, original target identity, partial
completion, inverse conflict checks and no replay of completed effects. Their
individual receipts are not evidence of command-wide undo support. HTTP/monitor
adapter coverage and the complete command/flag inventory must be checked against
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
