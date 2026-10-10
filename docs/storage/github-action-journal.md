# GitHub action journal implementation

This work belongs to #9. The codec, bulk reader, inverse planner and an opt-in
issue-update write protocol are present. `last` and `undo --list` have GitHub
dispatch. CLI create/update (including creation shortcuts and the edit alias)
now request action tracking with the real device-local td session. Their issue
writes use durable intent and verified result protocols; carrier bootstrap is
lazy, after input and target validation. CLI transition, logical deletion,
dependency, schedule, file and label-repair writers also request tracking.
Tracked activity creation now records v2 receipts. HTTP and monitor client
factories now enable the same issue/activity protocols with their actual scoped
actors. Raw comment deletion now preserves v4 original comment snapshots;
single-target linked-file and relation undo also restore verified original review
state. Single-target native close/reopen changes have a guarded compensation executor. Coordinated workflow and multi-target undo remain pending.
`td undo` executes supported scalar, linked-file, single-source relation and own comment-creation inverses. This document is
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
the previous one. Inspect the failed attempt's printed identity first. Explicit result reconciliation is provided by `undo --reconcile`, described
below. Persistent retry identity for arbitrary repeated command text is not
inferred; repeating command text still starts a new operation.

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
remains unchanged. Dedicated executors below recognize basic workflow, review,
visibility and native-state changes. Unrecognized session/history edits and
coordinated multi-target effects still return an explicit scope error before
target writes. Legacy per-target journals also need separate-carrier
migration before execution, avoiding receipt writes on the target itself.
Creation retains the explicit logical-delete alternative. These are pending
implementation boundaries, not SQLite-only exemptions or proof of #9 completion.

## Linked-file inverse and original review verification

Single-target `link`/`unlink` changes restore the exact original native issue
body and labels, including file IDs, paths, roles, linked SHA and times. The
scope recognizer permits only the file writer's complete effect: file-set
replacement, reserved label mirroring, clearing current review pointers and
superseding existing active review rows at one instant. Changed participation,
parent/dependencies, review authors/decisions, native state, other metadata or
new/deleted review rows cannot be smuggled into that inverse. Both recorded
file sets must pass the same repository-relative path, role, ownership and
uniqueness checks as the ordinary writer. Legacy unlabelled issues retain their
exact original body/labels after undo.

Restoring a review uses its original identity and attribution; it does not
create a new approval. If before-state contains an active approval or review
basis, the executor revalidates the original content basis, native close/reopen
event digest and handoff identity/update time before intent, before target
PATCH, after target readback and after receipt verification. Closed approvals
must still satisfy the recorded-close event proof. Stale evidence produces a
conflict; API/permission/rate-limit errors retain their original cause. A failure
after a target write reports uncertainty, never replaying the inverse. Receipt
chains and already-undone no-ops recheck file review evidence too.

The original before/after snapshots remain immutable. A successful `undone`
receipt proves a new restored revision, allowing sequential undo only through
unambiguous verified same-session/author/carrier edges. Restoration touches
metadata links, not repository files or commits. GitHub does not provide an
atomic transaction across issue state, handoff comments and events, so checks
at these fences remain best-effort conflict detection. Explicit observation of
an `undo_prepared` result uses the non-causal recovery policy below; matching
fields alone cannot prove the original target write. This is a completed implementation
stage within #9, not closure of the whole task.

## Single-source relation inverse

Dependency add/remove, complete set replacement and reordering, and parent
assignment/clearing/replacement can restore one source issue's exact original
body/state/labels. Combined scalar/file/relation changes on one issue use the
same graph guard, with linked-file validation and original review revalidation.
Session/participation/status changes, review attribution/decision edits,
rewritten transition history and unrelated transition deltas remain outside
this scope. Existing transition prefixes must remain immutable, and appended
dependency events must describe the exact set delta with the journal's actual
td session. Legacy graph updates without new transition events can restore
their recorded source state. Both original snapshots remain unchanged in the
separate carrier; the source's transition rows restore their original values.

