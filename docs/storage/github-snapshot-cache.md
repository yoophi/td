# GitHub snapshot cache

GitHub aggregate issue/history observations are regenerable JSON data, separate
from SQLite tasks, device sessions, configuration and pending-write state. The
cache root comes from Go `os.UserCacheDir()`, not a hardcoded home directory:

- macOS: `~/Library/Caches/td/gh-issue/v1/`
- Linux: `$XDG_CACHE_HOME/td/gh-issue/v1/`, or `~/.cache/td/gh-issue/v1/`

Each `<context-key>/snapshot.json` is isolated by schema version, `github.com`,
normalized repository, credential fingerprint and issues-only/full-history scope.
The credential fingerprint is SHA-256 of the actual credential resolved by
`gh auth token`, including environment overrides. It identifies an authentication
context, not a verified account display name. Tokens, auth headers and auth-token
command output never appear in snapshots or diagnostics. A missing/unidentifiable
credential context disables shared caching. `TD_GH_CACHE=off` explicitly disables
shared snapshot reuse (useful for diagnosis and hermetic fixtures). Auth changes use a different key.
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
full statistics, full-history export, handoff summary and change-token reads.
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
Incremental cursor is absent: the collector performs full reconciliation.

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
error and a later read can try again. Rate-limit errors remain subject to the
existing data-source cooldown, not a new cache-wide quota manager.

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
in aggregate to **256 MiB**, evicting oldest-used files, and files unused for
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
