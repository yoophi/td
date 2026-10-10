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

Claim preview and `ws tag --no-start` do not create a mutation group. A changed
selection is rejected before journal bootstrap or task writes. Single-target
paths retain the existing standalone journal. A later failure leaves earlier
actual effects and the group intent available for explicit reconciliation.

## Remaining writer integration

| Writer | Current effects requiring an aggregate |
|---|---|
| Multi-ID reject | Individually journaled review rejection |
| Multi-ID update | Task fields, relationships, status and optional comments |
| Create with relationships | Creation followed by edits to the new task and related tasks; new GitHub ID must be bound to its actual creation result |
| Multi-edge dependency add/remove | Repeated updates to the same source task |
| Multi-ID delete/restore | Individual logical deletion/restoration updates |
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

Board/note operations currently mutate one entity per command; rendering and
filter loops do not constitute batch writes. Session cleanup and work-session
start/end/tag removal change device-local JSON; remote journal undo must not
claim to restore those local records. Portable history and multipart storage
remain #66/#67. In particular, a large closure watch set can exceed the current
single-comment limit even when only one task would actually change.
