# M0125-0035 — C2: single-table qualifier placement

Status: **partially landed** (binary-join arm). Filed by `M0125-0026` as class
C2 of the TPC-DS timeout taxonomy; the item stays open for the two arms named
in §6.

Related: `docs/design/0125-0004-q75-join-residual-evaluation-order.md` (the pass
this extends), `docs/design/0125-0026-timeout-class-plan-comparison.md` (the
capture that filed C2), `docs/design/0125-0034-setop-join-promotion.md` (C1).

---

## 1. The defect as filed

Across the 18 captured goopg plans, **2 of 68 `Filter:` lines sat on a
`Seq Scan`**; the other 66 sat on join nodes. The named cost: goopg hashes all
**73,049** `date_dim` rows and applies `d_year = …` afterwards, where PG's
`Parallel Seq Scan on date_dim Filter: ((d_moy >= 3) AND (d_moy <= 6) AND
(d_year = 2001))` yields **71** — a ~1000× larger build side on the dimension
that nearly every fact table joins to.

`M0125-0026` did not settle whether that plan text is *truthful*, and made the
determination the item's mandatory first step:

> **First step is not a fix:** determine whether the `Multi-Way Hash Join`
> operator pre-filters its build side at RUNTIME despite the plan text — an
> `EXPLAIN` cannot settle that, and the answer decides whether this is a
> costing-only defect or an execution one.

## 2. The determination: it is an EXECUTION defect

Measured on the SF0.5 cluster (`:65437`) at `91f530c9`, serial
(`max_parallel_workers_per_gather = 0`) so per-node counters are not hidden
under a `Gather`. `EXPLAIN ANALYZE` is a **counting** instrument, so this
reading is valid on the loaded host the nightly CI batch was holding; no
timing is claimed from it.

**Binary hash join** — `store_sales ⋈ date_dim WHERE d_year = 2002`:

```
->  Hash Join (INNER)  (cost=… rows=57686190) (actual … rows=1374770.00 loops=1)
      Filter: (date_dim.d_year = 2002)
  ->  Seq Scan on public.store_sales (stats)  (actual … rows=1439608.00 loops=1)
  ->  Seq Scan on public.date_dim   (stats)  (actual … rows=73049.00   loops=1)
```

The build side delivers **73,049 rows — the whole table** — and the join emits
**1,374,770** rows to produce a **275,107**-row answer. The qual is a
post-join residual.

**Multi-Way Hash Join** — `customer ⋈ customer_address ⋈ customer_demographics
WHERE ca_state IN ('IL','TX','ME')`:

```
->  Multi-Way Hash Join (3 tables)  (actual … rows=96562.00 loops=1)
      Filter: (ca.ca_state = ANY ('IL', 'TX', 'ME'))
  ->  Seq Scan on public.customer_address ca   (actual … rows=50000.00   loops=1)
  ->  Seq Scan on public.customer_demographics (actual … rows=1920800.00 loops=1)
  ->  Seq Scan on public.customer c            (actual … rows=100000.00  loops=1)
```

Same answer: `customer_address` is hashed whole (50,000) and the MHJ emits
96,562 rows for an 11,049-row answer.

The code agrees with the measurement. `multiHashJoinOp.Open`
(`internal/executor/multi_hash_join.go`) drains each build child through
`drainRowsCtx` and hashes **every** row; `partitionFilters` then classifies a
build-table qual by "the deepest chain step its referenced columns require" and
evaluates it at **step** time — after the hash table exists, once per matched
pair. Nothing pre-filters a build side.

**Conclusion: C2 is an execution defect as well as a costing one, and the plan
text is truthful.** Both engines answer correctly today (275,107 and 11,049
both equal PG); what is wrong is the work done to get there.

## 3. Why the qual was stranded on the join node

Two passes can place a single-relation restriction, and neither reached these
sites.

**Slice A** — `partitionConjunctsForJoinPlanning` + `attachRelationLocalFilters`
(`internal/planner/local_filters.go`) is goopg's analogue of PG's
`distribute_restrictinfo_to_rels`, but `shouldAttachBeforeMHJ` gates it:

```go
if costDrivenJoinOrder {
        return len(bindings) >= 2
}
if len(bindings) < 5 {
        return false
}
for _, b := range bindings {
        if b.table != nil && b.table.SmallDimension {
                return true
        }
}
return false
```

