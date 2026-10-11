# GitHub store support audit

The machine-readable [matrix](github-support-matrix.json) assigns all current
151 Cobra command nodes and 975 effective flags to audit responsibility. The
inventory includes parent commands, generated help/completion, aliases and
hidden commands/flags in both sync feature modes. Alias and flag metadata are
retained in `cmd/testdata/cli-inventory-sync-{false,true}.json`; aliases use
their canonical command's responsibility.

This is an incomplete audit, not a completed support or acceptance report.

| State | Meaning |
| --- | --- |
| pending | No complete contract classification has been established. This does not mean the command is unimplemented. |
| partial | Named fixture tests prove selected behavior; complete option combinations and native acceptance remain pending. |
| unsupported | Source inspection proves the GitHub implementation is absent. This is required work, not an exception. |
| sqlite-only | An explicitly excluded SQLite internal administration operation. |

Flag classifications are separate from command classifications. A command with
passing fixtures does not automatically prove all its flags, combinations,
aliases and global options. These flags remain pending until their actual
contract is checked. Test names in the matrix are evidence pointers, not a
claim that the tests passed in every environment. Consult stage test logs for
the result and run the tests against the current source.

`TestGitHubSupportMatrixCoversInventoryAndEvidence` rejects omitted/duplicate
commands, omitted/stale flags, missing responsibility/reasons and references to
nonexistent tests. `TestCLIInventoryMatchesCapturedSurface` separately compares
the inventory with the real Cobra tree in each feature mode. Changing the tree
requires both the inventory and the responsibility audit to be reviewed; no
automatic supported classification is generated from registration.

## Action journal audit: #9 and #65

`last` and `undo --list` read current-session journals from bulk issue/comment
observations. Compound command effects collapse into one history entry. The
[current writer matrix](github-batch-writers.md) records the concrete CLI,
monitor and HTTP integration boundaries, including workflow cascades, claim
sweeps, work-session fan-out and creation with relationships.
The [#79 writer audit](github-writer-audit.md) maps those adapters to production
entry points and regression evidence. Native import acceptance is recorded in
the [import journal contract](github-import-command-journal.md#native-acceptance-78).

Supported inverse executors retain original snapshots, attribution, review
checks, graph observations and native-event evidence. Related creation records
its requested input before POST, binds the real allocated ID to reverse edges,
and compensates by restoring actual external effects before logically deleting
the created task. GitHub identity and history survive. The CLI and monitor use
the shared creation command; HTTP creation has no dependency/block-list input
and retains its standalone POST contract.

Receipt recovery observes uncertain outcomes without replaying target writes.
Observed evidence is not promoted to a verified write. Changed tasks, activities,
original receipts or watched graph membership stop the inverse. GitHub PATCH
still lacks atomic compare-and-swap; these are best-effort conflict checks, not
a distributed transaction. Legacy standalone creation and physically deleted
native comments/issues keep their explicit compensation limits. Remote undo
does not restore device-local focus or work-session records.

The machine-readable matrix remains a partial command/flag audit. New creation
fixtures prove command grouping, whole-command compensation, failure recovery
and foreign-change rejection; they do not replace native acceptance or the full
release gates. Builtin-board first settings (#83) now share the creation POST
and journal, with the existing explicit creation-undo limit. Portable history
(#66) now has a [separate archival contract](github-portable-journal.md), with
native round-trip and destination-undo isolation evidence. Full command/flag
and adapter coverage (#31/#68), and the full #9 scope must be verified separately.
Earlier authentication or loopback failures
are historical observations, not current implementation exclusions.
