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
Root `td`, `td --help`, `td --version` and root dispatch failures may bypass
command execution hooks entirely. Diagnostics also omit session discovery when
no command executed. Subprocess tests in a real configured SQLite project with
an existing session cover these paths, including an unknown-flag JSON error;
they do not rely on an empty database to hide a session lookup.

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

`workflow` describes the status graph without resolving project policy or
fetching issues. Its state-machine guard modes (`liberal/advisory/strict`) are
separate from the command review policy (`trusted/delegated/strict/balanced`).
Human output, JSON (including diagram envelopes) and Mermaid source describe
that separation, the default trusted acknowledgement paths and gh-issue's
best-effort conflict boundary. JSON explicitly reports
`review_policy.project_policy_resolved=false`; the default is not a claim about
the current project's override. Existing transition/guard JSON fields remain.
Extra positional arguments and simultaneous `--mermaid --dot` now fail rather
than silently ignoring input. Tests compare documented implementer-approval
boundaries with the real policy evaluator and run the CLI in both feature modes
with analytics enabled, proving no gh, SQLite database or issue context creation.

## CLI surface inventory

`cmd/testdata/cli-inventory-sync-false.json` and the `sync-true` counterpart
capture the actual Cobra tree in isolated subprocesses, including built-in
help/completion, hidden commands, aliases, runnable/group nodes and every local
or inherited flag (type, shorthand, hidden/deprecated and persistent markers).
An inherited flag shadowed by a local flag records the effective local type.
The current inventory is 151 command nodes, 973 flag entries and 23 aliases in
each mode; registration is currently identical with sync_cli disabled/enabled.

The regression compares the tree with those committed inventories. An explicit
`TD_UPDATE_CLI_INVENTORY=1 go test ./cmd -run TestCLIInventoryMatchesCapturedSurface`
regenerates them after reviewing a CLI change. Defaults are excluded because
they can depend on the environment. This inventory detects surface changes;
it does **not** establish that a command or flag supports gh-issue. Individual
store classifications and native acceptance evidence remain required by #31.

Device-local `errors`, `security` and `stats analytics/errors/security` also
skip implicit issue sync/session discovery. Their explicit reading/clearing of
local diagnostic files is retained. Ordinary `stats` still reads issue data.

`feature get --json` returns `{name,value,source}` with a boolean or mode-string
value and the effective `env/config/default` source. The review policy source
honors modern environment/config settings and the legacy boolean override,
using the same precedence as the policy resolver. A conflicting or invalid
review policy fails in `feature list` as well as `get`; it is never silently
omitted from the list.

`feature set --json` returns `{action,name,value,scope:"project"}` after saving
the stored override. The value is not a claim about effective environment
overrides; use `get` to read the effective value. `unset --json` reports
`{action,name,scope:"project"}` after removing the override. Existing human
messages remain. `list` rejects extra arguments. The CLI lifecycle contract
covers boolean and string features, environment precedence, legacy conflicts,
invalid inputs, preserved store selection and no gh/SQLite/issue-context access.
