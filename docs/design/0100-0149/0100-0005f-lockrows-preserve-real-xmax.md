# 0100-0005f — `stampLock` must preserve a real updater's xmax (no MultiXact)

**Milestone:** M0100-0005 (RC isolation suite — 21-spec pass)
**Date:** 2026-05-15
**Status:** landed

## Summary

`SELECT … FOR KEY SHARE / FOR SHARE / FOR UPDATE` writes a lock-only
xmax stamp on the heap tuple via `lockRowsOp.stampLock` →
`storage.PageSetHeapTupleLockOnly`. The primitive unconditionally
overwrites the existing `t_xmax` field and sets the
`HEAP_XMAX_LOCK_ONLY` bit. When the row already carries a real
(non-lock-only) xmax stamped by an in-flight UPDATE on a different
session, this overwrite **erases the deletion marker**.  After the
updater commits, `mvcc.TupleVisible`'s lock-only short-circuit
treats the (now lock-only) xmax as no-xmax and reports the dead old
version as visible — alongside the new heap version — so a
subsequent `SELECT *` returns two rows where one is correct.

This is the residual gap captured by the `lock-committed-update`
spec after M0100-0005e closed the page-RLock deadlock.  The bug
surfaces as the `s1hint` permutations:

```
step s1hint: SELECT * FROM lcu_table;
 id|value
 --+-----
+1|one          ← old version, dead but visible
 1|two
-(1 row)
+(2 rows)
```

## Root cause

Upstream PostgreSQL represents "row has a committed/in-flight updater
**and** concurrent KEY SHARE / SHARE lock holders" via MultiXact:
`t_xmax` becomes a multixact id whose members include the updater's
xid plus every lock holder, and the LOCK_ONLY bit stays clear.
Visibility code then resolves the multixact to find the actual
deleter.

goopg v0 has no MultiXact infrastructure (deferred under M0100 scope).
`PageSetHeapTupleLockOnly` therefore has only one slot for `t_xmax` and
naively writes the lock holder's xid + LOCK_ONLY bit, regardless of
prior contents.

## Fix

Localised to the executor: `lockRowsOp.stampLock`
(`internal/executor/operators_lockrows.go`) reads the tuple header
under the page exclusive lock before calling
`PageSetHeapTupleLockOnly`.  If the existing `t_xmax`:

  - is non-zero,
  - lacks the `HEAP_XMAX_LOCK_ONLY` bit, **and**
  - belongs to a different transaction,

then the page-level stamp + `markHeapLockDirty` WAL emit are
**skipped**.  The lockmgr tuple-tag lock acquired earlier in
`stampLock` (via `acquireTupleLock`) is retained for the lifetime of
the locking transaction, so row-level locking semantics for our holder
still hold; we simply forgo the on-page advertisement of the lock.

The page primitive `PageSetHeapTupleLockOnly` is left untouched — it
remains the unconditional low-level operation; the policy decision
sits at the executor caller.

## Trade-offs accepted

1. **Lock advertisement loss on the page.** A third session that
   inspects the heap tuple for foreign-lock detection
   (`foreignLockOnly`) sees only the original updater's xid (real,
   non-lock-only).  It therefore treats the row as concurrently
   updated and uses the EPQ retry / wait path against the updater —
   not the lock-holder.  Once the updater commits, the third
   session's UPDATE follows the HOT chain to the new version, where
   no page-level lock is recorded for s2 either.  This is more
   permissive than upstream (which would block the third session on
   s2's KEY SHARE via the multixact resolution).  Acceptable for v0
   because:

   - the only correctness invariant we must protect is **MVCC
     visibility** (the dead version must not reappear), and that is
     preserved by leaving the original updater's xmax in place;
   - the spec-driven scope of M0100-0005 does not cover three-session
     KEY SHARE × UPDATE × UPDATE schedules; full MultiXact will land
     when those specs are queued.

2. **No WAL record for the skipped stamp.** Standby replay sees no
   lock-only entry for s2's KEY SHARE — but standby is read-only and
   does not consult tuple-level lock state, so the missing entry is
   inert there.

## Why not modify `PageSetHeapTupleLockOnly` itself?

Two reasons:

  - the storage primitive should remain a "do what you're told"
    operation; visibility/locking policy belongs in `lockRowsOp`;
  - existing call sites (only `stampLock` today) are exhaustive — the
    sentinel-error pattern would propagate complexity without
    benefit.

## Regression pins

  - `internal/server/lockrows_preserve_real_xmax_test.go::TestForKeyShare_PreservesRealUpdaterXmax`
    — two sessions exercise s1.UPDATE → s2.FOR KEY SHARE → s1.COMMIT
    → fresh-connection `SELECT *` must return exactly one row.
    Verified to **fail** (`expected 1 row after commit, got 2`)
    without the fix and **pass** with it.
  - `internal/testport/isolation_port_test.go::TestPort_IsolationLockCommittedUpdate`
    — full upstream `lock-committed-update.spec` (24 permutations)
    now matches the expected output exactly; previously deferred with
    3-line diff (one extra dead `1|one` per s1hint permutation).

## Verification

  - `go test -race ./internal/executor/ ./internal/storage/
    ./internal/server/ ./internal/mvcc/` — PASS post-fix
    (no regressions in concurrent_update_xmax, hot_update,
    operators_lockrows, storage_dml, mvcc visibility).
  - `go test -count=1 -timeout 60s -run
    TestPort_IsolationLockCommittedUpdate ./internal/testport/` — PASS.

## Files touched

  - `internal/executor/operators_lockrows.go` — `stampLock` body.
  - `internal/server/lockrows_preserve_real_xmax_test.go` — new
    regression test.
  - `docs/design/0100-0005f-lockrows-preserve-real-xmax.md` — this
    document.
  - `docs/design/README.md` — index entry.

## Follow-ups (out of scope for this loop)

  - Full MultiXact infrastructure (own xid for combined updater +
    lock holders) — required before three-session lock × update
    schedules can land.  Tracked under M0100 follow-on items.
