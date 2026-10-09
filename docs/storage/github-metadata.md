# GitHub issue metadata

GitHub owns issue numbers, title, visible description, labels, native state,
URLs, and server timestamps. A trailing `td:issue:v1` HTML comment stores the
remaining fields. Native issues without the block use task/P2 defaults.

The v1 envelope supports `operation_id`, `type`, `priority`, `points`,
`acceptance`, `last_state_reason`, optional `entity_kind`, and separate `details`, `board`, or `note` payloads.
Omitted `entity_kind` means `issue`. Reserved `board` and `note` entities are
excluded from issue lists and rejected by issue Get/update/activity operations.
Their feature-specific payloads and commands are described below.

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
Lifecycle and review commands use these records and the shared review policy.
The remaining shared writer contracts are tracked in #16.

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

## Device-local work bundles and context

For `gh-issue`, all `ws`/`worksession` subcommands use bundles inside the
device-local GitHub context file. Repository, worktree, branch and agent/context
identity select the bundle. Listing does not combine another agent's bundles.
A new session clears the active selection while preserving past bundles; an
unfinished previous bundle is listed as abandoned. No SQLite issue database or
GitHub carrier issue is created for a bundle.

Tagging validates all requested issue IDs before changing any issue. By default
an open issue is started through the observed shared workflow transition;
`--no-start` keeps its status unchanged. Repeated tags are idempotent. Untagging
accepts canonical/numeric IDs even when the remote issue is missing or deleted,
so a stale local tag can be removed. Historical issue references remain available
for log/handoff reads after untagging. Ending a bundle preserves claims and issue
status and does not imply that a handoff was recorded.

`ws log` supports progress and the blocker/decision/hypothesis/tried/result flags,
plus `--only` for a verified issue (which need not be tagged). Each target receives
a GitHub activity comment with the bundle ID and a common operation ID. A log
without any targets is device-local; tagging issues later does not upload that
old local entry. Reads use authoritative GitHub comments plus explicitly local
entries, including shared activity on previously untagged or soft-deleted issues.
`ws show --full` displays all these entries; JSON exposes the complete activity
array, while ordinary human `show` displays handoffs and `current` recent activity.

`ws handoff` accepts repeated done/remaining/decision/uncertain flags, including
file and stdin expansion. With no content it summarizes the bundle's logs,
deduplicating operation IDs across issue copies. A remaining item tagged `(gh-N)`
is sent only to that issue. Empty content is an explicit error. `--continue`
retains the active bundle; otherwise a successful handoff ends it. `--review`
uses the shared review policy for tagged in-progress issues. If comment creation
or review fails, the bundle stays active and the error identifies already-written
comments and operation IDs. Successful remote writes are retained; no automatic
retry occurs. Multi-issue operations and local/remote updates are not atomic.

`session cleanup --older-than DURATION` previews removal of stale identity
history in the selected local context; `--force` performs it after checking the
current local revision and re-reading held GitHub claims. A historical session
with an unreleased open/in-progress claim is preserved. Current identity, other
context files, local bundles/activities, and every GitHub comment and workflow
record remain untouched. Positive durations are required. Claim checks are
best-effort observations, not a distributed lock.

`status`/`current`, `usage` (including quiet/compact/new-session), `whoami` and
`resume` include the current bundle, preserved bundle history and its local/shared
activity. Missing/deleted tagged IDs are reported separately; closed tags remain
readable. Shared review queues follow the active review policy. Identity rotation
and focus changes are committed only after successful remote reads and an
unchanged local revision. JSON emits null for no active bundle and arrays for
empty histories; unavailable remote history fails rather than masquerading as an
empty result. All paths validate gh authentication, the configured repository and
remote before proceeding, with no SQLite fallback.

## Shared activity and exit checks

`log`, `comment`, `comments add`, `handoff`, and update's comment/note options
append versioned activity comments through gh. Native comments retain their
GitHub author and timestamps and are never rewritten as td records. Readers
paginate current comments, identify edits and duplicate operation IDs, omit
comments deleted on GitHub, and fail explicitly on corrupted or unknown metadata.
The operation ID in an uncertain write error identifies the comment to inspect;
it is not an idempotency guarantee and writes are never retried automatically.