Before intent, before target PATCH, after readback and after receipt, graph
validation uses one fresh complete paginated issue collection per fence. It
checks the source collection revision against its point observation, then
validates the proposed original dependency closure and parent ancestor chain.
Self-links/cycles, missing/deleted tasks and auxiliary storage entities fail
before any target write. Reachable dependency/ancestor revisions captured at
the first fence must agree at subsequent fences; unrelated valid issue edits
do not invalidate restoration. The final source point read occurs after graph
and review validation. A permission/rate-limit/pagination failure retains its
original cause and cannot become an empty graph or trigger SQLite fallback.

The graph portion costs four paginated collection invocations for a normal
inverse, with zero per-reference issue GETs, regardless of graph size. HTTP
request count within an invocation grows with repository pagination. Receipt
chains and review verification have their existing separate costs. Already
undone no-ops validate the current proposed graph and original review, then
recheck the source; they never replay a target or receipt write. GitHub cannot
make these separate graph/source reads and writes atomic. A changed graph or
source after a write is an explicit partial/uncertain result, without automatic
rollback or target replay. Original stale/unverifiable review state is not
restored as a valid approval; callers must start and submit a new review cycle.

Batch reverse edges and cascades remain separate per-source/per-target journals.
This inverse does not silently reverse other issues or claim one distributed
transaction for a multi-target command. Coordinated workflow/cascade undo,
explicit undo-prepared recovery and portable journal transport remain required
work before #9 can close.

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
existing behavior until their writer integration supplies an actor. HTTP and
monitor issue/activity clients now supply their actual scoped actor. Any lost/denied/unverified POST or receipt returns
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

Sequential scalar/comment undo now follows exact causal links through verified
scalar and comment inverse receipts. Comment witnesses must be absent by both
native ID and logical operation in the same fresh bulk history, before intent
and again before the target write. Ambiguous, edited, unfinished or foreign
receipts never authorize a revision change; the issue clock is never ignored. Comment
deletion journaling, coordinated policy/graph inverse operations and portable
journal transport remain required work. Auxiliary entity inverses and HTTP/monitor
writers are implemented in the subsequent sections.

## HTTP and monitor actor integration

The production HTTP writer factory checks the selected store, web worktree/branch
and current web session before enabling tracking. HTTP issue creation, updates,
workflow changes, logical deletion and activity creation share the established
journal protocols; no new SQLite fallback or automatic retry is introduced.

Monitor data/board factories enable tracking without remote writes. The real
model revalidates configuration, worktree/branch and session before opening
clients for issue, board or notes operations. Retired context requests fail
before reaching GitHub. Board/note entity writers now use the v3 protocol below; simply enabling
tracking never substitutes for their native confirmation checks.

Production-factory subprocess tests cover HTTP creation/update/comment/workflow
and monitor creation/edit/workflow/deletion, including automatic handoff activity.
They assert durable applied receipts with actual actors. Intent denial, target
denial, receipt denial and lost native comment responses remain errors and do
not invent applied receipts. Session rotation blocks old HTTP writers; session,
branch and store replacement block monitor issue/board/notes clients. Aggregate
read request-count tests remain applicable: enabling tracking itself makes no
GitHub request and creates no carrier or receipt on reads.

## Auxiliary entity journals (v3)

`td:action:v3` records note/board configuration writes. Creation keeps the raw
validated input separately from an unallocated target, then records native
identity and exact verified result. Updates preserve native before/after title,
full metadata body, state, labels and update clock. The input/body identifies
entity kind, so no extra versionless discriminator is added. v1 issue and v2
comment records remain readable; entity payloads in those versions are rejected.
Older binaries do not read v3 records and running monitors must be restarted
after upgrading. A new protocol is never presented as backward-readable data.