`costDrivenJoinOrder` is default-off, so production planning needs ≥5 tables
**and** a `SmallDimension` relation — and `SmallDimension` is a hardcoded
name-tag, set only for `region` and `nation`
(`internal/initdb/open.go`: `tr.RelName == "region" || tr.RelName == "nation"`).
**No TPC-DS relation can ever qualify.**

**M0125-0004's pass** — `pushSingleSideQualsIntoInnerJoinInputs`
(`internal/planner/inner_join_qual_pushdown.go`) already implements exactly the
right transformation, and cites PG's `distribute_restrictinfo_to_rels` for it.
Its D2 scoping deliberately excluded base-relation leaves:

> Scoping (D2) is deliberately narrow: the target input must be a CTE
> reference, never a base-relation leaf. Pushing filters toward base-relation
> leaves is exactly what `shouldAttachBeforeMHJ` withholds behind its
> `SmallDimension` guard, whose comment records that without it "Slice A
> regresses Q8 / Q21 from PASS to CANCEL".

## 4. What landed: the D2 leaf scoping is retired

That scoping borrowed the wrong risk. The two passes are not comparable:

| | Slice A | `pushSingleSideQualsIntoInnerJoinInputs` |
|---|---|---|
| when | **before** DP enumeration | **last** (`planner.go`, after `remapWithBindings` and `pushSingleSourceFiltersAfterRemap`) |
| what | **MOVES** the conjunct out of the DP's input | **DUPLICATES** it; the residual is untouched |
| effect on join ORDER | changes it — this is what "Q8 / Q21 PASS → CANCEL" recorded | none; the order is already fixed |

So admitting a leaf to *this* pass changes what a scan **emits**, never which
join is built first. `innerJoinPushEligibleInput` now also accepts `*SeqScan` /
`*IndexScan` / `*IndexOnlyScan` (`innerJoinPushLeafScan`).

Two details carry the correctness:

1. **`LeafLocal`.** A `Filter` above a leaf carries leaf-local `ColumnRef`s by
   the M0077-0001 convention — `attachRelationLocalFilters` sets the same flag
   on the same shape. `shiftConjunctForInput` has already put the duplicated
   conjunct in the input's own coordinate space (`-leftWidth` on the right
   side, `0` on the left), so the flag states what is true. A CTE-reference
   target keeps M0125-0004's behaviour (flag clear).
2. **Idempotence.** A planned subtree is walked again whenever an enclosing
   scope's `planSelect` reaches this pass with the subtree embedded, so the
   AND-in branch now declines a conjunct already present. Without the guard
   TPC-DS Q69 printed
   `(((((d_year = 2002) AND (d_moy >= 1)) AND (d_moy <= 3)) AND (d_year = 2002)) AND …)`
   — the same rows, but a wasted re-evaluation per row and a divergence from
   PG's plan text. `exprEqual` is **fail-closed**, so an undecidable conjunct
   degrades to the old duplicate, never to a dropped qual.

Property 1 (positional name validation, RC-1b) and property 4 (INNER only) are
untouched.

## 5. Measured effect

**Qual placement.** Re-capturing `M0125-0026`'s 18-query set
(`analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0035/`): scan-level
`Filter:` lines go **5 → 71**, and all 18 plans change. The witness, Q69:

```
-  ->  Hash Join (INNER)  (cost=… rows=525809623)
+  ->  Hash Join (INNER)  (cost=… rows=287921)
       ->  Seq Scan on public.store_sales (stats)
       ->  Seq Scan on public.date_dim (stats)
+            Filter: (((d_year = 2002) AND (d_moy >= 1)) AND (d_moy <= 3))
```

The build side is now restricted and the estimate becomes truthful — the
cardinality was wrong by 1,826× at that node, which is also why C2 was filed as
an aggravator of every other class.

The 2-table probe after the fix: `date_dim` scan carries
`Filter: (d_year = 2002)`, the join emits **275,107** instead of 1,374,770, its
estimate moves 57,686,190 → **288,395** against an actual 275,107, and the
answer is unchanged at 275,107 = PG.