Issue-level `log` and `handoff` carry the active local bundle ID, including when
the target issue is not tagged. Its historical reference is retained locally so
bundle readers can discover the authoritative shared comment. If that association
cannot be saved after a successful comment, the error states that the comment
was already written. Explicit issue/task flags compare canonical GitHub numbers.

`check-handoff` checks the current actor's effective in-progress issues and the
active local bundle. A focus alone, another actor's claim, closed/deleted issues
or an ended bundle does not require handoff. A recorded handoff does not release
a claim: the check remains conservative until work leaves in-progress or the
bundle ends. Both stores return 0 when clear and 1 when handoff is needed,
including `--json`; JSON is one result object with an unconditional
`in_progress_issues` array. `--quiet` suppresses reminder text while preserving
that exit status. Repository/authentication/read failures remain explicit errors,
including in quiet mode, and are never converted to a clear check or SQLite
fallback. Latest handoffs remain readable in shared context/review consumers;
complete `show` detail parity is tracked separately in #17.

## Lifecycle commands and repeated execution

GitHub native state is `open` for td open/in_progress/blocked/in_review and
`closed` for td closed. Metadata remains authoritative for the open-family
status, claims, review cycles, and historical participation; labels mirror it.

| Command | Accepted current state | Result / repeated execution |
| --- | --- | --- |
| start (begin) | open, in_review, blocked with --force | in_progress; own existing claim is a no-op; another holder is rejected even with --force |
| unstart (stop) | in_progress, open | open, release claim; already open without claim is a no-op |
| block | open, in_progress, blocked | blocked; already blocked is a no-op |
| unblock | blocked, open | open, release claim; already open is a no-op |
| review (submit/finish) | open, in_progress | in_review; repeated review is an error requiring an explicit new cycle |
| approve | in_review | closed, or remain in_review with --record-only; already td-approved closed is a verified no-op; native closure alone is rejected |
| reject | in_review, open | open, clear claim, record changes requested; already open is a no-op |
| close (done/complete) | open, in_progress, blocked, eligible in_review, closed | closed subject to policy; already closed is a no-op |
| reopen | closed, open | open, invalidate active review/claim; already open is a no-op |

Other states are rejected before issue writes. `update --status` uses the same
workflow rather than bypassing review policy. Starting a single issue selects
focus only when it changes state; a batch start and a no-op retain previous focus.
Human transition output goes to stdout. Multi-ID `--json` output is one JSON
object per successful issue (NDJSON), in argument order. A failure stops the
batch, returns nonzero, names completed IDs, and states that earlier changes
remain; it does not roll them back or automatically retry.

Non-minor review requires a handoff: the latest valid handoff is captured, or a
handoff is generated from current activity. Approval verifies its identity and
edit timestamp, review-relevant content and native close/reopen event history.
Changing these invalidates the cycle, including a native closed/open round trip.
Deleted handoffs and transport failures prevent approval. Minor issues skip the
handoff requirement, but record-only approval is not available for them.

Trusted mode requires truthful `--self-review --reason` or `--reviewed-by` when
acknowledging participation; the latter is an attestation, not verified identity.
Delegated/strict enforce their shared independence rules and reject
`--self-review`. Implementation/session history survives claim release and is
used for eligibility. A recorded approval retains the actual reviewer when a
separate actor closes it; another actor must supply a reason. Duplicate
record-only approval is rejected. `--admin "reason"` and
`--self-close-exception "reason"` record explicit close exceptions, and do not
pretend to be review approval. A non-minor in_review issue without a valid
recorded approval must use approve, not close, even with an exception flag.

A native GitHub close is readable as closed, but grants no td approval. When native state disagrees with metadata, reads clear effective stale
attribution. A close/reopen round trip can return native state to agreement
with the stored open-family status (for example in_review); reads do not rewrite
that metadata. Approval still checks the full native event history and rejects
the old cycle. Restart and submit a fresh review. Historical reviews cannot
authorize approval. Authorization errors, concurrent claims and
observed revision conflicts are explicit failures. Checks around GitHub PATCH
and comment writes are best-effort: they cannot provide a distributed atomic
transaction. A post-write failure may have applied a change; operation IDs and
fresh `td show`/activity reads identify what remains before a manual retry.

## Relationship command reads and dependency writes