Board and notes client factories enable tracking with their real scoped actor.
Entity creation uses durable intent, response/author/readback confirmation,
fresh bulk operation uniqueness and completion receipt. Board catalog uniqueness
is still checked separately. Entity updates recheck the target after durable
intent, retain existing native/catalog confirmations and then verify receipt plus
final target revision. Invalid input, stale observations and actor mismatch do
not authorize a target write; uncertain POST/PATCH or receipts are explicit
errors and never automatic retries or invented success. Note no-op flag changes
retain their existing no-write behavior. No SQLite fallback is introduced.

`last`/`undo --list` display `note`/`board` with nt-gh-N/bd-gh-N (or the builtin
board identity), retain target_issue_id and preserve native snapshot contracts.
Entity updates support guarded title/body inverses, including flags, logical
deletion, board queries/view modes/positions and builtin settings. They require
matching session/author and exact revision (or verified causal undo receipts),
preserve native state/labels and recheck active board-name uniqueness before
intent, before PATCH and after target/receipt verification. Original entity
events remain in the separate action journal even when the restored body rolls
back its embedded history. A later catalog conflict can occur after PATCH because
GitHub supplies no atomic transaction; errors retain the uncertain outcome and
never replay the write. Board position restoration changes configuration only,
does not modify referenced tasks, and current query/filter semantics still
determine visible rows. Native
creation cannot be erased losslessly; explicit note/board logical deletion is
the supported compensation for custom entities. Builtin board materialization
cannot be logically deleted under the builtin policy; select another board or
explicitly adjust its settings while retaining identity/history.


### Creation collection visibility

GitHub may return the new issue from POST and point GET before repository-wide
issue listing observes it. Creation receipt verification therefore makes at most
three fresh bulk observations, waiting two and then four seconds only when the
operation has no matching result. It never repeats the creation POST. Duplicate
or changed results, invalid history, read failures (including rate limits), and
cancellation stop immediately. Persistent absence leaves the intent prepared and
returns an explicit unverified-result error; inspect GitHub before retrying.
Each observation uses paginated repository issue/comment collections, without
per-issue activity queries. These additional reads apply only to tracked creation
verification and do not change monitor polling intervals or normal cache policy.

## Native comment deletion history (v4)

A tracked `DeleteComment` records `action=comment_delete` in a separate carrier
using `td:action:v4`. `comment_before` retains the original native comment ID,
issue membership, raw body, GitHub author, creation/update clocks and checksum.
The actor/session of the deletion belongs to the journal; it does not replace
the original author's attribution. This is distinct from v2 comment creation,
v1 issue changes and v3 auxiliary entity changes. Older readers must report an
unknown schema rather than silently dropping the original state.

Only ordinary comments may be deleted; logs, handoffs and journal receipts
remain protected. The intent is validated and read back before another target
and comment revision check. A denied or uncertain DELETE is not retried and
leaves a prepared record for inspection. Success requires comment absence,
unchanged issue fields, a verified applied receipt and a final fresh bulk
observation. Before publishing that receipt, an exact clock-only difference
between point and collection reads permits at most three read observations
with cancellable 1s/2s waits. Normalizing only the observed clock must reproduce
the full point revision; any other field difference stops immediately. An
additional point read fences the collection match before receipt publication. DELETE
is never replayed, and final receipt verification retains its exact revision
check. Persistent lag remains an explicit unverified result. An external target edit or malformed/incomplete collection fails
explicitly. A later receipt failure includes the operation and journal ID,
never treating an unverified deletion as ordinary success.

`last` and `undo --list` expose the original comment identity and
`comment_before` data. Exact inverse execution is an explicit exception:
GitHub's [issue comment API](https://docs.github.com/en/rest/issues/comments)
provides creation, edit and deletion, with creation accepting a body rather
than an original ID/author/clock. There is no restore endpoint for a deleted
comment. Posting preserved text as a new comment is deliberate compensation,
with a new ID and author/timestamps; it cannot be claimed as an exact undo.
The executor returns this limitation and alternative before any target write
and never creates an `undone` receipt for a deleted original identity.

This adds native deletion history; workflow/relation inverses, explicit
prepared-creation reconciliation and portable journal transport remain #9
work. It does not close the whole task.
## Explicit result reconciliation (v5)

