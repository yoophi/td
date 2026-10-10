# GitHub action journal implementation

This work belongs to #9. The codec, bulk reader, inverse planner and an opt-in
issue-update write protocol are present. `last` and `undo --list` have GitHub
dispatch. CLI create/update (including creation shortcuts and the edit alias)
now request action tracking with the real device-local td session. Their issue
writes use durable intent and verified result protocols; carrier bootstrap is
lazy, after input and target validation. CLI transition, logical deletion,
dependency, schedule, file and label-repair writers also request tracking.
Tracked activity creation now records v2 receipts. HTTP/monitor writers,
comment deletion/note/board write protocols and coordinated
workflow/graph undo remain pending.
`td undo` executes supported scalar and own comment-creation inverses. This document is
an implementation boundary, not an acceptance or completion declaration.

An issue journal is a versioned `td:action:v1` block on a repository carrier's GitHub
comment. New tracked updates use a dedicated `entity_kind=journal` issue, excluded
from task lists. Its comment writes are separate from the target issue, so the
target's observed update time does not need to be ignored or refreshed after
journaling. Optional `carrier_id` identifies this distinct carrier; records
without it retain the original per-target comment format. The record identifies
the operation, session, target issue, action and phase, and
preserves native title, complete body (including td metadata), native state,
labels and observed update time. Native `open` can represent td `in_progress`;
the raw body and native state are preserved separately from display fields.
Comment identity, author and clocks come from the GitHub response, not an
authored JSON field. Sessions are attribution, not independent authentication.
Checksums detect inconsistent state data; they are not signatures or atomic
GitHub compare-and-swap tokens.

| Phase | Meaning |
| --- | --- |
| prepared | Recorded intent; execution/result has not been verified. |
| applied | After-state verified by the eventual write executor. |
| undo_prepared | Recorded undo intent; outcome not yet verified. |
| undone | Verified restored-state and distinct undo identity recorded. |

The parser rejects unknown versions/fields, inconsistent checksums, mismatched
issue identities, missing before/after data, invalid phase/receipt combinations
and edited visible text. Ordinary activity exposes a concise `action_journal`
summary instead of displaying full before/after bodies as native comments.
This format's checks cannot establish who executed a manually forged action;
execution must validate the actual GitHub author and session before undo.

`td last [-n N]` and `td undo --list` read current-session journals from one
complete issue collection and one complete comment collection; they do not
enumerate comments per issue, write GitHub or open SQLite. `last` defaults to
one entry and requires positive N on GitHub; `undo --list` displays up to ten.
Both reject unused positional arguments. SQLite retains its existing backend
and action output. Human output labels prepared/applied/undo-prepared/undone;
creation without an allocated target displays `new issue`.

JSON retains the established array shape, including `[]` for empty history.
Rows include common action-log identity/session/action/entity/snapshot/time/
undone fields plus operation, phase, carrier, GitHub author/URL and explicit
history/data scope. Before/after data is native issue state JSON, not a SQLite
Issue snapshot; `data_format=github-native-issue-state-v1` identifies it.
Prepared results have no verified after snapshot. A scope note on stderr
explains that legacy/native changes are not reconstructed and coverage across
all writers and workflow/multi-target undo is incomplete. Empty history must not imply that no
older actions occurred. Actual undo selects the newest current-session action
that is not already undone from a fresh bulk observation. A prepared or
undo-prepared action stops execution with a conflict error; it is never skipped
to undo an older action. Creation returns an explicit logical-delete alternative.
Unsupported scope maps to `unsupported_operation`; stale target state maps to
`conflict`. Other failures retain the original error. No SQLite fallback occurs.

Successful JSON undo identifies target, original/undo operation, receipt and
verified issue. Human output reports UNDONE only after receipt verification.
With no remaining tracked action, JSON reports `action=no_tracked_actions` with
session and explicit history scope; it does not claim there were no legacy
changes. Repeating the command after undoing the sole tracked action writes
nothing. Sequential scalar undo uses verified receipt chains: an undone action's
exact before revision links to its restored revision only when the restored
content equals the before content and the action belongs to the same GitHub
author, td session, target and carrier. The selected receipt must agree with a
fresh complete bulk observation. Missing/ambiguous edges, unfinished outcomes,
cycles, changed content and later native edits remain conflicts, even when field
values have returned to their earlier values.

