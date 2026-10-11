# GitHub sync

`td sync status` is available independently of the sync CLI feature flag.
For `store=gh-issue` it validates the configured Git origin, gh availability,
GitHub authentication and repository Issues support, then reports the local
snapshot cache's freshness. It does not fetch issue/comment collections, open
SQLite, consult td-sync credentials or contact a td-sync server. Missing gh,
origin mismatch, authentication, permissions and network errors are explicit
failures rather than successful diagnostics with guessed state.

JSON contains `store`, `repository`, `mode=direct-gh`, `local_queue=false`,
`td_sync_server_used=false`, `cache` and explanatory `notes`. Cache observations
are not evidence of a completed push/pull or an atomic repository snapshot.
Use `td --refresh <read command>` for a fresh GitHub observation.

In GitHub mode, `td sync` and `td sync --pull` force a fresh complete task/comment
observation through the two bulk collections, including closed/deleted tasks.
They refresh reusable local snapshots but make no remote writes or SQLite replay.
JSON reports observation times/mode, task/native-activity counts and
`remote_writes=0`; collection failure exits without a success payload.
`td sync --status` uses the same diagnostic as the status subcommand. These
GitHub paths are available on both sync feature-mode parents.

`--push` exits with an explicit error: GitHub task commands already write
directly and there is no pending queue to upload. It never retries uncertain
writes. Combining status/push/pull modes is rejected before any gh call.
With the SQLite sync CLI feature off, the minimal parent rejects its new transfer
flags; its always-available `status` diagnostic remains available.

`td sync init` validates the existing GitHub store selection and credentials
without entering the td-sync server/project wizard or changing configuration.
Its JSON action is `validate-existing-config`; repository selection remains
the responsibility of `td config set store gh-issue`.

`sync enable/disable` report an explicit store-specific limitation in GitHub
mode, without changing the global SQLite autosync switch. They do not control
direct gh writes or the monitor's refresh interval. The shared td-sync runtime
closes its gate for GitHub before reading SQLite or td-sync credentials, even
if autosync is explicitly enabled or an embedding caller supplies a stale DB.

`td sync tail [-n N] [-f] [--interval 1m]` displays observed native comments,
td logs/handoffs and metadata transitions, including closed/deleted tasks.
GitHub tail is reachable on both sync parents; SQLite tail still requires its
sync CLI feature. Reads use the shared bulk snapshot/cache path, so follow polls
default to 1 minute (minimum 30 seconds) and use incremental reconciliation.
Follow remembers initial identities even with `-n 0`, emits new/changed records
once, and stops on cancellation or API errors without automatic retry.
`--json -f` emits one JSON object per initial/changed batch. These observations
are not a lossless event stream: physical deletion, edits between observations,
and metadata history limits can lose changes. Imported history remains passive
provenance rather than a newly active log/handoff. There are no push/pull records
or server sequence numbers to fabricate.

`td sync conflicts [--limit N] [--since 24h]` reads device-local CLI diagnostics
without requiring gh/authentication, opening SQLite or contacting any server.
New top-level typed `ConflictError` failures record repository, entity ID and
whether the conflict was detected after a write. Wrapped original error text
is retained. JSON declares `coverage=device-local-cli`; records are scoped to
the configured repository and returned newest first. Plain legacy error strings
and other repositories are excluded. An empty list does not prove that remote
state has no conflicts. Other devices, HTTP/TUI failures and errors already
handled inside a command are outside this recorded CLI coverage. After-write
conflicts may have applied and require fresh state inspection before retrying.

`config sync.*` values belong to global SQLite/td-sync configuration. In a
GitHub project, `config set sync.*` exits explicitly before reading or changing
that global configuration; change global SQLite settings from a SQLite project.
`config get sync.*` retains its global read/feature-flag semantics.
`config list` declares `sync_scope=global-sqlite` and
`sync_applies_to_store=false` in GitHub mode, even if global values are included
with the sync feature enabled. They do not affect direct gh writes or GitHub
monitor/tail intervals. No setting starts SQLite or td-sync for a GitHub store.

## Acceptance and explicit exceptions (#30)

The setup validator is reachable on both sync parents; the SQLite setup wizard
still requires the sync CLI feature. GitHub has no local pending-event queue,
SQLite snapshot bootstrap, action-log replay or SQL FK/orphan repair to transfer.
These store-specific operations are explicitly excluded. Use direct task writes,
`sync`/`sync --pull` for fresh observation, `sync status` for cache diagnostics,
and project `config set store gh-issue` for setup. Inspect remote state before
retrying an uncertain write. GitHub autosync toggles are rejected; monitor/tail
polling uses their own interval options instead.

Native acceptance used source `2256bc0` in a disposable Git checkout selecting
`yoophi/td-sample`, with an enforced GET-only gh wrapper. Eighteen checks cover
both feature modes for `status`, `--status`, `init`, `conflicts` and rejected
`--push`; complete refresh, `--pull`, tail, rejected enable/disable, conflicting
transfer flags, a below-minimum interval and rejected global sync setting writes.
The environment deliberately enabled autosync and supplied an unreachable
localhost td-sync URL. All expected results were verified, with zero remote
writes, no SQLite database, and unchanged project settings and Git remotes.
Fresh sync took 6.562 seconds and pull took 7.335 seconds; these are observations
of this sample and do not promise fixed latency or API-cost limits. Each refresh
used one paginated issue collection and one paginated comment collection; no
per-task detail requests were observed. A gh invocation is not an HTTP page count.

Race-enabled command tests additionally cover empty snapshots, collection errors
without partial success, missing gh, root feature registration/error envelopes,
follow cancellation and deduplicated edits, offline repository-scoped conflict
records, and blocked global settings. Shared runtime tests check the GitHub gate
even when autosync is enabled and an unusable SQLite handle is supplied. SQLite
sync and global configuration regressions remain part of verification.

Evidence is retained in `artifacts/task-audit-2026-10-10/gh-30-native-summary.json`,
`gh-30-request-counts.json` and the `gh-30-*-race.log` files. Command tests passed
in 48.144 seconds; targeted gates passed in 11.214 seconds. The complete shared
SQLite sync/config/runtime suites passed in 1.556/2.659/19.857 seconds, and the
selected monitor/runtime regressions passed in 80.029/1.715 seconds. Earlier
filtered runs reporting no tests for a package are not counted as its coverage.
Live validation does not fabricate a server push queue, lossless event stream
or an atomic GitHub snapshot.