Use `td undo --reconcile ghc-ID` to inspect an uncertain action by its printed
receipt ID. The default is read-only. Add `--apply` to record a verified result
in that existing journal; the command never replays the original issue POST,
target PATCH or comment DELETE. It requires GitHub storage and cannot open a
SQLite fallback. `--list` and `--reconcile` are mutually exclusive; `--apply`
requires a receipt ID.

The authenticated GitHub author must own the original journal. Recovery may be
requested from another real CLI/web session; the original session and snapshots
stay intact. A v5 `reconciliation` records the recovery session, native actor,
observation time and verified current issue state separately. A prepared
creation gets its uniquely verified allocated issue number/after-state. An
already applied receipt preserves its original after-state even if the current
native clock has advanced. Old v1–v4 envelopes remain readable; v5 under an old
header and mismatched native recovery authors are rejected.

Creation proof requires one complete repository result with the original
operation ID, native creator, exact title/body/state/labels, matching point read
and the original journal. Missing or duplicate results stop without POST.
Comment-deletion proof requires the exact original comment absent from complete
fresh comments, unchanged target fields and a matching point observation. A
still-present comment stops without DELETE. Missing pages, changed receipts,
foreign actors, target edits and ambiguous carriers remain explicit failures.

Apply rechecks the target and old receipt, PATCHes only the existing journal,
verifies its canonical response/readback and repeats the full result proof.
A lost receipt response is unverified; reobserve before retrying. An already
reconciled receipt returns a no-op only while the verified state still matches,
and cannot rewrite the original attempted action. Creation reconciliation is
not creation undo; native comment deletion remains irreversible.

This completes explicit result recovery for issue/auxiliary creation and native
comment deletion. Uncertain inverse reconciliation uses the separate policy
below. Forward scalar/workflow/graph partial writes and coordinated group
recovery remain #9 implementation work rather than passing through this
creation/deletion result-only executor.

## Basic workflow inverse

Single-source `start`, `unstart`, `block`, and `unblock` receipts can now be
undone while the issue's native GitHub state remains open. The inverse restores
the exact original body/state/labels, including the original claim, session and
transition history, review pointers and review rows. The original before/after
snapshots remain in the journal; removing the new transition from the current
issue does not erase the audit receipt.

The scope checker recognizes the entire normal transition writer effect. It
requires one attributed transition, an unchanged history prefix, the expected
claim and participation delta, matching reason/time, exact review invalidation,
and unchanged task content/relations/files. Arbitrary status edits or additional
actors/reviews cannot be included in that inverse. A restart of an in-review
issue may restore its original review only when the original content basis,
native event digest and handoff remain valid. These proofs are read before
intent, before the target write, after readback and after the receipt; the final
source revision check follows the proof reads. Existing undone receipts also
recheck the original review and source revision without writing again.

This is single-source restoration, not a coordinated undo of a multi-ID command
or cascade. Review, native state, logical visibility and uncertain inverse
observation have dedicated policies below. A new GitHub close/reopen event
cannot be removed or represented as an old review event. Coordinated
multi-target inverse planning, forward partial-write recovery and portable
journal history remain work under #9. Failures preserve the existing
uncertain-result diagnostics and do not automatically repeat the target PATCH.

## Review checkpoint inverse

While native GitHub state remains open, a single-source review request,
record-only approval or changes-requested decision, and rejection can be
undone. The inverse restores the exact original body/labels, participation and
review metadata. It removes only the selected writer's new transition/session/
review delta from the current issue; the immutable journal before/after and
undo receipt preserve its audit history. An existing approval superseded by a
changes-requested decision or rejection is restored with its original row ID,
reviewer, attribution, decision and time, never replaced by a new approval.

Scope validation checks the complete normal writer effect, including the
unchanged transition/session/review prefixes, original actor, matching reason
and time, unique new identities, review basis and expected field changes.
Ordinary handoff-based and minor review requests are recognized. Trusted
self-review and attributed reviews retain their existing provenance; undo does
not claim another reviewer examined the work. Arbitrary title, file, relation,
claim or old review-row changes cannot accompany a review inverse.