`dep`, `dep add`, `dep rm` (`remove`), `depends-on` (`deps`/`dependencies`),
`blocked-by`, `critical-path`, and `tree` read/write the repository's GitHub
metadata. Parent IDs and dependency IDs are repository-local canonical `gh-N`,
not GitHub native sub-issues or relationships. PRs, cross-repository references,
self-reference and detected cycles are rejected. `dep add --depends-on` accepts
comma-separated IDs alongside positional IDs. Duplicate adds are verified
no-ops. Removal can clean an edge whose target is deleted or missing.

Queries use the complete non-deleted issue set, including closed issues, and
fetch referenced records missing from the list directly. A missing/invalid
relationship is an explicit error, not an implicitly resolved dependency.
Detected parent/dependency cycles produce an error, even when a tree depth limit
would otherwise hide the cycle. Tree JSON has `children: []` on leaves and
at a depth cut. `blocked-by --direct` limits results to direct dependents;
without it, transitive dependents are included. Critical path considers an
in_review dependency unresolved and does not offer its dependent as ready.
Closed dependencies are resolved; epics are containers rather than candidate
work. Bottleneck JSON rows expose `id` and `score`.

Dependency batches produce one JSON object per completed target. On the first
failure, the command returns nonzero and names targets already completed;
earlier writes remain. Each write uses the observed source and validates the
reachable target graph before/after PATCH. These remain best-effort checks,
without rollback, distributed locking or automatic retries. No SQLite database
is opened. File commands and create/update relationship options are described below.
Show/list hierarchy options are described below.

## Linked file commands

`link`, `unlink`, and `files` use `details.files` in the issue metadata. Each
association preserves ID, issue ID, repository-relative path, role, linked time
and `linked_sha`. The SHA is a SHA-256 content hash used for change detection,
not a Git commit ID. `files` displays the latest start transition's Git commit
snapshot separately. Relinking updates the role/hash/time only when the role or
content changed; an identical link is a no-op.

`link` accepts multiple paths/globs and directories; `--recursive=false` limits
a directory to immediate files. Roles are implementation/test/reference/config.
All inputs are expanded, deduplicated and hashed before the metadata PATCH;
a missing pattern, invalid role, inaccessible file or out-of-repository target
fails without attempting file changes. Paths stay relative to the selected
worktree, including when macOS exposes the same directory through `/var` and
`/private/var`. Symlinks must resolve within that worktree. Recursive directory
walks skip `.git` and `.todos`. `link --depends-on` uses the dependency writer
and rejects combinations with file patterns or file options.

`unlink` matches stored repository-relative paths, so it can remove links to
files deleted from disk. `files --changed` reports new/modified/deleted linked
files, and `--untracked` adds current Git changes not associated with this issue,
including filenames containing spaces and rename destinations. JSON remains an
array of file rows and adds `status`, `current_sha`, and `unlinked` when relevant.
It returns `[]` when no rows match. Human output includes roles and the start
commit when available. A file-inspection or Git-status failure is an explicit
error rather than a successful empty result.

A file-set update preserves other metadata, supersedes active review records,
and clears the current reviewer/review basis. Old participation/reviews remain
historical. The whole set uses one observed PATCH with pre/post revision checks;
there is no distributed atomicity guarantee. A shared activity log is appended
afterward with the real session. If that comment fails, the command returns
nonzero and explicitly reports that the file changes are already saved; it does
not retry or roll back. Inspect `files` and activity before taking recovery
steps. These paths never open SQLite. Show/list hierarchy options are described below.

## Create/update relationship options

`create --depends-on/--blocks` accepts repeated flags and comma-separated IDs.
`update --depends-on/--blocks` replaces the requested set; an explicit empty
value clears it, and an omitted option preserves existing relationships. IDs
are canonicalized/deduplicated before writes. `blocks` is the reverse of
`depends-on`: it edits dependencies on the blocked issues, rather than storing a
second potentially inconsistent relation on the blocker.

The requested final graph is validated before writing issue fields. Missing,
deleted, cross-repository, PR, self-reference and detected cyclic targets are
errors. A source dependency replacement validates the reachable target graph
and applies the complete set in one observed PATCH, preserving unrelated
metadata and recording actual actor/add/remove history. Invalid input cannot
first discard the old set. Approval is invalidated on changed source edges.

