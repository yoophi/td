# GitHub snapshot cache

GitHub aggregate issue/history observations are regenerable JSON data, separate
from SQLite tasks, device sessions, configuration and pending-write state. The
cache root comes from Go `os.UserCacheDir()`, not a hardcoded home directory:

- macOS: `~/Library/Caches/td/gh-issue/v2/`
- Linux: `$XDG_CACHE_HOME/td/gh-issue/v2/`, or `~/.cache/td/gh-issue/v2/`

Each `<context-key>/snapshot.json` is isolated by schema version, `github.com`,
normalized repository, credential fingerprint and issues-only/full-history scope.
The credential fingerprint is SHA-256 of the actual credential resolved by
`gh auth token`, including environment overrides. It identifies an authentication
context, not a verified account display name. Tokens, auth headers and auth-token
command output never appear in snapshots or diagnostics. A missing/unidentifiable
credential context disables shared caching. `TD_GH_CACHE=off` explicitly disables
persistent snapshot reuse (useful for diagnosis and hermetic fixtures). Same-process
concurrent reads still share in-flight sweeps; completed results are not retained.
Auth changes use a different key.
Only github.com repositories are currently supported by the storage backend.

## Freshness and safety

TTL is **30 seconds**, replacing the original issue's five-minute proposal so
#56's one-minute default and 30-second minimum monitor interval obtain fresh
periodic observations. An observation's time is collection start, not cache-hit
time. The TUI last-refresh time and HTTP monitor timestamp retain that time.
Clock skew into the future, expiry, missing/incomplete data, corrupted JSON,
invalid metadata, wrong version/scope/auth/repository or generation are misses.
Every loaded snapshot passes the same issue/activity validation as remote data.

Only bulk aggregate `ReadSnapshot` consumers use this cache: monitor/context,
full statistics, handoff summary and change-token reads. JSON export forces a
full remote sweep and can publish its validated result for later display reads.
This does not cache every td command or every HTTP endpoint. Individual issue,
review eligibility, event and mutation reads retain their fresh remote paths.
A cache hit makes zero issue/comment HTTP calls; `Open` still validates Git,
remote, credential availability and repository access. Consequently cached data
cannot hide a missing gh executable/origin, denied access or preflight rate limit.

Expired data is never returned as a successful CLI/export read. A failed remote
refresh propagates the original error. The TUI retains its prior displayed data,
last successful observation time and error, with existing reset waiting; this is
not an explicit stale-success mode. HTTP calls continue to report failures rather
than disguising last-known data as fresh. No write retries are introduced.

## Publication and concurrency

Directories are 0700; snapshot files are 0600. Publication writes a private temp
file in the same directory, syncs, closes and atomically renames it only after all
pages and metadata have succeeded. The envelope records identity, scope, complete,
collected_at, generation, canonical raw issue/comment entries and full-history
change token. PR data is excluded; retained task and auxiliary entries are kept.
New envelopes retain a validated `cursor` (collection-start timestamp),
`full_reconciled_at` and known pull-request numbers for comment classification.
Older envelopes without this evidence are treated as misses and replaced by a
full collection. Pull-request bodies/comments are not retained.

A short filesystem lock protects publication/generation comparisons, never HTTP.
A repository generation fence spans all credential/scope caches; all-clear also
advances a global fence. Reads collected before invalidation cannot republish
old data, and a slower collector cannot overwrite a later collected observation.
A write attempt invalidates before and after the request, including cancellation
or uncertain failure; final invalidation has a short independent cleanup context.
Cache invalidation failure warns without preventing the remote write. Fresh
mutation verification remains the safety boundary even if display cache cleanup
fails. Remote changes by other tools are observed after TTL or `--refresh`.

