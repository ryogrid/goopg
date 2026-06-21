# MultiXact tuple-lock subsystem (engine-first slice) — M0118-0003/0004

- Status: accepted (partial — engine core only; hot-path wiring deferred)
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

## Deferred (resume points)

The risky hot-path integration lands in later loops, each on top of this
verified primitive:

1. **xmax encoding** — represent a MultiXactId in the tuple header
   (`HEAP_XMAX_IS_MULTI`) and a member store (in-memory analog of the multixact
   SLRU). Keystone for everything below.
2. **`stampLockInner` member combination** — teach branch (a)
   (`operators_lockrows.go`) to *combine* a new locker with an in-progress
   updater into a multixact instead of skipping.
3. **`isConcurrentlyUpdated` multixact decode** — make it resolve a multixact
   xmax to its update member via `GetUpdateXid` instead of returning `false`
   for any lock-only xmax.
4. **Spec promotion** — once 1–3 land, promote the MultiXact-cluster isolation
   specs (`lock-update-traversal`, `multixact-no-forget`, `nowait-2`,
   `skip-locked-2`, `propagate-lock-delete`, `tuplelock-conflict`,
   `aborted-keyrevoke`) per the [0118-0001](0118-0001-isolation-spec-port-strategy.md)
   promotion workflow.

Sibling-path discipline applies throughout: the encode (stamp) and decode
(`isConcurrentlyUpdated`) twins must change together — see
[[pattern_sibling_paths_must_agree]].