Reverse relationships require writes to multiple sources. The plan orders them
to avoid an intermediate dependency cycle, checks observed blocked membership
before/after writes, and checks each source revision and reachable targets. A
failure names completed sources and states that earlier writes remain; there
is no cross-issue transaction or automatic rollback/retry. Lists and checks
remain best-effort because GitHub does not expose an atomic relation lock. A
create that succeeds before relationship attachment fails names the created
issue explicitly; inspect it rather than repeat creation. A multi-issue update
also reports earlier completed issue IDs when a subsequent issue fails.

For `update --status` combined with relation changes, field/relation edits
precede policy evaluation. An old review cannot approve changed dependencies.
If the transition fails, saved edits remain and the error says so. The current
status can be inspected through show. Hierarchy display/filter options are
described below.

## Hierarchy display and list filters

`show --tree` uses the same recursive parent/child view as `tree`, with explicit
`children: []` for leaves. It requires one issue ID; `--json` and `--format json`
produce the tree object. `show --children` preserves the normal issue fields
and adds a direct `children` array in JSON, including `[]` for a leaf. Multiple
IDs produce an array of enriched records. Human output includes direct children,
and epics show children by default in long output. Closed children remain
visible. Stored terminal text is sanitized and command data goes to stdout.

`list --parent ID` selects direct children. `list --epic ID` selects descendants
at every depth, excluding the selected root. The two filters intersect when
combined. They retain normal status/type/priority/label/search/sort/limit filters.
Closed ancestors are included when resolving hierarchy, even when closed issues
are excluded from the results. Missing ancestors and detected parent cycles are
explicit errors. Targets are repository-local issue IDs; PRs and other
repositories are rejected. No SQLite issue database is opened.

For `--parent .` / `--epic .`, the current local context resolves a saved focus
first. A missing/deleted saved focus is an error, not a silently substituted
choice. Without focus, current-session in_progress/in_review work resolves to a
common root; then shared session logs/handoffs/transitions are tried; finally an
active work session's tagged issues can supply a common root. Multiple roots
without a usable active bundle and no usable context both return explicit
errors. Authentication, activity-read and hierarchy errors do not silently
substitute another store or context. Current context reads may span multiple
GitHub requests, so they remain best-effort rather than an atomic snapshot.

### Detailed issue display

`show` (and `context`/`view`/`get`) displays shared logs, the latest structured
handoff, the last three reviews in chronological order, linked files, outgoing
and incoming dependencies, reviewer/closer attribution, and the latest start
snapshot. `--render-markdown` renders the sanitized description and acceptance.
`--short` avoids fetching unused activity history. Activity read failures abort
full output rather than silently presenting missing history as an empty result.

JSON preserves the existing GitHub record fields and adds `logs`, `handoff`,
`comments`, `review_history`, `files`, `dependencies`, and `blocks`. Empty lists
are `[]`; an absent handoff is `null`. `--children` adds direct children to the
same detailed record. Multiple IDs produce an array, and `--tree` retains its
hierarchical output contract.

With no ID, show uses this worktree/branch/device's focus first, then a unique
in-progress issue, then a unique in-review issue, matching SQLite's selection
order. Ambiguity, stale focus, and no available context are explicit errors.

The shared start snapshot is compared with the current local clone. A commit
not available locally (including a repository without its first commit) yields
`git.comparison_available=false` with an explicit warning; unavailable diff or
commit counts are omitted, not reported as zero. Available comparisons include
current commit/branch, dirty files, commits since start and committed diff totals.
These are observations of local Git state, not a remote snapshot or a transaction
with GitHub issue/history reads.

### Calendar dates and deferral

`due ID DATE`, `due ID --clear`, `defer ID DATE`, and `defer ID --clear` write
shared `details.due_date`, `details.defer_until`, and `details.defer_count`
through the observed metadata PATCH/read-back path. They append a shared progress
log afterwards; a failed log explicitly reports that the date was saved and
must not trigger an automatic retry. A date together with `--clear` is rejected
as ambiguous before any write. Missing/deleted IDs, PRs, invalid dates and
permission failures are explicit errors without a SQLite fallback.

`create` and `update` (including aliases and task/epic shortcuts) accept `--due`
and `--defer`. Relative dates use the same `dateparse` implementation as SQLite.
An empty update value clears the date. The first deferral has count zero;
moving an existing deferral later increments the count, while moving earlier,
repeating a date, or clearing it preserves the count. Dates are shared calendar
strings, `YYYY-MM-DD`, and are not converted to UTC instants.

