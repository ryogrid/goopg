# M0125-0026 — the TPC-DS timeout class, goopg's EXPLAIN next to PostgreSQL's

Date: 2026-07-31 · Branch `tpcds-fix2` · HEAD `927742f8`
Task: `.ralph/fix_plan.md` **M0125-0026** · Work plan:
`docs/design/0125-0026-timeout-class-plan-comparison.md`

**This is the artifact the design doc said did not exist:** goopg's plan and
PostgreSQL 18.3's plan, side by side, for every member of the timeout class.
Nothing here was executed. Every file is a plain `EXPLAIN`; `EXPLAIN ANALYZE`
was not run on goopg for any of these queries (they are, by definition, the
queries that do not finish).

## What was captured

| arm | dir | engine | regime |
|---|---|---|---|
| goopg default | `goopg-warm/` | goopg @ `927742f8`, :65437 db `postgres` | **warm statistics** (M0125-0029/-0030) + `GOOPG_RELSIZE_FALLBACK` default (stage 2) |
| goopg, fallback off | `goopg-relsize0/` | same server, restarted `GOOPG_RELSIZE_FALLBACK=0` | warm statistics, size fallback disabled |
| PostgreSQL 18.3 | `pg/` | :65438 db `tpcds05`, user `ryo` | ANALYZEd reference cluster |

Reproduce with `capture.sh <arm-dir> goopg|pg` (in this directory).

**18 queries**: the warm gate's 12 hard members (Q5 Q8 Q14 Q30 Q31 Q35 Q54 Q64
Q65 Q71 Q78 Q81) + Q72, which is in the hard set at `relsize=2` + the four the
size fallback ANSWERS (Q10 Q47 Q67 Q69 — kept as a contrast set) + Q18, the
warm-stats regression filed as `M0125-0033`.

SF0.5 cardinalities used for the arithmetic below (PG `pg_class.reltuples`):
`inventory` 4.71 M, `customer_demographics` 1.92 M, `store_sales` 1.44 M,
`catalog_sales` 721 k, `web_sales` 359 k, `store_returns` 143 k, `customer`
100 k, `time_dim` 86.4 k, `date_dim` 73.0 k, `catalog_returns` 71.9 k,
`customer_address` 50 k, `web_returns` 35.9 k, `item` 18 k,
`household_demographics` 7.2 k, `promotion` 300, `income_band` 20, `store` 12,
`warehouse` 5.

## Headline: two suspects are REFUTED, and the class has one dominant mechanism

### Refuted — suspect (b), "join order chosen with no cardinality signal"

**All 18 plans are byte-identical between `GOOPG_RELSIZE_FALLBACK=2` (the
shipped default) and `=0`.** Not "similar" — a zero-line `diff` on every
query. Once every relation is ANALYZEd the fallback never fires, because
`plan.go:580` stamps `SeqScan.EstRelRows` only when the relation is *not*
ANALYZEd. The flag is inert on a warm cluster.

This closes the arm-ON/arm-OFF experiment the work plan wanted, at plan level,
and it agrees with `M0125-0031`'s runtime finding from the other direction: the
12 hard members failed under all three cardinality regimes. **Cardinality is
not what is wrong with these plans. Shape is.**

### Refuted — suspect (d), "a CTE referenced N times has its body executed N times"

goopg's EXPLAIN *does* print the CTE body once per reference (Q31 six times,
Q47 three times, Q64 twice — an entire 18-relation join subtree, twice). That
is a plan-tree artifact of Stage-A clone-per-consumer inlining
(`internal/planner/plan.go:1071`, `CTEScan` — "purely a labeling artifact").
At runtime `executor.Build` routes `*planner.CTEScan` to `newCteScanOp`, which
materializes on first `Open()` into `ctx.CTERowCache` keyed by CTE name and
replays for every later reference (`internal/executor/executor.go:75-85`,
`internal/executor/context.go:537`). **The body runs once.** The real defect in
these queries is where the *qualifiers* land — class C2 below — not how often
the body runs. (What N clones do cost is N× planning work, which no measurement
here bounds.)

### The dominant mechanism: goopg emits Cartesian products

`Nested Loop (CROSS)` — `planner.JoinTypeCross`,
`internal/executor/operators_explain.go:1319` — appears **14 times across 8 of
the 18 queries**, always with the equi-join predicate demoted to a `Filter` on
the CROSS node or on its parent:

