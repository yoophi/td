# Import command journal contract (#65)

Status: implementation in progress. The CLI now locally connects original v33
command intents, v34 reference archives, owned execution, completed command
history, explicit resume and receipt-only reconciliation. These formats remain
unpublished to live repositories. Whole-command undo and compensated reimport
now pass local CLI acceptance, including reuse of the same native issue IDs.
Remaining failure and native acceptance requirements below still gate
#65 and the intermediate release; local success is not final acceptance.

## One executor, one original command

Keep the existing import planner, allocation, capture, provenance, detach, apply
and completion stages. The command journal binds their existing checkpoints to
one invocation; it does not introduce a second import workflow engine.

An allocation identity (`td-import-v1-` plus the source-key digest) identifies a
source's destination across runs. A command operation ID identifies one run.
They are distinct. The command freezes ordered source digests, force selection,
original destination decisions and native observations before any target write.
Every owned checkpoint must bind to that command, source digest and real target.

The complete original task/operation identity inventory is retained separately
from full observations of selected destinations. Live planning and validation
of frozen evidence use the same planner. Changing an overwrite to a skip, a
mapped target to a new allocation, or the effective force decision must fail.
Duplicate allocation identities remain errors, including on unselected tasks.

## Resume ownership

Discover pending command roots before planning a new mutation. A single matching
root keeps its original input, actor, session and before-state. Never replace
its before-state with a partially imported target or start a second root around
the remaining rows. Overlapping pending roots or conflicting input are explicit
errors before target writes. A completed identical import is a read-only no-op.

The original GitHub author and td session can explicitly resume unattempted
stages after fresh checkpoint and target checks. A lost response is reconciled
from native observations first; retrying import must not automatically repeat
that write. Reconciliation only records observed effects and never performs
unfinished allocations, graph changes or compensation. Evidence stays marked
observed rather than being upgraded to proof of a verified original write.

A different session may inspect the plan and reconcile observable results under
the existing journal author rules. It cannot silently take ownership, continue
target mutations or undo the original command. Diagnostics identify the owning
session and command. Resuming the original session is required for those writes;
there is no implicit ownership transfer through `--force`.

Older imports without command roots retain their existing resumability. A new
tracked continuation explicitly records a legacy baseline and owns only effects
performed after that baseline. It must not claim the earlier allocation as its
own creation or erase earlier imported data when undone. Existing per-source
checkpoints remain provenance, and no historical review becomes active authority.

Local execution-scope integration now stamps v2 intent/capture/completion
checkpoints with the original native command ID, operation and session. Newly
allocated task bodies carry the same execution owner alongside their stable
source identity. Every replan checks checkpoint evidence from its existing bulk
observation before any writes: new checkpoints require the matching command,
source digest and target, the original native author, unedited comment clocks,
and creation at or after the root. A matching JSON owner alone is insufficient.
Legacy or older-generation checkpoints must exactly match native comments in the
frozen baseline; missing or edited baseline checkpoints stop the continuation.
Original target inventory also prevents claiming a preexisting issue as a new
allocation when native timestamps share the same second.

Local executor tests cover response loss, rejected owner transfer, foreign or
edited v2 checkpoints, new v1 checkpoints after allocation, and preservation of
legitimate legacy intent/capture/completion baselines. The CLI prepares the root
before target writes and uses this scope with the existing executor. Each current
root fence reads one fresh paginated carrier collection containing both the root
and its referenced parts, checks the exact original body/native record, and
revalidates the archive. The duplicate root point GET is removed. If there are F
fences and P carrier pages, this path now uses F × P REST requests instead of
F × (1 + P); other snapshot/event reads are additional. Reusing parsed semantics
after verified native equality remains an efficiency follow-up. Fresh conflict
checks must remain intact.

Completed results are stored separately in `import_forward`, with actual native
target/comment/event observations and an observer attribution. The original
command envelope and its document digests never change. A successful initial
invocation records a verified result; a resumed or reconciled invocation records
an observed result and retains the original command's actor/session. Completion
checks source projections, complete provenance, owned checkpoints and the native
close/reopen event boundary. Large forward observations use a separate reference
document rather than consuming the ordinary decoded root budget.

`last` and `undo --list` show one import command. An identical completed import
does not publish another root or change targets. A changed-source `--force`
import starts a new command around the current baseline, keeping original issue
identities; the previous command must be complete with unchanged checkpoint
evidence. This is distinct from the compensated reimport contract below.

A task allocated from a legacy source can later be exported and explicitly
restored from its native repository with `--force`. With no checkpoints for that
new native source, the current task becomes the new command's Before state; its
original allocation identity and owner remain unchanged. The allocation owner
must belong to a completed command. Pending creations are also discovered by
their stable allocation identity, even though their root has no target number
yet, so exporting a partially allocated task cannot transfer ownership. Once a
completion exists for the native source, subsequent target edits remain
conflicts even with `--force`.

