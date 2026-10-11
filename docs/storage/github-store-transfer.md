# Explicit store transfer (#8)

Store configuration changes select a backend; they do not migrate data. #8
requires a separate command with source/destination validation, preview, stable
qualified ID mappings, checked writes and explicit partial-failure resume.
This implementation is in progress. `td store transfer --dry-run` is registered
in this feature branch; explicit execution is not yet available and returns an
error before source or destination access.

## CLI preview (#93, execution in progress)

The source is the store configured for `--work-dir`. The destination must be
explicit and use the other store; no configuration is changed. Examples:

```bash
# From a SQLite project to an existing Git checkout with a GitHub origin:
td -w /path/to/sqlite-project store transfer --dry-run --to gh-issue \
  --source-namespace my-sqlite-source --destination /path/to/github-project \
  --remote origin --json

# From a GitHub project to a SQLite destination (preview does not initialize it):
td -w /path/to/github-project store transfer --dry-run --to sqlite \
  --destination /path/to/sqlite-project --destination-namespace my-sqlite-target --json
```

JSON returns `dry_run`, `writes: 0`, store names, whether SQLite initialization
would be needed and the full plan. Human output includes counts and handling
disclosures. `--map qualified-source-key=existing-target-id` may be repeated;
unknown keys, duplicate keys, wrong kinds and conflicting target bindings fail.
`--max-bytes` sets the input capacity limit. Implicit issue-session discovery
and autosync hooks are skipped because transfer owns its explicit I/O.
`--execute` currently returns `unsupported_operation`; this is a feature-stage
limitation, not completion of the execution task.

`BuildTaskImportInput` now prepares the existing task importer's validated
inputs from the immutable capsule. It supplies a bridge from qualified transfer
keys to importer keys. SQLite namespaces and IDs use stable source-local hash
proxies because valid SQLite rows can include `td-gh-N`, whereas the existing
importer accepts a narrower ID alphabet. Proxies depend on identity, not changing
payload bytes or filesystem paths. Parent/dependency and side-table owners are
mapped together. Original identities remain in the full capsule and key bridge.
Legacy SQLite `blocks` rows remain in that capsule and are omitted from active
GitHub dependencies. The general import/export ID incompatibility is #96.

GitHub task projection shares the export converter rather than repeating it.
`ProjectSQLiteTasks` resolves task-only caller mappings, clears destination
workflow actors/approval stamps and namespaces historical logs/handoffs. It
does not project active review/session records or device work bundles. File
identities use validated unique paths, including when native file metadata has
no row ID. Carrier ownership and unique paths are checked without inventing an
original file ID. This conversion is not receipt verification or write authority.

The captured 196-task inputs passed preparation in both directions. Replaying
projected GitHub tasks into a private temporary SQLite database retained 196
tasks, 49 logs, 52 handoffs, 20 dependencies, 4 files and 119 logical deletions;
no original workflow actors or active reviews were installed. The complete
source capsule was retained alongside this fixture. This is task projection and
fixture replay, not completion of whole-store execution. #93 still requires
checked task/note/board writes, workspace archive installation, verified target
ownership/receipts and writer audit before `--execute` becomes available.

The private SQLite writer now inserts prepared tasks, notes, live boards,
positions and the complete original capsule/mappings in one `BEGIN IMMEDIATE`
transaction. It compares the current destination against its prior observation
while holding the write transaction, rejects colliding IDs/history rows/active
position slots, and resolves parents explicitly because the current SQLite
schema has no parent foreign key. Dependency and position foreign keys are
deferred until commit. A failed write rolls back both entity rows and the
archive table creation. Capacity errors stop before mutation; commit errors
retain the original error and require observation before any retry.

The passive capsule lives in `store_transfer_archives`, created transactionally
only when writing. Read-only collection never creates this table, but includes
existing capsules in the next workspace input. Previous capsules do not become
active reviews, source actors, or executable action history. Nested capsule
data remains subject to the existing capacity limit; exhaustion is an explicit
error rather than a promise of unlimited history storage.

Deleted GitHub boards have no corresponding SQLite board tombstone column.
Their configuration and positions remain in the complete capsule; the planner
marks them `archive` and does not recreate them as live boards. Their queries
and positions are reported as passive rather than pending writes. Board names
are preserved under the current SQLite schema, which permits duplicate names;
identities remain distinct by ID.

