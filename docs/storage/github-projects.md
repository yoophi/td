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

`td sp list --all` lists repositories visible to the active credential through
owner/collaborator/organization affiliation. It uses the paginated `user/repos`
collection (`per_page=100`, sorted by full name), with no per-repository detail
or issue requests. JSON declares `scope=credential-accessible-repositories`,
the currently configured repository, and an allowlist of repository ID/name,
visibility, Issues/archived/disabled state and returned base permissions. Raw
response credentials and unrelated fields are never copied to output. An empty
list is `projects: []`; malformed pages fail without partial success. gh errors
retain diagnostics and are not automatically retried. Token scope can limit
visibility, so this is not a list of all public repositories or proof of issue
write access. No selection/configuration change follows listing; normal remote
and repository checks still apply when explicitly selecting a result.
`--all` is rejected by SQLite handlers. See
[authenticated repository listing](https://docs.github.com/en/rest/repos/repos#list-repositories-for-the-authenticated-user).

GitHub storage cannot be unlinked while retaining a usable repository-less
GitHub store. `td sp unlink` therefore fails with instructions to switch stores
explicitly (`td config set store sqlite`) after exporting/importing any needed
data. No migration or remote deletion is implied or attempted.

`td sp members` lists visible collaborators through one paginated collection
(`per_page=100`), with no per-account lookups or issue/comment fetches. JSON
preserves GitHub account IDs, login names, `role_name` and `permissions`, and
declares `scope=visible-collaborators`. Empty results are `members: []`.
The response can include inherited organization/team access; it cannot identify
the source of a grant and does not include pending invitations. Collaborator
listing requires access beyond ordinary issue access; denial remains an explicit
failure with the original gh diagnostics, not a successful empty list.
See [GitHub collaborator API](https://docs.github.com/en/rest/collaborators/collaborators#list-repository-collaborators)
for the effective role semantics and required permissions. Native verification
is still pending; this list alone does not establish administrator privileges.

`td sp kick LOGIN` removes the repository collaborator grant using a single
DELETE request after the usual repository/origin/authentication validation.
The argument is a literal GitHub login, not an email or a td-sync user ID;
numeric input is treated as a numeric login, never looked up as an account ID.
Malformed handles are rejected before any gh call. Success reports
`action=remove-repository-collaborator` and `effective_access_verified=false`.
Inherited organization/team access may remain and related changes can be
asynchronous. GitHub can also unassign issues and delete affected forks;
see [removal API semantics](https://docs.github.com/en/rest/collaborators/collaborators#remove-a-repository-collaborator).
This command does not claim to remove organization membership, teams or all
effective access. Failures preserve gh diagnostics and instruct inspection of
repository/access state before retrying; no automatic retry or SQLite fallback
occurs. An unexpected response after a write is also an explicit uncertain
failure, not a successful revocation report.

`td sp invite LOGIN PERMISSION` sends one GitHub collaborator permission
request. Both arguments are required; email and td-sync default roles are not
converted. Standard inputs are `pull`, `triage`, `triage_plus`, `push`, `maintain`
and `admin`; `--custom-role` explicitly forwards an exact custom role name.
GitHub validates its availability. The command checks repository ownership
before writing: explicit permission selection is documented as organization-only;
personal repositories accept only the API default `push` request here. Other
requests fail before PUT. These flags are rejected by SQLite handlers.

JSON distinguishes `invitation-created` (with invitation ID, still pending)
from `assignment-accepted` (no new invitation receipt). Requested permission is
reported separately; effective inherited access is not verified. The API can
update an existing collaborator and can generate an invitation notification.
See [permission request semantics](https://docs.github.com/en/rest/collaborators/collaborators#add-a-repository-collaborator).

`td sp role LOGIN PERMISSION [--custom-role]` checks existing collaborator
access before the same PUT. A failed check makes no permission write. This is
best-effort: a concurrent removal can still cause an invitation. If that receipt
is returned, td reports an after-write conflict with the invitation ID instead
of success. Inspect/cancel it explicitly; td does not retry or roll back with a
second uncertain write. Malformed receipts after PUT are uncertain failures.
These commands do not change organization membership or all inherited grants.

`td sp create OWNER/REPO --visibility public|private|internal [--description TEXT]`
creates an explicit github.com target with `gh repo create`, then verifies its
name, visibility and Issues setting. Both owner and visibility are required;
creation does not infer an owner or default public/private visibility. The
current GitHub store must first pass normal origin/auth/repository validation.
The subprocess pins GH_HOST=github.com and disables prompts. No clone, push,
Git remote mutation, store relink or task migration is performed. JSON declares
`linked=false`, `remote_changed=false`, `data_migrated=false` and the verified
repository details. A created repo with Issues disabled is reported as such
with instructions to enable Issues before selecting it as an issue store.
See [gh repository creation](https://cli.github.com/manual/gh_repo_create).

Creation is attempted once. Failure or failed/mismatched verification after
the creation command never triggers another create or a delete rollback;
inspect the explicit remote target before retrying. Raw gh failures are retained.
This differs from SQLite create's automatic td-sync linking: a new GitHub repo
does not match the current origin automatically. Configure a deliberate remote,
then use `config set store gh-issue --remote NAME` or `sp link OWNER/REPO --remote NAME`.
The visibility option is rejected by SQLite handlers.

Native repository acceptance and task review remain pending #29 requirements.
#29 is not complete.