Reconciliation never allocates tasks or replays field/checkpoint writes. A
partial execution returns its remaining plan without changing the root. Once
all source completions exist, explicit apply can publish the observed command
result under the actual observer's session without transferring original
ownership. Repeating reconciliation of a completed command is read-only and
checks its frozen native result. Whole-command inverse uses the existing graph
planner and journal publisher with ordered step intents and native results.

## Undo and subsequent import

Undo preserves issue numbers, authors, native timestamps and comments. It marks
owned allocations logically deleted and restores overwritten fields/relationships
with fresh conflict checks. Retained source/destination archives are disclosed.
All original forward proofs remain immutable as inverse progress is appended.

Before each inverse PATCH, a durable Started marker is published and the graph,
selected native tasks, comments and state events are checked again. A result
may be appended from a verified response or explicit observed reconciliation.
An uncertain PATCH is never automatically replayed, including when the target
currently appears unchanged. Only the original executor can record a verified
pre-PATCH cancellation on a return path that has not called PATCH. This retains
the canceled step/stage and executor attribution, and lets a later explicit undo
repeat normal conflict checks and resume. Failed cancellation publication or a
process exit without that evidence leaves the pending step uncertain. Original
read and cancellation errors remain visible; reconciliation cannot invent a
no-write attestation from the current target state.

Inverse field restoration invalidates previous approvals. New allocations are
logically deleted; overwritten tasks regain their original fields and graph.
Outside active tasks referring to an allocation prevent its logical deletion.
Graph reversal uses the existing detachment/order planner. All original native
comments and issue identities remain intact; compensation is not a rollback of
GitHub authors or timestamps.

A completed inverse explicitly links each compensated destination to its prior
completion receipt and verified resulting basis. Only this link permits the
planner to supersede that receipt on a subsequent import. Never discard receipts
or treat an arbitrary mismatch as an undone import, even with `--force`.

Reimport starts a new command generation linked to that completed inverse. It
reuses real allocated issue identities and archives the compensated baseline;
it does not allocate duplicate sources. An inverse of this new generation
restores that baseline. A partial inverse or later unrelated edit remains a
conflict until explicitly resolved. Source identity remains stable across these
generations; execution ownership does not.

## Storage and capacity

The v34 publication format keeps immutable input and original-observation
documents in existing v1 journal-part comments. The root references their actual
native IDs, creation clocks and body hashes, with document sizes/content hashes.
Transfer identity binds every chunk to the owning command, carrier, document and
offset. Raw chunks use the existing bounded part decoder; there is no unbounded
compressed document expansion. Rewrites reuse original documents and reject
changes to the original session, input, mapping or before-state before uploads.
The inverse plan and each actual step result are separate immutable documents.
Progress, pre-write cancellations and whole completion remain in root metadata.
Starting a step or completing the command reuses all evidence parts; appending
one result adds only that result's parts. This removes full inverse-history
republication at each step, without changing the existing part transport or
relaxing native author/clock/hash validation.

The normal journal publisher and native collection readers handle roots, including
compressed/multipart roots when reference metadata itself is large. Uploads are
verified in a fresh collection before publication. A lost response is never
automatically retried; an explicit attempt can reuse exactly matching unedited
orphan chunks while no root exists. Duplicate/foreign chunks and existing command
roots are explicit errors. No source history becomes live review authority.

Preserve existing large-import capability: the 4 MiB decoded journal limit is
not a new total import-history limit. Preparation checks original root/reference
capacity before uploads, and completion checks its realized result capacity.
Inverse preparation reserves completed inverse reference capacity before
its first target write. A capacity error may stop an import or its undo; the
command must retain the original error, disclose earlier effects and never
automatically replay an uncertain write or report incomplete work as successful.
Earlier changes and native history remain available for inspection; a capacity
failure does not promise automatic rollback or successful retry with the same
oversized result. Per-publication limits stay enforced. Predicting every possible
forward/inverse result before the first forward write, unlimited import sizes,
and automatic storage expansion are not release requirements. This follows the
user's explicit 2026-10-11 decision to allow clear capacity errors and stopping.
Missing, edited, duplicate or foreign referenced parts fail explicitly. Partial
archive uploads stay inert until a verified root owns them; they cannot authorize
target changes. Resolving
references adds no per-source or per-part GitHub GET requests to bulk reads.
Incremental reads defer reference validation until the delta is merged with
unchanged baseline chunks.

