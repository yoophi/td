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