**TPC-H plans** (`make plan-diff LABEL=m0125-0034-setop-join-promotion`,
`analysis/m0125-0035-c2-qual-placement/plan-diff-vs-0034.txt`): **4 / 22
DIFFER** — Q12, Q14, Q16, Q20. Every node-kind line in the diff appears an even
number of times (once removed, once re-added at a shifted offset), i.e. **zero
structural change**; the semantic delta is exactly **4 net-new scan-level
`Filter:` lines and 0 removed**, which is property 2 (duplicate, never move)
holding end-to-end on real plans:

```
+ Filter: (n_name = 'CANADA')                                    -- Q20
+ Filter: ((l_shipdate >= '1995-09-01') AND (l_shipdate < …))     -- Q14
+ Filter: (((p_brand <> 'Brand#45') AND (p_type NOT LIKE …)) …)   -- Q16
+ Filter: (((((l_shipmode = ANY ('MAIL','SHIP')) AND …)) …)       -- Q12
```

**Correctness.** Full 99-query SF0.5 gate, four contiguous `QUERIES=` chunks on
one binary (`analysis/m0125-0035-c2-qual-placement/sweep-chunk[1-4].txt`):

```
PASS=87 (53 ck-verified)  MISMATCH=0  CKMISMATCH=0  ERROR=0  TIMEOUT=8  SKIP=4
```

**Zero correctness failures across all 99**, which is the bar that matters for
a pass that now fires on nearly every query. All eight timeouts are
already-filed open items and **no new timeout appeared**: Q30 Q64 Q65 Q81
(`M0125-0034`'s open C1 arm), Q18 (`M0125-0033`), Q35 (`M0125-0003`'s
acceptance), Q31 and Q78 (this item's own remaining arms, §6).

Run under `FORCE=1` while the nightly CI batch held the host — legitimate for
row-count/checksum work per the fix_plan banner, **but every wall-clock number
in those reports is contaminated and none is quoted as a timing.** Q47 (306 s)
and Q72 (325 s) reported `PASS` above the nominal 300 s cap for that reason.

`scripts/tpch-spotcheck.sh`: `RESULT=PASS`, Q12 = 2 rows, Q13 = 35 rows — and
Q12 is one of the four queries whose plan changed.

## 6. NOT fixed — the item stays open

**(a) The acceptance query, Q78, is untouched.** Its acute form is not a
join-node qual at all: `ss_sold_year = 1998` sits on the residual above two
`Hash Join (LEFT)` nodes and refers to a **CTE output column** that renames
`d_year`. Two separate reasons this pass declines:

- **property 4** — INNER joins only. goopg has no `nullingrels` model, so it
  declines every outer join. PG pushes here because `ss` is the *preserved*
  side, where a restriction is always safe; that is a well-defined extension
  (preserved side only) and does not need the full model.
- **property 3** — it must not rewrite a CTE **body**, because a
  multiply-referenced body would receive one reference's restriction. PG
  instead **inlines** a non-recursive CTE referenced exactly once (PG 12+
  `cte_inline`) and then pushes into the resulting subquery, which is how
  `d_year = 1998` reaches `date_dim` in PG's plan. Q78's three CTEs are each
  referenced once; Q31's `ws3` is not, which is why PG attaches a
  *per-reference* filter to each of its six `CTE Scan` nodes.

This is the same boundary the fix_plan predicted `M0125-0034`'s CTE-reference
arm shares with this item; the two should be taken together.

**(b) The MHJ arm is unchanged.** `pushSingleSideQualsIntoInnerJoinInputs`
targets `*Join` inputs; `MultiHashJoin.Tables` is owned by
`pushSingleSourceFiltersIntoMHJTables`, whose walker disqualifies any conjunct
containing an `InExpr`. That is why `ca_state = ANY ('IL','TX','ME')` stayed on
the MHJ node in the §2 probe. `conjunctIsLocalEligible` already draws the
correct distinction (`InExpr` with `Plan != nil` is a subquery and unsafe; a
plain IN-list is fine), so the fix is small — but the walker is a documented
executor/planner **sibling pair**, so both must move together.

**(c) `SmallDimension` is still a hardcoded name-tag** and Slice A is still
gated off for TPC-DS. This change routes around it rather than fixing it; the
cardinality that reaches the **DP** is still unfiltered, so join *order* is
still chosen for full-table sizes. That is the costing half of C2 and it is
what `M0125-0038` (no cost/cardinality propagation above base scans) and the
`docs/design/cost-model/` "0077 line" own.

Ledger rows appended 2026-07-31 for (a), (b) and (c).