Local tests cover input and result history beyond 4 MiB, multipart reference roots, changed
native/semantic evidence, partial upload recovery, original-document reuse and
bulk/incremental reading. CLI tests cover success/no-op, lost root/allocation/
target/completion responses, denied final receipts, cross-session refusal and
receipt-only recovery, and changed-source generations. The whole-command CLI
acceptance test covers success/no-op, undo, same-ID reimport and the new
generation's undo. Pre-write snapshot/target failures, failed cancellation
publication, rejected PATCH and lost PATCH response are also exercised. Recovery
preserves observed attribution and never repeats an uncertain target write.
Compensated reimport rejects subsequent field/comment/native-event changes,
partial inverses and attempts to use an old inverse over a newer source receipt,
including with `--force`, before target or command writes.
Mixed overwrite/create and graph-detachment interruption now pass local CLI
acceptance (#76): a lost first-detachment response leaves one original command,
read-only reconciliation does not replay work, and explicit resume preserves
the original input/before-state, native issue identities/authors and comments,
and unrelated tasks. Each existing task receives exactly one detach and one
final PATCH; the newly allocated task receives one final PATCH and is not
allocated again. A completed repeat makes no writes. Whole undo of that resumed
mixed command now passes local CLI acceptance (#77), restores both original
graphs and fields, logically deletes the original new allocation, and preserves
native identities/comments and the immutable original/forward command evidence.
Its overall attribution remains observed. First-allocation response loss also
resumes and undoes both the original and subsequently allocated task together.

Inverse result-receipt response loss permits explicit continuation from its
retained result without another PATCH to that task. Denial of the final inverse
completion receipt is repaired by receipt-only observation, with no target
writes and observed attribution. External edits during a partial inverse stop
both undo and result adoption before changing targets or journal evidence.
Completed recovery confirmation is read-only. These are CLI fixture results;
live GitHub acceptance remains #78.
Source/destination provenance already written by the
legacy executor is unchanged; reuse across that format and command documents is
a future integration/efficiency concern, not a claim of zero storage duplication.

Active monitors/servers must understand v34 before it is published to their
repository. Installing a new CLI alone does not replace running readers. The
sample repository's three owned GUI/backends were upgraded before the first
native v34 publication in the acceptance below. Main installation and release
activation remain separate preparation/deployment steps.

## Native acceptance (#78)

On 2026-10-11, candidate `39e81b5` passed the real GitHub lifecycle in
`yoophi/td-sample`, using disposable issues #205 and #206 and an isolated local
session. A local proxy discarded the successful first allocation response once;
this was not a GitHub service failure. Receipt-only observation was read-only.
Explicit resume kept the original command `ghc-6108178915`, both native IDs and
the immutable source input, and retained `observed` forward attribution.

The first explicit resume allocated #206, but its immediate fresh bulk plan did
not contain the allocated ID. It stopped before relationship writes, preserving
the error and partial effects. Native point reads and a later read-only plan
recognized both owned allocations; a second explicitly inspected continuation
completed with zero creations. This is consistent with transient collection
visibility, not proof of its underlying cause. The test did not retry a write
automatically or conceal that additional interruption.

Identical import repetition and completed inverse confirmation made GETs only.
Whole undo logically deleted both allocations while preserving native identities,
authors, creation timestamps, comments and original/forward evidence. Reimport
created a new command `ghc-6108251446` linked to the completed inverse, reused both
IDs and retained earlier comments. Its undo restored the compensated baseline.
Both fixtures finished logically deleted, exactly two task POSTs occurred, all
task PATCHes targeted those fixtures, and no `.todos/issues.db` was created.

With cache disabled and existing repository history retained:

| Command | Seconds | gh API invocations | Observed HTTP responses |
| --- | ---: | ---: | ---: |
| Inspected continuation | 115.8 | 70 | 175 |
| Identical import repeat | 8.1 | 4 | 12 |
| Whole undo | 192.0 | 93 | 284 |
| Same-ID reimport | 172.0 | 100 | 260 |
| New-generation undo | 192.5 | 93 | 284 |
| Final read-only confirmation | 14.7 | 9 | 22 |

Response counts include pagination within each `gh api` invocation, but exclude
authentication's internal requests and other running readers. These are one
repository's measurements, not a latency or quota guarantee. Repeated bulk
verification remains expensive; broader optimization belongs to #73. Detailed
per-stage artifacts and both interruption results are recorded with #78.

## Required acceptance scenarios

1. Success, identical read-only repeat, whole undo, same-source reimport using
   the same target identities, and undo of that new generation.
2. Partial allocation, same-session explicit resume, one original root, and
   whole undo covering both the initial and resumed owned effects.
3. Partial allocation, different-session observation/reconciliation, explicit
   refusal of unauthorized resume/undo, then original-session completion.
4. Existing overwrite plus new allocation, failure during graph detachment,
   fresh recovery and resume or whole compensation without replacing the
   original graph/before-state or changing unrelated tasks.

Each scenario also checks preserved native history, no SQLite access, original
error visibility, and absence of automatic replay after an uncertain response.
Local model tests alone do not satisfy the runtime/CLI/native acceptance gate.

## Release preparation tracking

The remaining required checks are separate tasks under
[release preparation epic #74](https://github.com/yoophi/td/issues/74), starting
with [capacity error handling #75](https://github.com/yoophi/td/issues/75).
#65 retains its original scope and aggregates the evidence; it must not serve as
the only active work item. Only the current implementation/verification task is
marked in progress. Broad API optimization (#73), additional archive
deduplication and a generalized workflow framework are outside this release gate.
After preparation is reviewed and complete, a separate deployment task publishes
the release and verifies `yoophi/homebrew-tap` and the installed version.