| Q | crosses | the predicate that was demoted |
|---|---:|---|
| Q64 | 4 | `c_first_sales_date_sk = d_date_sk AND c_first_shipto_date_sk = d_date_sk` (two `date_dim` copies, 73.0 k each) |
| Q14 | 2 | `i_brand_id = brand_id AND i_class_id = class_id AND i_category_id = category_id` |
| Q54 | 2 | `sold_date_sk = d_date_sk AND item_sk = i_item_sk` (+ the category/date restrictions) |
| Q65 | 2 | the `store`×`item`×aggregate join keys |
| Q8 | 1 | `substr(s_zip,1,2) = substr(ca_zip,1,2)` |
| Q30 | 1 | `ca_address_sk = c_current_addr_sk` |
| Q71 | 1 | `sold_item_sk = i_item_sk AND i_manager_id = 1` |
| Q81 | 1 | `ca_address_sk = c_current_addr_sk` |

PG produces a hash/merge/index join at every one of these sites. Example — Q71,
the same join:

```
goopg:  Nested Loop (CROSS)                 PG:  Nested Loop
          -> Seq Scan on item (18000)              -> Parallel Append   (the 3 UNION branches)
          -> *planner.SetOp                        -> Index Scan using item_pkey
        Filter: sold_item_sk = i_item_sk                 Index Cond: i_item_sk = sold_item_sk
```

Every one of the eight sites has a subquery on at least one side — a set
operation (`*planner.SetOp`), a CTE reference, or a derived aggregate. **Joins
between two base relations are fine everywhere in this capture; joins where an
input is a subquery degenerate to a Cartesian product.** That is the class.

## Classification table

`✔` = the class is present and is the plausible owner of the ≥20× gap; `·` =
present as an aggravator; blank = absent.

| Q | oracle / PG s | C1 cross | C2 qual placement | C3 uncached SubPlan | C4 set-op opaque | C5 no cardinality above scans | verdict |
|---|---|:-:|:-:|:-:|:-:|:-:|---|
| Q5 | 100 / 1 | | · | | ✔ | · | plan invisible below `SetOp` — C4 blocks classification |
| Q8 | 0 / 1 | ✔ | ✔ | | ✔ | · | C1 over the INTERSECT's zip list |
| Q10 | 0 / 11 | | ✔ | ✔ | | · | C3 — `EXISTS OR EXISTS`, PG uses `hashed SubPlan` |
| Q14 | 200 / 16 | ✔ | · | | ✔ | · | C1 inside `cross_items`, ×2 phases |
| Q18 | (warm regression) | | · | | ✔ | · | C4 blocks; see `M0125-0033` |
| Q30 | 31 / 4 | ✔ | ✔ | ✔ | | · | C1 + C3 compounded |
| Q31 | 19 / 4 | | ✔ | | | · | C2 — 5 of 6 per-reference filters hoisted to the top join |
| Q35 | 100 / 0 | | ✔ | ✔ | | · | C3 — the measured RC-8 instance |
| Q47 | 100 / 2 | | ✔ | | | · | C2 only; budget-marginal, see below |
| Q54 | 0 / 10 | ✔ | ✔ | | ✔ | · | C1 ×2 over a `SetOp` |
| Q64 | 2 / 0 | ✔ | ✔ | | | · | C1 ×4, two of them over full `date_dim` |
| Q65 | 100 / 0 | ✔ | ✔ | | | · | C1 ×2 (`store`×`item`×aggregate) |
| Q67 | 100 / 3 | | · | | ✔ | · | C4 blocks; the `rank() ≤ 100` filter IS present |
| Q69 | 100 / 0 | | ✔ | | | · | proper SEMI/ANTI chain — **fits no class**, see below |
| Q71 | 580 / 0 | ✔ | ✔ | | ✔ | · | C1 over the 3-channel UNION |
| Q72 | (fallback costs) | | ✔ | | | · | deep NL+index chain, no cross — **fits no class** |
| Q78 | 45 / 2 | | ✔ | | | · | C2 — `ss_sold_year = 1998` never reaches `date_dim` |
| Q81 | 100 / 15 | ✔ | ✔ | ✔ | | · | Q30's shape on `catalog_returns` |

### C2 is pervasive, and it is measurable

Across the 18 goopg plans, **2 of 68 `Filter:` lines sit on a `Seq Scan`**;
the other 66 sit on a join node. (The two exceptions are inside scalar
SubPlans in Q14 and Q54.) On the same six queries where PG was counted, PG puts
3, 3, 0, 2, 3, 3 filters at scan level.

