# GitHub API call cost investigation

## Work-session reads (#69)

`ws current` and `ws show` now read selected task details and activity from one
validated repository snapshot. Their former path performed one detail GET and
another issue GET plus a comments listing for every tagged task, with more
history reads for formerly tagged tasks. Two tagged tasks reproduced six
individual data requests. The regression now requires one paginated issue
collection and one paginated repository comment collection for both 2 and 145
tagged tasks; it rejects individual issue/comment endpoints.

The snapshot covers the union of current tags and retained history membership,
including logically deleted tasks, and merges matching work-session activities
with local-only entries in chronological order. Missing selected tasks and
failed collection reads return an error without a partial successful view.
An entirely local work session does not fetch issue or comment collections. Existing cache
and `--refresh` controls apply to display reads; automatic handoff composition
requests fresh history. Later write eligibility and conflict checks retain
their own fresh observations.

Two collection invocations cost I+C HTTP requests when uncached, where I and C
are the repository's issue and comment page counts; repository preflight is
additional. This removes growth per tagged task, not pagination or all other
requests. A cold full repository scan can cost more than targeted reads for a
small selection in a large repository. Shared snapshot reuse avoids repeating
that scan while its cache is valid. Mutation verification is not skipped to
reduce this read budget.

Native acceptance on `yoophi/td-sample` compared the same two-target active and
ended work sessions. Public JSON was identical before and after. Both uncached
versions observed seven HTTP responses including repository preflight: the new
collection calls paginated, so fewer gh invocations did not mean fewer cold HTTP
requests for this small selection. An immediately repeated cached `ws current`
used no issue/comment calls and one HTTP response for repository preflight.
All six native commands were guarded against remote writes and created no
SQLite database.

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

The initial bounded mitigation raised GitHub monitor and event polling to a five-minute
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

## Monitor interval and quota budget (#56, 2026-10-10)

`td monitor` now defaults to **1 minute**, for both SQLite and gh-issue.
`td monitor --interval 30s` is supported; CLI intervals below 30 seconds fail
explicitly before opening a store or launching the TUI. Longer intervals remain
unchanged. Embedded GitHub callers are bounded to 30 seconds, so existing hosts
cannot accidentally restore two-second remote polling. This replaces the earlier
five-minute TUI bound; `td serve` event polling remains separate and unchanged.

After #46/#47/#48, a basic dashboard sweep uses **1 + I + C** HTTP responses:
one repository preflight, I issue pages and C repository comment pages. It no
longer adds individual review checks for every in-review task. A native CLI
context read against `yoophi/td` after creating #56 observed four HTTP responses:
one preflight, one issue page and two comment pages. Diagnostic evidence is
`artifacts/gh-api-n-plus-one-plan/gh-56/context-api.log` in the tracking checkout.
The same aggregate reader powers dashboard/context data; the monitor consumer
and HTTP router regressions independently forbid individual review endpoints.

| Periodic aggregate polling | Sweeps/hour | REST responses/hour | Share of 5,000 |
| --- | ---: | ---: | ---: |
| 1 minute (default), 4 responses/sweep | 60 | 240 | 4.8% |
| 30 seconds, 4 responses/sweep | 120 | 480 | 9.6% |

These are steady-state estimates, excluding the initial fetch and user-triggered
reads. GitHub's ordinary authenticated REST allowance is 5,000 requests/hour,
shared with other clients using the account. At this measured repository size,
one basic monitor comfortably fits that allowance. This is not a guarantee for
every screen, repository or account: selected details, active boards, mutations,
other running monitors/servers, and other tools add traffic. Auxiliary board/TDQ
history queries may still perform per-issue reads; shared caching and polling
coordination remain #52/#50. Pagination itself grows with all retained issues
and comments, including logically deleted records. At 1 minute, a sweep costing
84 responses already exceeds 5,000/hour even without other traffic. Secondary
limits can also apply below the hourly allowance.

Main dashboard refreshes still skip overlapping sweeps and preserve the original
error/reset guidance. After a rate-limit response, that data source performs no
new sweep until its observed reset/retry deadline; a shorter polling interval
does not bypass this wait. The #50 shared gate now also protects detail/board
API reads, writes and new preflights within the same repository/credential
process context. Separate processes remain independent. Writes are never
automatically retried. Regression tests cover
overlap suppression, diagnostic preservation, reset waiting and bounded bulk
review costs alongside the new interval boundaries.

Source: [GitHub REST rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api).

## Request-scoped cost accounting (#51)