Every original checkpoint being restored must still pass its content basis,
native close/reopen event digest and original handoff verification at all four
write fences. Native/transport errors retain their cause; stale evidence is a
conflict. Already-undone observations also recheck the original proof and final
source revision without replaying a write. Closing approvals and close/reopen use the separate native compensation policy
below; logical deletion and uncertain inverse observation also have dedicated
executors. Coordinated cascades, partial group recovery and portable journal
history remain under #9.

## Observe an uncertain inverse without replaying it

`td undo --reconcile ghc-ID` now also previews `undo_prepared` and completed
`undone` receipts. `--apply` may update only the existing receipt. It never
replays the target PATCH or native comment DELETE. The authenticated GitHub
account must be the original journal author; the actual recovery session is
recorded separately and never replaces the original attempt's session.

Recovery uses complete fresh issue/comment collections and a matching point
read. The original carrier and receipt must be unique and unchanged. The target
must exactly match the original inverse contents with a non-regressing native
clock. For comment-creation inverses the original comment and operation result
must be absent. Restored reviews, relations and board catalogs retain their
normal proof checks; stale/missing/foreign data, incomplete pagination and
transport failures cannot produce a successful empty observation.

An already completed receipt is confirmed without any write, including with
`--apply`. A pending receipt whose inverse contents are observed can be saved
as `undone` with a v6 `undo_observation`: original before/after, attribution and
undo operation ID remain immutable, while the actual recovery actor/session,
observation time and exact native state are recorded separately. The outcome
explicitly declares `target_write_verified=false` and
`causal_undo_chain_eligible=false`. Content equality does not prove who performed
the original PATCH/DELETE. Consequently this observation cannot authorize a
causal bridge for undoing an earlier action over a newer native revision.
Inspect the current state and make an explicit new field edit when that earlier
inverse cannot be proven. A genuine verified completion receipt retains its
existing causal proof; confirmation does not turn an observation into one.

Receipt failure remains uncertain and keeps the original diagnostic. Repeating
explicit observation rechecks the saved state/proofs and writes nothing if the
receipt already exists. Later target/proof changes remain conflicts. The v6
reader retains v1–v5 compatibility; older readers fail closed on a new version.
Native close/reopen uses the v7 proof policy below. Coordinated multi-target
and partial group recovery still need their own proof policies.

### Logical deletion and restoration inverses

A single-target `delete` or `restore` can be undone when its complete writer
change consists of the visibility timestamp and one transition attributed to
that journal's session. Native state, issue content, claims, relations, files,
and review rows must remain unchanged. Undo restores the exact original body
and labels; it does not create an approval or change a native closed issue into
an open issue. A retained deletion checkpoint is readable for inverse proof
checks, while ordinary review and issue commands still reject deleted issues.

Existing review basis, native close/reopen events, and handoff identity/time are
checked before and after the write and receipt. If they changed, the inverse
fails explicitly. Uncertain completion can be observed with `undo --reconcile`
without replaying target writes, under the same non-causal observation policy.
Multi-target deletion is still represented by individual journals; this does
not claim atomic rollback for a batch or support native close/reopen inverses.


## Native close/reopen compensation

Single-target `close`, closing `approve`, and `reopen` now have a guarded
compensation executor. It recognizes the complete writer effect, including
review rows, session closure and the unique original transition. Arbitrary
content/history changes and coordinated cascades are rejected. The current td
session and authenticated GitHub author must match the original journal.

The executor restores the former task state and claims while retaining forward
review/session/transition audit rows. It supersedes active approvals, clears the
review basis/requester/reviewer/handoff checkpoint, and adds an attributed
`undo_native` transition. Ordinary reviewed work needs a genuinely new review
before another approval; optional minor-review and administrative closures do
not invent a review requirement. A compensated closed issue does not acquire
an approval by undoing a reopen.