The executor reads complete history only when the original revision guard
fails. Before the target PATCH it bulk-reads history again to verify every used
receipt and reject newly ambiguous edges, then rechecks the target revision.
These are two repository issue/comment collection reads, rather than per-issue
history requests. Original action snapshots are never rewritten to excuse a
changed server clock. REST writes still lack atomic compare-and-swap; existing
readback and uncertain-result limits continue to apply.

`Snapshot.ActionHistory` consumes the existing bulk issue/comment observation,
including note/board carriers and logically deleted issues. It makes no extra
network requests and validates duplicate operation identities before applying a
session filter. Tests exercise 60 mixed carriers with two paginated collection
invocations, not one comments call per issue. Pagination can require more HTTP
requests inside each gh invocation.

`PlanIssueUndo` checks current session and GitHub author attribution, target
identity and the observed after-state checksum. Later changes, including update
time changes with identical values, are conflicts. Label ordering is immaterial.
Prepared/undo-prepared outcomes require inspection and are never replayed as
successful actions. An existing undone receipt is an idempotent no-op only if
the current target still matches its verified restored-state. Creation has no
previous issue to restore: the planner explains explicit logical deletion as a
compensation, preserving issue identity/history.

The inverse planner performs no writes. Its payload alone is not safe to send:
the executor still needs current repository/permission validation, graph and
review constraints, another revision check immediately before PATCH, post-write
verification, durable intent/result receipts, and partial-failure reconciliation.
It must never overwrite later changes or mark an uncertain PATCH as successful.
Multi-target relation/cascade operations need their own per-target results and
failure handling. Creation, physically deleted comments/issues and external API
side effects need explicit compensation limits. Import/export preservation of
the new journal is also still pending. GitHub PATCH cannot provide an atomic
distributed transaction; this remains best-effort conflict detection.

## Opt-in issue update protocol

`Client.EnableActionJournal` validates session identity and finds one dedicated
carrier through the complete issue collection. With none present, it creates
one, verifies its response/readback and rescans for concurrent duplicate
creation. Multiple carriers fail explicitly; none is selected or deleted to
hide ambiguity. Changing a carrier into an ordinary task fails before any
intent/target write. Closing its native GitHub issue does not repurpose it.

On an enabled client, `UpdateObserved` keeps its existing input, policy, parent
and revision checks. It writes a prepared comment, validates the response and
readback, then rechecks the exact original target revision and parents before
PATCH. The intent never excuses a stale policy decision. Existing PATCH response
and target readback checks must succeed before writing an applied receipt. The
receipt's prior identity/content/update time and its PATCH/readback are checked;
the target is read again afterward to report later changes explicitly. An
unverified intent prevents a target write; an uncertain target or receipt write
returns an error and is never automatically retried. A verified target change
with a failed receipt is reported as a partial result, not ordinary success.

This adds explicit calls per mutation for carrier/intent/receipt verification;
it does not add per-issue calls to history listing. Large batches still need
cost/rate-limit testing and grouped partial-result handling. Fixture tests
simulate target permissions, intent permissions/readback failure, uncertain
target/receipt responses, concurrent target/journal edits, changed carrier
identity and duplicate bootstrap. Native GitHub acceptance remains pending.
Enabling is currently an internal API opt-in; CLI/HTTP/monitor writers have not
yet been switched. Unchanged clients keep their existing behavior. No claim of
full #9 support follows from this update-only protocol.

## Creation intents and result linking

An unallocated creation intent has `action=create`, `phase=prepared`, a distinct
journal `carrier_id`, an empty target `issue_id` and `create_input` containing the
exact title, metadata body and labels. It has no observed after-state or clock.
This is the only form allowed to omit target identity. After allocation and
response/readback checks, an applied receipt links the canonical issue number
and verified after-state. The operation identity in input/issue body/journal
must agree; the parser rejects after-state that disagrees with the input.

The enabled creation path scans complete issue/comment collections before
intent publication. A previously observed journal or issue with the same
operation identity blocks another POST and requires reconciliation. After POST,
response fields, label application, parent consistency and issue readback must
succeed. A fresh bulk scan checks for changed/duplicate operation results and
duplicate journal identities before recording an applied receipt. Concurrent
creates cannot be made atomic; an observed duplicate remains an explicit error
with both remote issues preserved for inspection.