Concurrent same-context/scope/generation misses are merged within a process.
Separate processes can still collect simultaneously; the short publication lock
prevents stale overwrite, not all cross-process network duplication. The initiating
caller's cancellation can abort a shared collection; waiting callers receive the
error and a later read can try again, subject to rate protection. Rate-limit
observations now create a process-local repository/credential cooldown shared
by new repository preflights and all client API reads and writes. This protection
is independent of `TD_GH_CACHE=off`; missing credentials cannot establish a
shared identity. Local Git/credential validation still runs on every Open. Concurrent remote
preflights with the same repository/credential digest now share their in-flight
request. The completed result is not memoized: later Open calls recheck remote
permissions and Issues/archive settings. Other identities never join that
flight, and unavailable identity falls back to an independent check. A canceled
waiter returns promptly without canceling the initiator; the initiator's context
controls the shared request, so its failure/cancellation reaches waiting callers
and is not automatically retried.
No additional GitHub request is attempted before the observed reset/retry
deadline (or a conservative one-minute wait without usable headers). The
original typed error, request diagnostic and reset metadata are retained. A
shorter subsequent observation does not shorten an active wait. Cancellation
takes precedence and expired observations are discarded. Already in-flight
requests cannot be recalled; other td/gh processes do not share this state.
No write is automatically retried, and after expiry the caller must issue a
new request. An explicitly blocked write performs no cache invalidation because
no attempt was made. This is a cooldown gate, not an account-wide quota scheduler.

The shared gate, preflight/sweep merging, incremental collection and periodic
full comparison implement #50; the completion audit appears below.

## User controls and storage limits

- `td cache status [--json]`: enabled state, repository, TTL, scope paths,
  observation times, sizes and freshness. Repository preflight is still required.
- `td cache clear`: invalidate snapshots for the configured repository across
  credentials/scopes.
- `td cache clear --all`: invalidate all GitHub snapshots without requiring gh or
  a configured repository. Generation fences remain so active collectors cannot
  recreate pre-clear data. Task/config/session/pending-write files are untouched.
- `td <command> --refresh`: bypass reusable bulk snapshots for that invocation;
  it does not change unaffected individual reads or auto-retry writes.

A snapshot above **32 MiB** is not persisted. Published snapshot files are bounded
in aggregate within the current schema root to **256 MiB**, evicting oldest-used files, and files unused for
**7 days** are removed on publication. Successful hits touch the file's usage
mtime. Generation files and empty context directories are small and retained for
invalidation correctness. Cleanup is best-effort; inaccessible files may remain
and diagnostics report cache access/publication errors. Cache path failures fall
back to remote reads. Symlink/non-private/oversized snapshot files are not reused.
Disk caching currently targets macOS/Linux; Windows falls back to remote reads
with an explicit cache warning.

## Verification (#52)

Race tests cover TTL/bypass/scope/credential separation, metadata/schema/corruption
misses, private permissions, failed pagination without publication, overlapping
read coalescing, uncertain-write invalidation, invalidation during collection,
separate-process concurrent publication and all-clear fences, capacity/age limits,
and fresh individual safety reads. GitHub-focused ghstore/serve/monitor/CLI race
regressions and macOS/Linux lint pass.

Native CLI verification against `yoophi/td` observed four REST responses on a
miss, one repository preflight on a hit (zero issue/comment requests), and four
after `--refresh` or clear. All-clear left configuration bytes unchanged. Missing
gh and an absent origin were explicitly rejected; a warm cache did not hide a
simulated preflight rate limit, its original request diagnostic or reset time.
The pre-existing CLI JSON envelope classified that rate error as `invalid_input`;
correcting its typed code is separately tracked as #57.

Disposable `yoophi/td-sample#114` verified actual writes: a warm context read
used one preflight; updating its title invalidated the cache and the next context
used four responses with the new title. Selected detail still made fresh reads.
The fixture was logically deleted and absent from the subsequent context.
Evidence: tracking checkout `artifacts/gh-api-n-plus-one-plan/gh-52/`.

#57 now classifies typed GitHub rate-limit errors as `rate_limited` in the CLI
JSON envelope, including through generic command wrappers. It preserves the
message/reset guidance and nonzero exit; ordinary input errors retain their code.
Native warm-cache preflight simulation and JSON envelope regression tests verify
this correction.

### #50 shared cooldown verification (2026-10-10)

Race tests exercise 100 simultaneous cross-client read/write calls after one
failed write: exactly one underlying attempt, original cause/reset preserved,
repository and credential isolation, local-auth revalidation, expiry, cancellation
and no shortening of an active deadline. Consumer GitHub tests and full macOS /
Linux lint passed.

A separate native server on port 7778 used a simulated gh executable with
`TD_GH_CACHE=off`. Startup made two preflights and one failed issue collection
(429 / Retry-After 3600). Subsequent monitor, stats and selected-detail requests
each returned HTTP 429 / `rate_limited`, original request-id and retry guidance,
with zero additional API attempts. This is simulated error-path evidence, not a
claim that the real GitHub account was rate-limited. The server was stopped.