GitHub's native close/reopen events cannot be erased through the issue-events
REST API. Compensation checks the complete original event prefix and requires
exactly one appended opposite state event. It also checks the original content
review basis and handoff identity/time, then performs a final target revision
read at intent and completion fences. Native API failures retain their cause.
The optimistic read/write boundary cannot provide a GitHub atomic CAS.

A v7 receipt records the immutable compensation plan and original event
baseline before the single target PATCH. Successful completion records the
exact resulting issue revision and event sequence with
`proof=verified-native-inverse`. JSON adds `compensation` and `undo_outcome`;
human output says `COMPENSATED`. `exact_historical_restore=false` and
`causal_undo_chain_eligible=false` are explicit, because native events and
forward audit rows remain. Even a verified compensation cannot bridge an
older scalar undo over a changed native revision.

If the target write or completion receipt is uncertain, there is no automatic
replay. `td undo --reconcile ghc-ID` observes the exact planned target contents,
original proofs and appended inverse event. `--apply` writes only the existing
pending receipt with `proof=inverse-contents-observed`, the actual observer
session/time, and `target_write_verified=false`; it preserves original
before/after, session, plan and undo identity. An already completed receipt is
confirmed without writes. Stale, extra, missing or delayed native events fail
explicitly until the remote evidence can be inspected. v1–v6 reading remains
supported; older readers reject v7 rather than silently accepting it.


## Forward update recovery and bounded transport

New task, note and board updates record their complete planned title, body,
native state and labels before the target write (v8). Native review/state
changes also retain a real event baseline. Changed relation and board-position
inputs retain the already validated related issue observations; unchanged
legacy edges are not revalidated as new writes.

If the target PATCH or completion receipt is uncertain, `td undo --reconcile
ghc-ID` previews the exact remote planned contents and proofs. `--apply` updates
only the existing receipt after a second fresh proof/source check. It never
replays the target write or invents the original server timestamp. Recovery
records the actual observer session, GitHub author, time and native state with
`proof=forward-contents-observed`. JSON exposes `forward_outcome`, with
`target_write_verified=false` and `causal_undo_chain_eligible=false`. A completed
original receipt is confirmed without writes; repeated recovery is read-only.
Observed forward contents cannot bridge a causal undo chain. A separately
verified direct inverse can report its own write as verified while remaining
ineligible as a causal witness.

Large new receipts use a v9 envelope containing the original v1–v8 schema,
gzip/base64 data, decoded size and SHA-256. Repeated large quoted JSON strings
are stored once in a versioned string archive before compression, preserving
exact token order and opaque body bytes. Earlier plain gzip envelopes remain
readable. Both decompression and dictionary expansion enforce the decoded
budget. The implementation chooses a
conservative 60,000-byte single-comment budget and a 4 MiB decoded budget;
these are internal transport bounds, not claims about GitHub API maxima.
Completion and recovery observation capacity are checked before an update
writes its target, with reserved headroom. Incompressible records that require
multipart transport fail explicitly before the target write. Malformed,
unknown, recursive, corrupt and oversized envelopes fail closed. Reading
retains all earlier schemas, and rewriting a compressed receipt fences against
its exact original bytes instead of recompressing historical data.

This stage does not implement grouped cascade intent/recovery, portable
journal history, or multipart transport for large incompressible snapshots.
Older updates without recorded full inputs cannot be recovered by guessing
what the original command requested. Optimistic GitHub checks do not provide
an atomic compare-and-swap boundary.

## Explicit basic transition groups

A command with several IDs for `start`, `unstart`, `block` or `unblock` records
one immutable v10 group intent before any target write. It preserves declared
order and actual initial task revisions. Every attempted member stores its
complete v11 update input and original group operation ID before its own write.
Unattempted future inputs are never invented. Capacity checks reserve group
completion and inverse space before writes. Group membership checks use fresh
bulk task snapshots; changing a sibling prevents subsequent writes.

