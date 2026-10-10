# GitHub action journal implementation

This work belongs to #9. The codec, bulk reader, inverse planner and an opt-in
issue-update write protocol are present. `last` and `undo --list` have GitHub
dispatch; default write integration, multi-target/note/board journaling and
undo execution remain pending. Issue
creation also has an opt-in intent/result protocol; actual command writers have
not yet been switched to either protocol.
`td undo` without `--list` remains unsupported for GitHub storage. This document is
an implementation boundary, not an acceptance or completion declaration.

A journal is a versioned `td:action:v1` block on a repository carrier's GitHub
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
explains that legacy/native changes are not reconstructed and default CLI
journaling/undo execution is incomplete. Empty history must not imply that no
older actions occurred. Actual undo returns `unsupported_operation` before any
GitHub call or SQLite fallback until its executor is implemented.

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
