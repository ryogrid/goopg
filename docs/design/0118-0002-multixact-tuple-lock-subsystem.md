# MultiXact tuple-lock subsystem (engine-first slice) — M0118-0003/0004

- Status: accepted (partial — engine core + member store + tuple-header
  hint-bit encoder + **cross-session lock-only AND updater-bearing multixact
  producer/consumer wired into `stampLockInner`** + **four-way lock-strength
  member status**; savepoint/subxact members + update-chain lock propagation +
  WAL persistence + spec promotion deferred)
- Date: 2026-06-21
- Supersedes: none
- Related: [0118-0001](0118-0001-isolation-spec-port-strategy.md) (isolation
  spec port strategy), [[goopg_no_hot_update_index_reeval]]

## Problem

A heap tuple's `xmax` must sometimes name a **set** of transactions, not one.
PostgreSQL models this with a `MultiXactId`: when `t_infomask` has
`HEAP_XMAX_IS_MULTI`, `xmax` is a MultiXactId that resolves (via the multixact
SLRU) to a list of `MultiXactMember{xid, status}` — any number of row-lock
holders (`FOR KEY SHARE` / `FOR SHARE` / `FOR NO KEY UPDATE` / `FOR UPDATE`)
plus **at most one** updater.

goopg's tuple `xmax` is currently **single-holder**: it carries exactly one
`TransactionID`, distinguished only by the `HEAP_XMAX_LOCK_ONLY` infomask bit
(locker vs. updater) and `HEAP_KEYS_UPDATED` (key vs. no-key update). That
representation cannot express *"a `FOR KEY SHARE` locker AND a concurrent
no-key updater on the same row"* — which is exactly the shape the upstream
isolation specs in the MultiXact cluster exercise:

| spec | why it needs a multi-member xmax |
| --- | --- |
| `lock-update-traversal` | follow an update chain whose tuples are *also* key-share locked |
| `multixact-no-forget` | a multixact must not "forget" a still-running locker when its updater finishes |
| `nowait-2`, `skip-locked-2` | `NOWAIT`/`SKIP LOCKED` against a tuple locked by a *multixact*, not a plain xid |
| `propagate-lock-delete` | a key-share lock must propagate to the new tuple version across an update, then a delete |
| `tuplelock-conflict` | two sessions taking different-strength locks combine into one multixact |
| `aborted-keyrevoke` | an aborted key-revoking update must leave surviving key-share lockers intact |

Today goopg's branch (a) in `stampLockInner` (`operators_lockrows.go:686`)
**skips** stamping when a `KEY SHARE` request meets an in-progress no-key
update, and `isConcurrentlyUpdated` (`operators_storage.go:1877`) returns
`false` for any lock-only `xmax`. Both are correct *only* because the
single-holder model has nowhere to record the second holder; a faithful port
needs the multi-member representation first.

## Approach: engine-first, wire-later

This lands the **self-contained, fully-unit-testable core** of the subsystem as
a standalone leaf package `internal/multixact`, with **no wiring into the
executor / storage hot paths**. This mirrors the `internal/amcheck` build-out
([0110-0005](0110-0005-verify-heapam-engine.md) and successors), which landed
the `verify_heapam`/`verify_nbtree` engines as pure functions over raw bytes
across many loops before the risky SQL-surface wiring. The same risk calculus
applies: the member representation, the conflict matrix, and the update-xid
selection are deterministic, pure, and verifiable against upstream in
isolation; the hot-path integration (xmax encoding, `stampLockInner` member
combination, `isConcurrentlyUpdated` multixact decode) is where regressions hide
and must land incrementally on top of a verified primitive.

### What this slice contains (`internal/multixact/multixact.go`)

- **`MultiXactId`** (`uint32`) with `InvalidMultiXactId` / `FirstMultiXactId`,
  in the same numbering space goopg already stamps into `pg_control`
  (`nextMulti`/`oldestMulti`, `internal/initdb/pgcontrol.go`).
- **`Status`** — the six `MultiXactStatus` values, pinned to their upstream
  numeric encoding (`0x00`..`0x05`, load-bearing as the on-disk member status
  and the index into `MultiXactStatusLock`). `IsUpdate()` mirrors
  `ISUPDATE_from_mxstatus` (`status > ForUpdate`).
- **`Member{Xid, Status}`** — mirrors upstream `MultiXactMember`.
- **The lock-mode conflict matrix**, ported as three *verbatim* upstream
  tables rather than a hand-collapsed 6×6 result, so the mapping stays
  auditable against upstream drift:
  - `lockConflicts` ← `LockConflicts[]` (`storage/lmgr/lock.c`).
  - `statusHWLock` ← the composition `tupleLockExtraInfo[MultiXactStatusLock[s]].hwlock`
    (`access/heap/heapam.c`): each `MultiXactStatus` → `LockTupleMode` → heavyweight
    `LOCKMODE`.
  - `StatusesConflict(held, req)` / `doLockModesConflict` ← `DoLockModesConflict`.
- **`MembersConflict`** — the pure, liveness-free core of
  `DoesMultiXactIdConflict`: scan members, skip self (lock upgrade) and
  (via an injected `isCurrent` callback) finished members, report the first
  incompatible holder. Transaction liveness is a runtime concern the leaf
  package deliberately does not own — the wiring layer supplies it.