List applies calendar filters before sorting/limit. By default it hides
`defer_until > today`; `--all` and `--status all` include these issues. An explicit
`--status closed` alone still hides future deferrals. `--deferred` selects future
deferrals, `--overdue` selects past due dates except closed issues, `--surfacing`
selects deferrals at/before today with a positive count, and `--due-soon` selects
today through three days ahead. Multiple date flags use SQLite's precedence:
deferred, overdue, surfacing, due-soon, then the default deferral exclusion.

TDQ fields `due`/`due_date`, `defer`/`defer_until`, and `defer_count` use the same
stored values and calendar-day evaluator for GitHub snapshots, board queries,
and SQLite. For example, `due <= today AND (defer = NULL OR defer <= today)`
selects due tasks that are available today. Query expressions remain subject to
their explicit status conditions; they do not implicitly apply list defaults.

Create label spellings keep SQLite's first non-empty precedence:
`--labels`, `--label`, `--tags`, `--tag`. Each accepts repeated/comma-separated
values. File/stdin rich-text input uses the command's configured input stream;
multiple stdin consumers or inline/file conflicts fail before issue writes.

### Query and ranked search

`query` evaluates parsed TDQ against a fresh repository-scoped GitHub issue
snapshot using the same evaluator as SQLite and board queries. Deleted records
and pull requests are excluded. Explicit query conditions determine status and
deferral scope. `--output table|json|ids|count`, `--limit`, `--sort` and
`--max-scan` are supported; count reports matched records before the output limit.
Limit and scan warnings go to stderr, including JSON mode, leaving stdout usable
as one JSON array. An empty JSON result is `[]`. Fields, examples and explain
remain offline. Invalid predicates, formats and negative limits fail explicitly.

`list "TDQ expression"` and `list --filter "TDQ expression"` use the same
snapshot and evaluator, including calendar, relation and activity predicates.
As with SQLite's TDQ list mode, the expression defines the scope; ordinary list
filter flags do not add implicit predicates. `--filter ''` and combining the
flag with a positional expression are explicit errors. Sort, reverse, limit and
short/long/JSON formatting retain list semantics. Detailed TDQ listings fetch
shared logs and handoffs before emitting output, so activity failures cannot
leave an apparently successful partial list.

Ordinary list mode supports points (`N`, `>=N`, `<=N`, `N-N`), implementer,
reviewer, `--mine`, reviewable queues, created/updated/closed ranges, normalized
status aliases, ID/type/label/search filters, hierarchy and calendar filters.
All predicates apply before sorting and limit. Points zero is an actual filter
in both stores; malformed numbers, invalid dates and reversed ranges fail
explicitly. Types, IDs and labels accept repeated or comma-separated values.
`--status all,open` includes deferred issues but retains the explicit open
condition. `--mine` uses this repository/worktree/branch/device's actual session;
use TDQ `@me` for predicates expressed in query syntax.

Created/updated/closed flags retain SQLite list's UTC timestamp bounds:
`after:DATE`, `before:DATE`, `DATE..`, `..DATE`, `DATE..DATE`, or a single date.
Both bounds are inclusive; a single date runs from its UTC midnight through
the following UTC midnight. This legacy list contract differs from local-day
TDQ and due/defer calendar predicates. An absent closure timestamp does not
match a closure date filter. Sort fields include timestamps, points, due_date,
defer_until and defer_count; null dates sort before populated dates ascending.
Long listings read shared logs and the latest handoff, failing before output
if an activity read fails.

`blocked`, `in-review`/`ir`, `ready` and `next` read GitHub issues directly.
Ready/next select open issues without a nonclosed, nondeleted dependency.
They retain SQLite shortcuts' deferral scope: they do not implicitly exclude
future deferrals. JSON is a bare issue array, except next is one issue or `null`.
`task list` and `epic list` use the same common list path and options.

`reviewable` returns separate `awaiting` and `ready_to_close` arrays in JSON;
`--include-approved` enables the close bucket. `list --reviewable` keeps a bare
list, with approved issues excluded unless `--include-approved` is set, in
both human and JSON output. Approval classification precedes limit in both
stores. Trusted mode can surface your implemented issue, but an eventual
approval still needs honest attribution or self-review acknowledgement.
Other modes enforce their existing implementation/history/creator policy.
GitHub queue reads verify the review snapshot and the observed revision;
stale, missing, conflicting or failed review observations abort explicitly.
Queue membership is informational and never authorizes a later mutation.

