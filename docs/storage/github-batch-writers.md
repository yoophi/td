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

Claim preview and `ws tag --no-start` do not create a mutation group. A changed
selection is rejected before journal bootstrap or task writes. Single-target
paths retain the existing standalone journal. A later failure leaves earlier
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

## Remaining writer integration

| Writer | Current effects requiring an aggregate |
|---|---|
| Multi-ID update with relationships/status/comments | Mixed task, relationship, workflow and activity effects; scalar-only batches are connected above |
| Create with relationships | Creation followed by edits to the new task and related tasks; new GitHub ID must be bound to its actual creation result |
| Multi-edge dependency add/remove | Repeated updates to the same source task |
| `ws log` / `ws handoff` | Activity comments on multiple tagged tasks |
| `ws handoff --review` | Activity fanout followed by task review requests |
| File link/unlink | One whole file-set update followed by an automatic activity comment |
| Due/defer | Task schedule update followed by an automatic activity comment |
| State/priority label repair | Multiple selected task repairs, including closed/deleted tasks |

These remaining paths need a shared mixed-effect contract, including complete
command inputs fixed before the first write, original target identity, partial
completion, inverse conflict checks and no replay of completed effects. Their
individual receipts are not evidence of command-wide undo support. HTTP/monitor
adapter coverage and the complete command/flag inventory must be checked against
#31 before closing #65/#68.

Individual uncertain activity creations now support receipt-only reconciliation
with explicit v18 observer provenance. This supplies the missing recovery proof
for a future activity group; the work-session and mixed writers above still
need their aggregate membership, execution and inverse checks.

Board/note operations currently mutate one entity per command; rendering and
filter loops do not constitute batch writes. Session cleanup and work-session
start/end/tag removal change device-local JSON; remote journal undo must not
claim to restore those local records. Portable history and multipart storage
remain #66/#67. In particular, a large closure watch set can exceed the current
single-comment limit even when only one task would actually change.
