# GAP-ANALYSIS's "parallel work division bug": a mislabel, and the real defect

Status: DESIGN (rev 2, after adversarial review). Branch
`fix-parallel-worker-bug`. Date: 2026-09-08.
Origin: `docs/design/not_ralph/optimize-row-decode/GAP-ANALYSIS.md` §2(b), §5.2.

## 0. Summary

`GAP-ANALYSIS.md` §5.2 called parallel work division "a concrete, isolated
bug". **The label is wrong; the measurement under it is right.**

1. **goopg's parallel work division is correct** and PG-faithful. There is no
   work-division bug to fix.
2. **The 6,001,255 rows per worker in Q12 are REAL WORK**, not a reporting
   artefact — ~94% of that query's runtime is inside that node. GAP-ANALYSIS's
   *observation* stands; only its *diagnosis* was wrong, and GAP-ANALYSIS
   already gives the correct explanation two paragraphs later (the E-18
   parallel merge join re-reads its inner per worker).
3. **A separate, real reporting bug exists** and is what made the mislabel
   easy: `EXPLAIN ANALYZE` prints a collapsed node's **pre-filter** row count
   beside its filter's removal count. It is bounded and testable, and it is
   what this document designs a fix for.

**Rev 2 corrects rev 1**, which claimed (2) was itself the reporting bug —
contradicting its own §1.1. An adversarial review caught the contradiction and
measured the timing that settles it.

## 1. Why there is no work-division bug

### 1.1 PG does the same thing — this is the whole refutation

A partial join in PG pairs a **partial outer** with a **complete inner**:

```c
/* postgres/src/backend/optimizer/path/joinpath.c:1437-1443 */
cheapest_partial_outer = (Path *) linitial(outerrel->partial_pathlist);
if (inner_path->parallel_safe)
    cheapest_safe_inner = inner_path;
else if (save_jointype != JOIN_UNIQUE_INNER)
    cheapest_safe_inner =
        get_cheapest_parallel_safe_total_inner(innerrel->pathlist);
```

Every worker runs the **whole** inner. For a merge join there is no shared
build to divide, so this is semantics, not a missing optimisation. goopg
implements exactly this, with the citation, in
`internal/optimizer/joinpathsparallel.go:250-254`.

That single fact is the refutation. It stands alone and needs no measurement.

### 1.2 The partitioning mechanism is exclusive by construction

Where goopg *does* divide work — the `orders` side of Q12 is a Parallel Index
Scan — the division is by **exclusive leaf-block claim**
(`internal/executor/parallel_scan.go:230-275`, `claimLeaf` via `LoadOrStore`),
so no two workers can take the same block. The observed sum of exactly
1,500,000 rows is therefore structural, not a coincidence to be explained.

**Rev 1 used that sum as its refutation. That was a category error**: the
`orders` outer proves nothing about a claim made about the `lineitem` inner.
Rev 1 also argued "parallel is faster than serial, so nothing is wrong", which
is a non-sequitur — redundant per-worker inner work is routinely net-positive
when outer partitioning dominates, and *correctness* of division is not
observable in wall time at all.

### 1.3 One latent inconsistency worth recording

`attachParallelScan`'s `joinOp` arm is fail-closed on non-hash algorithms
(`internal/executor/parallel_scan.go:164-173`), but `attachParallelIndexScan`
(`:348-353`) and `attachParallelBitmapScan` (`:214-219`) call bare
`probeSideIsLeft(x.plan)` with no algorithm check. They are *accidentally*
correct — `probeSideIsLeft` returns `!BuildLeft`
(`internal/executor/parallel_hash_build.go:184-191`) and a merge join leaves
`BuildLeft` false — but the seq-scan walk's own comment at `:160-163` says that
field must not be trusted for a merge join.

Not live, and not fixed here. Recorded because §1's claim is "work division is
correct **today**", not "cannot break": if that field ever changes meaning, two
of the three walks would attach shared claim state to a side that must be read
whole, which is silent row loss.

## 2. The real defect: collapsed nodes print pre-filter rows

### 2.1 Witness — and it is not scan-specific

```sql
SELECT l_shipmode, count(*) FROM lineitem GROUP BY l_shipmode
HAVING count(*) > 858000;
```

goopg:

```
HashAggregate  (actual rows=7.00 loops=1)
  Group Key: l_shipmode
  Filter: (count > 858000)
  Rows Removed by Filter: 6
  ->  Seq Scan on lineitem  (actual rows=6001255.00 loops=1)
```

The true answer is **1** row. The node claims 7 produced and 6 removed. The
same 7 − 6 = 1 signature appears on the index-scan case that started this
investigation:

```
->  Index Scan using idx_lineitem_orderkey_fkidx on lineitem
      (actual rows=6001255.00 loops=1)
      Index Cond: (l_orderkey > 0)
      Filter: ((l_orderkey > 0) AND (l_shipmode = 'MAIL'))
      Rows Removed by Filter: 5143567
```

6,001,255 − 5,143,567 = 857,688, which is what the **seq scan** reports for the
same predicate.

### 2.2 Root cause — general, not scan-specific

**Rev 1 rooted this in "`indexScanOp` does not implement
`filterRemoveCounter`". That is wrong.** The cause is that
`walkPlanAnalyzeFiltered` (`internal/executor/operators_explain.go:1553-1571`)
collapses a `*optimizer.Filter` into **whatever child it has** and then renders
that child's `rowsOut` (`:1612` timing, `:1615` non-timing). `rowsOut` counts
rows the *child* returned (`internal/executor/instrument.go:200`), which is
pre-filter relative to the collapsed line.