- **`GetUpdateXid`** — the member-selection core of `MultiXactIdGetUpdateXid`
  (the SLRU/liveness lookup is the wiring layer's job).
- **`HasLockers`** — whether a member set retains a pure-lock holder after its
  updater finishes (the `multixact-no-forget` invariant).

### The member store (`internal/multixact/store.go`)

The conflict matrix above answers *"do these statuses conflict"*; the **member
store** answers *"which transactions compose MultiXactId N"* — the in-memory
analog of PostgreSQL's multixact SLRU (`pg_multixact/offsets` +
`pg_multixact/members`). It is still a pure data structure with no hot-path
wiring; the eventual `stampLockInner` integration consumes it.

- **`Store`** hands out monotonically increasing `MultiXactId`s
  (`NewStore`/`NewStoreAt(next)` to seed from `pg_control`'s `nextMulti`,
  `Next()` to read the allocator) and resolves each back to its immutable
  member set (`Members(multi) ([]Member, bool)`, returning a defensive copy).
  It is mutex-guarded (the future hot path is concurrent;
  `TestStoreConcurrentCreate` runs under `-race`).
- **`Create(m1, m2)`** ← `MultiXactIdCreate` (`multixact.c:430`): the
  two-member constructor (the members must differ in xid or status).
- **`CreateFromMembers(members)`** ← `MultiXactIdCreateFromMembers`
  (`multixact.c:811`): validates at most one *updater*
  (`ErrMultipleUpdaters`, mirroring the upstream `elog(ERROR)`) and that every
  xid is valid, then sorts the set canonically (`sortedMembers` ←
  `mxactMemberComparator`, `multixact.c:1652`: ascending `(xid, status)`, **not**
  wraparound order) and **re-uses an existing id for an identical set**
  (`setKey` ← the `mXactCacheGetBySet` sorted-array `memcmp`,
  `multixact.c:1704`).
- **`Expand(multi, add, live)`** ← `MultiXactIdExpand` (`multixact.c:483`):
  immutability by construction — adding a member never mutates an existing id,
  it mints a new one. Returns `multi` unchanged if `add` is already a member
  with the same status; creates a singleton if `multi` is obsolete (no
  resolvable members); otherwise keeps the members *still of interest* (running,
  or a **committed** updater — an aborted update is dropped so the result keeps
  the single-updater invariant) plus `add`.
- **`Liveness`** injects transaction state (`IsInProgress`/`DidCommit`) into the
  otherwise-pure `Expand` filter, the same way `MembersConflict` injects
  `isCurrent`; the leaf package does not own liveness. A zero `Liveness{}` is
  conservative (keep every member).

Two deliberate divergences from the SLRU, neither affecting member-set
semantics: the dedup cache is **global and unbounded** (upstream's
`mXactCacheGetBySet` is per-backend, LRU-capped at 256 — deterministic and fine
at spec/test scale), and ids are **never truncated/vacuumed** (the MultiXactId
wraparound/`SetOffsetVacuumLimit` machinery is a separate, deferred concern).

### The tuple-header representation (`internal/storage` + `HintBits`)

The store answers *"which transactions compose MultiXactId N"*; the **tuple
header** is where that id is recorded so a heap reader knows to ask. This slice
lands the representation — the bit and the *encoder* — with still **no hot-path
wiring** (nothing yet stamps a multi xmax, so every existing tuple decodes
exactly as before):

- **`storage.HeapXmaxIsMulti`** (`0x1000`, ← `HEAP_XMAX_IS_MULTI`): the
  `t_infomask` bit that flips `xmax`'s interpretation from a single
  `TransactionID` to a `MultiXactId`.
- **`storage.IsHeapTupleXmaxMulti(infomask)`**: the decode predicate a reader
  consults *before* applying any single-xid logic (`WaitForXID`,
  `TransactionIdIsCurrent`) to `xmax` — a multi xmax must instead be resolved
  through `Store.Members`. Its sibling `IsHeapTupleLockOnly` (unchanged)
  classifies a multi as locked-only vs. updated; goopg needs no upstream
  pre-9.3 `EXCL_LOCK` fallback clause because the encoder below always sets
  `HEAP_XMAX_LOCK_ONLY` for an updater-free multi (no pg_upgrade legacy tuples).
- **`multixact.HintBits(members)`** ← `GetMultiXactIdHintBits`
  (`heapam.c:7455`): the **encoder**. Given a member set it computes the
  `(t_infomask, t_infomask2)` bits that accompany `HEAP_XMAX_IS_MULTI`: the
  *strongest* member's lock-strength bits (`KEYSHR`/`SHR`/`EXCL_LOCK` via the
  `MultiXactStatusLock`/`LockTupleMode` ordering, ported as `statusTupleLock`),
  `HEAP_XMAX_LOCK_ONLY` iff no member is an actual update, and
  `HEAP_KEYS_UPDATED` iff any member reserves the key (a `FOR UPDATE` lock *or*
  a key-changing `Update` — note `FOR UPDATE` sets it even while lock-only, and
  a no-key `UPDATE` does not). It lives in `multixact` (not `storage`) because
  it reads member *statuses*; the existing `multixact`→`storage` import lets it
  reference the bit constants, and the reverse import would be a cycle.

