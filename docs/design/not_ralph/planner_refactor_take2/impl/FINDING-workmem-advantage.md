# Finding — the recorded goopg-vs-PG gap was measured with goopg holding 8× the memory

Established 2026-09-02 by landing P0-12 (aligning the TPC-H bench clusters'
planner-visible memory settings) and timing both arms.

---

## 1. The measurement

The PG reference cluster (:65432) has always set `work_mem = 64MB` and
`effective_cache_size = 2GB` explicitly. The goopg bench cluster (:65433) set
neither, so both sat at goopg's boot defaults — **512MB** and **4GB**. Every
recorded goopg-vs-PG TPC-H figure was therefore produced with goopg holding an
**8× `work_mem` advantage**.

Aligning them, everything else held constant (same binary, fresh server per
arm, same server age, `GOGC=100 GOMEMLIMIT=12GiB`):

| arm | TPC-H total, 24 timed items |
|---|---|
| `work_mem = 512MB` (goopg's boot default) | 248.71 s |
| `work_mem = 64MB` (PG's setting) | **403.27 s** |

**+62.2 %.** Row counts identical on all 24 items.

## 2. Where it lands

| query | goopg 512MB | goopg 64MB | | PG @ 64MB | goopg/PG |
|---|---|---|---|---|---|
| Q14 | 0.47 s | 11.42 s | **+2330 %** | 1.08 s | **10.6×** |
| Q3 | 2.57 s | 28.68 s | **+1016 %** | 1.10 s | **26×** |
| Q16 | 0.76 s | 3.57 s | +370 % | — | — |
| Q10 | 3.80 s | 14.54 s | +283 % | 0.58 s | **25×** |
| Q2 | 0.74 s | 1.77 s | +139 % | — | — |
| Q13 | 5.12 s | 9.45 s | +85 % | — | — |
| Q9 | 49.94 s | 90.79 s | +82 % | — | — |
| Q18 | 60.53 s | 93.37 s | +54 % | — | — |
| Q7 | 21.97 s | 33.09 s | +51 % | — | — |
| Q21 | 9.86 s | 14.34 s | +45 % | — | — |
| Q5 | 40.01 s | 47.46 s | +19 % | — | — |
| Q12 | 13.74 s | 15.76 s | +15 % | — | — |

PG runs the three worst in about a second each, at the **same** `work_mem`.

## 2b. CORRECTION — it is plan choice, not spill

**The first version of this document concluded that the regression was hash-join
spill efficiency. That was wrong, and it was wrong because it was inferred from
the timings rather than read off the plans.** Profiling Q14 and Q3 refutes it.

Q14, goopg at `work_mem = 64MB`:

```
Merge Join (actual time=13317..13861 rows=75001)
  Merge Cond: (lineitem.l_partkey = part.p_partkey)
  ->  Index Scan using lineitem_part_supp_fkidx on lineitem
        (actual rows=6001255)  Rows Removed by Filter: 5926254
  ->  Index Scan using part_pk on part (actual rows=200000)
```

PostgreSQL, same query and the same `work_mem`:

```
Parallel Hash Join (actual rows=15033.80 loops=5)
  ->  Parallel Seq Scan on lineitem
  ->  Parallel Hash  Buckets: 262144  Batches: 1  Memory Usage: 14624kB
```

**PG does not spill — one batch, 14.6 MB.** And goopg does not spill either: Q3's
hash join reports `Batches: 1  Memory Usage: 15868kB`. Nothing batched in either
engine.

What actually happened is that reducing `work_mem` made goopg's hash join look
dearer, which tipped the choice to a **merge join whose outer input is a full
index scan of `lineitem`** — 6 001 255 rows scanned to yield 75 001, taking
12.9 s of the 13.9 s total. Q3 is the same shape: a full index scan of
`lineitem` returning 6 001 255 rows, 17.1 s of a 30.1 s total.

The displayed cost of that scan is the tell:

```
Index Scan using lineitem_part_supp_fkidx on lineitem
  (cost=0.00..66680.61 rows=666806 width=550)
  Filter: (l_shipdate >= '1995-09-01' AND l_shipdate < ...)
```

There is **no `Index Cond:`** — the index is used purely for its ordering, so it
returns every tuple and the date range is a post-fetch filter. A scan that
touches 6 M heap tuples is costed at 66 680, which is why a merge join over it
outbids a hash join.

Two distinct defects are visible and neither is spill:

1. **The full-range ordered index scan is under-costed.** PG's `cost_index`
   with no index quals uses `indexSelectivity = 1.0` and charges for every tuple
   the index returns, applying the filter's selectivity only to the OUTPUT row
   count. goopg's own `costIndexScan` does the right thing and the ordered
   producer does pass `selectivity: 1.0` — so the number above is not coming
   from that path, and establishing which producer emitted it is the next step.
   **P0-11's `DPPATH` provenance trace was built for exactly this question** and
   is the resume point.
2. **goopg has no Parallel Hash Join.** PG shares one 14.6 MB hash table across
   five workers; goopg would build a private table per worker. That is P5-06,
   and it is why PG stays comfortably inside 64 MB where goopg's costing thinks
   it cannot.

## 3. What it means

**The headline was flattered.** The recorded 227.0 s / 22.9 s = 9.9× compared a
goopg with 512MB of hash memory against a PG with 64MB. At a matched setting the
ratio against PG's recorded 22.9 s is roughly **17.6×** — and that is the honest
number, because a benchmark that gives one engine eight times the memory is not
measuring the engines.

**The dominant remaining problem IS the planner — specifically the cost model.**
§2b shows the mechanism: a mis-costed full-range index scan lets a merge join
outbid a hash join, and the resulting plan reads 6 M rows to produce 75 000. The
row counts prove the answers are right; the plan is simply the wrong one.

This is consistent with, and explains, the conclusion recorded from three
selectivity A/Bs (`perf-20260902-cumulative.md` §4b): refining CARDINALITY did
not move TPC-H time, because the binding constraint is on the COST side, not the
row-estimate side. Those are different halves of the estimator and the project
had been working the wrong one.

It also means the alignment did not merely expose a slower engine — it exposed a
costing defect that was previously masked by an over-generous memory budget. The
512MB arm was hiding a bug, not just flattering a number.

## 4. Why the alignment is kept

Making goopg 62 % slower looks like a regression and is not one. The 512MB arm
was measuring a configuration advantage, and every comparison drawn from it —
including any judgement about whether a planner change helped — was contaminated
by it. The alignment is kept, and the 403.27 s figure becomes the new control.

`shared_buffers` is deliberately **not** aligned (PG 512MB, goopg 2048MB).
goopg's buffer arena is a Go-heap object under `GOMEMLIMIT` (M0032-0001);
shrinking it to PG's value would measure Go's garbage collector rather than the
database. It is recorded as a permitted divergence.

## 5. Why this could not have been done earlier

P0-12 was blocked on P2-02, and that ordering was established by reading the
code rather than by a failure. Until P2-01/P2-02, `work_mem` reached the
**executor** (`hashsize.EffectiveMemLimit`) but not the **planner**
(`defaultCostParams()` was hard-wired at 512MB). Setting it in the bench conf
before those landed would have made the two disagree — the planner pricing
geometries the executor would not build, which is exactly the hazard
`cost_funcs.go`'s `workMem` comment names. The measurement above is only
meaningful because planner and executor now read the same value.

## 6. Resume points

1. **Identify which producer emits the under-costed full-range index scan.**
   `costIndexScan` and `pathindexordered.go` both look correct, so the number is
   coming from somewhere else. Run with `GOOPG_PGSHAPED_DP_TRACE=1` and read the
   `DPPATH` records (P0-11) for the `lineitem` relid: producer, rows, startup,
   total, verdict. This is the first use the trace was built for.
2. Re-measure the PG side in full at the current configuration, rather than
   comparing against the recorded 22.9 s from 2026-08-31.
3. P5-06 (parallel hash join) is now evidenced, not speculative: PG's 14.6 MB
   shared table across five workers is why it stays inside 64 MB.
