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

The #30 implementation remains incomplete: the parent `sync` command and its status/push/pull flags,
init/enable/disable/conflicts/tail and autosync hooks still need their own GitHub
contracts and implementation. SQLite snapshot bootstrap, action-log replay and
FK repair are store-specific operations; they cannot be reported as successful
GitHub transfers. Native repository checks and task review are still pending.
