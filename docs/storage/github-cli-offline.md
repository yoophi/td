# Store-independent CLI helpers

Directory associations are device-local settings shared across issue stores.
`config associate [DIR] TARGET`, `config associations` (alias `config assoc`)
and `config dissociate [DIR]` do not require gh or a repository API operation.
Their existing persistence location is unchanged (`~/.config/td/associations.json`).

With `--json`, associate returns `action`, normalized `directory` and `project`;
dissociate returns `action` and normalized `directory`. Listing returns an array
of `{directory, project}` rows sorted by directory; an empty list is `[]`.
Errors use the ordinary top-level JSON error envelope. Unexpected listing
arguments fail rather than being silently ignored. Human output is unchanged.

The #31 regression test exercises root CLI dispatch, the alias, --work-dir,
feature modes, successful/empty/error JSON, forced autosync and absence of gh
calls or SQLite files. It injects a temporary association persistence backend
instead of changing HOME or writing the user's real global settings. This
proves the command contract, not native persistence validation of the user's
actual configuration. The complete command/flag support matrix, other offline
helpers, native acceptance and task review remain pending #31 work.
