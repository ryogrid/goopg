# M0131-S30.7 — after crash recovery, aborted transactions' rows read as committed

Status: **accepted / fixed** (2026-08-12, loop #148)
Milestone: M0131 (task M0131-S30.7 / S30.8)
Predecessors: `0131-0020` (S30 re-measurement), `0131-0022` (S30.1 WAL tail),
`0131-0023` (S30.7 first half: XID reuse), `0131-0024` (S30.9 retraction),
`0131-0026` (S32 series, the live-server half of the same invariant)

## 1. The measurement

With the S32 series closed, `RUNS=2 bash analysis/crashprobe30.sh` was re-run at
HEAD `3e621d22`. No rows were lost (500000/500000, index parity, anti-join 0),
but **both** runs broke the atomicity invariant, and — new — in the OPPOSITE
direction from the one S30.8 was filed for:

| run | `sum(pgbench_accounts.abalance)` | `sum(pgbench_history.delta)` |
|---|---|---|
| 1 | 138347 | 135367 |
| 2 | 480230 | 469903 |

`sum(abalance) > sum(delta)`: the heap carries *more* update than the history
table records, i.e. the crashed cluster came back with **extra** work, not with
missing work. That inverts S30.8's premise ("committed UPDATEs are not visible
after replay") and is what made this loop diagnosable.

## 2. Localisation

Run 2's cluster was preserved and probed offline.

Exactly **13** accounts diverge from their history rows:

```sql
select count(*), sum(d) from (
  select a.abalance - coalesce(h.s,0) as d
  from pgbench_accounts a
  left join (select aid, sum(delta) s from pgbench_history group by aid) h
    on h.aid = a.aid) x
where d <> 0;
-- 13 | 10327
```

Ten of the 13 have **no** history row at all, three have one history row whose
delta differs from `abalance` by a single plausible `:delta`. Every difference is
one transaction's worth. Cross-table:

* `sum(pgbench_branches.bbalance)` == `sum(delta)` **exactly**,
* `sum(pgbench_tellers.tbalance)` is over, like accounts.

That is the shape of a **torn transaction at the WAL cut**: pgbench's TPC-B body
is `UPDATE accounts; UPDATE tellers; UPDATE branches; INSERT history; COMMIT`, so
a transaction whose records were partly flushed (by a *concurrent* committer's
`XLogFlush`) has its accounts and tellers updates durable and its branches update
and history insert past the cut. Replay applies what is durable. Upstream does
exactly the same — and then hides it, because the transaction never committed.

goopg did not hide it. Decoding `pg_xact` proves the recovery bookkeeping is
correct: 40267 COMMIT, **13 ABORT** (contiguous, at the very top of the XID
range), 3 in-progress lanes above `NextXID`. Thirteen aborts, thirteen wrong
accounts — the `MarkUnknownAsAborted` sweep in `initdb.Open` did its job and the
reader ignored it.

## 3. Root cause (two defects, one symptom)

### 3.1 `Snapshot.SeesCommittedXID` short-circuits below `Xmin`

```go
if s.HasAborted(xid) { return false }   // in-memory list — rebuilt EMPTY on restart
if xid < s.Xmin      { return true  }   // <-- assumed committed, CLOG never consulted
...
if s.clog != nil { /* the CLOG consult lived HERE, in-window only */ }
```

The durable-abort consult added by M0117-0002 guarded only the *in-window*
residual case (`Xmin <= xid < Xmax`, not running). But `initdb.Open` advances
`NextXID` past every XID seen during replay (`0131-0023`), so the FIRST snapshot a
post-restart session takes has `Xmin` **above the whole aborted range** — every
recovered abort takes the `xid < Xmin` shortcut and reads as committed.

PostgreSQL has no such shortcut. `HeapTupleSatisfiesMVCC` resolves a non-hinted
`xmin` through `TransactionIdDidCommit` → the CLOG regardless of the snapshot's
xmin (`heapam_visibility.c`); `XidInMVCCSnapshot`'s `xmin` fast path only answers
"was it running *then*", never "did it commit". The hint bits
(`HEAP_XMIN_COMMITTED` / `HEAP_XMIN_INVALID`) — which goopg already reads in
`mvcc.TupleVisible` before calling `SeesCommittedXID`, and lazily writes in
`operators_storage.go` — are what keep the CLOG off the repeat-scan hot path,
upstream and here alike.

Fix: hoist the consult into `Snapshot.clogSaysNotAborted` and call it *before*
the `xid < Xmin` shortcut. The contract is unchanged and still conservative —
only a positive `TxnStatusAborted` overrides; `Unknown` and `Committed` both read
as not-aborted, and XIDs below `OldestClogXid` still short-circuit.

### 3.2 `Manager.SetCLog` had NO production caller

The above would have changed nothing, because `Snapshot.clog` was **always nil on
a live server**. `mvcc.Manager.SetCLog` was added by M0117-0002 with the explicit
contract "intended to be called once during startup/recovery wiring
(initdb.Open)" — and that call was never written. `grep -rn "SetCLog("` matched
the definition and nothing else. The entire durable-abort fallback, its tests
(`snapshot_clog_fallback_test.go`) and the in-window consult were dead code in
production for the whole of M0117..M0131.

Fix: `txnMgr.SetCLog(clog)` in `initdb.Open`, immediately after the CLOG's SLRU
mirror and WAL-flush hook are wired and before any recovery sweep — the sweep
mutates the same `*CLog` object, so ordering against it is irrelevant.

Both halves are required: 3.1 without 3.2 is inert; 3.2 without 3.1 only covers
in-window XIDs, which after recovery is none of them.

## 4. Verification

`RUNS=2 bash analysis/crashprobe30.sh` at the fix:

* **run 1 PASS** — `sum(abalance) == sum(delta)` **exactly** (-1498 / -1498),
  500000/500000 rows, index parity, anti-join 0. This is the first crash run in
  the S30 series to satisfy the atomicity invariant.
* run 2 FAIL — but on a *different, already-filed* defect: replay stopped early
  with `end of WAL reached during replay reason="invalid page header"
  detail="magic=0x0020 want 0xd118" lsn=117432305`, losing 6762 rows. That is the
  S30.1/S30.2 class (`0131-0022` fixed the `padding bytes nonzero` variant of the
  same walk; this is the sibling `invalid page header` variant), and its
  atomicity failure is downstream of the row loss, not of visibility. Ledger row
  filed; S30.1 re-opened as S30.1b.

Gates: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS,
`go test ./internal/mvcc/ ./internal/initdb/` PASS,
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35), pgbench pre-commit smoke.

## 5. What this does NOT fix

* The early-end-of-WAL row loss (S30.1b / S30.2) — see above.
* Hint-bit poisoning on an already-affected cluster: a tuple whose
  `HEAP_XMIN_COMMITTED` bit was lazily written *while the bug was live* keeps
  reading as committed, because the caller trusts the bit before consulting the
  CLOG (as does PG). Re-probing the preserved run-2 cluster with the fixed binary
  therefore still shows the old sums; only clusters recovered by a fixed binary
  are correct. There is no repair path short of `VACUUM`-style rewriting, and PG
  has the same property — the bug had to be fixed before the bit was ever set on
  a non-committed xmin, which is exactly what 3.1 now guarantees.
* Performance: first-touch of a non-hinted, non-frozen tuple now costs one CLOG
  SLRU lookup, as upstream. The spotcheck (Q12 15.9 s / Q13 7.9 s) and the
  pgbench smoke show no regression, but a dedicated A/B was not run.
