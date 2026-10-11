# Logical deletion in the GitHub store

`td delete <id>...` marks tasks as deleted in their td metadata. It never
physically deletes a GitHub issue and never changes its native open/closed
state. Workflow status, attribution, relationships and other fields remain.
`td restore <id>...` removes that marker; it does not reopen a natively closed
issue. A native GitHub close and td logical deletion are different operations.

Both commands accept a nonblank `--reason`, recorded with the actual session
in shared history. An explicitly blank reason fails before writes. `--force`
and `--yes` on delete are documented compatibility no-ops; they do not bypass
missing-ID, stale-state or permission errors. Repeat deletion/restoration is
a no-op and preserves the existing target clock. GitHub issue numbers, `#N`
and canonical `gh-N` task IDs are accepted; pull requests are not tasks.

`td deleted --json` returns the deleted tasks, including native closed tasks,
and an empty result is `[]`. Normal list (including `--all`), task list, TDQ
query, show and board views exclude deleted tasks. `--all` includes closed
and deferred tasks; it does not include logically deleted tasks. For ID
selection with `--all`, use `--id`; combining `--all` with a TDQ list filter
is rejected explicitly. Device-local history may retain a deleted task's
identity so its earlier work remains intelligible.

## Multiple tasks, failure and undo

Multiple IDs are planned before target writes, including existence, original
states and requested input. A later preflight failure prevents earlier target
changes. A failure during execution stops the command, returns an error and
reports the completed prefix. Earlier changes remain; this is not an atomic
transaction. Inspection/receipt recovery observes uncertain outcomes without
automatically replaying target writes. A no-op member cannot turn a partial
operation into a complete one.

The command journal groups multiple targets. Its supported inverse restores
original visibility and verified workflow/review fields while preserving
native identity and attribution. Foreign target/receipt/native-event changes
stop the inverse; conflict detection remains best-effort without atomic GitHub
compare-and-swap. See the [change command audit](github-change-command-audit.md).
Deletion is retained in task export/import; see the
[export](github-export.md) and [import](github-import.md) contracts for their
separate preservation and migration boundaries.

## Verification for #18

The current source passed deletion, reason, empty JSON, journal inverse,
multi-target planned command, SQLite regression and storage boundary race
tests. Store tests also cover external native close, stale observations,
permission failures, deleted relationship import and deleted task export.
Board positioning filters deleted candidates.

Real GitHub mutation evidence from #65 was audited against the original
native observations and inverse receipts for sample issues 179–181. Native
number/identity, title, body, state, labels, creation time and author were
restored. No-op/unattempted members retained their target clock. The
[delete receipt](https://github.com/yoophi/td-sample/issues/127#issuecomment-6100101927)
remains a partial operation after undo: its members are undone, unattempted
and no-op respectively. The
[restore receipt](https://github.com/yoophi/td-sample/issues/127#issuecomment-6100124836)
has its changed member undone and both no-op members preserved. Original
partial evidence is retained rather than promoted to a fully applied command.

Current-binary, GET-only native checks observed deleted sample `gh-181` in
`deleted`, then verified that ordinary list, task list, TDQ query, show and
the All Issues board hide it. No remote write or SQLite issue database was
created. These reads supplement the retained native mutation evidence; they
do not claim a fresh mutation replay. Full parent journal review (#9) and
the aggregate command/option audit (#31) remain separate gates.
