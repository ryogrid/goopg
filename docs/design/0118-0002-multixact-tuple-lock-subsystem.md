# MultiXact tuple-lock subsystem (engine-first slice) — M0118-0003/0004

- Status: accepted (partial — engine core + member store + tuple-header
  hint-bit encoder + **cross-session lock-only multixact producer/consumer
  wired into `stampLockInner`**; updater-bearing multixact + WAL persistence +
  spec promotion deferred)
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

**Still deferred on the read side** (tracked in resume point #2): the *snapshot*
visibility consumer `mvcc.TupleVisible` (8 production call sites would need the
`Store` threaded through their signatures) and `storage/freeze.go` (package
`storage` cannot import `multixact` — that is an import cycle — so it needs a
resolver callback injected at init). These plus the producer are gated together:
the updater-bearing producer (branch (a) of `stampLockInner`) **must not** land
until `TupleVisible` and `freeze.go` are also multixact-aware.

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
   - ⛔ `mvcc.TupleVisible` (the snapshot visibility consumer) — must resolve the
     updater xid for a non-lock-only multi; needs the `Store` threaded through
     its 8 production call sites (executor scans, vacuum, analyze, toast).
   - ⛔ `storage/freeze.go` — must resolve the updater before freezing; package
     `storage` cannot import `multixact` (cycle), so inject a resolver callback
     (set at init, like other storage hooks) rather than a direct import.
   - ⛔ **producer**: branch (a) of `stampLockInner` (`operators_lockrows.go`, the
     "non-key update + `FOR KEY SHARE`" path that currently skips) must *combine*
     a new locker with the in-progress updater into a multixact and stamp a
     **non**-lock-only multi. **GATE: do not land the producer until both
     remaining consumers above are multixact-aware** — unlike the lock-only case
     this is **not** transparent to visibility, so producer + *all* visibility
     consumers must agree ([[pattern_sibling_paths_must_agree]]).
3. **4-way lock strength + savepoint/subxact members** — goopg's `lockStrength`
   collapses PG's four lock modes to two (`ShrLock`/`ExclLock`); faithful
   `tuplelock-conflict`/`tuplelock-upgrade-no-deadlock` parity needs the full
   `FOR KEY SHARE` / `FOR SHARE` / `FOR NO KEY UPDATE` / `FOR UPDATE`
   distinction threaded into the member status, plus subtransaction xids as
   distinct multixact members (the savepoint permutations).
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
