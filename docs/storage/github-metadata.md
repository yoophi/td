# GitHub issue metadata

GitHub owns issue numbers, title, visible description, labels, native state,
URLs, and server timestamps. A trailing `td:issue:v1` HTML comment stores the
remaining fields. Native issues without the block use task/P2 defaults.

The v1 envelope supports `operation_id`, `type`, `priority`, `points`,
`acceptance`, `last_state_reason`, optional `entity_kind`, and optional `details`.
Omitted `entity_kind` means `issue`. Reserved `board` and `note` entities are
excluded from issue lists and rejected by issue Get/update/activity operations.
Their feature-specific payloads and commands remain separate work.

`details` holds minor/sprint, parent, creator/implementer/reviewer/requester/closer
session attribution, creation branch, reviewed/deleted timestamps, due/defer dates
and defer count, dependency IDs, linked files, review records, session history,
and transition records. Transition records keep actor, action, previous/new
status, reason, operation ID, timestamp, and optional Git snapshot together.
Dates use YYYY-MM-DD. Repository-local relationship IDs use canonical `gh-N`.
Readers validate these fields, including nested records, before exposing them.

Missing details preserve existing v1 behavior. Unknown versions, fields, entity
kinds, or invalid fields produce errors without rewriting the body. Older fork
builds reject metadata fields they do not understand; upgrade collaborating
clients before using extended fields. Field updates preserve unrelated metadata.
The schema does not imply that every corresponding CLI workflow is implemented.
Review/lifecycle policy integration, board/note payloads, and the complete shared
writer contracts are still tracked in #3 and #16.

Native closed/open state overrides conflicting detailed state. A native state
change does not grant approval: effective reviewer, implementer, review-requester,
closer, and reviewed time are cleared on a detected mismatch, while historical
records remain available under details. This read path reconciles current state only. Approval transitions additionally
check native close/reopen event history, including intermediate round trips. Consumers
must not treat historical review records as an active approval without validating
the current review cycle and relevant mutations.

Soft-deleted records are excluded from normal Get/List, including `List(all=true)`
(which includes closed issues). Restore/history/migration consumers explicitly use
GetIncludingDeleted/ListIncludingDeleted. Internal entities remain excluded there.

## Writes and policy observations

`Client.UpdateObserved` accepts the record used for policy validation, checks its
repository and issue identity, and compares its original revision immediately
before PATCH. It does not refresh away the policy observation. A stale observation
returns a conflict without writing. Callers must not modify the observed record;
changes belong in the Changes argument. `CopyDetails` returns a deep copy of
details so nested history can be edited without mutating that observation. Existing Update delegates through this
same path. Writes validate the response and fetch again to detect observed races.
Cancellation before a request prevents executing gh.

These checks are best-effort, not an atomic compare-and-swap. A writer can still
race between GET and PATCH. Writes are never automatically retried, and errors
after PATCH can mean the write applied. Existing SQLite workflows retain their
native transaction paths. No SQLite database is opened for GitHub operations.

## Available detail options

`td create --minor` (including task/epic shortcuts) stores the minor flag.
`td update ID --sprint NAME` changes sprint; an empty string clears it. These
updates preserve dates, relations, review/session records, and other details.

## Status label projection

State labels are a display/search projection, never the source of claims or
review decisions. The five exact reserved names are `td:open`, `td:in_progress`,
`td:blocked`, `td:in_review`, and `td:closed` (case-insensitive). Writes preserve
other labels and include the desired mirror in the issue PATCH when needed.
Repository label definitions are created lazily and verified before the issue
write. Returned labels and the subsequent issue revision are checked, so ignored
label changes are surfaced as partial failures.

A missing/conflicting label produces `state_label_warning` in JSON reads and a
warning in `td show`/`td list`. `td config sync-state-labels` repairs these from
the effective metadata/native status. A closed native issue retains precedence
over stale open-family metadata; mirroring does not infer approval from closure.

## Dependency changes

Dependency writes record `add_dep` / `remove_dep` transition actions with a
canonical `related_issue_id` and actual session actor. Other transition actions
must not carry this field. Older clients that reject these v1 fields/actions
need upgrading before collaborating on issues using them. Review rows remain
historical evidence with `superseded_at`; the effective reviewer/reviewed time
and review basis are cleared after relation changes. The native state and
implementer attribution are retained. A fresh review cycle is required for
approval. Source revisions and reachable target graph observations are verified
around writes, but GitHub offers no atomic graph lock or conditional PATCH.
Removal can clean edges whose targets are deleted or missing.

## Board carrier schema (read foundation)

