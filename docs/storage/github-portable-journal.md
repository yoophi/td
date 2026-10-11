# Portable GitHub journal history

`td export --format journal --output history.json` exports all native action
journals (including prepared, uncertain and undone records) and previously
restored journal archives. `td import history.json --format journal --dry-run`
validates and previews restoration; omit `--dry-run` to store the archive.
Both commands require `store=gh-issue`, a matching GitHub remote and working
`gh` authentication. They never open a SQLite task database.

This is a history archive. Restoring it does not execute the original commands
or modify tasks. Restore task fields and relationships separately with the
normal JSON issue import, then pass its allocated IDs as mappings when needed:

```json
{"github:source/project:gh-12":"gh-42"}
```

```sh
td import history.json --format journal --mapping ids.json --dry-run --json
td import history.json --format journal --mapping ids.json --json
```

`FILE=-` accepts stdin. `--mapping` is a JSON object with lowercase qualified
source repository keys and canonical destination `gh-N` values. Keys must
refer to structured IDs present in source history; destination IDs must exist
in the selected repository's fresh issue collection. Ambiguous, conflicting,
many-to-one (within a source repository) and missing-target mappings fail before archive writes. Mappings
annotate references separately; they never rewrite source IDs, snapshots,
embedded text/JSON, authors, sessions, clocks or checksums. Omitted mappings
leave references qualified by the original repository, even when issue numbers
coincide. Previous bindings are retained on further export/import hops.

`--force` and `--source` do not apply to journal archives and are rejected.
`--mapping` on other formats is rejected. Journal export always contains full
JSON history, so `--all` and `--render-markdown` are rejected rather than ignored.
The existing JSON array and Markdown task formats retain their own contracts.

## Integrity and attribution

The `td-journal` format has version 1, flat `sources` and `bindings` arrays,
source SHA-256 values and a bundle SHA-256. A source contains its original
repository and decoded native action journal records. Each action retains its
native comment ID/URL/author/timestamps and versioned logical fields. Existing
action checksum and ownership validators check state snapshots and compound
member references. Unknown fields/versions, invalid IDs, duplicate operations,
conflicting mappings and missing compound members are errors. Checksums detect
corruption; they are not signatures and do not authenticate an exported author.

Repeated sources are deduplicated by checksum on re-export. Different snapshots
of a source repository remain distinct. There are no nested archive-of-archive
wrappers: original source history and mapping provenance stay readable across
hops. Original multipart transport comments and intermediate restore comments
are not reconstructed as original GitHub events. Destination transport comment
IDs, author and clocks come from GitHub and identify the actual restoring user;
the archive manifest binds those observed IDs to the stored source data.

## Undo and review boundary

Imported source actions are passive data. They do not appear as executable
`td last` actions, authorize reviews, recreate native events or become undo
receipts in the destination. Passing an archive comment to receipt recovery
returns an explicit unsupported-operation error. New destination-native
actions, including a separately executed issue import, retain their ordinary
undo/recovery rules. No claim is made that a copied source review, session or
native comment identity has been recreated in another repository.

Use journal export to inspect/retrieve restored history. Normal task reads,
monitor and old readers can ignore the passive archive comments on the existing
`journal` carrier; no new issue entity kind or executable action schema is added.

## Publication and limits

The payload is gzip-compressed and base64-encoded into immutable native comments
with `td:journal-archive-part:v1` markers. A `td:journal-archive:v1` manifest is
published last, after a fresh collection verifies every part. It binds native
IDs, creation clocks, content hashes, writer/session identity and decoded/encoded
sizes. Manifest readback must verify before success is reported. A reader checks
all parts, checksums, author/carrier identity, clocks and the complete bundle.

Limits are 64 MiB of JSON input/output/decoded payload and 128 parts of 48,000 base64
bytes. Capacity failure stops explicitly. Known input capacity is checked before
carrier/comment writes; a later API error can leave a carrier or partial archive.
Part POST starts are spaced by at least one second within the invocation. Other
processes and GitHub's own limits can still cause rate-limit errors.

An uncertain POST is never automatically retried. The error preserves the
original GitHub diagnostic and identifies the input checksum and partial work.
Inspect remote state before explicitly rerunning the same input and mappings.
The same real destination session and authenticated author can reuse observed,
unchanged parts. A fully verified repeated restore is read-only. Another
session/author cannot adopt an unfinished archive. Duplicate parts/manifests,
external edits or conflicting bindings cause errors; no history is overwritten
or silently deleted. Re-export refuses an incomplete archive rather than
silently publishing an incomplete backup. Preserve the input file until restore
has succeeded; incomplete parts alone may not contain enough data to recover it.

Archive writes are append-only best-effort operations, not a GitHub transaction.
A concurrent writer can still create duplicate parts between observation and
POST; collection verification reports that conflict. GitHub offers no atomic
compare-and-swap for these comment writes. Archives are never automatically
deleted as rollback. Shared task data is not mutated by archive restoration.

Export, dry-run and confirmed repeated restore use one paginated issue
collection and one paginated repository comment collection, without per-action
or per-part GETs. Initial restoration uses a bounded number of fresh collections
around publication plus one POST per missing part and the final manifest.
Repository/authentication checks and page count add HTTP requests. File export
publishes via a private temporary file and rename after complete validation;
an error preserves the previous backup.

## Verification

- `TestPortableJournalRestoreRoundTripMappingsNoExecutionAndBulkReads`:
  multipart round trip, exact source preservation, mapping hops, no target
  mutations, no active-history promotion, read-only repeats and bulk reads.
- `TestPortableJournalRestoreUncertainWritesStopAndExplicitRetryObserves`:
  accepted-response loss and denied part/manifest writes preserve errors and
  stop; explicit reruns reuse observed parts without duplicate POSTs.
- `TestPortableJournalArchiveCorruptionAndForeignResumeAreRejected`:
  edited/deleted/duplicate/foreign parts, versions, checksums, sizes and manifest
  references fail; another session cannot adopt an unfinished archive.
- `TestGitHubJournalPortableCLIExportRestoreAndDestinationUndo`:
  actual CLI subprocesses and gh fixture, dry-run, restore, repeat, re-export,
  original/native identity separation, subsequent destination undo and no SQLite.
- `TestGitHubJournalPortableInvalidInputAndFlagsFailBeforeNetwork`:
  malformed input, unsupported store/flags and SQLite isolation.

Native acceptance on `yoophi/td-sample` preserved 368 source actions across
80 archive parts and one manifest (`ghc-6109903726`, carrier #127). A mapping
from source `gh-138` to destination `gh-139` survived re-export without changing
either task. Restoration took 131.519 seconds: 93 API invocations / 129 HTTP
responses, including 81 required comment writes. There were no per-part or
per-action detail reads. Repeated restore was read-only (9.095 seconds), and
re-export took 8.668 seconds; each used 3 API invocations / 12 HTTP responses
including repository verification and pagination. All original task issue
observations were unchanged, and no SQLite task DB was created. All 81 test-only
archive comments were deleted after validation. A final export matched the
original history exactly; the existing journal carrier and history were retained.