Revision hashing compares label names in sorted order because GitHub PATCH and
GET responses can return an identical label set in different orders. Display
order is preserved. Membership/name changes and every other revision field
(including metadata body and timestamps) still participate in conflict checks;
normalizing order does not make PATCH atomic or add automatic retries.

`search` searches IDs, titles, descriptions, shared logs and all historical
handoffs. Native comments and structured user comments are excluded; use
`query 'comment.text ~ text'` for comments. Search retains SQLite LIKE semantics
for `%` and `_`, including ASCII case folding; TDQ `~` uses the shared literal
text evaluator instead. Status, type, priority and label filters apply before
ranking, and limit applies after ranking the complete candidate set. Scores
prioritize exact ID/title matches over description and activity matches;
`--show-score` exposes the score and matching field. JSON keeps the existing
SQLite search result shape with `Issue`, `Score` and `MatchField` keys.

Issue pages and activity pages are fully paginated. Activities are loaded lazily
and cached only within one command snapshot, once per relevant issue. A denied,
malformed or rate-limited read aborts output instead of substituting cached or
partial results. There is no persistent read cache or automatic retry. Separate
GitHub reads are not an atomic snapshot: concurrent edits can change records
between issue and activity reads. Search may require one comments pagination
sequence per candidate issue, so large repositories can consume significant API
quota even when the final output limit is small.

GitHub's issue listing can also lag a successful direct create/read-back: a new
issue may briefly be missing from query/search/list results. Fresh requests do
not guarantee immediately current results. The CLI does not silently retry or
label such a listing as a strongly consistent snapshot; repeat a read when you
need to observe a just-created issue in list-based queries.

Set `TD_GH_DEBUG=1` to print numeric API invocation counts, observed HTTP response
counts (including paginated responses), last status and remaining quota to
stderr. Counts include repository verification through td's API runner and
exclude requests performed internally by `gh auth`; they are observations of
received headers, not a claim to count every network exchange. Credentials,
bodies and arbitrary response headers are not printed. Rate-limit errors remain
explicit and include the retry/reset observation when available.

### Board CLI

`td board list/create/show/edit/delete/move/unposition` use the configured
GitHub repository when `store=gh-issue`. They never open SQLite or fall back to
it. Board references accept the public ID (`bd-gh-N`, or `bd-all-issues` for the
builtin) or an unambiguous name. Board query `@me` uses the actual device-local
td actor; this differs from the web API's existing anonymous query scope.
`show --status` accepts repeated/comma-separated valid statuses and defaults to
hiding closed tasks. Future-deferred tasks remain eligible according to the
board's query, without an implicit list deferral filter.

Explicit CLI configuration edits are shared: name, TDQ, and `--view-mode` are
saved together in one carrier update. Board history records the actual actor.
The builtin cannot be renamed, filtered, or deleted; an explicit view/position
write may materialize its virtual carrier. If a subsequent position write
fails, the error reports that creation already occurred. `move` uses a positive,
one-based slot among saved positions, matching the SQLite CLI contract;
unpositioned cards retain query order. `unposition` also permits cleanup of a
saved reference to a missing or logically deleted task.

Writes compare the original observed revision before saving and verify the
GitHub response and readback afterward. GitHub does not provide atomic
compare-and-swap for these updates; conflicts or uncertain write outcomes are
errors and are never automatically retried. Refresh and inspect the carrier
before deciding whether to retry. Read-after-list propagation delays can occur.
CLI mutations with `--json` emit the confirmed board object; list and show emit
bare arrays. The wider monitor TUI integration is tracked separately within #6.