Board configurations use `entity_kind: "board"` and a separate `board` object
with `version: 1`. It contains a validated TDQ `query`, `view_mode` (`swimlanes`
or `backlog`), `builtin`, optional nonzero viewed/deleted timestamps, canonical
`gh-N` position entries (nonnegative unique positions and IDs, `added_at`), and
actor/operation/time-stamped board history. Unknown fields/versions, mixed issue
and board details, malformed queries and duplicated positions/history fail
closed. The carrier's native title and timestamps provide the board name and
clocks. Ordinary board IDs are `bd-gh-N`; the builtin has `bd-all-issues`.

General issue/task lists exclude board carriers. Board reads paginate all native
states; a native close can archive a carrier without deleting its configuration.
Explicit board `deleted_at` governs logical deletion. A builtin cannot have a
filter or be logically deleted, and its carrier must be named `All Issues`.
Duplicate builtin carriers and ambiguous names require explicit reconciliation;
reads never silently select or merge them. Exact board IDs take precedence over
names. Legacy board markers without the versioned payload remain recognized as
auxiliary entities, but board API reads report a repair-needed error rather than
guess their configuration. Until a builtin carrier exists, reads expose a
virtual All Issues board with no native carrier/timestamps; they perform no
GitHub creation. Custom-board storage writes create carriers, change names/queries,
and record logical deletion. Writes preserve native state/labels and unrelated
configuration. They check the original private observation before and after a
write; GitHub does not provide atomic compare-and-swap, so intervening edits
can still be overwritten in the request window. Names are checked before and
after creation/rename, but concurrent clients can still create duplicates; an
explicit saved-carrier error requires reconciliation. Writes are attempted once,
and uncertain outcomes require inspection before retrying. View mode, last-viewed time and saved-position operations use the same
private-observation guards. Moves among saved positions respace to positive
sparse keys in one carrier write, retaining other entries' added timestamps.
Setting/moving verifies a live task target; removing a saved position permits
missing/deleted targets for cleanup. Target reads and carrier writes are not an
atomic transaction. The shared board reader evaluates TDQ against a complete request-scoped task
snapshot using the private observed board query. Positioned tasks sort first;
unpositioned tasks retain query order. Closed-task filtering never bypasses
the TDQ predicate or logical deletion. ID-anchored moves position the necessary
prefix and retain query ordering below it; hidden saved sort keys stay unchanged.
The caller must obtain candidates from that shared reader. Task membership and
comments can change after the read, so this is not an atomic board snapshot.
Sort-key overflow and missing anchors fail before a write.
Explicit builtin persistence creates a carrier only on a write path, or reuses
an existing single carrier; concurrent creation reports duplicates requiring
reconciliation. `GET /v1/boards` and `GET /v1/boards/{id}` expose the existing board/card DTO
contract through `board_reads`. Detail accepts only one `include_closed=true|false`
parameter. Card dependency summaries include unresolved blockers outside the
board query; missing/deleted blockers produce an explicit repair error. TDQ
candidates and summaries use the same task listing. Virtual builtin clocks are
empty strings because no GitHub carrier timestamps exist. Reads do not persist
last-viewed settings or create carriers. `board_crud` adds `POST /v1/boards`,
`PATCH /v1/boards/{id}` and `DELETE /v1/boards/{id}` with actual web-session
actors and strict JSON fields. Creation returns 201; updates return 200 with
the saved board, `revision` and ETag. Detail reads expose the same revision.
If supplied, If-Match must match the original carrier observation; weak/multiple
tags and wildcards do not match. Missing If-Match retains legacy behavior with
backend pre/post observation guards; no atomic CAS is claimed. Builtin rename,
filter and deletion fail with 403; logical deletion never closes/deletes the
native carrier. `board_positions` adds `POST /v1/boards/{id}/issues` (visual
slot among saved positions), `POST /v1/boards/{id}/move` (task-ID anchor, optional
include_closed in body or query) and `DELETE /v1/boards/{id}/issues/{issue_id}`.
Slots <= 1 mean the top, matching the existing API default; task IDs must be
canonical gh-N. Position operations use the actual web actor and optional
If-Match, returning saved revision/ETag. Move candidates preserve the board
query even when a moved closed task bypasses the status filter.
A virtual builtin is tied to its originating repository and materialized only
after task/move validation. A carrier appearing after the virtual observation
causes conflict, requiring refresh. Creation and position writes are separate
GitHub requests: if positioning fails after creation, the error explicitly says
the carrier was created. Removing a virtual position fails without creating a
carrier; removing a saved position permits missing/deleted targets for cleanup.
Restoration/undo and CLI/TUI adapters remain pending.

Clients with strict v1 decoders must be upgraded before board carriers using the
new field are written. Do not remove unknown fields or rewrite auxiliary bodies
with an older client. Board history is shared editable metadata, not an immutable
audit log or a claim that the named actor's identity is independently verified.