### #50 overlapping preflight verification (2026-10-10)

Full ghstore race tests and GitHub consumer tests for serve/monitor/CLI passed,
as did full macOS/Linux lint. Twelve concurrent repository resolutions perform
24 local Git checks and 12 local credential reads, but one remote repository
check. Tests additionally cover identity isolation, no reuse after completion
and cancellation of a waiter without canceling the initiator.

A native server with simulated gh responses and `TD_GH_CACHE=off` on port 7778
returned HTTP 200 for concurrent monitor/stats/issues requests. Their additional
remote preflight count was one; a later issues request added one fresh check.
The initiating route receives the preflight API cost; waiting routes still count
their own local auth invocation. With persistent caching disabled, their issue
collections remained independent in this smoke: this verifies preflight merging,
not completion of all sweep coordination or incremental collection in #50.
The verification server was stopped.

## Incremental observations and full comparison (#50)

The display TTL remains **30 seconds**. A fresh validated snapshot can be reused.
After TTL expiry, an identity/scope/generation-valid baseline whose last full
scan began less than **5 minutes** ago supports paginated issue and repository
comment `since` queries. The boundary overlaps the prior successful collection
start by **2 seconds**, rounded down to the API's UTC second precision. Feeds
sort by updated time ascending, with `state=all` for issues. This is one issue
collection and, for activity scope with tasks, one comment collection: no
per-issue detail/comment/event requests. Empty activity repositories and
issue-only observations omit the comment collection.

