# Where the remaining gap to PostgreSQL 18.3 comes from

Branch `optimize-row-decode`. goopg build `9112c762c` (the four-part row-decode
series). PG 18.3 reference on port 65432. Date: 2026-09-08.

## 0. Summary

After the row-decode series, TPC-H SF=1 stands at **55.10 s (goopg) against
14.48 s (PG 18.3) — 3.8×**, down from ~7.3× before this work.

**The gap is not uniform, and "goopg is 3.8× slower" is the wrong model.** It
decomposes into two layers with very different causes and very different fixes:

| layer | size | cause |
|---|---|---|
| **executor constant factor** | **~2×** | 48-byte `Datum`, interpreted expressions, Go GC, per-row cloning |
| **planner** | **the rest, up to 26×** | wrong plan shapes doing orders of magnitude more work |

**goopg is faster than PG on two of 21 queries.** That single fact rules out a
uniform-slowness model and is what points the analysis at plan quality.

## 1. Measured per-query ratios

Both engines: `shared_buffers = 2GB`, `work_mem = 64MB`,
`max_parallel_workers_per_gather = 4`, same host, warm, minimum of measured
passes. goopg figures are the part-4 after-arm minima; PG is a measured pass
after a discarded warm pass.

| query | goopg (s) | PG (s) | ratio |
|---|---:|---:|---:|
| Q19 | 1.56 | 0.06 | **26.0×** |
| Q21 | 9.46 | 0.55 | **17.2×** |
| Q12 | 6.35 | 0.42 | **15.1×** |
| Q4 | 1.36 | 0.12 | 11.3× |
| Q22 | 0.54 | 0.06 | 9.0× |
| Q7 | 3.11 | 0.41 | 7.6× |
| Q5 | 2.32 | 0.32 | 7.2× |
| Q3 | 0.97 | 0.23 | 4.2× |
| Q9 | 1.49 | 0.44 | 3.4× |
| Q18 | 17.54 | 5.94 | 3.0× |
| Q10 | 1.68 | 0.61 | 2.8× |
| Q13 | 3.69 | 1.40 | 2.6× |
| Q2 | 0.57 | 0.22 | 2.6× |
| Q1 | 2.19 | 1.04 | 2.1× |
| Q6 | 0.54 | 0.26 | 2.1× |
| Q8 | 0.37 | 0.18 | 2.1× |
| Q14 | 0.40 | 0.20 | 2.0× |
| Q11 | 0.14 | 0.09 | 1.6× |
| Q16 | 0.27 | 0.27 | 1.0× |
| **Q20** | **0.13** | **0.22** | **0.6× — goopg faster** |
| **Q17** | **0.42** | **1.44** | **0.3× — goopg 3× faster** |
| **TOTAL** | **55.10** | **14.48** | **3.8×** |

**The ratio is inversely correlated with PG's absolute time.** Queries PG
finishes in 0.06–0.5 s run 9–26× slower on goopg; queries PG takes 1–6 s on run
2–3× slower. A constant per-row overhead cannot produce that shape — it would
give a roughly flat ratio. Something is making goopg do *more work*, and it
shows up most where the honest amount of work is small.

**Caveat on the data:** the two clusters are separate HammerDB SF=1 loads
(`lineitem` 6,001,255 rows in goopg vs 5,998,835 in PG, a 0.04% difference), so
row counts differ trivially between engines. This does not affect ratios at
this magnitude.

## 2. Layer 1 — the planner, and Q12 as the worked example

Q12 is 15.1×. Both plans captured with `EXPLAIN ANALYZE`.

**PG 18.3** — filter pushed into the scan, then a targeted index probe:

```
Finalize GroupAggregate (actual rows=2)
  -> Gather Merge (Workers Planned: 4, Launched: 4)
    -> Partial GroupAggregate
      -> Sort  (actual rows=6270.80 loops=5)
        -> Nested Loop  (actual rows=6270.80 loops=5)
          -> Parallel Seq Scan on lineitem  (actual rows=6270.80 loops=5)
               Filter: l_shipmode = ANY('{MAIL,SHIP}') AND l_commitdate < l_receiptdate
                       AND l_shipdate < l_commitdate AND l_receiptdate >= '1994-01-01' ...
               Rows Removed by Filter: 1193496
          -> Index Scan using orders_pk on orders  (actual rows=1.00 loops=31354)
               Index Searches: 31354
Buffers: shared hit=254794
```

PG reduces `lineitem` from ~1.2 M rows per worker to 6,270 **inside the scan**,
then performs 31,354 index probes into `orders`.

**goopg** — merge join over a full index scan:

```
Sort (actual time=8382.209 rows=2)
  -> Finalize HashAggregate
    -> Gather (Workers Planned: 4, Launched: 4)
      -> Partial HashAggregate
        -> Merge Join  (cost=0.75..1608261.68 rows=6001255)
          -> ... orders side, ~300K rows per worker
          -> Index Scan using idx_lineitem_orderkey_fkidx on lineitem
               Worker 0:  rows=6001255.00 loops=1
               Worker 1:  rows=6001255.00 loops=1
               Worker 2:  rows=6001255.00 loops=1
               Worker 3:  rows=6001255.00 loops=1
               Worker 4:  rows=6001255.00 loops=1
```

Two distinct defects compound here:

**(a) Wrong plan shape.** goopg chose a **Merge Join** requiring `lineitem`
sorted by `l_orderkey`, and satisfied it with a **full index scan of all
6,001,255 rows**, applying the filter afterwards. PG chose a filtered parallel
sequential scan feeding a nested-loop index probe. goopg touches roughly
**1000× more rows** for the same answer.