Denied/lost creation responses, changed results, failed readback and receipt
failure never become ordinary success and are not automatically retried. A
prepared intent can outlive an issue POST that reached GitHub. A retry with the
same explicit/deterministic operation identity is rejected without another
intent or issue POST. Ordinary `create` generates a new identity, so manually
repeating the command is a new operation, not an automatic reconciliation of
the previous one. Inspect the failed attempt's printed identity first. Explicit
reconciliation and persistent CLI retry identity are still implementation work;
this protocol does not claim safe retry of arbitrary repeated command text.

Creation undo still cannot erase an issue safely; the planner describes logical
delete compensation. These library tests do not prove creation is already
journaled by the CLI/HTTP paths. Import/export transport of the new creation
intent/receipt and high-volume write cost validation also remain pending.

## Scalar undo executor

`Client.UndoAction` executes a checked inverse for single-issue scalar updates.
It rehydrates the comment by ID, validates its repository/carrier and original
identity, reads the authenticated GitHub `/user` login, and requires matching
session/author attribution. It verifies the current target against the recorded
after-state. An already-undone receipt returns a no-op only if the target still
matches its verified restored-state. Later edits remain conflicts.

The executor records/readbacks `undo_prepared` with a distinct operation ID,
rechecks the original target revision, PATCHes the exact before values, verifies
response fields/labels and target readback, records/readbacks `undone` with
restored state, and rechecks the target after recording the receipt. Receipt
updates cannot alter original snapshots/identity or skip the expected phase
transition. Failures never cause automatic target retries or success without a
verified receipt. A lost receipt response can be reconciled by reading an
existing valid undone receipt; a still-prepared result requires explicit
inspection/reconciliation and cannot be declared done merely because fields
look like the old values.

Current scope includes title, visible description, acceptance, type, priority,
points, labels and minor/sprint/date scalar data when workflow/graph/review data
remains unchanged. Native state changes, session/review/transition/history or
parent/dependency/file changes, note/board entities and multi-target effects
still require coordinated undo integration. They return an explicit scope
error before target writes. Legacy per-target journals also need separate-carrier
migration before execution, avoiding receipt writes on the target itself.
Creation retains the explicit logical-delete alternative. These are pending
implementation boundaries, not SQLite-only exemptions or proof of #9 completion.

The scalar executor is connected to CLI `undo`, with latest-action selection,
scope/conflict error mapping and subprocess command tests in both feature modes.
HTTP/monitor/remaining CLI write journaling, workflows/cascades, recovery tooling
and portable journal transport remain required work.

## Default create/update command tracking

`Client.TrackActions(sessionID)` assigns a validated real actor session without
remote calls. `EnableActionJournal` then initializes the carrier when a validated
issue create/update reaches intent preparation. Invalid input and stale target
observations cannot bootstrap a carrier. No read-only command requests tracking.

CLI create/update now resolve the local td session even for scalar edits. They
request tracking once per invocation, so root edits and creation shortcuts share
the same protocol. Underlying issue updates during relation/status changes also
leave individual before/after receipts, but this does not make a multi-target
operation atomic or enable its coordinated undo. Comment-only updates still
produce activity comments rather than action journals; this remains required
work, together with other command and HTTP/monitor writers.

Failed intents stop before a target write. Denied/lost target or result receipt
operations retain their error and never report a verified success or retry
automatically. A target request failure identifies the operation and journal
comment to inspect while retaining the original error. A saved prepared journal
records an uncertain outcome; it is not a completed action. A lost response to a saved applied receipt can be reconciled
by inspecting fresh history, rather than repeating the mutation.

## Comment-create schema and readers (v2)

`td:action:v2` currently defines `action=comment_create`. It stores exact
`comment_input.body` for a canonical td activity with the same session,
plus verified `comment_after` with repository-local `ghc-N` identity, canonical
issue membership, complete raw body, native author, created/updated clocks and a
consistency checksum. This checksum is not authentication. The containing
journal's native GitHub author must match the recorded comment result author.
The carrier must be distinct, and issue create input cannot be mixed with comment
input. Optional `comment_input.operation_id` identifies the activity operation;
without it, older v2 records use the journal operation. Work-session fan-out
shares an activity operation across targets while each journal has its own ID. A prepared intent has neither an issue after-state nor a comment result.
Later phases require both. Comment creation may advance the issue clock but
cannot change its title/body/state/labels; neither resulting nor restored clocks
may move backwards.

