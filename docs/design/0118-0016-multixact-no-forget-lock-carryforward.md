# 0118-0016 — Don't-forget-the-lock: conflict-filtered wait + lock carry-forward on UPDATE (M0118-0009 slice: multixact-no-forget)

**Status:** accepted
**Spec:** `postgres/src/test/isolation/specs/multixact-no-forget.spec`
**Test:** `TestPort_IsolationMultixactNoForget` (`internal/testport/isolation_port_test.go`)
**Result:** all 9 permutations byte-identical vs PostgreSQL 18.3.

## Problem

The spec's invariant (its name): *if transaction A holds a row lock and transaction
B updates the same row, do not forget A's lock — whether B aborts or commits.*

- s1 takes `FOR KEY SHARE` on the single row.
- s2 issues a no-key `UPDATE` (value-only, HOT-eligible). goopg's UPDATE producer
  ([[0118-0011]]) preserves s1's KEY SHARE into a `{s1-keyshare + s2-no-key-update}`
  MultiXact on the old tuple.
- s2 then either **aborts** or **commits**.
- s3 probes with `FOR KEY SHARE` (compatible with s1), `FOR NO KEY UPDATE`
  (compatible with KEY SHARE), or `FOR UPDATE` (conflicts with KEY SHARE).

Two permutations diverged.

### Divergence 1 — over-conflict after ABORT (perm `s2_abort … s3_fornokeyupd`)

After s2 aborts, the row's xmax is the multi `{s1-keyshare (active), s2-no-key-update
(aborted)}`. s3 `FOR NO KEY UPDATE` is **compatible** with s1's KEY SHARE, so PG lets
it proceed immediately. goopg instead made s3 **wait** until s1 committed.

Root cause: `lockRowsOp.stampLockInner`'s key-conflict branch resolves the real
updater (s2, aborted) and then waits on every *other* still-active multi member via
`activeLockHolders` — **without checking whether that member's mode conflicts**. So it
blocked on s1's KEY SHARE even though FOR NO KEY UPDATE does not conflict with it.

### Divergence 2 — under-conflict after COMMIT (perm `s2_commit … s3_forupd`)

After s2 commits, the live row is the **new** tuple version (value 2). In PG,
`heap_update` carries A's non-conflicting lockers *forward* onto the new version
(`compute_new_xmax_infomask`), so the new tuple's xmax is `{s1-keyshare}`. s3
`FOR UPDATE` then conflicts with that inherited KEY SHARE and **waits** until s1
commits. goopg inserted the new tuple with **no inherited xmax**, so s3 saw no holder
and proceeded immediately — the lock was forgotten across the version boundary.

## Fix

Both halves live behind the same narrow gate as the existing producer (a pre-existing
active lock-only foreign holder), so the pgbench / TPC hot path is untouched.

### Fix 1 — conflict-filter the multi-member wait set (`operators_lockrows.go`)

In `stampLockInner`'s real-updater branch, the non-updater co-member wait set is now
built from `conflictingLockHolders` (MultiXactIdWait semantics — only members whose
status conflicts with our request) instead of `activeLockHolders` (every active
member). The updater itself is excluded (it is resolved separately as `xmax`).

- s3 `FOR NO KEY UPDATE` after abort: the KEY SHARE member is non-conflicting →
  empty wait set → s3 reaches the "updater rolled back" arm and stamps immediately.
- s3 `FOR UPDATE` after abort: the KEY SHARE member *conflicts* → still waited on,
  preserving the existing `tuplelock-upgrade-no-deadlock` perms 2/3 behaviour.

`activeLockHolders` had no other caller and was removed.

To honour the spec's own name, `stampAtPtr` (the "updater rolled back, row live at
original ptr" stamp) now first tries `stampMultiLock`: it keeps every still-active
member (dropping the aborted updater) and adds our locker, so s1's KEY SHARE survives
into the new multi rather than being overwritten by a single-locker stamp. It falls
back to the plain single-locker stamp when there is no survivor (e.g. a lone aborted
updater, as in `delete-abort-savept`).

### Fix 2 — carry non-conflicting lockers forward onto the new tuple (`operators_storage.go`)

The locker-retention logic shared by the old-tuple stamp is extracted into
`survivingLockersForUpdate(ctx, hdr, keysUpdated)` — the still-relevant lock-only
holders to preserve, *excluding* our own updater member (drop dead foreign lockers and
conflicting foreign lockers; keep non-conflicting foreign lockers and outer-level self
lockers). `stampUpdaterXmaxPreservingLockers` now calls it and re-adds our updater;
the new helper `carryForwardLockersToNewTuple` calls it and stamps the **new** tuple
version's xmax as a lock-only MultiXact of exactly those carried lockers.

`tryApplyHOTUpdate` carries the lockers forward onto the new HOT tuple *before*
re-stamping the old tuple's xmax (the stamp rewrites it). A HOT update never changes a
key column, so the updater status is no-key and a FOR KEY SHARE holder is preserved.

After s2 commits, the new tuple carries `{s1-keyshare}`; s3 `FOR UPDATE` finds the
inherited KEY SHARE and waits until s1 commits — matching PG.

## Scope / crash safety

- Lock-only MultiXact membership is process-shared in-memory state, not persisted
  through the single-xid heap-lock / HOT-update WAL record (same caveat as
  `stampMultiLock` / `stampUpdaterXmaxPreservingLockers`). The carried lockers'
  transactions do not survive a crash, so losing the membership on recovery is
  correct; MultiXact WAL persistence remains deferred ([[0118-0002]]).
- The carry-forward is wired on the **HOT** update path — the spec's UPDATE (value
  column, no index) is HOT in goopg. The non-HOT delete+insert / `UPDATE…FROM` /
  MERGE / upsert paths still drop the inherited lock onto a fresh successor; this is
  the same narrow gate and is a bounded completeness follow-up (deferral ledger
  2026-06-22), not exercised by this spec or any currently-`port` spec.
- Strict no-op when there is no pre-existing foreign lock-only holder (the common
  case) — `survivingLockersForUpdate` returns empty and both producers keep the plain
  single-xid stamp.

## Oracle

Mirrors `heap_lock_tuple` (`MultiXactIdWait` waits only on conflicting members) and
`heap_update` (`compute_new_xmax_infomask` propagates non-conflicting lockers onto the
new tuple version) in `postgres/src/backend/access/heap/heapam.c`.

## Verification

- `TestPort_IsolationMultixactNoForget` — 9/9 permutations PASS.
- No regression: `TestPort_Isolation{DeleteAbortSavept,DeleteAbortSavept2,
  AbortedKeyrevoke,TuplelockUpgradeNoDeadlock,MultixactNoDeadlock,LockUpdateDelete,
  LockUpdateTraversal,LockCommittedUpdate,LockCommittedKeyupdate,PropagateLockDelete,
  UpdateLockedTuple}` PASS.
- `-race` `internal/executor` + `internal/multixact` + `internal/mvcc` PASS;
  `internal/executor` + `internal/storage` unit PASS (producer unit tests included).
- CI-parity pgbench smoke (TPC-B + `-N` + `-S`) 0 failed.

## Deferred (ledger 2026-06-22)

Lock carry-forward on the non-HOT update paths (delete+insert / `UPDATE…FROM` /
MERGE / upsert) and the remaining M0118-0009 misc specs.
