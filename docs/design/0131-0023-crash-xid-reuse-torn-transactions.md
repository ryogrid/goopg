# 0131-0023 — Crash recovery reused in-flight XIDs (M0131-S30.7, first half)

Status: accepted (fix landed 2026-08-11)
Task: `M0131-S30.7` — "a crash still tears transactions:
`sum(pgbench_accounts.abalance) != sum(pgbench_history.delta)`"
Predecessors: `0131-0020` (the measurement), `0131-0022` (S30.1, the row-loss
defect), `0131-0021` (S30.3, runtime/WAL page divergence).
Gate: `RUNS=2 bash analysis/crashprobe30.sh`.

## Summary

goopg's post-crash `nextXID` was `max(committed/aborted XID) + 1`. The XIDs of
transactions that were **in flight** at the crash were therefore handed out
again to new transactions after the restart — while those transactions' heap
records were in the WAL (a concurrent commit flushes everything ahead of it in
the stream) and *were* replayed. The first new transaction to receive a recycled
XID stamped it `Committed`, resurrecting the crashed transaction's replayed half
and breaking pgbench's atomicity invariant in **either** direction depending on
which half of the crashed transaction had reached the WAL.

Upstream advances unconditionally, per record, before applying it:

```c
/* postgres/src/backend/access/transam/xlogrecovery.c:1942, ApplyWalRecord */
/*
 * TransamVariables->nextXid must be beyond record's xid.
 */
AdvanceNextFullTransactionIdPastXid(record->xl_xid);
```

goopg only advanced on commit/abort records
(`xactStampAndAdvance`, `internal/initdb/xact_recovery.go`).

## Evidence (preserved crashprobe30 clusters, `/tmp/crashprobe30/run1`)

`pg_waldump` over the crashed cluster's `pg_wal`, before the fix:

| quantity | value |
|---|---|
| highest `xl_xid` in the WAL | **59985** |
| last `COMMIT` record | 59974 (last three commits 59967, 59974, 59973) |
| `nextXid` in the checkpoint the restarted server wrote | **59977** |

The tail also carries `HOT_UPDATE` records for 59983/59984/59985 — in-flight
work that replay applied. XIDs 59977..59985 were then re-issued.

After the fix, the same probe run shows highest `xl_xid` 60422 and the restart's
`RUNNING_XACTS nextXid 60423` — no reuse window at all.

## Fix

`internal/initdb/xact_recovery.go`, `replayCLogFromWAL`: for every record from
the replay start onward, `txnMgr.SetNextXID(xl_xid + 1)` (`SetNextXID` is
monotonic). This also tightens the implicit-abort sweep that runs immediately
after it in `initdb.Open` — `clog.MarkUnknownAsAborted(txnMgr.NextXID())` can
only reach XIDs below `nextXID`, so before this change an in-flight XID above
the last commit was neither swept to `Aborted` nor protected from reuse. With
the advance in place, an in-flight transaction's replayed tuples are correctly
invisible, matching upstream's treatment of an XID with no clog entry.

Guard: `TestReplayCLogFromWAL_AdvancesPastInFlightXID`
(`internal/initdb/xact_recovery_test.go`) — a commit for XID 11 followed by an
uncommitted record for XID 17; asserts `nextXID > 17` and that the sweep then
stamps 17 `Aborted` and leaves 11 `Committed`. Verified in both directions (with
the advance removed: `NextXID = 12 after replay, want > 17`).

## What this did NOT fix — successor defect (S30.8)

`RUNS=2 crashprobe30` after the fix still reports `OVERALL: FAIL` on the
atomicity invariant, but the signature CHANGED: the divergence is now
**unidirectional** in both runs — `sum(history.delta)` exceeds
`sum(accounts.abalance)` (118007 vs 108024; 616047 vs 600730), where before it
went both ways.

What the new signature says:

- Only committed transactions' history rows are visible, so the deficit is
  committed `UPDATE pgbench_accounts` work that replay did not make visible.
- `count(*) = count(distinct aid) = 500000`, `min=1`, `max=500000` still hold,
  so nothing is lost or duplicated at the row level — the surviving version of
  those rows is the **old** one. The update's new tuple is missing (if the old
  tuple's `xmax` stamp were missing instead, both versions would be live and the
  distinct/count assertions would have caught it).
- Replay reached the true end of the stream (`restart.log` carries no early
  end-of-WAL; only the expected "not properly shut down" WARN), so this is not
  an S30.1 recurrence.

Resume point: the non-HOT `UPDATE` replay arm and its `pd_lsn` skip guard for
`pgbench_accounts` (fillfactor 100, so most updates move the tuple to another
page) — the same runtime/WAL page-divergence family as `0131-0021` (S30.3) and
`0131-0022`'s unlogged-mutation finding (S30.5). Filed as `M0131-S30.8`.