The common decoder accepts v1 issue and v2 comment records together, rejects
payload/version mismatches and keeps unknown fields/versions as explicit errors.
Exact canonical serialization distinguishes nested raw activity markers in JSON
from a second top-level metadata block. The activity feed shows a concise journal
summary without leaking snapshot bodies. `PlanIssueUndo` rejects comment actions
with an explicit scope error so a comment receipt cannot become an issue PATCH.

`last` and `undo --list` retain `comment_input`, `comment_after`,
`target_issue_id` and `comment_data_format=github-native-comment-state-v1` in JSON.
Their common before/after strings remain native issue snapshots with the existing
`data_format`; entity type is comment, and the result ID is the native comment ID
(or `new comment` for prepared creation). Bulk observations require no added
per-comment queries. Actual CLI fixture tests cover v2 reads alongside v1 in both
feature modes, including preserved native body and author.

## Tracked activity creation

On a tracked client, `AppendActivity` validates activity input and actor scope
before carrier bootstrap. It records a separate prepared journal with the exact
issue before-state, input and independent journal operation. A fresh complete
bulk observation rejects an existing logical activity operation on the same
target; the same work-session operation on another target is allowed. The
original target revision is checked again after the intent, immediately before
POST. The response must have correct native membership, body, author and clocks;
its author must match the intent's native author. Direct comment readback,
unchanged issue fields with a nondecreasing clock, fresh bulk result uniqueness,
and applied receipt/readback are required. Comment readback is repeated after
the receipt so a later comment edit is not hidden by issue state alone.

CLI direct comment, comments add, log and handoff commands request tracking with
the actual context actor. Existing tracked schedule, file, update/review and
work-session activity paths share the same protocol. Local-only work-session
entries do not create remote records. Untracked library clients retain the
existing behavior until their writer integration supplies an actor; HTTP/monitor
integration remains pending. Any lost/denied/unverified POST or receipt returns
an explicit error with operation, target and journal identity, retaining raw
errors. Prepared uncertainty is never success or an automatic retry. A verified
receipt can precede an external later edit; the final conflict still returns an
error and preserves that edit. Neither uniqueness checks nor revision/readback
checks provide a GitHub distributed transaction or atomic reservation.

Actual CLI fixtures cover direct and automatic writes, two-target work-session
fan-out with distinct journal IDs, failed receipt after a saved native comment,
and refusal to undo an unfinished comment action. The fixture now
stores native comments, membership/author/clocks, direct reads and issue clock
changes. Separate library tests cover intent failures, stale targets, native
POST denial/loss, unexpected membership/author, denied/changed readback, duplicate
results, receipt failure/loss and a post-receipt external target edit. Fixture
success does not substitute for actual GitHub acceptance.

Comment-create undo now has a separate guarded DELETE executor. It requires the
same td session and native GitHub author, exact issue revision and exact native
comment ID/body/membership/author/clocks. An undo intent is saved and read back,
then both objects are rechecked before DELETE. Two fresh complete collections
verify absence of both the native comment ID and its logical operation, around
the verified completion receipt. Issue contents must stay unchanged and the
clock must not move backward. Completed replay rechecks absence and the exact
restored issue revision without another DELETE. Unfinished intents, lost DELETE
responses and denied/unverified receipts return errors without automatic retry.

Own log/handoff creation can also be undone through this protocol: their complete
original native body, author, timestamps and target remain in the immutable
journal for audit. Generic manual comment deletion still protects log/handoff
activities. This does not implement restoring a previously deleted comment:
recreation changes its native ID, author attribution and timestamps. GitHub
DELETE has no atomic compare-and-delete contract here; checks detect observed
changes but cannot prevent an edit in the gap between the final read and DELETE.

Sequential field undo after activity creation still requires comment inverse
receipts with exact causal links; the issue clock is never ignored. Comment
deletion journaling, note/board state, coordinated policy/graph inverse operations,
HTTP/monitor writers and portable journal transport remain required work.
