# GitHub store support audit

The machine-readable [matrix](github-support-matrix.json) assigns all current
151 Cobra command nodes and 975 effective flags to audit responsibility. The
inventory includes parent commands, generated help/completion, aliases and
hidden commands/flags in both sync feature modes. Alias and flag metadata are
retained in `cmd/testdata/cli-inventory-sync-{false,true}.json`; aliases use
their canonical command's responsibility.

This is an incomplete audit, not a completed support or acceptance report.

Export's `--all`, `--format`, `--output`, `--json` and `--render-markdown` carry
selected actual CLI and archive preservation evidence from #23/#88. #89 adds
default closed-task preservation, explicit work-dir from a different current
directory, refresh, and offline JSON/human help in both sync feature modes.
Native
full-task export and a same-repository read-only import preview are described
in the [export contract](github-export.md#task-archive-verification-2388).
The flag audit remains partial evidence; parent/import/migration gates are
still pending.

| State | Meaning |
| --- | --- |
| pending | No complete contract classification has been established. This does not mean the command is unimplemented. |
| partial | Named fixture tests prove selected behavior; complete option combinations and native acceptance remain pending. |
| unsupported | Source inspection proves the GitHub implementation is absent. This is required work, not an exception. |
| sqlite-only | An explicitly excluded SQLite internal administration operation. |

Flag classifications are separate from command classifications. A command with
passing fixtures does not automatically prove all its flags, combinations,
aliases and global options. These flags remain pending until their actual
contract is checked. `flag_evidence` ties each partially audited flag to named
tests; the checker rejects partial flags without evidence and stale flag/test
references. Test names in the matrix are evidence pointers, not a
claim that the tests passed in every environment. Consult stage test logs for
the result and run the tests against the current source.

`TestGitHubSupportMatrixCoversInventoryAndEvidence` rejects omitted/duplicate
commands, omitted/stale flags, missing responsibility/reasons and references to
nonexistent tests. `TestCLIInventoryMatchesCapturedSurface` separately compares
the inventory with the real Cobra tree in each feature mode. Changing the tree
requires both the inventory and the responsibility audit to be reviewed; no
automatic supported classification is generated from registration.

## Offline option audit: #31

`workflow` has separate evidence for human, JSON, Mermaid and DOT output,
help, explicit work-directory selection, and `--refresh` without issue access.
The same CLI checks run with sync features enabled and disabled, autosync and
analytics enabled, and a GitHub executable that fails on any call. They reject
conflicting diagram flags and positional arguments and assert that no SQLite
database or issue session is created. Review-policy documentation is checked
against the shared eligibility policy. These are selected contracts, not proof
of every global flag combination or native task workflow behavior.

The query `--fields`, `--examples` and `--explain` helpers have individual
evidence for valid JSON and error paths, both sync modes, no GitHub calls and
no SQLite database. Documented fields/examples are parsed and validated against
TDQ. Execution options such as output, scan limits and sorting remain separate
audits; their pending classification is unchanged by helper coverage.

Config get/list/set, feature get/list/set/unset, and directory
associate/associations/dissociate now carry separate JSON, work-dir and refresh
evidence. The subprocess tests run both sync modes and reject every accidental
GitHub invocation, including with autosync enabled. They assert no issue SQLite
database; config and feature checks also assert no issue session context.
Association tests inject only a temporary local settings backend and exercise
the `assoc` alias, empty results, stable ordering and failures. Feature tests
check project persistence, environment precedence, review policy conflicts and
unchanged store selection. Config tests reject GitHub writes to global SQLite
sync settings and separately validate SQLite config persistence and invalid
value rejection. Human output, help and untested remaining flags stay pending.

## Completion output contract: #87

`td completion bash|zsh|fish|powershell` emits the shell script by default.
With `--json`, it emits `{ "shell": "bash", "script": "..." }` (using the
requested shell); `--no-descriptions` applies to the script inside that envelope
as well. `--refresh` does not turn script generation into an issue-data request.
Generated help keeps the normal human/JSON help contracts. An unknown shell or
extra positional argument now fails explicitly rather than returning successful
help. Running `td completion` without arguments still shows help.

The CLI tests compare decoded JSON scripts with ordinary output for all four
shells, with/without descriptions, both stores and both sync modes. They cover
JSON help/errors and assert no GitHub call, issue SQLite database or issue
session. The inventory retains all 151 commands/975 flags; only the completion
parent becomes runnable so it can validate arguments before showing help.
The actual development binary was also checked for these outputs and invalid
arguments; Bash and Zsh script syntax passed. Fish/PowerShell runtime execution
is not claimed by output generation tests. This change does not replace the
already installed Homebrew release.

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
