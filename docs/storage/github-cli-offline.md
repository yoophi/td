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

Root dispatch skips implicit issue autosync and diagnostic session lookup for
config/feature management, workflow, debug stats, version, completion/help,
bootstrap init, explicit `--help` and offline query helpers. They still perform
their own intentional I/O: selecting a GitHub store validates gh/repo access,
SQLite init initializes its database, and version's update-check option retains
its behavior. Only unrelated implicit issue-context work is suppressed.
Analytics/error records remain device-local and omit issue session IDs for
these commands. Ordinary issue commands and executing queries retain their
normal sync/context behavior. Tests with a real configured SQLite project and
session verify skipped metadata hooks and retained data-command hooks/context.

`query --examples`, `query --fields` and `query EXPRESSION --explain` are also
offline across issue stores. With `--json`, examples return query/description
rows; fields return sorted issue/cross-entity fields, validation enum values,
special values and relative-date forms; explain returns `query`, normalized
`parsed` and `executed=false`. Examples share their definitions with human
output, and the field inventory comes from the parser's actual validation
registry. This includes sprint, note/dependency fields and date aliases instead
of maintaining a separate incomplete field list.

Choose only one helper. Fields/examples reject an unused expression; explain
requires one and reports parse/validation failures. Execution-only output,
limit, scan and sort options are rejected with helpers instead of being silently
ignored. Use global `--json` for helper JSON. These commands do not fetch issue
data or resolve actual session placeholders; explain is parsing, not query
execution. CLI tests cover both feature modes, successful/error JSON, absence
of gh calls/SQLite files and validation of every advertised field/example.