`seqScanOp` is the **only** case that reads correctly, and by accident: E-17
cut 2 absorbed the qual into the operator, so its own `rowsOut` is already
post-filter and no `filterOp` exists above it.

Every other collapsible parent is therefore affected — HashAggregate (§2.1),
Sort, Materialize, SubqueryScan, bitmap heap and index-only scans.

**Joins are NOT affected**, verified: a cross-table qual becomes a Join Filter
counted on the join node, and `rows` there is post-filter
(`Nested Loop (actual rows=3.00) / Rows Removed by Join Filter: 227`).

### 2.3 PG's behaviour, confirmed

`rows = instrument->ntuples / nloops` (`explain.c:1835`); `ntuples` increments
only for a **returned** tuple (`execProcnode.c:487`); `nfiltered1` is tracked on
the same node (`execScan.h:245`) and printed by `show_instrumentation_count`
(`explain.c:3965-3988`). Empirically on PG 65432:
`Index Scan … rows=856819.00 / Rows Removed by Filter: 5142016` — post-filter,
and PG does **not** repeat the index cond in the Filter.

### 2.4 Why fix a reporting bug

- **It caused a wrong engineering conclusion** — §0, this document's own rev 1,
  and GAP-ANALYSIS §5.2.
- **It diverges from PG**, which this project treats as absolute, and any tool
  comparing `rows=` across engines is comparing different quantities.
- **It is self-inconsistent inside one goopg plan**: two scan kinds with the
  same predicate report incompatible numbers.

## 3. Design

**Render a collapsed line wholesale from the outermost Filter's stats**, not
field-by-field from two operators.

The estimate block already does exactly this — `rowSrc = attachedFilterNode`
(`internal/executor/operators_explain.go:1600-1607`). The ANALYZE block must
follow, taking `rows`, `time` and `loops` from the same source. Taking only
`rows` from the filter, as rev 1 proposed, would produce a **third** mixed
line.

**The fix must also cover worker stats, or it does not fix the motivating
plan.** Q12's 6,001,255 figures are `Worker N:` lines rendered from
`w.RowsOut` (`:1710`), and nodes below a Gather have no `stats[n]` entry at
all — they live only in `workerStats[n]`. A main-line-only fix leaves Q12's
output byte-identical. The same substitution is therefore required at the
`workerStats` lookups (`:1650`, `:1706`).

Not in scope:

- **Absorbing the qual into `indexScanOp`.** Rev 1 excluded this as risky; the
  better reason is that **it would not fix the bug** — it removes one instance
  and leaves HashAggregate, Sort and Gather-collapsed nodes wrong. It is a
  *performance* item (§4's duplicated index cond), not the correct reporting
  fix.
- The `loops` divergences in §4.

### 3.1 Correctness plan

- **A general invariant test, not a two-scan-kinds test.** Rev 1 proposed
  asserting seq and index scans agree; that would leave most instances live,
  and the forcing mechanism is unreliable (`SET enable_indexscan = off` is
  accepted but did **not** change the plan on this build, despite
  `internal/optimizer/planner.go:3929-3947`). Assert instead: **any node
  printing `Rows Removed by Filter: R` must report `rows` equal to what its
  parent consumed.** Pin the HashAggregate/HAVING case of §2.1 explicitly,
  since it is constructible without a plan-forcing GUC.
- **A Gather case**, or F7 recurs: a plan with a collapsed Filter below a
  Gather must have its `Worker N:` rows corrected too.
- **`Rows Removed by Filter` is not already correct** — under a Gather it is
  *absent*, because both it and the `(actual …)` suffix are guarded on
  `stats[n]` (`:1659-1661`). Rev 1 asserted it was correct and must not change;
  that claim is deleted.
- Non-ANALYZE `EXPLAIN` byte-identical; plan-gate pin unmoved; units scope.

## 4. Known divergences NOT fixed here

Recorded so they are not mistaken for this fix's scope, and because §1's
"correct" claims are about work division only:

- `rows=` is **not** divided by `loops` (`:1612`/`:1615`) while PG divides
  (`explain.c:1835`), yet `Rows Removed by Filter` **is** divided (`:1660`) —
  two numbers on one line in different units when `loops > 1`.
- Parameterised inner index scans print no `(actual …)` at all; PG shows
  `rows=0.65 loops=55`.
- goopg labels the leader `Worker 4` beside `Workers Launched: 4`; PG labels
  only real workers and folds the leader into the parent line.
- In Q12, `Gather` and `Partial HashAggregate` report `rows=0.00` though 2 rows
  reach the Finalize node.
- goopg **duplicates the index cond into the Filter** where PG strips it —
  wasted per-row work as well as a plan-text divergence.

## 5. What this item does NOT deliver

**No performance change is expected or claimed.** This fixes what EXPLAIN
*says*, not what the executor *does*.

Q12's 15.1× gap versus PG is real and is a **plan-choice** problem: goopg picks
a merge join whose complete inner is re-read per worker, where PG picks a
filtered scan feeding 31,354 index probes. That is the plan-quality programme
in `GAP-ANALYSIS.md` §5.1, not a bounded bug.

## 6. Required correction to GAP-ANALYSIS.md

- **Retract** §5.2's "concrete, isolated bug … bounded and testable" framing.
- **Keep** §2(b)'s measurement — 6,001,255 rows per worker is real work.
- Replace the diagnosis with a cross-reference to GAP-ANALYSIS's **own** E-18
  paragraph, which already states it correctly.
- Note that the reporting bug of §2 is what made the mislabel easy.
