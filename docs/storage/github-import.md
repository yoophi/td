# GitHub issue import

The #24 implementation is in progress. Input validation and `--dry-run`
are available; full batch import orchestration and history replay
and import/export round-trip verification remain required. A regular nonempty
GitHub import currently exits with an explicit error and makes no remote changes.

`td import FILE --format json|md --dry-run [--force] [--json] [--source NAME]` reads a file;
`FILE=-` reads stdin. A `.md` extension selects Markdown with the default format.
Every input item is parsed and validated before remote lookup. The target must
still be a configured, accessible GitHub repository; there is no SQLite fallback.

JSON accepts the existing `issue`, `logs`, `handoffs`, `dependencies`, `files`
and optional versioned `github` envelope. The historical singular `handoff`
and dependency ID arrays are accepted. Missing type/priority/status in a legacy
envelope use task/P2/open; a GitHub source envelope must remain internally
consistent. Unknown fields, future versions, duplicate source identities,
inconsistent native body/metadata, bad statuses/dates/files/history and missing
or cyclic parent references fail before writes. Relationships must reference
items in the input, so use a complete JSON export with `--all` for restore.
Both legacy `depends_on` and `blocks` relations retain their direction in the plan.

Source keys qualify GitHub numbers with the original repository. Different
repositories' `gh-1` entries are distinct; foreign numbers are never reused as
destination IDs. New numbers can only be allocated by GitHub during creation.
For a snapshot of the same repository, existing native IDs are planned as skips,
or overwrites with explicit `--force`. Changes since export are shown. A missing
source number or a PR/note/board in its place is an error instead of being silently
recreated. The plan also recognizes a deterministic creation identity in the
existing metadata `operation_id`: a SHA-256 digest of the qualified source key.
It is embedded in the same POST as the issue body, not a later local receipt.
An existing identity produces a `resume` row with its allocated GitHub ID,
including when input contents have changed. It does not mean import completed.
Duplicate destination identities fail before writes. Ordinary creation uses
its existing random operation ID; metadata schema v1 is unchanged.

The shared internal creation path can use this identity and retains normal
validation, state/priority mirrors and uncertain-write diagnostics. Tests
simulate a POST that creates an issue but loses its response, then recover
the allocated ID from a fresh repository listing without a second POST.
The CLI still refuses nonempty writes: batch allocation/application ordering,
provenance replay and completion orchestration remain required before enabling it.

The planner reads a fresh complete issue/comment snapshot using two bulk
collections (each paginated by gh), even when a reusable cache exists. It
recognizes versioned native-comment completion receipts that bind a source
key and input digest to a target ID and target content basis. An unchanged
completed import is skipped. A changed source requires explicit `--force`;
a destination edited after completion is a conflict even with `--force`.
Receipt reading does not add per-issue or per-comment API calls. No completion
receipt is emitted by the current nonempty CLI path yet.

The internal preparation/completion publishers check fresh destination content
before writing their native comments and read it again afterwards. An uncertain
POST is never retried automatically; its underlying error is preserved and a
fresh plan can recover a saved checkpoint. A prepared intent records both
before/after content bases for a particular source digest. The plan reports
`resume_state=before_apply` or `after_apply` if the destination matches one of
them. Another destination state is a typed conflict even with `--force`, and
a changed source cannot replace a pending import version. Resume with the
original input first. A later completion supersedes its earlier intent; a newer
intent supersedes an older completion so an interrupted subsequent import can
be recovered. These publishers do not apply the issue PATCH or restore history;
full batch orchestration remains unfinished and is not exposed as a standalone CLI.

The internal single-issue apply stage now projects the imported fields into a
normal observed PATCH, prepares its exact before/after bases, rechecks content
after preparation, and performs standard revision/readback checks. Parent and
dependency IDs are resolved after allocation; reverse `blocks` edges are stored
on the actual dependent. Input dependency cycles fail before GitHub access.
Dependency observations are checked around the issue write. A lost PATCH
response can be resumed at `after_apply` without another PATCH or intent POST.
Unprepared creations must still match the minimal original creation payload;
external edits or a changed original payload cannot be overwritten on resume.
Imported workflow actors/approvals are cleared from active metadata; status,
dates, files, labels and mapped references remain portable fields. This stage
does not publish a completion receipt or finish history restoration.

