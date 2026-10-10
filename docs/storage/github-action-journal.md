# GitHub action journal implementation

This work belongs to #9. The codec, bulk reader and inverse planner are present;
command dispatch, write journaling and undo execution are not yet implemented.
`td last` and `td undo` remain unsupported for GitHub storage. This document is
an implementation boundary, not an acceptance or completion declaration.

A journal is a versioned `td:action:v1` block on a repository carrier's GitHub
comment. It identifies the operation, session, issue, action and phase, and
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