`HintBits` is the **encode twin** of the decode predicates above and of
`GetUpdateXid` (member-selection); per
[[pattern_sibling_paths_must_agree]] they land together and are round-tripped
in `TestHintBits` (encode a member set → assert exact bits → decode and assert
`IsHeapTupleLockOnly` agrees with `GetUpdateXid`'s updater presence).

### The load-bearing compatibility fact

The whole subsystem exists to model one case the single-holder xmax cannot:

> `FOR KEY SHARE` (→ `AccessShareLock`) does **not** conflict with a concurrent
> `NO KEY UPDATE` (→ `ExclusiveLock`).

`AccessShareLock` conflicts only with `AccessExclusiveLock`, so an FK child's
key-share lock coexists with a non-key parent update — which is *why* goopg can
skip-without-waiting today, and why combining the two requires a multi-member
representation tomorrow. The reverse — `FOR KEY SHARE` vs. a key-changing
`UPDATE`/`DELETE` (→ `AccessExclusiveLock`) — *does* conflict. Both are pinned
by `TestStatusesConflictKeyShareCompatibleWithNoKeyUpdate`.

### Hot-path wiring slice 1: cross-session lock-only multixact

The first hot-path slice wires the **lock-only** multixact case — two (or more)
concurrent non-conflicting row-lock holders on the same tuple (e.g. two
`FOR SHARE` sessions) — end to end through `stampLockInner`
(`internal/executor/operators_lockrows.go`). This is the deliberately *narrow*
producer/consumer pair that keeps blast radius minimal:

- **Producer** (`stampMultiLock`): when the live-tuple stamp path finds the
  tuple already carries a lock-only `xmax` from another **still-active**
  transaction that does **not** conflict with our request (`tupleLockConflicts`
  was false, so we fell through the wait/skip/nowait branch), it builds the
  combined member set (`existing survivors` filtered to in-progress + our own
  member at our strength), `Store.CreateFromMembers`, computes `HintBits`, and
  stamps the `MultiXactId` + hint bits via the new
  `storage.PageSetHeapTupleXmaxMulti`. Because the combined set is lock-only,
  `HintBits` always sets `HEAP_XMAX_LOCK_ONLY`.
- **Consumer** (`activeLockHolders`): the row-lock conflict branch resolves a
  `HEAP_XMAX_IS_MULTI` `xmax` through `Store.Members` to the subset of members
  still in progress **before** calling `IsXIDActive`/`WaitForXID` — the raw
  `MultiXactId` lives in a different numbering space and must never be passed to
  a single-`TransactionID` API. A `NOWAIT` request against a live multi fails
  `55P03`; a blocking request waits for **every** active member.

**Why this is safe for visibility.** A lock-only multi carries
`HEAP_XMAX_LOCK_ONLY`, and every visibility consumer
(`mvcc/visibility.go`, `mvcc/subxact_visibility.go`, `isConcurrentlyUpdated`,
`storage/freeze.go`, `storage/prune.go`, `storage/vm.go`) short-circuits a
lock-only `xmax` to "not a delete" *before* interpreting the value as a
`TransactionID`. So a lock-only `MultiXactId` is fully transparent to MVCC
visibility — the only consumer that must become multixact-aware is the row-lock
path itself. This is what bounds the change to `stampLockInner` and keeps the
single-holder fast path byte-identical.

The `Store` is process-shared: instantiated once in `cmd/goopg/main.go`
(`multixact.NewStore()`), carried on `server.Config.MultiXact`, and plumbed into
every `executor.Context` by the dispatch paths. A nil store disables the
multixact path entirely, preserving single-holder behaviour for tests and any
runtime that does not wire it.

The full producer/consumer round-trip is pinned by
`TestForShareFormsLockOnlyMultiXact` (two `FOR SHARE` holders → assert a 2-member
lock-only multi; `FOR UPDATE NOWAIT` against the live multi → `55P03`; holders
gone → fresh `FOR UPDATE` re-stamps a single-holder xmax).

### Hot-path wiring slice 2: updater-bearing visibility consumer (`isConcurrentlyUpdated`)

The lock-only case above is transparent to MVCC visibility, but the *next* case
— a multixact whose member set includes an **updater** (`HEAP_XMAX_LOCK_ONLY`
clear) — is not: a non-lock-only `MultiXactId` xmax must be resolved to its real
updater `TransactionID` before any single-xid reasoning. Per
[[pattern_sibling_paths_must_agree]] the **read side lands before the producer**,
so that when the updater-bearing producer (resume point #2) lands, every consumer
is already correct and no half-wired window exists where a non-lock-only multi can
be mis-read as a single deleter.

This slice makes the executor write-conflict / EPQ consumer multixact-aware:

- **`isConcurrentlyUpdated`** (`operators_storage.go`) — the predicate the
  UPDATE / DELETE / HOT-update paths use under the page lock to detect a
  concurrent xmax stamp. It now takes the process-shared `*multixact.Store` and,
  when the xmax is `IsHeapTupleXmaxMulti && !IsHeapTupleLockOnly`, resolves the
  members via `Store.Members` and the updater via `multixact.GetUpdateXid`,
  reasoning about that *updater xid* (the "effective xmax") in the self-stamp,
  `HeapHotUpdated`-abort, and `snap.HasAborted` checks instead of the raw
  `MultiXactId`. An all-locker multi (no updater) is treated as not-updated; an
  unresolvable multi (nil store or missing record, both impossible while no
  producer emits non-lock-only multis) is treated conservatively as updated to
  force an EPQ recheck. All ~12 call sites pass `ctx.MultiXact` /
  `o.ctx.MultiXact`. The single-holder and lock-only paths stay byte-identical
  (`effXmax == h.Xmax`).

Pinned by `TestIsConcurrentlyUpdatedMultiXact` (updater = another xact → updated;
updater = self → not updated; lockers-only → not updated; store unavailable /
multi absent → conservatively updated). The single-xid / lock-only cases remain
under `TestIsConcurrentlyUpdatedHelper`.

### Hot-path wiring slice 2 (continued): snapshot visibility consumer (`mvcc.TupleVisible`)

The second read consumer is the snapshot visibility check itself.
`mvcc.TupleVisible` (`mvcc/visibility.go`) now takes the process-shared
`*multixact.Store` and, when the xmax is `IsHeapTupleXmaxMulti` and *not* lock-only,
resolves the updater xid via `Store.Members` + `multixact.GetUpdateXid` before the
self-delete (`effXmax == currentXID`) and snapshot
(`!snap.SeesCommittedXID(effXmax)`) checks — exactly mirroring
`isConcurrentlyUpdated` and upstream `HeapTupleSatisfiesMVCC`'s `HEAP_XMAX_IS_MULTI`
arm. The conservative directions follow the *visibility* contract (which differs
from the write-conflict contract above): an all-locker multi (no updater) →
**visible** (a lock is not a delete); an unresolvable multi (nil store / missing
record, both unreachable while no producer emits non-lock-only multis) →
**invisible** (never expose a version whose committed successor we cannot rule
out). The single-holder and lock-only paths stay byte-identical (`effXmax ==
h.Xmax`), so every existing tuple is judged exactly as before.

`mvcc` may import `multixact` (no cycle: `multixact` imports only `storage`). The
`Store` is threaded through all production call sites — the executor
scan / index / index-only / upsert / toast paths and the two
`followHOTChain` / `followHOTChainNoCopy` helpers pass `ctx.MultiXact`; the
`vacuum` and `analyze` stats-sampling scans have no store in scope (their public
APIs take `pool`/`mgr`/`rel`) and pass `nil` — safe because no non-lock-only multi
exists until the producer lands, at which point the producer slice threads the real
store through those two maintenance paths.

Pinned by `TestTupleVisibleMultiXact` (committed updater → invisible; in-progress /
future updater → visible; self updater → invisible; lockers-only → visible; store
unavailable / multi absent → conservatively invisible). The single-xid, own-xid,
and lock-only cases remain under `TestTupleVisibleBasicCases` / `…OwnXIDRules` /
`…LockOnlyXmax` / `…NonLockXmaxRegression`.

### Hot-path wiring slice 2 (continued, 2): storage vacuum read consumers (`freeze.go` / `prune.go` / `vm.go`)

The last read consumers live in package `storage`, which **cannot** import
`internal/multixact` — `multixact` imports `storage`, so a direct dependency would
cycle. Instead `storage` exposes a process-level resolver hook
`storage.ResolveMultiUpdater` (in `heap.go`, beside `IsHeapTupleXmaxMulti`), wired
once from the process-shared `multixact.Store` in `cmd/goopg/main.go` right after
the store is created. It returns `(updater TransactionID, hasUpdater bool, resolved
bool)`, mirroring `Store.Members` + `multixact.GetUpdateXid`:

- `resolved == false` — MultiXactId not in the member store (or no hook installed);
  the caller must fall back to its own conservative default and must **not** read
  xmax as an xid.
- `resolved && !hasUpdater` — multi holds only lockers; the tuple is still live.
- `resolved && hasUpdater` — `updater` is the updater member's xid; reason about it
  exactly as a plain xmax xid.

Two `storage` read paths interpret xmax as a deleter and now funnel an
`IsHeapTupleXmaxMulti && !IsHeapTupleLockOnly` xmax through the hook before judging
it:

- **`PageFreezeOldTuples` (`freeze.go`)** — a non-lock-only xmax normally means
  "deleted, skip freezing"; for a multi it resolves the updater: only-lockers →
  the row is live and its xmin is frozen, updater present (or unresolved/nil hook)
  → skip. Pinned by `TestPageFreezeMultiXactXmax`.
- **`PagePruneOpt`'s `isDead` (`prune.go`)** — the old `hdr.Xmax < oldestXmin`
  horizon test was a **category error** for a multi (`hdr.Xmax` is a MultiXactId,
  not an xid, so the comparison could spuriously mark a live, only-locked row dead
  and prune it). It now resolves the updater and applies the horizon test to the
  *updater* xid; only-lockers / unresolved / nil-hook → conservatively **not**
  dead (never prune a tuple we cannot prove dead). Pinned by
  `TestPagePruneOptMultiXactXmax`, whose `updater newer than horizon` subtest is a
  direct regression guard for the category error.

`vm.go`'s `PageAllVisible` needs **no** resolver: an updater-bearing multi lands in
its `!IsHeapTupleLockOnly` "deleted" arm and correctly fails all-visible (the
conservative direction); resolving could only ever mark *more* pages all-visible,
and an all-locker multi already carries `LOCK_ONLY`. Only a clarifying comment was
added. `amcheck/verify_heapam.go`'s `checkXmaxBounds` already returns early on
`HEAP_XMAX_IS_MULTI` (it validates the multi separately — a deferred AC-003 path),
so it never misreads the MultiXactId either.

### Hot-path wiring slice 2 (continued, 3): main-scan visibility twin (`mvcc.TupleVisibleSubxact`)

A producer-gate audit (re-running the [[pattern_sibling_paths_must_agree]] sweep
over **every** `IsHeapTupleLockOnly` consumer, not just the originally-enumerated
set) found that the slice-2 read-consumer list above was **incomplete**.
`mvcc.TupleVisible` has a subtransaction-aware twin, **`mvcc.TupleVisibleSubxact`**
(`mvcc/subxact_visibility.go`), and it is the visibility check the *main sequential
scan* uses (`operators_storage.go` `seqScanOp.Next`), along with FK enforcement
(`operators_fk.go`), `MERGE` (`operators_merge.go`), CTE-modifying scans, and the
DDL table rewrites (`TRUNCATE` / `DROP COLUMN` / `ALTER COLUMN TYPE`). It read
`h.Xmax` as a raw `TransactionID` in its non-lock-only arm — so a producer-stamped
`HEAP_XMAX_IS_MULTI && !LOCK_ONLY` xmax would have been silently misread as a
deleter xid on *every plain `SELECT`*. This is the highest-blast-radius consumer of
all, and it had been missed.

This slice makes `TupleVisibleSubxact` resolve the updater member through the
process-shared `*multixact.Store` (added as its last parameter, threaded from
`ctx.MultiXact` at all ~13 call sites) before the self / hint-bit / snapshot xmax
checks — structurally identical to `TupleVisible`'s multi arm, with the same
conservative directions (all-locker multi → visible; unresolvable multi → invisible).
The self-`xmin` short-circuit branch keeps the raw `h.Xmax` comparison, mirroring
`TupleVisible` exactly (both share the same unreachable-until-producer self-update
edge case, to be re-audited in both twins when the producer lands). The single-xid
and lock-only paths stay byte-identical, so every existing tuple is judged exactly
as before. Pinned by `TestTupleVisibleSubxactMultiXact` (mirrors
`TestTupleVisibleMultiXact`) plus the existing `TestTupleVisibleSubxactDegrades`.

### Hot-path wiring slice 2 (continued, 4): wait-on-deleter consumers (`scanRelForFKMatch` / `findInProgressConflictKey` / `stampAtPtr`)

The audit's remaining **two** consumers interpreted a non-lock-only xmax as a raw
xid and passed it to a single-`TransactionID` API (`IsXIDActive` / `WaitForXID`) —
a category error for a `MultiXactId` (a disjoint use of the same `uint32` space,
disambiguated only by the `HEAP_XMAX_IS_MULTI` infomask bit). Both, plus the
`stampAtPtr` recheck flagged by the audit, are now multixact-aware, completing the
gate. Two shared package-level resolvers were added in `operators_storage.go`
(beside `isConcurrentlyUpdated`, which already imports `multixact`/`mvcc`), so the
three call sites stay DRY and `operators_upsert.go` needs no new import:

- **`multixactUpdaterXID(mxs, xmax)`** → the multi's update-member xid, or
  `Invalid` when the store is nil / the multi is unknown / it carries only lockers.
- **`multixactFirstActiveMember(mxs, txnMgr, self, xmax)`** → the first *active,
  non-self* member of a (lock-only) multi, or `Invalid` — one live holder to wait
  on; the caller's re-probe loop drains the rest as members settle out of
  `IsXIDActive`.

Wiring:

- **`scanRelForFKMatch`** (`operators_fk.go`) — for an updater-bearing multi
  (`IS_MULTI && !LOCK_ONLY`) it resolves `effXmax = multixactUpdaterXID(…)` before
  the `IsXIDActive` / pending-record logic, and records `effXmax` (the updater xid,
  never the `MultiXactId`) in `fkPendingRef` so the downstream `WaitForXID` /
  `epqChainCheckMovedPartition` / `HasAborted` all see a real xid. An all-locker
  (or unresolvable) multi degrades to "clean match" — lockers do not delete the
  matched parent row, exactly like the lock-only-xmax fast path.
- **`findInProgressConflictKey`** (`operators_upsert.go`) — **Case 2**
  (visible-being-deleted) resolves the updater for a non-lock-only multi; **Case 3**
  (lock-only xmax) now also handles a lock-only *multi* via
  `multixactFirstActiveMember`, waiting on one live holder instead of feeding the
  raw `MultiXactId` to `IsXIDActive`. Case 3's lock-only-multi arm is not just
  future-proofing: the **cross-session lock-only producer already landed**
  (slice 1), so a tuple locked `FOR SHARE` by ≥2 sessions carries a lock-only multi
  that an `INSERT … ON CONFLICT` arbiter probe can observe today.
- **`stampAtPtr`** (`operators_lockrows.go`) — the "another real updater arrived
  while we waited" recheck is refactored into `anotherRealUpdaterArrived(h)`, which
  resolves the updater member for an updater-bearing multi rather than comparing the
  `MultiXactId` to our own XID. Behaviour is byte-identical for every
  currently-producible state (single updater, lock-only single, lock-only multi);
  only the updater-bearing-multi case (unreachable until the producer lands) changes.

Pinned by `TestMultixactUpdaterXIDHelper` and `TestMultixactFirstActiveMemberHelper`
(`concurrent_update_xmax_test.go`), plus the existing FK / upsert / lock-rows and
row-lock isolation suites (behaviour-identical for existing tuples).

### Producer gate: SATISFIED

With `isConcurrentlyUpdated`, `mvcc.TupleVisible`, `mvcc.TupleVisibleSubxact`,
`freeze`/`prune` (visibility consumers) **and** `scanRelForFKMatch`,
`findInProgressConflictKey`, `stampAtPtr` (wait-on-deleter consumers) all
multixact-aware, every `IsHeapTupleLockOnly`/raw-xmax consumer agrees on how to read
an updater-bearing multi. The producer-gate ([[pattern_sibling_paths_must_agree]]) is
**satisfied**, and the updater-bearing producer (slice 3, below) has now landed. The
lesson stands: enumerate consumers by *mechanically sweeping every
`IsHeapTupleLockOnly` site*, not from a hand-maintained list (that sweep found the
`TupleVisibleSubxact` and these two wait-on-deleter consumers the hand list missed).

### Hot-path wiring slice 3: updater-bearing producer (`stampLockInner` branch (a))

With the gate satisfied, the **updater-bearing producer** lands — the twin of the
slice-1 lock-only producer for the case where the combined member set contains an
**updater**. Branch (a) of `stampLockInner` (`operators_lockrows.go`) is the path
where our request is a shared lock (`FOR KEY SHARE`/`FOR SHARE` → goopg's collapsed
`ShrLock`) and the tuple carries a non-lock-only no-key updater xmax from another
transaction. `FOR KEY SHARE` does not conflict with a no-key update (`AccessShareLock`
vs. `ExclusiveLock`), so the branch previously **skipped** stamping (M0100-0005f) —
the single-holder xmax had nowhere to record the second holder. It now combines both:

- **Producer** (`stampMultiUpdaterLock`): enumerate the existing holders (a single
  updater xmax decoded via `updaterMemberStatus`, or an already updater-bearing multi
  resolved through `Store.Members`), apply the `MultiXactIdExpand` survivor filter
  (keep in-progress holders **plus a committed updater** — committed ≡ `!IsXIDActive
  && !HasAbortedXID`; drop dead pure lockers and an aborted updater), append our share
  locker (`lockMemberStatus`), `Store.CreateFromMembers`, compute `HintBits`, and stamp
  via `storage.PageSetHeapTupleXmaxMulti`. Because the set has an updater, `HintBits`
  **clears** `HEAP_XMAX_LOCK_ONLY`: the result is an **updater-bearing** multi. When no
  holder survives (e.g. the updater aborted) it returns "not formed" and the caller
  preserves the M0100-0005f skip.

Unlike slice 1, an updater-bearing multi is **not** transparent to visibility — its
xmax must be resolved to the updater before any single-xid reasoning. That is exactly
what the slice-2 consumers were made to do; this producer is what finally exercises
them. The last two `nil`-store read consumers (`vacuum.Analyze`'s live-row count and
`executor.analyzeRelationWith`'s stats-sampling scan) are now threaded with the real
`*multixact.Store` (`ctx.MultiXact` for the live SQL `ANALYZE`; the process-shared
store via the autovacuum `Launcher.MultiXact` field for the background path) so they
do not undercount a live, only-row-locked tuple as invisible.

The collapse of `FOR KEY SHARE`/`FOR SHARE` to one `ShrLock` strength means our member
is recorded as `StatusForShare` regardless; `HintBits` is unaffected (the updater's
strength dominates the lock-strength bits and neither share status reserves the key).
The faithful 4-way member status is deferred (resume point #3).

Pinned by `TestForShareJoinsInProgressUpdaterFormsMultiXact` (FOR SHARE meets an
in-progress no-key updater → updater-bearing multi `{updater@NoKeyUpdate,
locker@ForShare}`, `GetUpdateXid` resolves the updater) and
`TestForShareSkipsAbortedUpdaterNoMultiXact` (aborted updater → survivor filter drops
it → no multi). The full row-lock isolation suite (9 dedicated specs) stays green —
the producer is byte-identical to the prior skip for the rows that yield (it only
*additionally* records the multixact), so query output is unchanged.

### Hot-path wiring slice 4: four-way lock-strength member status

Slices 1–3 recorded every locker's member status as one of two collapsed
strengths (`ShrLock`/`ExclLock`), because goopg's planner mapped `FOR KEY SHARE →
FOR SHARE` and `FOR NO KEY UPDATE → FOR UPDATE` (`lockStrengthFromParser`). That
collapse is wrong for the MultiXact-cluster conflict matrix: a no-key UPDATE must
**not** conflict with a `FOR KEY SHARE` lock (`AccessShareLock` vs `ExclusiveLock`),
but it **does** conflict with `FOR SHARE` (`RowShareLock` vs `ExclusiveLock`). With
the collapse, a `FOR KEY SHARE` locker is recorded as `StatusForShare` and would
spuriously block a concurrent no-key update. This slice threads the full four-way
distinction from the parser through to the member status and conflict check:

- **Planner** (`lockStrengthFromParser`): maps each parser strength 1:1 to its
  planner counterpart, preserving all four. The only consumer of
  `LockedRel.Strength` is the `lockRowsOp` executor, so widening from two to four
  strengths is local (no other planner/analyzer/server site branches on it).
- **Executor `Open`** (`operators_lockrows.go`): the four-way switch resolves the
  tuple-lock infomask bits and a new `lockKeysUpdated` flag, mirroring
  `heap_lock_tuple`'s per-mode `new_infomask`/`new_infomask2`:
  `FOR KEY SHARE → HeapXmaxKeyShrLock`, `FOR SHARE → HeapXmaxShrLock`,
  `FOR NO KEY UPDATE → HeapXmaxExclLock`, `FOR UPDATE → HeapXmaxExclLock +
  HEAP_KEYS_UPDATED`. The single-holder stamp now also calls
  `storage.PageSetHeapTupleLockKeysUpdated` (new) to **set** the key-reserved bit
  for `FOR UPDATE` and **clear** any stale bit on the weaker strengths — without
  the clear, a `FOR UPDATE` lock followed by a `FOR NO KEY UPDATE` re-lock on the
  same line pointer would mis-decode.
- **Member status, both twins** (`lockMemberStatus` encode / `lockOnlyMemberStatus`
  decode, per [[pattern_sibling_paths_must_agree]]): four-way maps using
  `lockStrength`+`lockKeysUpdated` (encode) and `infomask`+`infomask2` (decode);
  `FOR UPDATE` is distinguishable from `FOR NO KEY UPDATE` only by
  `HEAP_KEYS_UPDATED`.
- **Conflict check** (`tupleLockConflicts`): replaced the hand-rolled two-way test
  with a call into `multixact.StatusesConflict` — the verbatim upstream row-lock
  compatibility matrix — decoding the held strength via `lockOnlyMemberStatus`. The
  full four-way semantics now hold (e.g. KEY SHARE vs NO KEY UPDATE = no conflict).

Pinned by `TestLockMemberStatusFourWay` / `TestLockOnlyMemberStatusFourWay` (the
encode/decode twins) and a rewritten `TestTupleLockConflicts` (the full 4×4 matrix
+ the no-holder case), plus storage's `TestPageSetHeapTupleLockKeysUpdated` (set +
clear round-trip). The seven dedicated row-lock isolation specs (`nowait`,
`nowait-3`, `skip-locked`, `update-locked-tuple`, `lock-committed-update`,
`lock-committed-keyupdate`, and the multixact producer specs) stay green — those
exercise `FOR UPDATE`/`FOR SHARE` whose conflict outcome is unchanged by the
refinement; only the previously-impossible KEY SHARE-vs-no-key-update distinction
is now correct.

This is the member-status half of resume point #3. The remaining halves —
**savepoint/subxact members** (a transaction's main xid + a subxid locking the
same tuple form a multi) and the lockmgr advisory tuple-lock 4-way mode — are
still deferred. Measuring `lock-update-traversal` after this slice confirmed its
blocker is **not** the member status (now correct) but **update-chain lock
propagation**: `SELECT ... FOR KEY SHARE` on a row updated in-flight forms the
multi on the version the locker sees but does not propagate the lock forward to
the successor version, so a later `DELETE`/key-`UPDATE` of the live version does
not wait. That propagation (+ the plain-`UPDATE`/`DELETE` write-path lock-conflict
wait) is the next slice toward spec promotion.

## Verification

`internal/multixact/multixact_test.go` (9 test functions, all pure/deterministic):

- `TestStatusesConflictMatrix` checks the full 6×6 relation against an
  **independently-written** expected table (`expectedConflict`), derived from
  first principles rather than re-using the implementation's lock tables — a
  test that re-derives the answer from the tables it checks proves nothing.
- `TestStatusesConflictSymmetric` guards against an asymmetric `LockConflicts[]`
  transcription error.
- `TestMembersConflict` covers empty/compatible/conflicting sets, self-skip
  (lock upgrade), and the `isCurrent` liveness filter.
- `TestGetUpdateXid` / `TestHasLockers` cover the member-selection helpers.
- `TestConstantsMatchUpstream` / `TestStatusIsUpdate` / `TestStatusValid` /
  `TestStatusString` pin the on-disk numeric encoding and predicates.

The port was cross-checked verbatim against the live upstream tree this loop:
`MultiXactStatusLock[]` and `tupleLockExtraInfo[].hwlock`
(`src/backend/access/heap/heapam.c:205` and `:132`) and `LockConflicts[]`
(`src/backend/storage/lmgr/lock.c:65`).

`internal/multixact/store_test.go` (member store, all pure/deterministic, runs
under `-race`):

- allocator starts at `FirstMultiXactId` and never hands out
  `InvalidMultiXactId` (`NewStoreAt(0)` clamps up);
- `Members` round-trips a set canonically sorted and returns an independent
  copy (mutating it cannot corrupt the store), and reports `ok=false` for an
  unknown id;
- identical sets (in any argument order) re-use one id without advancing the
  allocator; sets differing in a single member's status get distinct ids;
- `Create` rejects an identical member pair; `CreateFromMembers` rejects
  >1 updater (`ErrMultipleUpdaters`), an empty set, an invalid xid, an invalid
  status, and does not mutate the caller's slice;
- `Expand` mints a new id leaving the original immutable, returns the original
  unchanged when re-adding an existing member, drops a finished pure locker but
  keeps a committed updater, drops an aborted updater, creates a singleton for
  an obsolete id, keeps every member under a zero `Liveness{}`, and rejects
  `InvalidMultiXactId`/invalid-xid input;
- `TestStoreConcurrentCreate` fans 16×64 distinct creates and asserts the
  allocator advanced by exactly the unique-set count (no lost/duplicated ids).

`internal/multixact/multixact_test.go` (tuple-header encoder):

- `TestHintBits` pins `HintBits` for each single-member status and the
  multi-member cases against an **independently-derived** expected
  `(infomask, infomask2)` table (its bit literals cross-checked against the
  `storage` constants, not re-using the implementation's bit-or expressions),
  then round-trips every case through the decode predicates to prove the
  sibling pair agrees — `IsHeapTupleXmaxMulti` always true,
  `IsHeapTupleLockOnly` true exactly when `GetUpdateXid` finds no updater.
- `TestHintBitsStrongestWins` proves the strongest-lock selection is
  order-independent (a max, not last-wins).

## Deferred (resume points)

The risky hot-path integration lands in later loops, each on top of this
verified primitive:

1. **xmax encoding — lock-only case: ✅ LANDED** (see *Hot-path wiring slice 1*
   above). The member store, tuple-header representation, **and** the
   cross-session lock-only producer (`stampMultiLock`) + consumer
   (`activeLockHolders`) in `stampLockInner` are all live, with the `Store`
   threaded through `server.Config`/`executor.Context`.
2. **updater-bearing multixact** — the case where a member is an *updater*
   (`HEAP_XMAX_LOCK_ONLY` clear). The read side is landing first
   ([[pattern_sibling_paths_must_agree]]); status:
   - ✅ `isConcurrentlyUpdated` (the executor write-conflict / EPQ consumer) is
     multixact-aware — see *Hot-path wiring slice 2* above.
   - ✅ `mvcc.TupleVisible` (the snapshot visibility consumer) is multixact-aware —
     the `Store` is threaded through every production call site (executor
     scans / index / index-only / upsert / toast + `followHOTChain` helpers pass
     `ctx.MultiXact`; vacuum/analyze stats scans pass `nil` until the producer
     slice threads them). See *Hot-path wiring slice 2 (continued)* above.
   - ✅ `storage` vacuum read consumers (`freeze.go` / `prune.go`; `vm.go`
     conservative, no change) are multixact-aware via the `storage.ResolveMultiUpdater`
     hook wired from `cmd/goopg/main.go` — see *Hot-path wiring slice 2 (continued, 2)*
     above.
   - ✅ `mvcc.TupleVisibleSubxact` (the **main sequential-scan** / FK / `MERGE` / DDL
     visibility twin of `TupleVisible`) is multixact-aware — see *Hot-path wiring
     slice 2 (continued, 3)* above. A producer-gate audit found this had been missed
     from the original read-consumer list; it is the highest-blast-radius consumer
     (every plain `SELECT`).
   - ✅ `scanRelForFKMatch` (`operators_fk.go`) — FK wait-on-deleter resolves the
     updater member (`multixactUpdaterXID`) and records the real updater xid in
     `fkPendingRef`; all-locker/unresolvable multis degrade to a clean match. See
     *Hot-path wiring slice 2 (continued, 4)* above.
   - ✅ `findInProgressConflictKey` (`operators_upsert.go`) — Case 2 resolves the
     updater; Case 3 waits on one live holder of a lock-only multi
     (`multixactFirstActiveMember`). See *slice 2 (continued, 4)* above.
   - ✅ `stampAtPtr` (`operators_lockrows.go`) — the "another real updater arrived"
     recheck (`anotherRealUpdaterArrived`) resolves the updater member for an
     updater-bearing multi. See *slice 2 (continued, 4)* above.
   - ✅ **producer**: branch (a) of `stampLockInner` (`operators_lockrows.go`,
     `stampMultiUpdaterLock`) combines a new share locker with the in-progress/
     committed no-key updater into a **non**-lock-only MultiXactId — see *Hot-path
     wiring slice 3* above. The real `Store` is now threaded through the two
     `vacuum.Analyze` / `analyzeRelationWith` read consumers that previously passed
     `nil`. Re-ran executor/storage/mvcc/btree + the 9 dedicated isolation row-lock
     specs (all green) ([[pattern_sibling_paths_must_agree]]).
3. **4-way lock strength + savepoint/subxact members** — status:
   - ✅ **member status**: the full `FOR KEY SHARE` / `FOR SHARE` /
     `FOR NO KEY UPDATE` / `FOR UPDATE` distinction is threaded from the parser
     through to the producer member status and the `tupleLockConflicts` matrix
     (now `multixact.StatusesConflict`) — see *Hot-path wiring slice 4* above.
   - ⛔ **savepoint/subxact members** — a transaction's main xid plus a subxid
     locking the same tuple must appear as distinct multixact members (the
     `tuplelock-conflict` savepoint permutations); needs the subtransaction xid
     threaded into the producer.
   - ⛔ **update-chain lock propagation** — `SELECT ... FOR KEY SHARE` on an
     in-flight-updated row must propagate the lock forward to the successor
     version so a later `DELETE`/key-`UPDATE` of the live version waits
     (`lock-update-traversal`/`lock-update-delete`/`propagate-lock-delete`). This
     also needs the plain-`UPDATE`/`DELETE` write path to honour a lock-only
     holder. Measured as the sole remaining `lock-update-traversal` blocker after
     slice 4.
4. **WAL persistence of multixact members** — the lock-only producer marks the
   page dirty *without* a heap-lock WAL record (the record carries a single
   xid + strength and cannot describe a multi). Lock-only multixact state is
   transient, so losing it on crash recovery is correct; but the
   updater-bearing case and `pg_multixact` SLRU parity need a real record +
   `Store` seeding from `pg_control.nextMulti`.
5. **Spec promotion** — once 2–4 land as needed, promote the MultiXact-cluster
   isolation specs (`lock-update-traversal`, `multixact-no-forget`, `nowait-2`,
   `skip-locked-2`, `propagate-lock-delete`, `tuplelock-conflict`,
   `aborted-keyrevoke`) per the [0118-0001](0118-0001-isolation-spec-port-strategy.md)
   promotion workflow. (`multixact-no-deadlock` / `tuplelock-upgrade-no-deadlock`
   additionally need deadlock detection across the row-lock wait graph.)

Sibling-path discipline applies throughout: the encode (stamp) and decode
twins must change together — see [[pattern_sibling_paths_must_agree]].