**(b) The merge join's inner is re-read per worker.** Every one of the five
participants reports `rows=6001255 loops=1` — each worker reads the entire
inner, so the 6 M-row scan is performed 5 times, ~30 M row-reads total. That
cost is real: ~94% of this query's runtime is inside that node.

> **CORRECTION (2026-09-08).** An earlier revision titled this "the parallel
> scan does not divide the work" and called it a bug. **The measurement is
> right; the label was wrong.** PG does the same thing — a partial join pairs a
> partial outer with a *complete* inner
> (`postgres/src/backend/optimizer/path/joinpath.c:1437-1443`,
> `get_cheapest_parallel_safe_total_inner`), and goopg implements exactly that
> with the citation at `internal/optimizer/joinpathsparallel.go:250-254`.
> goopg's work division is correct: the `orders` outer partitions to exactly
> 1,500,000 rows across the five participants. The right explanation is the E-18
> one given two paragraphs below — *a parallel merge join re-reads its inner per
> worker* — and the real defect is the **plan choice** that put a 6 M-row inner
> there at all. See `docs/design/not_ralph/fix-pworker-bug/DESIGN.md`.

Defect (b) is the merge-join manifestation of the parallel-execution gap the
previous workstream identified as E-18: goopg has no cooperative build to share
an inner side, so a parallel merge join re-reads its inner per worker. It is
recorded there as an executor gap that *a parity-correct plan exposes*; here a
parity-**incorrect** plan exposes it too.

## 3. Layer 2 — the executor floor, ~2×

Q1 is the control. It is a pure scan-plus-aggregate over `lineitem` with no
joins, and **both engines choose the same plan shape** — `Gather` over a
partial aggregate, finalised above. goopg 2.19 s, PG 1.04 s: **2.1×**.

Q6 (2.1×), Q8 (2.1×) and Q14 (2.0×) cluster at the same value. That
consistency across independent queries is what makes ~2× readable as the
engine's per-row floor rather than a property of one query.

From the final-build profiles, the floor's constituents are:

- **`Datum` is 48 bytes**; PG's is a single 8-byte word. Every value moved,
  returned or stored costs 6× the memory traffic. This is visible as
  `runtime.memmove` at 5.0% and the residual `dst[i] = v` store at 3.87 s.
- **Interpreted expression evaluation.** `evalFastExpr` + `evalExprSlot` are
  ~13% cum. PG 18 JIT-compiles expressions for queries above a cost threshold.
- **Go GC and allocation**: `mallocgc` 13.8% cum.
- **Per-row retention**: `cloneRowOwned` is 10.0% cum on TPC-H and **30.3% on
  TPC-DS**, because a scan reuses one row buffer and anything escaping
  downstream must be copied.
- **`numeric` via `math/big`** (6.0% cum): partly a benchmark property —
  HammerDB declares `l_extendedprice`, `l_discount` and `l_quantity` as
  `numeric`, not `float8` — but PG's hand-rolled fixed-point `NumericVar` is
  cheaper than `math/big` regardless.

## 4. Where goopg already wins, and why it matters

Q17 (0.3×) and Q20 (0.6×) are faster on goopg. These are *not* noise: Q17 is
1.44 s on PG against 0.42 s on goopg.

They matter to the argument. If the executor were uniformly 3.8× slower, no
query could invert. Their existence localises the deficit to **plan choice on
specific shapes**, and confirms the executor is within a small factor of PG
when both run comparable plans.

## 5. What this implies for the next work

**The row-decode series is finished and was the right work** — it took the gap
from ~7.3× to 3.8×. But the remaining gap is not more of the same:

1. **Plan quality is the dominant term.** Predicate push-down into scans, join
   method selection, and access-path choice. The previous workstream measured
   plan parity against PG at 6/14 matching shapes, which is consistent with
   what is measured here.
2. ~~**Parallel work division** is a concrete, isolated bug with a witness:
   every worker scanning the full table in Q12. This is bounded and testable,
   unlike the general plan-quality problem.~~
   **RETRACTED 2026-09-08.** There is no work-division bug — see the correction
   in §2(b). goopg's division is PG-faithful and exact. What made this look like
   a bug was a separate, real EXPLAIN defect: collapsed nodes printed
   **pre-filter** row counts, so the index scan showed all 6,001,255 lineitem
   rows beside `Rows Removed by Filter: 5,143,567`. That is fixed
   (`docs/design/not_ralph/fix-pworker-bug/`), with **no runtime change** — it
   was only ever a reporting bug. Q12's real gap belongs to item 1,
   plan quality.
3. **The ~2× executor floor requires design change, not optimisation** —
   a narrower `Datum`, a row-ownership contract, or columnar batches. That is
   recorded in `REPORT-EXHAUSTION.md` §3 as out of scope for incremental work,
   with measured reasons.

Ordering follows directly: **(2) first** — it is a bug with a reproducer and a
bounded fix; then **(1)**, which is a programme; and **(3)** only with an
explicit architectural decision.

## 6. Method

goopg: part-4 after-arm minima (`measurements/tpch-part4-arms.txt`), fresh
capped server, `GOGC=100`, `GOMEMLIMIT=12GiB`, one warm pass discarded.
PG 18.3: `measurements/tpch-pg18-baseline.txt`, same host and settings, one
warm pass discarded, one measured pass.

Both engines were driven by the same `tpch-runner` binary over the same query
text, so query formulation is identical. Plans were captured with
`EXPLAIN ANALYZE` on each engine directly.

**Single measured pass on PG**, against goopg's minimum of four. That biases
*against* goopg if anything (a single PG pass may include noise PG's own
minimum would remove), so the ratios quoted are conservative in goopg's
favour — the true gap is unlikely to be smaller than stated.