A captured GitHub input was projected and atomically written into a private
temporary SQLite destination: 196 tasks, 5 notes, 4 live boards, 1 active
position, 49 logs, 52 handoffs, 20 dependencies, 4 files and 119 deleted tasks.
Six deleted boards remained passive in one complete source capsule. Subsequent
source collection retained that capsule and mappings, with zero active source
workflow actors/reviews and unchanged original input bytes. This validates the
SQLite primitive against captured native input, not public CLI execution or a
live GitHub destination. Checked execution, adoption policy, GitHub writes and
checkpoint/resume remain required in #93/#94.

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

The source/input and read-only target/mapping/query/loss-plan stages are
implemented in this feature branch. Remaining stages: #93 explicit
bidirectional CLI/execution, #94 confirmed checkpoint/uncertain
write observation/resume, and #95 native/SQLite end-to-end tests, command matrix
and integration review. Source-stage completion in this feature worktree does
not mean those stages are implemented, main is integrated, or a new release
was published.

## Target/mapping/query and preservation plan (#92)

`PlanIdentities` currently operates on validated recorded source/target inputs.
Bindings use source-qualified keys, require existing target IDs of the same
entity kind, and reject duplicate targets and reserved builtin collisions.
Titles never imply adoption. The builtin maps to its logical `bd-all-issues`
identity; no native carrier number is invented. Other new target IDs remain
unresolved until checked creation receipts. The plan does not grant write
authority or prove current target access or receipt provenance.

`RewriteQuery` walks TDQ AST exact ID/parent/dependency predicates and relation
functions. It retains text searches and nonidentity values and preserves sort
semantics. It uses TDQ-compatible quoting rather than the AST's diagnostic
`String` formatter or Go escape sequences that the TDQ lexer does not decode.
Missing mappings and identity pattern/order selectors block automatic use of
the rewritten query. Pending creations must resolve before board writes; absent
selectors require an explicit preservation/loss policy.

Recorded native 196-task inputs were replayed in both directions without API or
database writes. This verifies mapping/query processing against captured data,
not live destination authorization, receipt validation or migration execution.
`InspectTarget` now validates an explicitly selected destination without
changing store configuration. An existing SQLite target is read with the same
private read-only transaction and schema checks, even if the project's current
store is GitHub. This explicit destination opt-in never weakens the source's
no-fallback guard. A missing database is represented as needing initialization;
preview does not create directories or databases. Dangling destination
directories fail inspection.

GitHub target inspection uses `OpenRemote`: one remote/auth/Issues preflight
opens the canonical repository without first selecting it in configuration.
Credential copies are cleared after digest-based client construction. Namespace
mismatch is rejected. Target collection retains the one-bulk-observation
contract. The native run observed a new SQLite target without initialization,
197 existing SQLite identities with unchanged database SHA256, and 211 GitHub
identities with one auth, one repo preflight and one issue/comment collection
each. No remote writes or GitHub-to-SQLite fallback occurred. Inspection does
not guarantee future write permissions, capacity or unchanged remote state.

`BuildDataPlan` now includes current parent/dependency edges, board positions,
rewritten board-query previews and preservation/loss disclosures. Pending
references retain qualified source keys. `ReadyRelations` and `ReadyPositions`
return concrete destination IDs only when every active reference is mapped;
an unresolved reference fails the whole batch rather than returning a partial
write list. These helpers do not authenticate receipts or grant write authority.
Query references to notes/boards are absent task references, not pending task
creations.

SQLite's raw legacy `blocks` rows do not participate in its effective blocker
queries. A GitHub target keeps those rows as passive evidence instead of
silently converting them into active dependencies. SQLite position tombstones
also remain passive for a GitHub target, whose current position model has no
tombstone field. A SQLite target can represent both rows directly.

The report counts tasks, notes, boards, applicable relationships/positions,
typed historical records, raw native comments and portable journal actions.
Historical counts describe collected projections, which can overlap native
evidence; they are not counts of distinct user actions. Original actors,
reviews, transitions and journals must remain passive evidence. The report also
discloses excluded local action kinds, unavailable hard-deleted data, changed
ID text-search behavior and unverified future destination access. These are
intended handling decisions, not proof that an archive or entity was written.

Recorded native inputs produced 211 identities, 42 relations and 2 positions
for GitHub-to-SQLite; SQLite-to-GitHub produced 197 identities and 42 relations.
No API or database write was performed. The complete planning stage was
self-reviewed against #92's acceptance criteria; storetransfer, query, db and
ghstore race suites passed. This does not complete the parent transfer task.
Creation receipt verification and checkpoint reconciliation belong to
the checked executor/resume stages (#93/#94), not to this read-only plan.
