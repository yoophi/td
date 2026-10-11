# GitHub issue export

`--format journal` additionally exports complete action journal history and
restored history archives. See [portable journal history](github-portable-journal.md)
for its separate format, ID mappings, restore command and undo boundaries.

`td export --format json|md --output PATH --all --render-markdown` uses
the configured issue store. GitHub mode verifies the remote and credentials,
uses gh directly and never opens SQLite. Without `--all`, only open,
non-deleted task issues are selected. `--all` includes closed and logically
deleted tasks. Pull requests, note carriers and board carriers are excluded,
matching the task scope of the existing SQLite export command.

JSON retains the existing array and `issue`, `logs`, `handoffs`, `dependencies`
and `files` fields. A versioned `github` field additionally preserves the source
repository, native number/URL/state, original body including td metadata,
author, native clocks, complete activity with authors/clocks, review/session/
transition history and schedule/delete data. No credentials, local focus,
device-local session registry, UI preferences or current work-session state
are exported. User-authored content is preserved without redaction.

Markdown is a readable task summary with the existing heading/field format;
it is not a complete archive and does not fetch activity. Rendering affects
only Markdown. JSON decodes through the shared export item type without
dropping the GitHub extension. GitHub import and cross-store replay are being
implemented under #24 and #8; their provenance/remapping/write semantics still
need verification before claiming an end-to-end backup/restore contract.

All selected pages and activity must be read and validated before any output.
Errors return nonzero and never emit a partial JSON array or replace an
existing backup. File output uses a private temporary file in the destination
directory, sync, close and rename; stdout is emitted only after serialization.
Observations are best-effort and are not an atomic repository snapshot. External
edits during export can produce observations from different times.

JSON reads one issue-list page set and one repository comment-page set;
it reuses the validated observations without per-issue or per-comment requests.
Markdown reads only the issue-list page set. Repository verification and
pagination add actual HTTP requests. Rate-limit errors preserve the original
GitHub diagnostic; there is no automatic retry.