`last` and `undo --list` collapse linked members into one command entry. Members
cannot be directly undone outside their validated group. Group `undo` checks
all member policy/revisions before its first inverse, records a group inverse
intent, and executes supported inverses in reverse declaration order. It
rechecks every watched target after individual intent reads and immediately
before each target PATCH. These optimistic checks do not provide GitHub atomic
CAS or a transaction. A failure retains earlier results and original undo IDs.

`undo --reconcile <group receipt>` previews all member states. `--apply` writes
only pending member/group receipts; it never replays targets. Results distinguish
applied, observed, noop, unattempted, unapplied (exact starting revision only),
and undone. Matching starting contents cannot claim that an uncertain attempt
never happened; it records only the observed unchanged revision. Unknown,
duplicate, foreign, changed or missing members fail explicitly. A partial
inverse completion can be observed and then a later explicit `undo` continues
remaining applied members without replaying already restored ones. An inverse
whose planned result is not observed remains unverified and is not retried.

Observers/session/time and original group membership, member inputs and group
undo identity are preserved. Observed proofs report unverified target writes;
even verified group inverses are excluded from causal undo-chain witnesses.
Completed/recovered group confirmations are read-only. v1–v9 reading remains
supported, and bounded v9 transport can wrap v10/v11 payloads.

Cascades, native-state groups, other explicit command batches, claim sweeps,
work-session fanout and portable history still require their grouping/inverse
integration. Their existing single-member receipts do not claim whole-command
rollback. Large incompressible groups still require multipart transport.

### Review cascade groups

Tracked `TransitionWithCascades` / `TransitionObservedWithCascades` calls
(including HTTP and monitor) freeze a single review root automatically. CLI
`review` also freezes multiple explicit roots plus all descendants, ancestors and
siblings used to decide epic completion in a v12 group before the first issue
write. A single isolated task retains the existing single-target receipt.
Every changed task links its complete v11 member input to the group; blocked,
already reviewed or closed watched tasks keep their original native revision.
The original hierarchy membership is checked from fresh bulk listings before
member writes, during recovery, and immediately before inverse writes. New
children, deleted/reparented nodes and edits to watched siblings fail explicitly.

Ordinary requests and automatic epic requests have separate complete writer
validators. An automatic request keeps its actual implementer, clears its
handoff checkpoint and records an event/basis checkpoint without inventing an
approval or reviewer. Group undo validates all original review proofs first,
then cancels the actual member writes in reverse native receipt order, which
also covers overlapping explicit roots. Future explicit roots are deferred
to their own callbacks so their options are preserved. Recovery retains original roots,
membership, member inputs and undo identity and writes receipts only. Successful
confirmation is read-only; group witnesses remain ineligible for causal undo
chains. This is optimistic compensation, not a GitHub transaction.

Close/approve cascades, native-state groups, other batch writers, sweeps,
work-session fanout, portable history and multipart transport still require
integration; this review support does not claim those operations are complete.

### Native closure and reopen groups

Tracked close/closing-approve entry points freeze their roots, ancestors,
siblings, dependents and all dependencies used to decide unblocking in v13.
Explicit multi-ID reopen and record-only approval use the same group transport
with root-only membership. An isolated single target retains its existing
receipt. Missing dependency existence is also fenced: a later appearing
otherwise missing dependency changes the watched membership. New dependents,
reparenting, sibling edits and deleted nodes fail before inverse target writes.

Complete root native effects, aggregate epic closures and dependency unblocks
have separate admission checks. Aggregate epic closure preserves its actual
implementer and never creates an approval. The group preflights every inverse
and original handoff/event proof before the first inverse, then applies member
inverses in reverse actual receipt order. Native members retain forward audit
rows, append compensation, supersede old approvals and preserve all actual
closed/reopened events; they do not restore an old approval onto new events.
Ordinary dependent unblocks restore their original snapshots. The native target
PATCH also invokes the full group receipt/membership/revision fence immediately
before writing. Completion capacity probes include native compensation audit
payloads and never claim future results or timestamps as observed evidence.