Under `TD_GH_DEBUG=1`, HTTP routes emit `gh-http-cost` with a numeric scope ID,
registered route template, status and duration. `gh-api-scope` joins each gh API
invocation to that scope and a fixed cost category. `gh-api-cost` summarizes API
invocations, local authentication invocations, their total gh invocations, observed
HTTP responses, cache hits/misses and category counts. CLI commands and TUI/main
SSE polls also get scopes. Existing process-global numeric counters remain.

Categories separate repository preflight, issue pages, repository comment pages,
individual issue/comments, label setup, named review validation, writes and named
write readbacks. The readback/review tags affect diagnostics only, not freshness
or policy. PATCH/POST responses already checked by a caller cost no extra HTTP
response; explicit post-write GETs are reported separately. Basic display detail
reads are not mislabeled as bulk collection. Tokens, auth headers, bodies, user
query strings and concrete request IDs/issue paths are absent from new cost logs.

`api_invocations` counts attempted gh API commands, including failed executions;
`auth_invocations` counts local gh authentication checks. An API command may
produce multiple HTTP response headers under `--paginate --slurp`. A failed call
without observable headers counts an invocation but zero observed responses;
these metrics cannot reconstruct network traffic that gh never exposed.
Concurrent HTTP requests have independent counters. A shared cache miss charges
network work to its initiating scope; waiters do not claim those calls. SSE
collection is accounted independently from an open HTTP event-stream request.

Cold-read regressions use the actual ghstore client/runner and gh header/slurp
framing for every aggregate consumer: ChangeToken, statistics, JSON/Markdown
export, list, actual TUI data source and HTTP monitor/stats/list/labels. Fixtures
reject individual issue/comment/event endpoints. The matrix covers issues
0/1/45/101 and comments 0/101 (nonzero comments require a parent issue).

| Retained issues | Comments | Full history: preflight + I + C | Issue-only: preflight + I |
| ---: | ---: | ---: | ---: |
| 0 | 0 | 2 (1+1+0) | 2 |
| 1 or 45 | 0 | 3 (1+1+1) | 2 |
| 1 or 45 | 101 | 4 (1+1+2) | 2 |
| 101 | 0 | 4 (1+2+1) | 3 |
| 101 | 101 | 5 (1+2+2) | 3 |

Empty collections still have one HTTP page; full-history collection skips
comments when there are no task/carrier issues. A 101-issue/101-comment full read
has three API invocations, five HTTP responses and one separate auth invocation.
The TUI test also runs a second Fetch and records its repeated preflight rather
than implying Open is free. Cache-hit collection itself performs no API calls;
production Open preflight remains one response.

Historical `d5cedaa` ChangeToken made I issue-page responses plus at least one
comment response per retained issue. Thus 45 issues with single-page comments
cost 46 responses for collection alone, **47 including preflight**; the earlier
44-issue incident was 46 including preflight. The new single-page 45-issue full
observation is three including preflight. These are explicit page budgets, not a
claim that every repository always costs two or three requests.

### #51 native smoke (2026-10-10)

The `yoophi/td-sample` smoke used a separate server on port 7778 and disposable
issue #115, subsequently logically deleted. All tested HTTP reads returned
`ok:true`. Counts below are observed HTTP response headers, not process counts.

| Consumer | API invocations | HTTP responses | Observation |
| --- | ---: | ---: | --- |
| Cold HTTP monitor | 3 | 4 | preflight 1, issue pages 2, comment pages 1 |
| Cached HTTP monitor | 1 | 1 | preflight retained; cache hit 1 |
| Concurrent HTTP stats | 1 | 1 | cache miss waiter shared the monitor collection; network charged to initiating scope |
| HTTP issue list | 2 | 3 | preflight 1, issue pages 2, no comments |
| CLI list (open only) | 2 | 2 | open issues fit one page; no comments |
| CLI JSON export (`--refresh`) | 3 | 4 | preflight 1, issue pages 2, comment pages 1 |
| CLI create #115 | 3 | 3 | preflight 1, label setup 1, write 1 |
| CLI title update #115 | 5 | 5 | preflight 1, detail reads 2, write 1, readback 1 |
| CLI review #115 | 12 | 16 | preflight 1, four issue collection invocations / eight pages, detail reads 2, label setup 1, review validation 2, write 1, readback 1 |
| HTTP selected detail #115 | 7 | 8 | preflight 1, issue collection 2 pages, detail reads 3, issue comments 1, review validation 1 |

