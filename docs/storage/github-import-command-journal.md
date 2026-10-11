# Import command journal contract (#65)

Status: implementation in progress. The v33 prepared-intent model and v34
reference archive transport have local implementation/tests and are unpublished.
Import is not yet connected to command history, whole-command undo or
reconciliation. The execution sections below specify required runtime behavior,
not completed support. #65 and the intermediate release remain gated on it.

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
legitimate legacy intent/capture/completion baselines. This scope is not yet
connected to CLI root preparation/completion or inverse execution. Each current
root fence still reads the root and its fresh paginated carrier collection and
revalidates the archive. Removing that duplicate point read and avoiding repeated
semantic parsing after verified native equality remain efficiency follow-ups;
fresh conflict checks must remain intact.

## Undo and subsequent import

Undo preserves issue numbers, authors, native timestamps and comments. It marks
owned allocations logically deleted and restores overwritten fields/relationships
with fresh conflict checks. Retained source/destination archives are disclosed.
All original forward proofs remain immutable as inverse progress is appended.

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

The normal journal publisher and native collection readers handle roots, including
compressed/multipart roots when reference metadata itself is large. Uploads are
verified in a fresh collection before publication. A lost response is never
automatically retried; an explicit attempt can reuse exactly matching unedited
orphan chunks while no root exists. Duplicate/foreign chunks and existing command
roots are explicit errors. No source history becomes live review authority.

Preserve existing large-import capability: the 4 MiB decoded journal limit is
not a new total import-history limit. Preparation checks root/reference capacity
before uploads; preflight of final forward/inverse proof metadata remains part
of the unfinished execution integration. Missing, edited, duplicate or foreign
referenced parts fail explicitly. Partial archive uploads stay inert until a
verified root owns them; they cannot authorize target changes. Resolving
references adds no per-source or per-part GitHub GET requests to bulk reads.
Incremental reads defer reference validation until the delta is merged with
unchanged baseline chunks.

Local tests cover input history beyond 4 MiB, multipart reference roots, changed
native/semantic evidence, partial upload recovery, original-document reuse and
bulk/incremental reading. This does not claim live GitHub or complete import
execution acceptance. Source/destination provenance already written by the
legacy executor is unchanged; reuse across that format and command documents is
a future integration/efficiency concern, not a claim of zero storage duplication.

Active monitors/servers must understand v34 before it is published to their
repository. Installing a new CLI alone does not replace running readers. This
implementation has not published native v33/v34 records or switched installations.

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
