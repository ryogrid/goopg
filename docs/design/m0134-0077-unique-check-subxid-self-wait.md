# M0134-0077 — Unique check self-deadlocks on its own sub-XID

**Status:** design (implemented in this loop)
**Case:** `transactions.sql` (regress-sql, status `failed`)
**Scope:** `internal/executor/operators_storage.go`

## Problem

`scripts/pg-regress-runner.sh --verbose transactions` **hangs** deterministically
(2/2 clean datadirs) at file line 344:

```sql
BEGIN;
INSERT INTO koju VALUES (1);
SAVEPOINT x;
INSERT INTO koju VALUES (1);   -- duplicate key, same subtransaction
```

The second INSERT's uniqueness probe blocks forever on the first INSERT's tuple
instead of raising `23505 duplicate key value violates unique constraint
"koju_a_key"`. The hang truncates the case at 530/1101 normalized lines (48%):
the entire 607-line post-hang region is unreachable until this is fixed.

## Root cause

Tuple `xmin` is stamped with the session's **sub-XID** inside a savepoint
(`effectiveWriterXID` → `BasicSession.EffectiveWriterXID`, `session.go:721`,
returns `currentSubXid`), but the statement context's `ctx.Tx.XID` is rebuilt
per statement from the **top-level** transaction (`dispatch.go:316`).

`uniqueCheckWithWait` (`operators_storage.go:8636-8643`) classifies an xmin as
"in-flight other-xact" when it is active and `xmin != ctx.Tx.XID`:

```go
selfXID := ctx.Tx.XID
if ctx.TxnMgr != nil && xmin != storage.InvalidTransactionID &&
    (selfXID == storage.InvalidTransactionID || xmin != selfXID) &&
    ctx.TxnMgr.IsXIDActive(xmin) {
    inflightXmin = xmin   // → WaitForXID on our own sub-XID → permanent self-deadlock
    return false, nil
}
```

The sub-XID is active and `subXid != topLevelXID`, so the branch fires and
`WaitForXID` blocks on goopg's own sub-XID — which never commits or aborts
because the very statement waiting on it is the one holding it. A single
multi-statement `psql -c` does NOT hang only because `execSavepoint` sets
`ctx.Tx.XID = subXid` (`operators_tx.go:536`) and no per-statement rebuild
intervenes; the regress runner drives separate Query messages, so the hang is
the real gate behavior.

## Fix

Introduce a `xidIsSelf(ctx, xid)` free helper mirroring the existing
`lockRowsOp.isSelfXID` (`operators_lockrows.go:1892-1903`) and PG's
`TransactionIdIsCurrentTransactionId` (`xact.c:941`, which returns true for the
top-level xact *and* any of its sub-xacts):

- `xid == ctx.Tx.XID` → self;
- else resolve both to their top-level ancestor via
  `ctx.TxnMgr.TopLevelXid` and compare — a sub-XID of the current top-level
  xact resolves to the same ancestor, so it is self.

Apply the helper to both sibling self-classification sites:

1. **`uniqueCheckWithWait`** (`operators_storage.go:8636-8643`) — replace the
   `xmin != selfXID` / `selfXID == Invalid` predicate with `!xidIsSelf(ctx, xmin)`
   so a self sub-XID no longer enters the wait branch.
2. **`isLiveForUniqueCheck`** (`operators_storage.go:8816`) — replace the
   `selfXID != Invalid && xmin == selfXID` arm with `xidIsSelf(ctx, xmin)`, so
   a self sub-XID is classified live even when the subtransaction is no longer
   active (e.g. after `RELEASE SAVEPOINT`).

Both are free functions taking `*Context`, so the helper is file-local; no
receiver surgery needed. The `operators_upsert.go` callers (`:699`, `:715`,
`:1421`) call `isLiveForUniqueCheck` and are fixed transitively.

## Acceptance criteria

1. `scripts/pg-regress-runner.sh --verbose transactions` no longer hangs at
   file line 344; the `koju` block's two `duplicate key … koju_a_key` ERROR
   lines appear and the run reaches the (previously unreachable) post-hang
   region.
2. A regression test starting from a **clean** datadir: duplicate-key INSERT
   inside a `SAVEPOINT` raises 23505 (not a hang, not a wait) — exercised via a
   bounded psql invocation against a throwaway capped server.

## PG oracle

- `src/backend/access/transam/xact.c:TransactionIdIsCurrentTransactionId` (L941)
- `src/backend/access/nbtree/nbtinsert.c:_bt_check_unique` (L227 — errors 23505
  immediately when the conflicting xmin is the current transaction tree)