[GitHub issue list documentation](https://docs.github.com/en/rest/issues/issues#list-repository-issues)
and [repository comment documentation](https://docs.github.com/en/rest/issues/comments#list-issue-comments-for-a-repository)
define `since` in terms of updated time. The lists do not supply a deletion
tombstone. Therefore incremental merging is an observation, not proof that
retained entities still exist. A physically deleted/transferred issue or deleted
comment may remain until full comparison. On the next successful read after
the **5-minute** full-scan deadline, the collector reads both complete lists
again. There is no background timer solely for cache reconciliation: observation
delay also includes the reader's polling interval, collection time and any
rate-limit/outage. Monitor keeps its user-requested **1-minute default /
30-second minimum**; the existing SSE interval remains 5 minutes.

Delta IDs are deduplicated across page overlap using newest updated timestamps.
Conflicting same-timestamp versions within a delta fail explicitly; same-second
edits relative to the older baseline are accepted. Older versions cannot replace
a newer baseline. All pages and applicable td metadata are validated before
publication. Rate-limit, malformed, incomplete or conflicting results leave the
old file and cursor unchanged and return the original error. A previously unseen
comment owner triggers a bounded full sweep to distinguish new issues/PRs; it
never triggers a per-issue lookup. Known PR comments remain excluded.

Only successful assembly advances `cursor`/`collected_at`; `full_reconciled_at`
advances only on full scans. Generation fences still reject obsolete publication.
Writes invalidate their baseline; `--refresh` forces full collection. JSON
export always forces full collection, even when the display cache is fresh, so
it cannot export ghosts retained by incremental observation. Actual mutation
verification uses fresh individual API reads, never these display observations.

The TUI footer displays observation mode and last full scan alongside the last
observation time. HTTP monitor DTOs add `observation_mode` and
`full_reconciled_at`; SQLite responses omit them. Cache status exposes those
fields and the 300-second full comparison interval. A failed refresh preserves
the previous observation time and shows the error. This does not assume remote
reads are atomic; clock skew/eventual consistency can defer changes to full
comparison despite the overlap window.

### #50 incremental verification (2026-10-10)

Full ghstore race tests and GitHub consumer race tests for serve/monitor/CLI
passed, together with full macOS/Linux lint. Tests cover reopen/edit/new entities,
unchanged baseline preservation, multi-page duplicate IDs, physical deletion
retention followed by full comparison, forced-full export, cursor publication,
PR filtering and unknown-owner full fallback. Malformed/partial/rate-limited
results preserve the previous cache bytes and cursor. TUI tests verify visible
mode/full time and unchanged timestamps after failure; DTO tests verify additive
GitHub fields and omission on SQLite.

Real `yoophi/td-sample` smoke used disposable issue #116 and comment 6087201736.
The initial full scan observed two issue pages and one comment page (4 HTTP
responses including preflight). A direct GitHub title edit and physical comment
deletion were then performed without td cache invalidation. After TTL expiry,
monitor returned `incremental`, the edited title, and the retained deleted
comment, with 3 responses (preflight 1 + issue delta page 1 + comment delta page 1).
`full_reconciled_at` remained the initial scan time. JSON export without an
explicit `--refresh` forced a full scan (4 responses) and excluded the deleted
comment; the next cached monitor returned mode `full` and excluded it too.
The comment was physically deleted and the test issue logically deleted during
cleanup; the port 7778 server was stopped. This verifies incremental behavior,
not completion of the remaining cache-disabled sweep sharing in #50.

## Transient sharing, schema compatibility and completion audit (#50)

Disabled or unavailable persistent caching does not disable concurrent sweep
sharing. Repository/credential, activity scope, explicit refresh intent and a
process-local repository generation separate in-flight collections. Complete
results are discarded after delivery in this mode. An unidentifiable credential
cannot safely share a sweep. Concurrent waiters receive the initiating request's
result/error; a canceled waiter returns promptly, while cancellation of the
initiator can fail the shared sweep. Network failures are not automatically retried.

Every attempted write advances the process repository generation before and
after the attempt, across credentials; a request blocked by cooldown advances
nothing. Post-write readers cannot join an older flight. Persistent invalidation
remains active for cache-disabled writers. If disk invalidation fails, the same
process also rejects a baseline collected before its known write boundary.
Other processes cannot share this in-memory fence: filesystem failures remain
explicit warnings, and mutation authorization always uses fresh remote reads.

Incremental-capable envelopes use **schema v2** and its own root/key namespace.
Legacy v1 readers assume fresh envelopes are full observations, so they must
never read v2 incremental data. v1 files are not migrated or reused. Writes,
repository clear and all-clear invalidate an existing v1 root as well as v2,
advancing its generation so an older in-flight publisher cannot republish
pre-write data. An absent v1 root is not created. Snapshot size/cleanup bounds
apply per schema root during coexistence; all-clear removes both known versions.
No task, session, configuration or pending-write data is migrated/deleted.

| Requirement | Verified evidence |
| --- | --- |
| Same-process dashboard/TUI/event-token reads share a sweep | External-gh regression concurrently runs actual HTTP stats, TUI Fetch and ChangeToken with 101 issues / 101 comments: total preflight 1 + issue pages 2 + comment pages 2; local auth invocations 3, individual detail/comment/events 0 |
| Cache disabled/unavailable sharing | 12 concurrent uncached clients execute one sweep; completed result is not retained. Native simulated-gh server: concurrent monitor/stats return 200 with one preflight and one issue sweep; later monitor performs a new sweep |
| Write/refresh boundaries | Cross-credential repository write fences separate old/new flights; uncertain cache-disabled writes invalidate persistent snapshots; process write knowledge rejects unchanged disk baselines; explicit fresh intent has a separate flight key |
| Identity/scope isolation | Digest-based repo/auth keys, scope keys, existing cache isolation tests and per-repository test fixtures |
| Successful-only cursor | Multi-page delta validation, exact two-second overlap, newest-ID merge; failed/rate/conflicting/malformed delta leaves original file/cursor unchanged |
| Edit/delete/reopen and full comparison | Race tests and real sample #116 title edit / comment deletion; delta retains deletion ghost, forced-full export and next monitor remove it; 5-minute deadline tests force full scan |
| Rate reset and writes | Shared cooldown across preflight/API/clients, original typed cause/reset retained, 100 blocked read/write calls make zero extra attempts; writes are never retried |
| Visible freshness | TUI mode/full-time footer and failure timestamp tests; HTTP and cache DTO fields; native full → incremental → full observations |
| Compatibility and limits | v2 namespace / legacy-envelope rejection, v1 generation fence regression; 30s TTL, 5m full deadline, polling/deletion delay, non-atomic reads and separate-process limitations documented above |

Verification: full ghstore race tests, GitHub consumer race tests for
serve/monitor/CLI, the concurrent actual-consumer test and full macOS/Linux lint
passed. The native cache-disabled server used simulated gh responses with
controlled latency; it made no real GitHub write and was stopped. Prior real
GitHub incremental smoke evidence is recorded above. This closes #50's scope,
not the remaining command parity, import, GUI or other project tasks.
