# GitHub repository selection

`td sync-project` (alias `td sp`) is available in GitHub projects independently
of `sync_cli`. SQLite handlers still require that feature and retain td-sync
project IDs. GitHub repository IDs are explicit `owner/repository` names.

`td sp link owner/repo [--remote NAME]` and
`td sp join owner/repo [--remote NAME]` select an existing GitHub remote.
They validate gh, authentication, repository access and Issues availability,
and require the supplied repository name to match the actual Git URL. The remote
defaults to the project's existing GitHub remote selection. They do not change
Git URLs, create repositories, grant membership, migrate tasks or replay actions.
`join` requires an explicit repository in GitHub mode and means local selection,
not an access invitation. `--force` cannot bypass validation or reset a queue.

This uses the same local store setting as
`td config set store gh-issue --remote NAME`; the latter is the way to select
GitHub storage from a SQLite project. Selection preserves other local settings.
Neither command discovers or silently rewrites an origin to match its argument.

`td sp list` verifies the currently configured repository and reports it with
`scope=configured-repository`. This is a limited local selection diagnostic,
not an account-wide repository listing. JSON explicitly declares that scope.
The operation reads no issue/comment collections and opens no SQLite database.

GitHub storage cannot be unlinked while retaining a usable repository-less
GitHub store. `td sp unlink` therefore fails with instructions to switch stores
explicitly (`td config set store sqlite`) after exporting/importing any needed
data. No migration or remote deletion is implied or attempted.

Repository creation and collaborators/invitations/permission management remain
unfinished #29 requirements. `create`, `members`, `invite`, `kick` and `role`
currently fail explicitly in GitHub mode before SQLite/td-sync access. This is
implementation pending, not an assertion that GitHub cannot support them.
SQLite emails and owner/writer/reader roles must not be silently interpreted as
GitHub accounts or permissions. Native repository acceptance and task review
also remain pending; this selection stage does not complete #29.