An uncertain native inverse is matched to its original compensation input and
actual event prefix before receipt-only recovery. The original group roots,
forward results and undo identity remain unchanged; completed members are not
replayed. Observed native completion keeps its weaker proof even when the rest
of the group is later undone through verified writes. Groups remain non-causal.
Repeated completed verification is read-only. The API still supplies no atomic
transaction or CAS over this hierarchy.

Multiple effects on the same issue in one compound command, remaining writers,
claim sweeps/work-session fanout, portable history and multipart transport still
need integration; this group support does not declare the whole undo task done.

### Ordered effect format under development (#64)

The v14 group and v15 member formats provide the storage and planning contract
for repeated effects on one issue. Native close, approve and reopen command
groups now emit these formats. The source executor compensates frozen ordered
groups through the public undo entrypoint; live GitHub verification remains
unfinished.
The installed, verified command behavior remains the v10/v12/v13 integration.

The source implementation supports observation and receipt-only reconciliation
of v14 records.
It reads one fresh bulk history and actual watched states, compares every frozen
original receipt including its native clock and body digest, and reads native
events for affected targets and any additional frozen watched observations.
It distinguishes an applied but unrecorded forward attempt, an unapplied tail
after earlier effects, a completed inverse step and an uncertain inverse input.
Observation never replays a target write or invents a completed receipt. The
point-read helper also checks the original native comment body and identity;
the source executor invokes it before each target write.

Explicit reconciliation can freeze actual forward outcomes and recover a
terminal applied-but-unrecorded member receipt before freezing the group. Every
receipt PATCH checks all original native bodies, current watched revisions and
events. An unapplied terminal input preserves earlier successful effects.
Recovery records its separate observer session without replacing the original
operation, actor or intent session. A pending inverse is completed only if its
requested contents and required events are actually observed; an unapplied
inverse rejects without replay. Completed inverse plans remain immutable and
their original proof is retained at final group completion. Lost completion
responses can be confirmed read-only from the actual saved original receipt.

An ordered member declares its sequence and actual preceding receipt on the
same target. The group freezes original receipt snapshots, native comment body
digests, forward outcomes and event observations. A terminal unapplied input
does not hide earlier actual effects. The group inverse records each observed
source and complete requested compensation separately from its result. One
pending step cannot admit a successor. Completed plans, results and observer
provenance are immutable; observing an inverse never promotes it to verified
proof. Empty actual observations remain distinguishable from absent evidence.

The pure compensation planner retains both forward and preceding inverse audit
rows, restores former task fields, supersedes approvals and clears their review
basis and handoff checkpoint. Every native change needs one opposite event;
scalar compensation retains the exact existing event prefix. Original review
event evidence is checked against its historical forward prefix, not rebased
onto later compensation events. These steps only connect the frozen group;
they do not authorize an ordinary causal undo chain.

The source reverse executor reserves eventual inverse capacity before any
write, including audit growth and clock/identity headroom in both transport
budgets. It saves one complete input before each target PATCH, fences all
original receipts and watched revisions, relationships, native events and
historical handoffs, and verifies the exact response, point readback and fresh
group observation before saving verified completion. Completed effects are not
replayed; an uncertain pending step requires explicit receipt-only recovery.
Observed proof remains weaker through later verified steps and final completion.
The final receipt also rechecks historical handoffs. Original member receipts
remain unchanged, and history collapses the entire ordered sequence while
rejecting missing or modified originals.

The forward writer retains each actual verified receipt and result in command
order. It rejects continuation after an uncertain receipt, checks all original
receipts, watched states, native events and historical handoffs before target
and completion writes, and records the actual preceding effect on each target.
Each attempt reserves both applied and unapplied completion and eventual inverse
capacity before saving its member intent. A later capacity failure may retain
earlier completed effects; the command does not promise an atomic transaction.
The CLI regression covers dependency unblock followed by explicit close on the
same target and three inverse steps, including read-only completed confirmation.
Further command-path regressions and actual GitHub/installed-binary verification
remain. Synthetic tests do not establish live GitHub behavior.
