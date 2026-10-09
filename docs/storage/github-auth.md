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
