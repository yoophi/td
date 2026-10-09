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

The #30 implementation remains incomplete: conflicts/tail and sync settings
still need their own GitHub contracts and implementation. SQLite snapshot bootstrap, action-log replay and
FK repair are store-specific operations; they cannot be reported as successful
GitHub transfers. Native repository checks and task review are still pending.
