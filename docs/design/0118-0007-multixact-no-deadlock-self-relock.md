# 0118-0007 — Multixact no-deadlock: re-acquiring a self-held row lock (M0118-0004 slice: multixact-no-deadlock)

Status: accepted

## Problem

The `multixact-no-deadlock` isolation spec describes a row-lock scenario that
PostgreSQL must resolve **without deadlocking**:

1. `s1` runs `SELECT * FROM justthis FOR SHARE` — stamps a single-locker SHARE
   `xmax` on the row.
2. `s2` runs `SELECT * FROM justthis FOR SHARE` — joins the SHARE lock, so the
   row's `xmax` becomes a **multixact** with members `{s1, s2}` (both SHARE).
3. `s1` takes a savepoint (`s1svpt`).
4. `s3` runs `SELECT * FROM justthis FOR UPDATE` — `FOR UPDATE` conflicts with the
   SHARE multixact, so `s3` **waits** behind both members (`<waiting ...>`).
5. `s1` runs `SELECT * FROM justthis FOR SHARE` again (`s1lock2`) — re-requesting a
   lock it **already holds** while a conflicting `FOR UPDATE` (`s3`) is parked.

The hazard the spec guards against: if `s1`'s re-request were forced to queue
behind the already-waiting `s3`, the system would deadlock — `s3` waits for `s1`
to release its SHARE, while `s1` waits behind `s3`. PostgreSQL's tuple-lock
algorithm specifically avoids this: a transaction that already holds a lock of the
requested (or stronger) strength on a row never waits to "re-take" it, regardless
of who else is queued. So `s1lock2` returns immediately; only after `s2` and `s1`
commit does `s3`'s `FOR UPDATE` complete.

This is the comment at the top of the upstream spec verbatim: *"If we already
hold a lock of a given strength, do not deadlock when some other transaction is
waiting for a conflicting lock and we try to acquire the same lock we already
held."*

The wait here lives entirely on the **row-lock `xmax`/multixact path**
(`WaitForXID` on the active multixact members), **not** on the heavyweight
`lockmgr` wait-for graph. The general/soft deadlock detector
([0118-0005](0118-0005-general-deadlock-timeout-detection.md),
[0118-0006](0118-0006-soft-deadlock-wait-queue-reordering.md)) is never consulted
for this scenario — and must not need to be, because the correct outcome is "no
wait at all", decided synchronously at lock-acquisition time.

## Change

**No new engine work.** goopg's existing tuple-lock acquisition path already
produces the correct (and byte-for-byte identical) behavior. This slice adds the
port test and promotes the spec, documenting *why* the invariant already holds so
the no-deadlock guarantee is regression-protected.

The relevant guards in `internal/executor/operators_lockrows.go` and
`internal/multixact/multixact.go`:

- **Conflict gate first** (`stampLockInner`, lock-only `xmax` branch): the wait
  branch is entered only when the new request actually *conflicts* with the
  existing tuple lock (`tupleLockConflicts(...)`). For `s1lock2` the request is
  `FOR SHARE` and the existing lock is a SHARE multixact — `FOR SHARE` is
  self-compatible, so `tupleLockConflicts` is **false** and the entire
  wait/NOWAIT/SKIP-LOCKED branch is skipped. `s1` falls straight through to
  re-stamp.

- **Self never counts as a blocker** (`activeLockHolders`): when extracting the
  set of transactions a request would have to wait on, the current backend is
  filtered out (`m.Xid != o.ctx.Tx.XID`), and so are any already-finished
  members (`IsXIDActive`). A backend can therefore never generate a wait edge
  against its own held lock.

- **Self-skip when re-forming the multixact** (`stampMultiLock`): when combining
  the existing members with the new request, the current backend's prior member
  is dropped (`if m.Xid == o.ctx.Tx.XID { continue }`) and re-appended at the
  current strength, so re-acquisition never self-conflicts.

- **Pure conflict core** (`multixact.MembersConflict`, port of
  `DoesMultiXactIdConflict`): `if m.Xid == reqXid { continue }` — a transaction
  never conflicts with itself when testing a request against multixact members.

Crucially, `s3`'s `FOR UPDATE` is parked *outside* the tuple's `xmax` (it is a
waiter, not yet stamped onto the row), so when `s1` re-requests SHARE the only
members it sees are `{s1, s2}` — both SHARE, both compatible — and there is
nothing to wait for. The "do not queue behind a conflicting waiter for a lock I
already hold" property thus falls out of the conflict-gate-before-wait ordering
plus the consistent self-filtering, with no detector involvement.

## Scope

This slice covers only the self-relock no-deadlock invariant on the row-lock
path. The remaining M0118-0004 deadlock specs stay future work:

- `tuplelock-upgrade-no-deadlock` — multi-session row-lock *upgrade* ordering
  (also row-lock path, but exercises the retry-after-avoiding-deadlock algorithm
  across many permutations).
- `deadlock-parallel` — requires a parallel-query **lock-group** abstraction
  (goopg has no lock groups; the soft/hard wait-for graph treats each backend as
  its own leader).

## Verification

- `TestPort_IsolationMultixactNoDeadlock` PASS byte-for-byte vs PG 18.3.
- Regression: `TestPort_IsolationDeadlockHard` / `DeadlockSimple` /
  `DeadlockSoft{,2}` / `LockNowait` / `TuplelockUpdate` still PASS.
- `go build ./...`, `gofmt`, `go vet` clean; `-race ./internal/lockmgr/...` green.
- CSV `failed` → `pass`; coverage/inventory regenerated (isolation pass 56 → 57).

## Oracle

- Spec: `postgres/src/test/isolation/specs/multixact-no-deadlock.spec`
- Expected: `postgres/src/test/isolation/expected/multixact-no-deadlock.out`
- Mirrored logic: `src/backend/access/heap/heapam.c` (`heap_lock_tuple`
  already-holds short-circuit) and `src/backend/access/transam/multixact.c`
  (`DoesMultiXactIdConflict`).