The concrete consequence, seen in almost every plan here: goopg hashes all
**73,049** `date_dim` rows and applies `d_year = …` (and `d_moy` ranges) on the
join node afterwards. PG's `Parallel Seq Scan on date_dim  Filter: ((d_moy >=
3) AND (d_moy <= 6) AND (d_year = 2001))` yields **71 rows**. That is a **1000×**
larger build side, on the dimension every fact table joins to, in nearly every
member of the class.

Whether goopg's `Multi-Way Hash Join` operator *also* fails to pre-filter at
runtime is not settled by an `EXPLAIN`; what is settled is that the costing
sees the relation unfiltered — which is exactly RC-5 as filed
(`internal/planner/local_filters.go:154`, `SmallDimension` hardcoded to
`region`/`nation`, so no TPC-DS relation can qualify). The fix task must check
the executor side before assuming the runtime cost follows the plan text.

Q31 is C2's acute form. PG attaches the per-reference filter to each of the six
`CTE Scan` nodes (`Filter: ((d_qoy = 1) AND (d_year = 1999))`, …), cutting each
join input to roughly one quarter. goopg attaches exactly one (on `ws3`) and
hoists the other five into a single conjunction on the top join node.

### C3, with PG's answer in the same file

Q10 and Q35 are `... and (exists (…) or exists (…))`. goopg:

```
Hash Join (SEMI)
  Filter: (EXISTS(SubPlan 1) OR EXISTS(SubPlan 2))
  SubPlan 1 -> Hash Join (INNER)  rows=131280740
                Filter: (($0 = ws_bill_customer_sk) AND (d_year = 2001) AND …)
