# 0118-0105 — RR/SSI concurrent-update 40001 robust to snapshot timing + horizons promotion (M0118-0009)

Status: accepted
Spec: `postgres/src/test/isolation/specs/horizons.spec` (PROMOTED)
Predecessor: 0118-0104 (horizons MVCC pruning-horizon temp-vs-perm core, the
enabler that landed 4/5 permutations and deferred perm-4 on this bug).

## Summary

Two coupled changes that together (a) fix a latent **lost-update correctness
bug** in the EvalPlanQual write paths and (b) unblock the final `horizons.spec`
permutation, promoting the spec to pass-required (strict).

1. **dispatch.go** — pin the REPEATABLE READ / SERIALIZABLE snapshot at the
   *first batched statement* after a `BEGIN ISOLATION LEVEL …` that shares its
   simple-query message with following statements (PG-correct timing). Before
   this, `BEGIN ISOLATION LEVEL REPEATABLE READ; SELECT 1;` (one message) never
   pinned the RR transaction's `firstSnap`/`xmin` during the batch, so the
   proc-array `xmin` was not registered and `OldestXmin` ignored the session —
   `VACUUM` reclaimed rows the session's snapshot still needed (`horizons` perm 4
   read `Heap Fetches = 0` instead of `2`).

2. **operators_storage.go** — make the RR/SSI concurrent-update detection in all
   five EvalPlanQual write sites robust to *when* the snapshot was pinned.

## The latent bug (why #1 alone regressed `eval-plan-qual-trigger`)

After an UPDATE/DELETE waits (`epqWait`) on a conflicting tuple's `xmax` and that
transaction settles, the executor must decide: did `xmax` abort (proceed with our
write), commit (RR/SSI ⇒ raise `40001`), or is it still active (retry)?

The legacy decision used **snapshot membership** as the discriminator:

```go
if visible {                                   // original tuple still live under our snapshot
    if !o.ctx.Snap.HasInProgress(xmax) {       // "absent from snapshot ⇒ aborted"
        epqDoUpdate = true; continue           // ← proceed: overwrite the row
    }
    ...
}
```

`snap.HasInProgress(xmax)` is true only for transactions that were **running when
the snapshot was taken**. A transaction that *started after* our frozen RR/SSI
snapshot is absent from `InProgress` — exactly like an aborted one — so the
shortcut classifies a concurrently-**committed** updater as aborted and proceeds,
silently overwriting its version (a lost update) where PostgreSQL raises
`40001 could not serialize access due to concurrent update`.

This stayed hidden because goopg pinned the RR snapshot *late* (at the first
separate-message statement, by which point the concurrent writer was already in
the snapshot's `InProgress` set, so `HasInProgress` returned true and the code
fell through to the correct `40001` path — partly via retry-exhaustion). Fixing
the snapshot timing (#1) moved the pin earlier, so the concurrent writer was no
longer in `InProgress`, and the latent shortcut fired — `eval-plan-qual-trigger`
regressed (the RR UPDATE overwrote the row + fired triggers instead of `40001`).

## The fix (#2): authoritative settle classification

New shared helper classifies `xmax` from the transaction manager (authoritative,
snapshot-timing-independent), used by every RR/SSI EPQ visible-branch:

```go
func epqXmaxSettled(ctx *Context, xmax storage.TransactionID) (aborted, committed bool) {
    if ctx.TxnMgr == nil { return false, false }      // unit-test path → legacy fallback
    if ctx.TxnMgr.HasAbortedXID(xmax) { return true, false }
    if ctx.TxnMgr.IsXIDActive(xmax)  { return false, false } // still active → retry
    return false, true                                // settled, not aborted ⇒ committed
}
```

At each site, for RR/SSI:

```go
if visible {
    if o.ctx.Tx.Isolation != mvcc.IsolationReadCommitted && o.ctx.TxnMgr != nil {
        aborted, committed := epqXmaxSettled(o.ctx, xmax)
        if aborted   { <proceed> }
        if committed { return <epqSerializationErr(...)> }   // 40001, delete-distinguished
        continue                                             // still active; retry
    }
    // RC (or no manager): legacy snapshot heuristic, byte-for-byte unchanged.
    ...
}
```

`epqSerializationErr` centralises the concurrent-delete-vs-update message
distinction (a DELETE'd tuple keeps its initial `{InvalidBlockNumber,0}` CTID;
an UPDATE stamps a forward CTID).

### Sites changed (sibling-paths discipline — all five together)

| site | path | proceed action |
|------|------|----------------|
| updateOp HOT | `epqRecheckVisible(rel, pu.blk, pu.slot)` | `epqDoUpdate = true` |
| updateOp trigger phase-1 | `…(captureRel, writeBlk, writeSlot)` | `break` |
| updateOp seqscan | `…(puRel, pu.blk, pu.slot)` | `epqDoUpdateSeq = true` |
| deleteOp phase-1 | `…(captureRel, deleteBlk, deleteSlot)` | `break` |
| deleteOp | `…(victimRel, v.blk, v.slot)` | `epqDoDelete = true` |

Two sites already had an `IsXIDActive`→`40001` arm, but it was unreachable when
`xmax` was absent from the snapshot because the `!HasInProgress` shortcut
returned first; the fix hoists the authoritative check above the shortcut.

## RC is provably unchanged

The new branch is guarded on `Isolation != ReadCommitted`. READ COMMITTED keeps
the exact legacy code path (`epqWait` refreshes the snapshot, so a committed
`xmax` becomes visible and the row is re-read via HOT-chain follow in the
not-visible branch). No RC behavior change ⇒ no pgbench / TPC-H impact.

## Crash safety

No on-disk format change. The classification reads in-memory manager state
(`HasAbortedXID`/`IsXIDActive`) that is already authoritative for live
transactions; settled-state after a crash is reconstructed from CLOG as before.

## Gates

- `TestPort_IsolationHorizons` strict PASS (5/5 permutations) — promoted.
- `TestPort_IsolationEvalPlanQualTrigger` strict PASS (was regressing under the
  early-snapshot change; now green via the 40001 fix).
- RR/SSI + EPQ + row-lock regression batch (write-skew, read-write-unique,
  update-conflict, multiple-row-versions, tuplelock/lock-committed/propagate-lock
  families) PASS.
- `-race` mvcc/executor; build + vet; pgbench smoke (pre-commit hook).
- CSV `horizons` row `failed`→`pass`; coverage + inventory regenerated.
