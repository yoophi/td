# GitHub store support audit

The machine-readable [matrix](github-support-matrix.json) assigns all current
151 Cobra command nodes and 973 effective flags to audit responsibility. The
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

## Confirmed implementation gap: #9

`cmd/gh_last.go` now dispatches GitHub `last` and `undo --list` to the bulk action
journal reader, with explicit scope and phase reporting. Actual `undo` executes
the verified scalar inverse including receipt-verified sequential undo; workflow/graph/
multi-target integration remains pending. The SQLite path remains
in `cmd/undo.go`. `internal/ghstore/action_journal*.go` provides before/after
snapshots and create/update protocols, now connected to default CLI create/update
with lazy bootstrap and real session attribution. CLI status transitions, logical
delete/restore, dependencies, scheduling, linked files, label repairs and work-session
auto-start/review writes now use the same journal protocol. Actual CLI tests cover
receipt failures and no-op start/preview behavior. HTTP/monitor writers,
comment deletion/note/board journaling and coordinated cascade undo remain work.

These writer hooks do not make workflow, relationship, file or deletion undo
supported: their policy and graph checks still require dedicated inverse planning.
Activity comments appended after a field receipt may also advance the native
issue revision; verified scalar and comment inverse receipts now provide exact
causal links for supported sequential undo. Workflow inverses remain pending. The subprocess fixture now
advances the target clock on activity creation;
`TestGitHubJournalCLIUndoRejectsUnjournaledCommentClock` verifies that a schedule
write followed by its activity comment fails undo with a conflict, without
changing the issue or marking the action undone. This validates the current
safety boundary, not support for reversing the complete schedule operation.
Version 2 comment-create records now preserve canonical activity input and the
native comment identity, author, raw body and server clocks, alongside the
issue before/after clock. They remain readable beside v1 issue records through
the same bulk collections. `last` and `undo --list` JSON retain both the comment
input/result and target issue identity. Actual subprocess tests exercise that
read contract in both feature modes. Tracked CLI clients now emit v2 receipts
for direct comments/logs/handoffs and automatic activity comments, including
work-session fan-out. Intent/POST/readback/uniqueness/receipt checks preserve
uncertain outcomes without automatic retries. A comment action cannot be
planned as an issue PATCH; guarded own comment creation undo is implemented; deletion journaling remains work.

Workflow history provides selected transitions but cannot reconstruct arbitrary
issue, relationship, note or board changes. An activity feed is insufficient
to fulfill #9.

The remaining implementation must connect all actual writers to the versioned
action journal and execute verified undo receipts. Reads use bulk snapshots
rather than enumerate comments per issue. Undo must compare the current target
with the recorded after-state,
preserve later changes, report uncertain/partial writes and reconcile an
existing receipt before any replay. GitHub PATCH remains best effort; it cannot
provide an atomic distributed compare-and-swap. Physical deletion and creation
need explicit compensation/limitations. Both shared history and local execution
context need distinct provenance. This remains implementable work, not an
exception caused by current authentication or socket restrictions.

GitHub authentication currently prevents a fresh remote task-state sweep,
remote tracking writes and native repository acceptance. Existing loopback bind
restrictions prevented full-suite server/browser checks. Those are validation
blockers; they do not convert incomplete features into SQLite-only exceptions.