Monitor integration (#6) uses `NewGitHubModel` as a separate
remote model constructor with verified configured origin, actual device-local
actor/focus, and owner cancellation. It never constructs SQLite or td-sync.
Monitor-only store adapters retain observed revisions for forms, lifecycle,
reviews, deletion, board editing, ordering, details and summaries. Slow dashboard
polls do not queue overlapping API sweeps; responses for superseded filters do
not replace the current display. Failed reads retain the previous dashboard.

The CLI and both embedded entry points now select this constructor for a
configured gh-issue project. Missing gh, authentication/remote errors and invalid
preferences stop initialization explicitly. The owner closes/cancels requests on
exit; cancelling a pending write cannot undo a request GitHub has accepted, so
the CLI prints an inspection warning when a write outcome is still pending.

Pane heights, search/sort/type/closed filters, onboarding state, last board and
per-board view modes are private device settings under the repository/worktree
context directory. They do not update the shared project config or GitHub board
carrier, do not depend on session rotation, and never create a builtin carrier.
Concurrent preference writes use a local lock and atomic private-file replacement.
Malformed/future preference files are preserved and reported rather than reset.
Shared board name/query/order changes still use observed carrier writes.

Board editor writes reject overlapping submissions. Delayed results cannot close
a new draft opened after the original editor closed. An uncertain creation keeps
the draft and disallows another creation attempt within that form: inspect GitHub
before reopening it. Request state and errors occupy separate footer rows so
long key hints do not hide loading/saving/cancellation feedback.

The N-key notes UI is not wired for either store yet; help and the action state
this explicitly. Follow-up #44 depends on #20 and is an implementation task,
not an impossible-work exception. Runtime verification and the #6 acceptance
audit are recorded on that issue. Complete dashboard refreshes read retained
activity for every task, including deleted tasks, and can take minutes on a
repository with substantial history. GitHub monitor and server event polling
have a minimum interval of five minutes; longer configured intervals are respected.
Overlapping dashboard sweeps are skipped. A monitor rate-limit response suppresses
further dashboard requests until its observed reset deadline (one minute when no
deadline was supplied), preserving the original error and the prior display.
No atomic repository
snapshot or immediate refresh guarantee is provided.

## Notes

`td note add/list` (`ls`)/`show/edit/delete/restore/pin/unpin/archive/unarchive`
select the configured store. GitHub notes have canonical `nt-gh-N` IDs, native
issue titles and visible Markdown bodies, with `entity_kind: "note"` and a
separate `note` payload (`version: 1`). The payload stores pin/archive flags,
optional logical `deleted_at`, and actor/operation/time-stamped history. Normal
task and board lists, lifecycle and review operations exclude these carriers;
task `gh-N` IDs and SQLite `nt-*` IDs cannot substitute for a GitHub note ID.
Native GitHub close/reopen and labels do not change note flags. Note writes
preserve native state and labels. Delete is logical; restore preserves the other
flags. Repeating an already-applied flag operation does not append history.

Search matches SQLite LIKE semantics (ASCII case folding, `%` and `_`
wildcards), across title and content, after complete pagination. Lists sort
pinned first, then newest native update time, with ID as a deterministic tie
breaker. Filters run before limit; nonpositive limit is unlimited. Normal
lists exclude deleted notes and, unless `--all`, archived notes. `--deleted`
returns only deleted notes; `show --include-deleted` explicitly reads one.
Empty JSON lists are `[]`. Titles/content support Unicode.

Notes and their flags/history are shared repository data. Actor/session state
is device-local and worktree-scoped; no SQLite database is opened in GitHub
mode. The public `pkg/notes.OpenWithContext` API accepts the actual worktree,
validates the selected GitHub remote/authentication before opening an editor,
and revalidates the remote/session on each operation. Close/cancellation stops
subsequent GitHub work. Changing the active session requires reopening the store.

Editors and UI clients should retain the returned Note and use `UpdateObserved`
(or the corresponding flag/delete/restore observed method). Private observations
preserve the revision originally shown to the user. The ID-only update API reads
at call time and cannot detect a change made during an earlier external editor.
Writes compare the original revision before PATCH and confirm returned payload
and a subsequent read. These are best-effort checks; GitHub has no atomic CAS
or distributed lock. A concurrent writer can still win in the request window.
A failed/uncertain write or failed readback requires inspecting GitHub before
retrying; td attempts each write once and reports its operation ID when known.
Permission, authentication, disabled Issues, rate limit, remote and network
errors are explicit, with no SQLite fallback. Upgrade older collaborating td
clients before creating notes with this extended payload: strict old decoders
reject an unknown `note` field rather than silently rewriting it.

Examples:

```sh
td note add "API decisions" --content "Use the shared endpoint"
td note list --search "API" --json
td note pin nt-gh-107
td note edit nt-gh-107 --title "Updated API decisions"
td note archive nt-gh-107
td note list --archived --output json
td note delete nt-gh-107
td note show nt-gh-107 --include-deleted --json
td note restore nt-gh-107
```

Monitor notes UI is tracked separately in #44.
