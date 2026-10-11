# Import command journal contract (#65)

Status: implementation in progress. The v33 prepared-intent model is local and
unpublished; import is not yet connected to command history, whole-command undo
or reconciliation. This document specifies the required runtime behavior, not
completed support. #65 and the intermediate release remain gated on it.

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

The current inline prototype is not the publication format. Do not copy entire
portable input and all native comment bodies into every root/rewrite. The root
must reference immutable, digest-checked source and destination archive parts,
reusing existing import provenance and bulk collection readers. Native comment
identity, clocks, author and content digests bind references to actual evidence.

Preserve existing large-import capability: the 4 MiB decoded journal limit is
not a new total import-history limit. Check root/manifest and final proof capacity
before target writes. Missing, edited, duplicate or foreign referenced parts
fail explicitly. Partial archive uploads stay inert until a verified root owns
them; they cannot authorize target changes. Resolving references adds no
per-source or per-part GitHub GET requests to bulk reads.

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
