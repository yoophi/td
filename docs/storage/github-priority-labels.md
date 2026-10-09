# GitHub priority labels

Priority remains authoritative in td issue metadata. Canonical GitHub labels
`td:priority:P0` through `td:priority:P4` mirror it for GitHub UI/filtering.
Only those five exact names (case-insensitive) are reserved. Other labels,
including `td:priority:custom`, are preserved. No metadata schema changes.

Creation and managed-issue updates include exactly one priority mirror in
the same issue write. Clearing user labels retains the state and priority
mirrors. A priority change updates metadata and the label together. A native
issue without td metadata is not interpreted through its priority labels;
a native-state-only edit preserves those labels. Metadata-changing actions
can adopt native issues into td, at which point the mirrors apply.

Legacy issues without priority labels and externally edited/multiple priority
labels still read priority from metadata. Record JSON exposes
`priority_label_warning`; human `td show` prints the mismatch warning.
An ordinary managed-issue edit repairs mirrors, or preview an explicit repair:

```sh
td config sync-priority-labels --json
td config sync-priority-labels --id gh-126 --json
td config sync-priority-labels --id gh-126 --apply --json
```

The default is read-only: no repository label creation or issue writes.
`--id` accepts repeated/comma-separated local issue IDs or numbers; every
selected target is validated before writing. Unknown or unmanaged targets
fail without repairs. Without selection, managed closed and logically deleted
issues are included, matching existing state-label synchronization.

Apply regenerates a fresh plan; it does not reuse an earlier CLI preview.
The complete labels payload is shown, including the normal state-mirror
repair if needed. Missing canonical repository labels are created and read
back before issue writes. Creation is never automatically retried, and a
missing/forbidden label stops the issue write with the underlying cause.
Each issue uses normal fresh revision checks, write readback and cache
invalidation. The operation is not transactional: an error reports how many
earlier repairs completed; an uncertain failed write may also have applied.
Inspect current state before any retry.

No server-side priority label filter is enabled: full paginated metadata
reads still include old/unmigrated issues. This presentation mirror does not
claim an N+1 reduction; the existing bulk snapshot supplies metadata already.
Priority values in exports remain metadata values. Import application is
still outstanding in task #24; this change does not establish import support.

## Verification and remaining work

Full ghstore race tests pass. Tests cover legacy/multiple/external labels,
ordinary edits, unmanaged boundaries, user-label preservation, dry-run,
selection validation, revision conflicts, permission causes, earlier applied
repairs and idempotence. The new CLI test verifies read-only previews do not
even create the fake remote-state file, and a no-op repeat leaves it unchanged.
Delete/restore, transition, config/cache command tests and Mac/Linux lint pass.

Before permissions changed, real sample gh-126 was previewed (0 writes),
applied (1 repair, bug and td:open preserved), and repeated (0 repairs).
It remains to be cleaned up and the new automatic create/update behavior
needs fresh native verification. A broader command suite is currently blocked
by tests trying to write user-global gh-context state outside allowed roots;
it has not passed. No global HOME/config override was applied.

Current implementation is in the permitted temporary checkout
/tmp/td-gh-49-priority-work, based on9595808. The original feature worktree
and its unrelated unfinished #24 files are preserved. Under current sandbox
permissions, push, external worktree integration and managed global installation
remain unavailable. #49 stays in_progress; it is not an impossible-task exception.
