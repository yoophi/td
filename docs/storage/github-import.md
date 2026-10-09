# GitHub issue import

The #24 implementation is in progress. Input validation and `--dry-run`
are available; remote import writes, receipts, ID allocation, resumable replay
and import/export round-trip verification remain required. A regular nonempty
GitHub import currently exits with an explicit error and makes no remote changes.

`td import FILE --format json|md --dry-run [--force] [--json]` reads a file;
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
The CLI still refuses nonempty writes: completion receipts, content/conflict
verification on resume, relation application and provenance replay remain
required before enabling it. Legacy exports do not identify their original
project beyond the td ID; explicit source namespace handling remains required
to distinguish two independent databases with the same legacy IDs.

Markdown imports summary fields and descriptions. Anonymous headings receive
deterministic IDs for an unchanged document; explicit td/gh headings retain
source IDs. Markdown is lossy and cannot restore missing JSON history, original
authors, native timestamps, review records or logical deletion metadata.

Original authors/timestamps will be retained as provenance, while newly created
GitHub resources necessarily use the actual authenticated actor and native clocks.
Imported historical review records will not grant active approval in the target.
The intended writes use best-effort conflict checks, not an atomic transaction
or distributed lock. No uncertain creation will be automatically retried.

The shared import envelope keeps SQLite's historical lenient deserialization;
strict input and semantic validation apply to the GitHub importer. Local import
behavior is covered by the existing SQLite import regression suite.