```

PG, same query: `Filter: ((ANY (c_customer_sk = (hashed SubPlan 2).col1)) OR
(ANY (c_customer_sk = (hashed SubPlan 4).col1)))` — **hashed**, built once.
goopg's is uncached and re-evaluated per outer row.

Q69 is the control that proves this is about the `OR`: its three EXISTS are
`and not exists … and not exists`, goopg unnests them into a proper
`Hash Join (ANTI)` / `Hash Join (SEMI)` chain, and Q69 completes. **An `OR` of
`EXISTS` cannot become a semi-join, and goopg has no fallback between
"unnest to a semi-join" and "re-execute per row".**

Note `0124-0004` §D4's standing rule: if `CacheMisses ≈ Calls`, the indicated
fix is hashed-SubPlan caching, **not** decorrelation. PG's own plan here is
hashed SubPlans, which corroborates it.

### C5 — the numbers in goopg's own EXPLAIN are the evidence

Every non-leaf node in all 18 goopg plans renders `cost=0.00..0.00 rows=1`.
Leaves carry real warm statistics (`Seq Scan on public.store_sales (stats) …
rows=1439608`). Where a join estimate *is* produced, it is the Cartesian
product of the inputs: Q10's SubPlan 1 reports `rows=131280740`, and
359,432 (`web_sales`) × 365.25 (`date_dim` after `d_year`) = 131,280,738. **The
equi-join key contributes no selectivity at all.**

So the DP is choosing among shapes it has costed as identical (all `0.00`),
which is why warm statistics changed nothing (`M0125-0031`) and why the size
fallback changes nothing here. C5 is why C1–C3 are never *corrected* by
costing, and it is the reason no cardinality-side work can reach goal (a).

## Step 3 — why 300 s is unreachable, per class (one significant digit)

| class | query | the product goopg forms | vs a 300 s budget |
|---|---|---|---|
| C1 | Q64 | stream(≈1×10⁵) × `date_dim` 7.3×10⁴ × `date_dim` 7.3×10⁴ ≈ **5×10¹⁴** row-pairs | short by ~8 orders of magnitude |
| C1 | Q54 | SetOp body (`catalog_sales`+`web_sales` ≈1×10⁶) × `item` 1.8×10⁴ × `date_dim` 7.3×10⁴ ≈ **1×10¹⁵** | ~9 orders |
| C1 | Q65 | (`store` 12 × `item` 1.8×10⁴) × aggregate groups (≈2×10⁵) ≈ **4×10¹⁰** | ~1–2 orders |
| C1 | Q71 | `item` 1.8×10⁴ × SetOp body (one month ≈2×10⁵) ≈ **4×10⁹** | ~1 order — consistent with a *marginal* timeout |
| C1+C3 | Q30/Q81 | CTE (≈2×10⁴) × `customer_address` 5×10⁴ = 1×10⁹ pairs, each surviving row rescanning the CTE ≈ **2×10¹³** | ~5 orders |
| C3 | Q10/Q35 | outer ≈1×10⁵ × (`web_sales` 3.6×10⁵ + `catalog_sales` 7.2×10⁵) ≈ **1×10¹¹** | Q35 measured at 8.16 s/outer row ⇒ **≈9 days at SF=1** (`0124-0004`); unreachable under any budget |
| C2 | Q31 | 6 join inputs each ~12× PG's (unfiltered quarter) ⇒ intermediate space up to 12⁵ ≈ **2×10⁵ ×** PG's 3.3×10⁴-cost plan | ~4–5 orders |
| C2 | Q78 | 3 channels aggregated over all ~5 years instead of `d_year = 1998` ⇒ ~**5×** the fact scan, ×3 | ~1 order — marginal |

At ~10⁷ row-touches/s, 300 s buys ≈3×10⁹ row-touches. Q71 and Q78 sit at that
line; everything else is 4–9 orders above it. Per `0124-0001` §D6 those two are
reported as **budget-marginal**, not as unbounded members.

**Q47 gets its own reading, as the work plan required.** It is C2-only, has no
cross product, and its single completion measurement is 142 s at SF=1
(`M0125-0013`). It is **budget-marginal**, not unbounded: fixing C2 alone
plausibly clears it.

## Queries that fit no class — findings, per the work plan

- **Q69** — the shape is *good*: `Hash Join (ANTI)` / `Hash Join (SEMI)` over a
  3-table MHJ, structurally comparable to PG's. It carries only C2 and C5, and
  it is one of the four the size fallback already answers. Nothing here needs a
  root-cause class.
- **Q72** — no cross product; a deep `Nested Loop` chain with `Index Scan`
  inners and a 4-table MHJ at the base. This is the query the size fallback
  makes *slower* (1.13×, `M0125-0005`), and its plan gives no reason why. The
  answer is not in its shape; leave it to `M0125-0005`'s open ledger row.
- **Q5, Q18, Q67** — cannot be classified at all. See C4.

## C4 — set operations are opaque to EXPLAIN, and that blocks this task

goopg prints `*planner.SetOp` — a raw Go type name — **with no children**.
For Q5, Q14 and Q18 the printed plan is four lines long and the entire query
body is below the opaque node. Seven of the eighteen queries contain one
(Q5 ×1, Q8 ×1, Q14 ×5, Q18 ×1, Q54 ×1, Q67 ×1, Q71 ×1).

Two consequences, and the second is the important one:

1. Q5, Q18 and Q67 could not be classified in this pass.
2. Every C1 cross product in this capture has a `SetOp` or a CTE/derived
   subquery on one side. The set-op node being an optimizer black box is the
   most likely *proximate* cause of C1 at those sites — the join-order DP
   cannot see a join key it cannot see through.

Related, and separate: goopg's EXPLAIN renders column references **unqualified**,
so real correlations print as self-comparisons — `Filter: (ctr_state =
ctr_state)` (Q30/Q81, where PG prints `ctr1.ctr_state = ctr_state`),
`(cd_marital_status <> cd_marital_status)` (Q64), `(d_week_seq = d_week_seq)`
(Q72). These are almost certainly distinct columns of a wide join tuple, but
**nothing in the output distinguishes that from a real always-true/always-false
predicate**, and Q31's top-level `(d_qoy = 1 AND … AND d_qoy = 2 AND … AND
d_qoy = 3)` reads as unsatisfiable for the same reason. A capture that cannot
be trusted to say what it means is not a usable instrument.

## Step 4 — the fix tasks filed from this classification

Filed in `.ralph/fix_plan.md`, largest class first. Every one inherits the
plan-shape bar (plan-diff `LABEL=tpcds-round2-head`, timed 22-query TPC-H on a
quiet host, full SF0.5 gate) and is accepted by **first-ever completion checked
against the git-tracked oracle row** — rows plus `ck` where the oracle has one.

| id | class | members | acceptance query |
|---|---|---|---|
| `M0125-0034` | C1 — Cartesian join wherever an input is a subquery | Q8 Q14 Q30 Q54 Q64 Q65 Q71 Q81 | Q71 (`71|OK|580|521a7af7…`, the least-deep member) |
| `M0125-0035` | C2 — qualifiers attached to the join, not the scan / not pushed into the producing subquery | all 18; acute in Q31 Q78 Q8 Q47 | Q78 (`78|OK|45|8f67acff…`) |
| `M0125-0036` | C3 — correlated SubPlan re-evaluated per outer row, no hashing | Q10 Q35 Q30 Q81 | Q10 (`10|OK|0|1f18d650…`); **Q35 is already `M0125-0003`'s acceptance row — do not duplicate it** |
| `M0125-0037` | C4 — set operations opaque to the planner and to EXPLAIN | Q5 Q8 Q14 Q18 Q54 Q67 Q71 | Q5 (`5|OK|100`) — but the EXPLAIN half must land first; it is what makes the rest measurable |
| `M0125-0038` | C5 — no cost/cardinality propagation above base scans | all | no acceptance query of its own; accepted when C1/C2 stay fixed under plan-diff |
| `M0125-0039` | diagnostics — EXPLAIN renders column references unqualified | — | Q30's `ctr_state = ctr_state` prints as `ctr1.ctr_state = ctr2.ctr_state` |

**Proposed selection order: `-0037` (EXPLAIN half only) → `-0039` → `-0034` →
`-0035` → `-0036` → `-0037` (planner half) → `-0038`.** The two diagnostic
items are small, host-independent, and every later item's evidence is unreadable
without them — three of the eighteen queries in this very capture could not be
classified for want of them.

## Not captured — deferred

TPC-H **Q21** (`M0125-0032`) was to be folded into this capture so one taxonomy
covers both benchmark families. It was not: the TPC-H clusters (:65432, :65433)
were both down, and standing them up is not host-independent work
(`bench/tpch/setup_goopg.sh` rebuilds the shared `tmp/goopg-bench-bin`). Q21's
capture belongs to `M0125-0032`; deferral-ledger row 2026-07-31.

---

## 2026-07-31 addendum — the C4 instrument was fixed, and the re-capture found C6

`M0125-0037` stage (i) landed the EXPLAIN half (design
`docs/design/0125-0037-explain-set-operations.md`). The seven set-op-bearing
members were re-captured against the same SF=0.5 goopg cluster, same plain-
`EXPLAIN`-only protocol, into **`goopg-warm-m0125-0037/`**
(`capture_setop_recheck.sh` is `capture.sh` with `QUERIES="5 8 14 18 54 67 71"`).
The three arms above are left untouched as the historical record.

| Q | plan lines before | after |
|---|---|---|
| Q5 | 4 | 128 |
| Q18 | 4 | 91 |
| Q67 | 6 | 94 |
| Q14 | — | 815 |

**The three unclassifiable rows in the table above now have a class.**

- **Q5 → C1 + C2.** `Nested Loop (CROSS)` between the
  `store_sales ∪ store_returns` `Append` and `date_dim`, repeated once per
  channel, with the `d_date` range predicate on the join's `Filter:` rather
  than on the scan — so `date_dim` is costed at its full 73,049 rows where PG's
  scan-level filter yields 8. PG hash-joins on `ss_sold_date_sk = d_date_sk`.
  Both existing classes; no new one.
- **Q18, Q67 → C6, a class this capture could not see.** Both are `ROLLUP`
  queries, and goopg expands the grouping sets into a UNION ALL of one
  independent aggregate branch per level, each re-running the entire join
  subtree. Q18: 4 branches, 5 `Multi-Way Hash Join`s, 5 full `catalog_sales`
  scans (720,657 rows each). Q67: 8 branches, 9 MHJs, 9 full `store_sales`
  scans (1,439,608 rows each). PG computes every level in ONE pass — Q18's PG
  plan is a single `GroupAggregate` with five stacked `Group Key:` lines, Q5's
  a `MixedAggregate` with `Hash Key` lines. **Neither query contains a
  `union all` in its SQL text**, which is exactly why C6 hid behind C4: the
  `Append` is goopg's own grouping-sets expansion, and it only became visible
  when the set-op node did.

C6 is filed as **`M0125-0040`** with the same bar as `-0034`. It is the most
likely proximate cause of the Q18 and Q67 timeouts and of the Q18 warm
regression (`M0125-0033`): an 8× multiplier on a 1.44 M-row join is not
something cardinality or join-order work can recover.

The selection order above is otherwise unchanged — next is `-0039`.
