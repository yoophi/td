# GitHub API call cost investigation

On 2026-10-09 at 15:08 UTC, the same authenticated account could run
`gh issue list --repo yoophi/td --limit 1 --json number`, while
`gh api 'repos/yoophi/td/issues?state=open&per_page=1'` returned HTTP 403.
The CLI trace showed the former used `POST /graphql` with HTTP 200.
`gh api rate_limit` reported REST core remaining 0/5000 and GraphQL
remaining 4998/5000. These APIs have separate primary limits:
[GitHub documentation](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).

The repository had 44 issues, verified through GraphQL. A deterministic fake
runner exercising the real `Client.ChangeToken` implementation measured 45 API
invocations for each complete sweep, even with no changes or comments. Opening
the store adds a repository verification request. With single-page responses,
46 requests every 30 seconds is approximately 5,520 REST requests/hour, above
the account's entire allowance. Pagination and the former `gh auth status`
preflight can add traffic.
Only one td server was running at inspection. This establishes a sufficient
exhaustion path, not attribution of every one of the account's 5,000 requests.

Monitor refreshes also visit all retained task histories, including deleted
tasks. Each activity read fetches the issue and its comments; the activity cache
lasts only for the current sweep. Independent monitor/server instances and
browser requests add to the same REST allowance. Server rate-limit backoff already
respects observed reset deadlines; repeated monitor refreshes did not.

The bounded mitigation raises GitHub monitor and event polling to a five-minute
minimum and suppresses monitor dashboard reads until an observed rate-limit
reset. At 44 issues, server polling costs approximately 552 REST requests/hour
instead of 5,520, before other traffic. This is not an account-wide quota budget;
large repositories, pagination, multiple clients, and manual operations can
still exhaust the allowance. A complete historical-comment observation remains
necessary for detecting edits/deletions; changing that fidelity requires a
separate design rather than caching write eligibility or skipping history.

CLI errors retain the original GitHub diagnostic, HTTP status, request ID and
timestamp, followed by rate-limit/reset guidance. HTTP and SSE responses retain
the same message and expose `rate_limited` plus retry guidance. No write is
automatically retried. `TD_GH_DEBUG=1` emits numeric API invocation/HTTP response
counts without credentials or response bodies.

The installed CLI also exposed a second failure path: `gh auth status` made
`GET /`, received HTTP 403 with core remaining zero, and reported the token
as invalid, losing the real rate-limit cause. Repository preflight now uses
`gh auth token` only to verify local credential availability, discards its
output without logging it, and validates actual access through the repository
API. Its rate-limit diagnostic and typed exception propagate unchanged.

## Repository-wide observation implementation (2026-10-10)

`Client.ReadSnapshot` now collects all issue pages and repository issue-comment
pages, joining on `issue_url`. `ChangeToken` uses that observation without
per-issue requests. The 44-issue cost regression now expects two paginated gh
invocations per complete sweep, instead of 45. Actual HTTP requests are I+C
(issue pages plus repository comment pages), with repository preflight separate.
On `yoophi/td`, 52 tasks and 99 comments were observed with three HTTP responses
including preflight. More than 100 comments increase pagination cost.

The snapshot includes retained/auxiliary issue comments, filters PR comments,
rejects repeated identities/operations and malformed metadata, and reports an
orphan comment as an incomplete observation if the repository changed during
the read. Existing comment edits/deletions remain part of the fingerprint.
The snapshot is not atomic and is never final mutation authorization.

Consumer conversion (#47), lazy review display (#48), persistent cache (#52),
and incremental reconciliation (#50) remain separate implementation tasks.
The existing running server must use the new build to benefit from this change.

Aggregate readers now consume the same bulk observation: monitor/context for
serve and TUI, TUI handoffs, shared statistics, and JSON export. In native tests
on the 52-task repository, HTTP monitor, CLI context and JSON export each used
three HTTP responses including preflight. Basic info remained two responses
because it does not need comment history. Markdown export still uses issue
pages only. Review eligibility event checks remain tracked separately in #48;
these measurements do not claim zero extra review requests for in-review tasks.
Persistent reuse between independent dashboard/event requests is #52/#50.
## Unverified review queues

GitHub aggregate monitor and TUI board reads classify in-review records
conservatively from their listing metadata. They do not call native events,
handoff history, or individual issue GETs to grant reviewability. These rows
remain pending until a selected detail or mutation performs fresh validation.
The monitor task-list response exposes `review_verification_required`; the TUI
footer explains that reviews are unverified. Recorded approvals never populate
the aggregate ready-to-close bucket without validation. Detail and mutation
checks remain unchanged.

The actual `FetchGitHubData` consumer regression runs the real ghstore client
against a fake gh executable with 1 and 60 in-review issues. Both require four
gh invocations: local auth-token check, repository preflight, issue pages and
repository comment pages. The auth-token check is not an HTTP response. Any
unexpected individual API endpoint fails the fixture. Existing non-bulk test
adapters retain their older explicit review-observation contract; production
ghstore clients implement `SnapshotReader` and use the conservative path.

The HTTP router regression uses the actual configured GitHub read store and
`GET /v1/monitor`: 1 and 60 in-review records both cost the same four gh
invocations and return the unverified flag with no reviewable/ready-to-close
grant. Individual endpoints are rejected by the fake executable.

The TUI board-source regression also covers 1 and 60 in-review records: one
issue listing in both cases, all rows pending, and any individual review/history
read fails the fixture. The selected-detail and write-side interfaces are not
replaced by these conservative queue facts.

Native verification used disposable yoophi/td-sample#113. The aggregate monitor
returned HTTP 200, the unverified flag, and no approval grant in four HTTP
responses (repository preflight, two issue pages, one repository comment page).
Selected detail performed ten HTTP responses and offered approve/reject after
fresh verification. A direct GitHub close/reopen invalidated that review;
detail then offered only reject, and an actual approve request returned HTTP
409 with `native close/reopen history changed` before writing. A subsequent
detail still had in_review status and no approval. The fixture was logically
deleted and excluded from an include_closed listing; the test server stopped.
Artifacts: tracking checkout `artifacts/gh-api-n-plus-one-plan/gh-48/`.

Full related ghstore/serve/monitor race tests and macOS/Linux lint passed for
the implementation; the additional HTTP router test also passed with race
detection and both lint targets. This change does not fix the entire #55
20-second review timeout: write-side parent cascades and selected availability
still perform their fresh checks.
