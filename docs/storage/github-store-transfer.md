# Explicit store transfer (#8)

Store configuration changes select a backend; they do not migrate data. #8
requires a separate command with source/destination validation, preview, stable
qualified ID mappings, checked writes and explicit partial-failure resume.
This implementation is in progress and no transfer CLI is registered yet.

## Source collection (#90)

The GitHub collector `Client.ExportWorkspace` takes one fresh complete issue
and repository-comment observation. The existing task export and portable
journal projections reuse that observation, rather than independently listing
tasks, notes, boards and action history. It includes all task statuses and
logical deletions, deleted/archived notes, deleted board carriers, board queries
and positions, and current/previously archived action history.

Native source records retain the modeled issue identity, body, author, state,
labels, URL and clocks. Raw comment identities, bodies, authors, URLs and clocks
remain source evidence, including journal transport comments that normal
activity views hide. Uncertain journal phases remain uncertain. Journal history
is passive archival data and must not become an executable destination history
or active workflow/review authority.

The board's device-local last-view clock is excluded from transferable board
settings. The original public carrier body remains unchanged as source evidence.
The collector does not read credentials, device session registries, focus, local
work bundles or UI configuration. Virtual boards are not fabricated as native
source resources. The destination planner must handle its builtin board
explicitly. This is not a complete copy of arbitrary GitHub API fields and is
not an atomic repository snapshot.

Collection errors and cancellation return no partial workspace. Tests cover
reversed multi-page native ordering, closed/deleted tasks, deleted notes/boards,
positions, native attribution/raw history, uncertain journals, empty results,
original API errors and cancellation before access. Existing task/journal export
CLI regressions still apply after sharing the projection helpers.

The real GET-only GitHub check in `yoophi/td-sample` collected 196 tasks, 5 notes
(4 deleted), 10 boards (6 deleted), 212 native carriers and 702 raw comments,
plus 404 journal actions. It made one bulk issue collection and one bulk
comment collection, no per-entity requests, no remote writes and no SQLite
access. Serialized output was 16,813,003 bytes. These collection counts include
pagination and are not HTTP request counts or quota guarantees.

## Remaining required work

The SQLite collector `db.ReadSQLiteWorkspace` opens only an existing database
with an encoded file URI and a private read-only connection. Every table read
shares one read transaction, including logical deletions and historical task
data, deleted notes, board queries and position tombstones. It requires the
current schema and never initializes or migrates the source. Unsupported local
action kinds are reported by count without exporting their payloads. Invalid
note or position clocks fail collection instead of inventing timestamps.

Tests verify the original database bytes remain unchanged, literal `?`/`#` and
Unicode filenames work, missing/old schemas and cancellation fail without
initialization, and concurrent committed writes do not change the read
transaction's observation. A real temporary SQLite dataset retained 196 tasks,
49 logs, 52 handoffs, 20 dependencies, 4 file links, 119 deleted tasks and 20
scheduled tasks; its database SHA256 was unchanged after collection. The common
SQLite initializer's separate literal-path bug is tracked by #91.

The `storetransfer` input envelope now binds format version 1, store and stable
source namespace to the serialized payload with SHA256. SQLite namespaces are
explicit identifiers rather than filesystem paths; GitHub uses `owner/repo`.
The checksum is change detection, not authentication. Reading has a default
64 MiB byte limit and rejects oversized, truncated, trailing, unknown-field,
wrong-version and checksum-mismatched input before returning a source. Writing
pre-encodes within the limit, so a capacity error writes no partial bytes; IO
failures retain their original errors and still require an atomic file
controller. Native 196-task inputs round-tripped with unchanged checksum:
SQLite 388,067 bytes and GitHub 16,813,153 bytes.

The source preview now qualifies task/note/board identities by store and source
namespace. Validation rejects duplicate IDs, malformed GitHub native bindings,
conflicting task body/state/author projections, duplicate native comments,
missing parent/dependency/position targets, wrong history owners and malformed
board queries. Passive journal bundles use their existing checksum/reference
validator. A materialized builtin board retains `bd-all-issues`; it is not a
fabricated virtual carrier. Exact ID selectors in board-query ASTs are reported
separately, including intentionally absent targets, and text search is not
interpreted as a reference. Mapping selectors/patterns to destination IDs is
still a planner responsibility.

Native validation retained 197 qualified SQLite task/board identities and 211
GitHub task/note/board identities, with six exact query references. Constructors
also apply the byte limit to the full envelope, including header/checksum
overhead, before returning input.

`storetransfer.Collect` now connects the project's selected source to collection,
the capacity-checked envelope and the validated passive preview. It requires an
explicit stable SQLite namespace and never falls back from GitHub to SQLite.
Collectors charge serialized fragments during projection, conservatively
including fragment separators. The full envelope limit is checked before
returning input. This bounds transferable serialized data, not peak memory of
the existing bulk GitHub snapshot, SQLite table readers, or one JSON fragment.
Invalid limits fail before source access. Capacity failures return no partial
input and perform no destination writes.

The integrated GET-only native run retained 196 tasks in each store. GitHub
made one bulk issue collection and one bulk comment collection, no remote
writes and no SQLite access. SQLite source SHA256 was unchanged; a 1,000-byte
limit returned the explicit capacity error and zero output bytes. These checks
complete the source/input stage (#90), not the transfer command or executor.

SQLite's foreign-key cascade removes a hard-deleted board's position rows.
Available logged board/position before-images are preserved in passive action
history; collection does not fabricate a live deleted board or restore data
that no longer exists. Unlogged hard-deleted content is an explicit source
limitation for the destination preservation/loss report.

The envelope is not a destination-ready plan. The source
must be preserved; previews may not initialize, upgrade or mutate it. Original
authors, sessions and reviews are historical provenance, not destination actors
or current approvals.

#8 additionally needs target validation and complete mappings for tasks,
notes, boards and query/position references; explicit unsupported/loss reports;
checked bidirectional execution; durable confirmed checkpoints; uncertain-write
observation and explicit resume without duplicate creation; whole-dataset/native
and SQLite validation. Reuse the existing import/export and passive journal
archive paths. Data collection alone does not satisfy these requirements.

Remaining stages: #92 target validation/mapping and query/loss preview,
#93 explicit bidirectional CLI/execution, #94 confirmed checkpoint/uncertain
write observation/resume, and #95 native/SQLite end-to-end tests, command matrix
and integration review. Source-stage completion in this feature worktree does
not mean those stages are implemented, main is integrated, or a new release
was published.
