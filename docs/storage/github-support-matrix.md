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

`cmd/undo.go` opens SQLite for both `last` and `undo`, including `undo --list`.
Neither has GitHub store dispatch. `internal/ghstore/activity.go` stores authored
comments, logs and handoffs, not before/after snapshots suitable for undo.
Workflow history provides selected transitions but cannot reconstruct arbitrary
issue, relationship, note or board changes. An activity feed is insufficient
to fulfill #9.

The implementation needs a versioned action journal with actor/session,
operation and target identity, before/after values, verified result and undo
receipt. Reads must use bulk snapshots rather than enumerate comments per
issue. Undo must compare the current target with the recorded after-state,
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
