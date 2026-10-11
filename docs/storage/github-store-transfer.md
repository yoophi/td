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

#90 still needs SQLite source collection and the versioned, capacity-bounded
input envelope with strict identity/reference/checksum validation. The source
must be preserved; previews may not initialize, upgrade or mutate it. Original
authors, sessions and reviews are historical provenance, not destination actors
or current approvals.

#8 additionally needs target validation and complete mappings for tasks,
notes, boards and query/position references; explicit unsupported/loss reports;
checked bidirectional execution; durable confirmed checkpoints; uncertain-write
observation and explicit resume without duplicate creation; whole-dataset/native
and SQLite validation. Reuse the existing import/export and passive journal
archive paths. Data collection alone does not satisfy these requirements.
