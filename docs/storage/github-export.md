# GitHub issue export

`--format journal` additionally exports complete action journal history and
restored history archives. See [portable journal history](github-portable-journal.md)
for its separate format, ID mappings, restore command and undo boundaries.

`td export --format json|md --output PATH --all` uses
the configured issue store. GitHub mode verifies the remote and credentials,
uses gh directly and never opens SQLite. Without `--all`, all non-deleted
task issues are selected, including closed tasks. `--all` additionally includes
logically deleted tasks. Pull requests, note carriers and board carriers are excluded,
matching the task scope of the existing SQLite export command.

This default includes every workflow status, not just the default `td list`
scope. #89 corrected an earlier GitHub-only omission of closed tasks while
preserving SQLite's existing selection behavior. Deferred tasks are included
as well. A complete relationship restore may require `--all` when another
task refers to a logically deleted task.

JSON retains the existing array and `issue`, `logs`, `handoffs`, `dependencies`
and `files` fields. A versioned `github` field additionally preserves the source
repository, native number/URL/state, original body including td metadata,
author, native clocks, complete activity with authors/clocks, review/session/
transition history and schedule/delete data. No credentials, local focus,
device-local session registry, UI preferences or current work-session state
are exported. User-authored content is preserved without redaction.

Markdown is a readable task summary with the existing heading/field format;
it is not a complete archive and does not fetch activity. Rendering affects
only Markdown (`--format md --render-markdown`); using that flag with another
format is an error. Unknown formats and positional arguments are also rejected
before reading either store. JSON decodes through the shared export item type
without dropping the GitHub extension. GitHub import and cross-store replay
remain under #24 and #8; their provenance/remapping/write semantics still
need verification before claiming an end-to-end backup/restore contract.

All selected pages and activity must be read and validated before any output.
Errors return nonzero and never emit a partial JSON array or replace an
existing backup. File output uses a private temporary file in the destination
directory, sync, close and rename; stdout is emitted only after serialization.
Both stores use this output boundary. SQLite activity, handoff, dependency and
file read failures also abort the export instead of writing an incomplete backup.
With `--output PATH --json`, stdout contains a success receipt with `action`,
`format` and `path`; the file retains the selected archive format. Without a
destination, JSON and journal archives remain JSON documents. Markdown with
`--json` returns `format`, `document` and `rendered`; without `--json` it remains
plain Markdown. If writing the receipt fails after the file has been saved, the
error explicitly says that the export was saved; it does not roll back the file.
Observations are best-effort and are not an atomic repository snapshot. External
edits during export can produce observations from different times.

JSON reads one issue-list page set and one repository comment-page set;
it reuses the validated observations without per-issue or per-comment requests.
Markdown reads only the issue-list page set. Repository verification and
pagination add actual HTTP requests. Rate-limit errors preserve the original
GitHub diagnostic; there is no automatic retry.

Export reads complete collections rather than a reusable or incremental cache
baseline. `--refresh` remains accepted and produces the same full collection
boundary. `--work-dir` selects the configured project independently of the
caller's current directory. Help is offline, including `--help --json`; it
does not authenticate, read task data or create an issue session/database.

## Task archive verification (#23/#88)

On 2026-10-12, candidate source `b004896` was checked against the public
`yoophi/td-sample` repository through a GET-only gh wrapper. `--all` exported
196 tasks, including 109 natively closed and 119 logically deleted tasks.
The independently observed native issue set exactly matched the exported IDs;
titles, complete issue bodies, URLs, native authors/states and creation/update
clocks matched. All 125 task comments retained their identities, authors, URLs
and clocks; typed activity payloads and ordinary native comment bodies matched.
Four derived preserved-history entries remained passive historical data.

The export made one bulk issue collection and one bulk comment collection, with
no individual task/comment reads. The observations contained three issue pages
and eight comment pages; collection counts are not HTTP request counts.
The actual GitHub import CLI accepted the full file in read-only same-repository
preview and planned 196 skips. No remote write or SQLite database was used.

The same file was imported with the actual SQLite CLI into a disposable local
project, then exported again. All 196 portable issue records matched after ID
normalization, including 119 deletion dates and 20 scheduled tasks. Portable
logs (49), handoffs (52), dependencies (20) and files (4) retained their modeled
content, references and clocks. Historical sessions stayed namespaced. This
does not put the GitHub extension, native comments/reviews or executable journal
state into SQLite; those are outside its portable task fields.

Race-enabled fixtures additionally cover output failures, bulk pagination and
API errors, archived provenance, and import/reimport/SQLite round trips. #88
fixed JSON output and incomplete SQLite backup handling. These observations
do not close the #18/#9 parent gates or the broader #24/#8 import and migration
requirements. Complete action journal history still requires `--format journal`,
and notes/boards are outside task-format export.

The #89 follow-up checked default/all selection with the real CLI from another
project's current directory. Of the same 196 task records, the default export
retained all 77 non-deleted tasks, including 43 closed tasks; `--all` retained
all 196. Default and refreshed JSON and Markdown agreed. Each JSON invocation
used one full issue and one full comment collection; Markdown used one issue
collection and no comments. These native checks disabled reusable caching,
made GETs only and created no SQLite database. A separate ghstore regression
checks that export bypasses an actual cached/incremental baseline, including
physical deletions that cannot be discovered by incremental polling.
