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
