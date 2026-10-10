# GitHub authentication

In a GitHub-store project `td auth` uses the installed gh CLI, independently
of `sync_cli`. SQLite authentication still requires that feature and uses
the existing td-sync server credentials. The two credential stores are separate.

`td auth status [--json]` validates the active github.com credential with
`gh api user`. Its report contains the account login and host, never a token.
Authentication failures retain gh diagnostics and fail without a success report;
rate-limit failures are not interpreted as logged-out status. This check does
not need an accessible repository origin and does not open SQLite.

`td auth login` runs `gh auth login --hostname github.com --web` in an interactive
terminal. Select the account in gh's browser flow. JSON or noninteractive login
fails explicitly instead of reading tokens into td. For automation configure
`GH_TOKEN` outside td; for a different active stored account use
`gh auth switch --hostname github.com --user LOGIN`. Only github.com is currently
supported by the GitHub store. gh manages storage and its host-wide git protocol
prompts; td does not copy credentials to project settings, issues or logs.

`td auth logout --account LOGIN --confirm-global` removes that account's local
github.com credential through gh. This affects every project using the account,
does not revoke its tokens, and does not clear td-sync credentials. Environment
credentials such as GH_TOKEN are controlled by the environment and are not
removed by logout. gh failures remain failures; no fallback credentials are used.

GitHub auth does not change the configured repository or Git remotes. Account
authentication alone does not prove access to the configured repository;
normal store operations still validate origin, permissions and Issues support.

## Verification (2026-10-11)

The installed td `d1b5bbf` successfully reported the active github.com account
through read-only `auth status --json`. Its authentication code matches the
reviewed source. The command tests cover registration with `sync_cli` both off
and on, the original SQLite routing, logout scope and preserved 401 diagnostics.
The shared API runner's cancellation test also passes in the ghstore race suite.

An isolated executable-gh fixture verified 13 additional CLI cases: status with
both feature settings, empty account response, 401 and rate-limit failures,
noninteractive/JSON login guards, logout confirmation guards, PTY login, explicit
account logout, logout failure and missing gh. It checked the exact github.com
host/account arguments, one call per attempted operation, no calls after guard
failure, uncontaminated JSON, unchanged project configuration and no SQLite DB.
An extra token field in the synthetic account response was neither reported nor
persisted. The fixture has no Git origin, confirming that account status is
host-scoped. Actual user credentials were not changed; login/logout success was
verified through the fixture, not a real credential replacement.

Evidence is recorded under the tracking checkout's
`artifacts/task-audit-2026-10-10/gh-28-native-auth-status.json` and
`gh-28-auth-contract-results.json`.