Each scope additionally attempted one local `gh auth token` command. It is
counted separately and contributes no observed HTTP response. The review's
repeated collection invocations remain a concrete optimization candidate for
#50; aggregate display reads have no per-issue detail/comment/event requests.
Route logs use registered templates, including `GET /v1/issues/{id}`.

Verification: consumer matrices passed under the race detector for ghstore,
serve, monitor and CLI packages. Full ghstore race tests (including partial,
malformed and rate-limited pagination cache rejection) passed, as did full
macOS and Linux lint. These checks do not claim completion of #50 or the
remaining command/import tasks.

### #50 completed coordination

Repository/credential-identical overlapping preflights and bulk history sweeps
now coalesce, including cache-disabled or cache-unavailable consumers. A
concurrent real-consumer subprocess regression (HTTP stats, TUI Fetch and
ChangeToken) with 101 issues and 101 comments observes **5 HTTP responses total**
(preflight 1 + issue pages 2 + comment pages 2) and three local auth commands.
It forbids per-issue detail/comment/event calls. Completed uncached results are
not retained. Persistent display reuse still has a 30s TTL; expired baselines
use validated since deltas until the 5-minute full comparison deadline.

These changes supersede earlier pending-#50/#52 notes for aggregate display and
event-token reads. The expensive repeated review collection noted in the #51
native smoke is a separate mutation-path performance investigation (#55);
mutation safety reads have not been replaced by display snapshots.
See [cache and coordination policy](github-snapshot-cache.md) for cursor,
physical deletion delay, fresh export, versioning and process limitations.

### #67 multipart native acceptance (2026-10-10 UTC)

Two disposable sample issues each contained 28,000 random bytes encoded as
base64. Their combined journal exceeded one comment. The test injected one
group-completion PATCH rejection, explicitly reconciled the existing results,
undid the group, and logically deleted the test issues. Every command disabled
display caching. HTTP counts include native pagination and Open preflight;
the injected rejection produced no HTTP response from GitHub.

| Command | API invocations | Observed HTTP responses |
| --- | ---: | ---: |
| Create each sample issue | 19 | 32 |
| Start both, completion receipt rejected | 96 | 162 |
| `last` for the prepared group | 3 | 7 |
| Reconciliation preview | 7 | 13 |
| Apply reconciliation without target replay | 27 | 55 |
| Undo both | 134 | 250 |
| `last` after undo | 3 | 8 |

The final undone manifest referenced five parts. Display/history reads use the
existing bulk collections and no per-part GETs. Mutation paths are substantially
more expensive: original-receipt checks, publication verification and subsequent
readbacks repeatedly sweep the carrier's paginated comments. These measurements
are evidence for a separate mutation-path optimization, not a claim of low write
cost or a budget for ordinary small issues. Any reuse must stay within a verified
mutation boundary and preserve rejection of changed parts, roots and watched
targets; reusable display snapshots cannot replace those checks.

### #70 publication readback budget

The native trace exposed a repeated sequence immediately after manifest writes:
a full carrier collection, a root point read, then the same full collection.
The publisher now returns the verified native readback. Multipart publication
uses one complete collection to compare the root with its write response and
validate all referenced parts; callers do not repeat those reads. Inline
publication retains its point readback. No display cache or observation from an
earlier mutation substitutes for original-receipt, target, group or event checks.

The deterministic `TestMultipartMutationReadbackBudget` exercises real client
update and undo with different 28,000-byte random before/after descriptions and
a one-page fixture. Counts include all client API invocations in each operation:

| Operation | API calls before → after | Carrier collections before → after | Root point reads before → after |
| --- | ---: | ---: | ---: |
| Update | 32 → 28 | 9 → 7 | 3 → 1 |
| Undo | 35 → 31 | 11 → 9 | 5 → 3 |

The test also caps each publication at one readback request before the next
target observation. Mutation tests retain failure handling for edited/missing
parts, changed native root clocks/authors, incomplete publication, partial
forward/inverse completion and explicit recovery without replay. A later inverse
must reject a changed part even when publication and a display snapshot had
previously verified it. Reads across uploads or separate mutation boundaries
remain intentional and continue to use fresh native data.

Actual GitHub comparison used the same two sample issues (#184/#185), the same
28,000-byte seeded descriptions, and cache disabled. Before each variant's
start, native title/body/state/label names matched the original inputs exactly.
Both variants injected one group-completion PATCH rejection, reconciled without
changing target bodies, revisions or comment counts, and undid the group back
to its original contents. Test issues were logically deleted afterward; no
SQLite database was created.

| Command | API calls before → after | Observed HTTP responses before → after | Common-page HTTP workload before → after |
| --- | ---: | ---: | ---: |
| Start both, completion receipt rejected | 96 → 86 | 190 → 165 | 218 → 188 |
| Apply reconciliation | 27 → 25 | 65 → 60 | 75 → 69 |
| Undo both | 134 → 122 | 294 → 279 | 340 → 304 |
| `last` after undo | 3 → 3 | 8 → 9 | 9 → 9 |

Retained history grows between runs, so equal API calls can produce more HTTP
pages (`last` above). The common-page column normalizes every GET endpoint to
its largest observed page count across both variants, keeping actual write
responses and zero-response injected failures. It is an estimate under equal
pagination, not an additional native measurement. Observed counts come from
td's parsed native-header counters, including `gh --slurp` pagination; they
exclude `gh auth`'s internal network traffic. Successful multipart publications
remain 5, 1 and 6 for start, recovery and undo respectively. The saved API
invocations are exactly the removed point read and carrier collection per
publication. The large-operation cost remains substantial; these results do
not assert an API-limit guarantee for arbitrary workloads.

### #73 native-event observations within a validation boundary

The sequential paired-command CLI trace exposed repeated event collections
inside one fresh proof check: the same completed command is a witness for both
members, and each member rechecked every event baseline in that command.
The actual CLI regression first failed with 290 event collection invocations
and 50 adjacent repeated reads. Ordinary open-task field updates had no event
proofs, so that initial experiment was not used as evidence of this duplication.

Paired/update-activity observations and fences now allocate a private event
observation. Witness checks in that same read-only boundary share it; every
recorded baseline is still compared independently. Planning and the next fence
allocate another observation, including after intent publication or a target
write. There is no TTL/process cache and no replacement of fresh issue/comment
collections, membership/receipt/author/session checks, or uncertain-write
handling. Returned event arrays are copied, clients are isolated, cancellation
is checked even on a hit, and errors are returned without automatic retries.
The observation remains best-effort rather than atomic with a later write.

The real CLI fixture creates two tasks, starts/reviews them, makes two paired
points/comment updates, and undoes both while retaining the original native
fields. It measures each command separately. Both zero and 1,000 additional
historical native comments give the same invocation reduction:

| Command | gh invocations before → after | Event collection invocations before → after |
| --- | ---: | ---: |
| Review both | 277 → 277 | 76 → 76 |
| First paired update | 119 → 119 | 28 → 28 |
| Second paired update | 119 → 119 | 28 → 28 |
| Undo later command | 190 → 190 | 54 → 54 |
| Undo earlier command through the completed-command witnesses | 240 → 190 | 104 → 54 |

Here gh invocations include authentication commands, while the events column
counts paginated API invocations. These are not HTTP page counts. The fixture
returns each collection in one page: added history increases payload/processing
work but cannot model GitHub's extra pagination requests. The two history sizes
both pass native-field restoration and report zero adjacent duplicate reads.
Baseline and candidate test durations use different race instrumentation and
are not used to claim a latency speedup. Original single-command handoff/review
cost and intentional reads across write boundaries are not claimed to improve.

A GET-only integration against `yoophi/td-sample` compares two synthetic proof
inputs against actual event collections for sample tasks 135/138. Two checks
inside one boundary use two requests instead of four; the next boundary makes
two fresh requests, and a differing expected event baseline is still rejected.
No native compound command, task, receipt or access write is performed by this
integration, so it does not claim a live compound-command latency measurement.
Run it explicitly with `TD_GH73_NATIVE_DIR=<validated-sample-checkout>` and
`go test -race ./internal/ghstore -run '^TestNativeEventObservationLiveReadOnly$'`.
Ordinary CI skips this opt-in live test and runs the deterministic boundaries.

Validation: full ghstore race 206.713 s; two-volume CLI race 159.452 s;
sequential paired/update-activity foreign-change and partial-inverse recovery
race 382.177 s; GET-only integration race 7.010 s. Native event changes, missing
receipts/effects, foreign fields/clocks/authors, observed-only proof and uncertain
completion/deletion continue to be rejected or reconciled without write replay.
Local evidence: `artifacts/task-audit-2026-10-10/gh-73-*`, including the per-command
comparison, baseline failures and native observation log. These savings do not
establish a rate-limit guarantee for arbitrary repository/workload sizes.