Remaining batch cases include changing an existing graph without temporary
cycles, logical-deletion ordering/references, skip-row relation consistency and
retaining prior destination history before a forced overwrite. These remain
implementation work, not reasons to exclude the original import scope.

The target basis includes the currently modeled native issue identity, author,
state, title, body and label set. Native timestamps and label ordering are
excluded because comments/server responses can change them without changing
writable issue content. Unknown/edited receipt fields, mismatched targets and
multiple completed targets for one source are explicit errors. Receipts remain
ordinary native comments and cannot create active review/workflow privileges.
They are conflict evidence, not an authenticated signature or atomic lock.

Legacy exports do not identify their original project beyond the td ID. Use
`--source project-a` to qualify SQLite/Markdown source identities, consistently
on preview, apply and resume. Portable JSON can instead declare the optional
`source_namespace` on each legacy item, allowing independent projects with the
same td IDs in one input. Parent/dependency references stay in their item's
namespace; a matching ID in another project cannot resolve them. The option
cannot override a conflicting declaration. Namespace names use 1-128 ASCII
letters/digits and `.`, `_`, `/`, `-`, starting with a letter or digit.
GitHub exports keep their repository-qualified identity and cannot declare a
legacy namespace. `--source` on GitHub-only input or a SQLite destination is an
explicit error. For compatibility, unnamed legacy previews retain the original
identity format; actual cross-project application must require a source name
before it is enabled. Names identify public import provenance, not credentials.

Markdown imports summary fields and descriptions. Anonymous headings receive
deterministic IDs for an unchanged document; explicit td/gh headings retain
source IDs. Markdown is lossy and cannot restore missing JSON history, original
authors, native timestamps, review records or logical deletion metadata.

Original authors/timestamps will be retained as provenance, while newly created
GitHub resources necessarily use the actual authenticated actor and native clocks.
Imported historical review records will not grant active approval in the target.
The intended writes use best-effort conflict checks, not an atomic transaction
or distributed lock. No uncertain creation will be automatically retried.

## Provenance storage stage

Internal provenance helpers preserve the complete portable source envelope in
versioned native comments, including original GitHub body/author/activity/reviews
and SQLite authors/timestamps/logs/handoffs/files. JSON bytes are split into
32 KiB pieces and encoded as Base64 so embedded historical td markers cannot
be interpreted as current activity or approvals. Each comment is bounded to a
conservative 60000 bytes, and each set binds source identity/digest, destination
ID, content digest, part index and total count. The same representation can
archive the prior destination before a forced overwrite, but batch capture of
that prior destination is still outstanding.

Inventory is built from one complete bulk comment observation. The writer
posts only missing pieces, checks destination content before/after the set,
reports earlier successful parts and preserves the underlying failure cause.
An uncertain POST is not retried: re-read comments to find a saved piece first.
Incomplete, duplicate, misbound, edited or inconsistent pieces fail explicitly.
Recovery verifies the assembled content digest before decoding the original
source envelope; original author/time/review fields stay historical data.

Tests cover large Unicode histories, embedded activity markers, original GitHub
review/author data, loss of a response after a piece was saved, missing-only
resume and a no-write repeat. Chunk replay makes two issue content checks per
set, not one issue/comment lookup per piece. It still consumes one comment POST
per missing piece and may encounter account/secondary API limits.

These helpers are not yet connected to the normal CLI write path. Batch
orchestration must capture prior destination data, verify complete source sets
before final completion receipts and on later replays, and expose preserved
history appropriately in reads/exports. Native provenance comments currently
remain passive comments rather than active log/handoff/workflow events.

The shared import envelope keeps SQLite's historical lenient deserialization;
strict input and semantic validation apply to the GitHub importer. Local import
behavior is covered by the existing SQLite import regression suite.
