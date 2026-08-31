# 0131-0025 — a single-session UPDATE loop on one row silently stops applying after 64 updates

Status: **root-caused and FIXED for the single-session case** (M0131-S32);
the concurrent case is a distinct, still-open defect (M0131-S32.1).
Filed 2026-08-12 (loop #143), fixed 2026-08-12 (loop #144).

## Root cause (loop #144)

Every HOT/CTID chain walker in goopg bounded its loop with a hand-written
`const maxChain = 64`:

| site | function |
|---|---|
| `internal/executor/operators_index.go` | `followHOTChain`, `followHOTChainNoCopy`, `heapChainDeadToAll` |
| `internal/executor/operators_storage.go` | `epqChainCheckMovedPartition`, `epqFollowChainFull` |
| `internal/storage/prune.go` | `pruneChainTip` |

A HOT chain grows by one version per HOT update, so the 65th version of a
repeatedly-updated row sits at chain depth 65 and `followHOTChain` gave up
before reaching it, returning `found == false`. Consequences, all silent:

- The index scan driving `UPDATE … WHERE id = 1` found no live tuple, so the
  statement's `pending` set was empty and it wrote **nothing** — while still
  reporting `UPDATE 1`, because the row count comes from the planned row set.
- `SELECT … WHERE id = 1` (index) returned nothing; `WHERE id+0 = 1` (seq scan)
  still returned version 64, whose `xmax` was never stamped since no update was
  ever applied. That is the "index-unreachable row" symptom this doc first
  attributed to S31 — here it is a *consequence* of the same truncation, not a
  second defect.
- `pruneChainTip` gave up at the same depth, so the dead versions were never
  reclaimed and the page stayed full — which is why the boundary looked like a
  page-capacity edge.

Nothing in PG behaves this way: `heap_hot_search_buffer` loops until
`at_chain_end` with no step cap, and `heap_prune_chain` sizes its `chainitems[]`
array by `MaxHeapTuplesPerPage` (291 for an 8 KiB page) — the real bound, since
a HOT chain visits *distinct slots of one page*, so exceeding it proves a cycle.

## Fix

- New `storage.MaxHeapTuplesPerPage` (`internal/storage/heap.go`), the
  PG-faithful `(BLCKSZ - SizeOfPageHeaderData) / (MAXALIGN(SizeofHeapTupleHeader)
  + sizeof(ItemIdData))` = 291, pinned to PG's value by
  `TestMaxHeapTuplesPerPagePGParity`.
- All four single-page walkers (`followHOTChain`, `followHOTChainNoCopy`,
  `heapChainDeadToAll`, `pruneChainTip`) now bound by it.
- The two **cross-page** t_ctid walkers (`epqChainCheckMovedPartition`,
  `epqFollowChainFull`) span pages, so no per-page bound applies; they use
  `maxCTIDChainWalk = 1<<20`, an explicit corruption backstop mirroring PG's
  uncapped `heap_get_latest_tid` loop.

Verification: `analysis/hotstall.sh` reports `OVERALL: PASS` at N=300 **and at
N=2000** (2000 sequential updates on one row, index read == seq read == 2000);
`TestHOTUpdateChainBeyond64Versions` (`internal/executor/hot_update_test.go`)
reads the value back through the index after every one of 120 updates, and fails
at exactly "after update 65: v=64" when the bound is put back to 64.

## Still open: M0131-S32.1 (the concurrent arm)

`analysis/atomicity-nocrash-control.sh` was extended this loop to assert
`sum(tbalance)` and `sum(bbalance)` against `sum(delta)` — the arm that first
exposed S32 (§2 below) and the sensitive one, since 50 tellers / 5 branches
means the same row is updated thousands of times. With the chain-walk fix in
place, scale 5 / 16 clients / 30 s still fails, LIVE and after a clean restart:

| table | sum | vs sum(delta) = -31482 |
|---|---|---|
| `pgbench_accounts.abalance` | -31482 | exact |
| `pgbench_tellers.tbalance` | -14938 | under-applied |
| `pgbench_branches.bbalance` | -309543 | ~10× **over**-applied |

Both directions are wrong, so this is not the same truncation: it is a
concurrent hot-row update defect (over-application points at the
duplicate-index-entry path guarded by AI-20260810-011258-006, which dedupes
only *within* one statement). The script's header records that `OVERALL: FAIL`
is the known state there.

Related: [`0131-0019`](0131-0019-index-entry-loss-on-update.md) (the HOT-chain
prune/redirect defect, S31), [`0131-0020`](0131-0020-crash-recovery-row-loss-confirmed.md)
(S30), [`0131-0023`](0131-0023-crash-xid-reuse-torn-transactions.md) (S30.7),
[`0131-0024`](0131-0024-live-server-committed-insert-loss.md) (S30.9, retracted).

## Summary

goopg silently discards committed `UPDATE`s. The repro needs **no concurrency,
no crash, and no special configuration**:

```sql
CREATE TABLE t(id int primary key, v bigint);
INSERT INTO t VALUES (1, 0);
-- then, in ONE session, autocommit, 300 times:
UPDATE t SET v = v + 1 WHERE id = 1;
```

`v` advances `1, 2, … 64` and then **freezes at 64 forever**. Every one of the
remaining 236 statements still reports `UPDATE 1` and commits successfully. No
error is raised at any point, `count(*)` stays `1`, and the server log is
silent. The only observable is a wrong value.

Probe: `bash analysis/hotstall.sh` (~40 s, self-contained cluster).

```
  20 |    20 |    1
  40 |    40 |    1
  60 |    60 |    1
  80 |    64 |    1      <-- stalls here
 ...
 300 |    64 |    1
updates_issued=300 seq_read='64' idx_read='' rows=1
```

The stall point is stable at 64 across runs, which is why this reads as a
capacity boundary (the page runs out of room for another HOT version and the
fallback path then no-ops) rather than as a race.

A second, independent symptom rides along: after the stall the **index** read
`WHERE id=1` returns *nothing* while the seq-scan read `WHERE id+0=1` returns
the stale row. That is the S31 signature (`0131-0019`) reappearing, and it means
"lost update" and "index-unreachable row" must never be conflated in any
measurement here — `analysis/hotstall.sh` therefore reports both reads.

## How this was found, and what it reframes

The loop set out to work S30.8 ("committed `UPDATE pgbench_accounts` new tuples
are not visible after replay"). S30.8's premise was that the atomicity invariant

```
sum(pgbench_accounts.abalance) == sum(pgbench_history.delta)
```

fails only after a crash. Loop #142 retracted a claim that a no-crash control
also failed, because that control's harness was broken (`0131-0024` §7) — so the
control had still never actually been run. It has now been run properly.

### 1. The control validates the invariant (new: `analysis/atomicity-nocrash-control.sh`)

Two runs, scale 5, 16 clients, 45 s, **no crash anywhere**, measured both on the
live server and after a *clean* `goopg stop` + restart:

| run | phase | count/distinct/min/max | sum(abalance) | sum(delta) | history rows | pgbench processed |
|---|---|---|---|---|---|---|
| 1 | LIVE | 500000/500000/1/500000 | -54526 | -54526 | 76828 | 76828 |
| 1 | RESTART | 500000/500000/1/500000 | -54526 | -54526 | 76828 | 76828 |
| 2 | LIVE | 500000/500000/1/500000 | 455041 | 455041 | 78111 | 78111 |
| 2 | RESTART | 500000/500000/1/500000 | 455041 | 455041 | 78111 | 78111 |

`OVERALL: PASS (2 runs, no crash)`. The invariant is sound, and the clean
shutdown/restart path preserves it exactly. **S30.8's premise therefore
survives**: `RUNS=2 crashprobe30` at the same HEAD fails 2/2
(`31384 != 44061`, `-157231 != -165901`), and that failure needs a crash.

### 2. …but the invariant is only sound for `pgbench_accounts`

The control cluster was re-opened and the *other* two balance tables checked.
On the **clean, no-crash** run 1:

| table | sum |
|---|---|
| `pgbench_accounts.abalance` | -54526 |
| `pgbench_history.delta` | -54526 |
| `pgbench_tellers.tbalance` | **-90351** |
| `pgbench_branches.bbalance` | **-182750** |

pgbench's TPC-B transaction updates accounts, tellers and branches by the same
`:delta` and inserts one history row, all in one transaction, so all four sums
must be equal. Tellers and branches are wrong **with no crash at all**. The
magnitude tracks contention exactly: 500000 accounts (0 error), 50 tellers
(large error), 5 branches (larger error) — i.e. it scales with how often the
*same row* is updated repeatedly, not with how often two sessions collide.

### 3. Reduced to one row, one client

| clients | committed updates | final `v` | lost |
|---|---|---|---|
| 1 | 6653 | 64 | 6589 |
| 2 | 6088 | 6087 | 1 |
| 4 | 8150 | 4551 | 3599 |
| 8 | 14883 | 4363 | 10520 |

The single-client row is the important one: **concurrency is not required**, so
this is not a lost-update/EvalPlanQual defect and none of the S30/S31
concurrency machinery is implicated. (The non-monotonic `clients=2` row is
noise from how quickly a run happens to reach the stall boundary, not a
protective effect of concurrency.)

## Why the existing conflict machinery is not the explanation

The read-committed update path *does* have the checks one would first suspect,
so they are ruled out by inspection:

- `tryApplyHOTUpdate` (`internal/executor/operators_storage.go:3555`) re-reads
  the old tuple **under the exclusive content lock** and calls
  `isConcurrentlyUpdated` before stamping, returning `(false, nil)` on conflict
  so the caller falls back to the delete+insert path.
- `isConcurrentlyUpdated` (`:3180`) returns true for a *committed* foreign
  `xmax` as well as an in-progress one — it excludes only aborted ones — so a
  committed concurrent updater is detected, not skipped.
- The `!used` fallback runs a full EPQ retry loop (`:4328`) with chain-following
  under READ COMMITTED.

None of that can fire with one client and no concurrency. The suspicion is
therefore the *capacity* edge: `tryApplyHOTUpdate` returns `used == false` when
the page cannot hold another version, and the `!used` fallback then fails to
place the new tuple while still reporting success to the client.

## Next step (cheapest decisive probe)

1. Instrument the `used == false` return sites in `tryApplyHOTUpdate` and the
   entry of the `!used` fallback with an unconditional counter, and run
   `analysis/hotstall.sh`. The prediction is that updates 1–64 take the HOT
   path and 65+ take the fallback; if instead the fallback is never entered,
   the stall is inside `tryApplyHOTUpdate`'s own "skip this row" return
   (`oldItem.Flags != storage.ItemIDNormal`, `:3625`), whose comment already
   says *"Caller treats (false, nil) as skip this row"* — a stale comment,
   since the caller actually treats it as "fall through to the non-HOT path".
2. Whichever arm is taken, check the row-count reported to the client: the
   statement reports `UPDATE 1` regardless, so the count is being taken from
   the *planned* row set rather than from rows actually written. A silent
   no-op that reports success is itself a defect independent of the cause.
3. Only then look at pruning: 64 versions on an 8 KiB page implies the
   opportunistic prune (`PagePruneOpt`) is either not running or not reclaiming
   the dead HOT versions, which is what makes the page permanently full. Note
   S31's prune fix (`0131-0019`) is in this same code, and the index-unreachable
   symptom above suggests the two are related.

Upstream reference: `heap_update` (`postgres/src/backend/access/heap/heapam.c`)
never returns "success, nothing written" — it either succeeds, returns
`TM_Updated`/`TM_Deleted` for the caller to handle, or errors. There is no PG
path in which a committed `UPDATE` reports one row updated and applies nothing.

## Severity

Highest open severity in M0131. It is a wrong-answer defect reachable by the
simplest possible workload (one client, one row, no crash), it reports success,
and it silently corrupts any counter-style column. It also invalidates
`pgbench_tellers`/`pgbench_branches` as measurement signals in every S30 probe,
and plausibly contributes to S30's crash-probe divergences.

## Artefacts

- `analysis/hotstall.sh` — minimal deterministic repro (gate:
  `OVERALL: PASS` when `v == N`).
- `analysis/atomicity-nocrash-control.sh` — the missing no-crash control for
  `crashprobe30`'s atomicity invariant; three phases (LIVE / clean RESTART) and
  a pgbench-transaction-count cross-check, with the S30.9 `wait`-hygiene fix.
