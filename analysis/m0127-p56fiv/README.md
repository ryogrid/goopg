# M0127-P5.6-f-iv — PG has no functional-dependency arm for JOIN clauses

**Date:** 2026-08-05 · **Tree:** `786edcb6` · **Verdict:** the filed premise is
**REFUTED**. Implementing it as written would have added a **non-PG heuristic**
and aimed it at a symptom PostgreSQL 18.3 *shares*. The measured divergence is
somewhere else, and this note names it.

## 1. What was filed

§5.15 / the 2026-08-05 ledger row concluded that Q47's 31 s → 523 s regression
came from P5.6-f pricing every equi-pair under independence, and that

> PG does not multiply blind: `clauselist_selectivity` consults extended
> statistics first — `dependencies_clauselist_selectivity` /
> `statext_clauselist_selectivity` — … goopg landed the FK arm but has **no
> functional-dependency arm**.

The resume point was `internal/planner/cardinality.go:465-483`: damp pairs whose
columns are functionally dependent.

## 2. The oracle says extended statistics never see a join clause

`clauselist_selectivity_ext` (`postgres/src/backend/optimizer/path/clausesel.c`)
gates the whole extended-statistics branch on `find_single_rel_for_clauses`:

```c
	rel = find_single_rel_for_clauses(root, clauses);
	if (use_extended_stats && rel && rel->rtekind == RTE_RELATION && rel->statlist != NIL)
		s1 = statext_clauselist_selectivity(root, clauses, ...);
```

and `find_single_rel_for_clauses` bails on the first clause that is not
single-relation:

```c
		if (!bms_get_singleton_member(rinfo->clause_relids, &relid))
			return NULL;		/* multiple relations in this clause */
```

A join clause `a.x = b.y` has **two** relids by construction. So `rel` is `NULL`
for any clause list containing one, `statext_clauselist_selectivity` is not
called, and `dependencies_clauselist_selectivity` — which it fronts — never runs
on a join clause list in any PG version that has this gate. Extended statistics
in PG are a **restriction-clause** mechanism.

What remains for a multi-pair join in PG is exactly what goopg does: each pair
priced by `eqjoinsel` and multiplied together, blind, minus whatever
`get_foreign_key_join_selectivity` (costsize.c:5651) removed first — the arm
goopg already landed in P5.6-f.

## 3. Measured on the oracle: PG collapses Q47's join too

PG 18.3, `tpcds05` on :65438, plain `EXPLAIN` of `query47.sql` verbatim:

```
   ->  Merge Join  (cost=0.00..745.29 rows=1 width=400)
         Merge Cond: ((v1_lead.i_category = v1.i_category) AND (v1_lead.i_brand = v1.i_brand)
                  AND ((v1_lead.s_store_name)::text = (v1.s_store_name)::text)
                  AND ((v1_lead.s_company_name)::text = (v1.s_company_name)::text))
         Join Filter: (v1.rn = (v1_lead.rn - 1))
         ->  CTE Scan on v1 v1_lead  (cost=0.00..152.86 rows=7643 width=684)
         ->  Materialize  (cost=0.00..515.97 rows=1 width=1404)
               ->  Merge Join  (cost=0.00..515.97 rows=1 width=1404)
                     Merge Cond: (… same four columns …)
                     Join Filter: (v1.rn = (v1_lag.rn + 1))
                     ->  CTE Scan on v1 v1_lag  (cost=0.00..152.86 rows=7643 width=684)
                     ->  Materialize  (cost=0.00..286.62 rows=4 width=720)
                           ->  CTE Scan on v1  (cost=0.00..286.61 rows=4 width=720)
```

**`rows=1` on both correlated 5-pair joins.** PG's estimate is collapsed exactly
as goopg's is. The correlation between `i_category`↔`i_brand` and
`s_store_name`↔`s_company_name` is invisible to PG here for the reason in §2,
and additionally because the inputs are CTE scans with no column statistics at
all. **The join-cardinality collapse is therefore not the divergence.**

## 4. What actually differs: the size of the join's INPUTS

Same query, same scale factor, same two engines:

| node | PG 18.3 | goopg `096d3949` |
|---|---|---|
| the `v1` CTE (`CTE Scan on v1 v1_lead`) | **7 643** | **18** |
| the grouping node under it | `GroupAggregate rows=7643` | `HashAggregate (6 keys) rows=18` |
| that node's input | `Gather Merge rows=7643` | `Hash Join rows=36` |
| top join method | **Merge Join** | **Nested Loop** |
| top join estimate | rows=1 | rows=1 |

PG reaches a Merge Join from an estimate of 1 because a nested loop would have
to rescan a **7 643-row** CTE per outer row. goopg reaches a Nested Loop from
the same estimate of 1 because its inner is **18 rows**, and rescanning 18 rows
is free. The plan flip is downstream of a **425× under-estimate of the v1
subtree**, not of the 5-pair collapse. P5.6-f only supplied the last nudge that
tipped an already-mispriced comparison; the 18 predates it (`30293f78` has the
same 18 and picks the Hash Join).

## 5. Where the 425× comes from: a pushed-down restriction is charged twice

goopg's `HashAggregate rows=18` is `child/2` over `Hash Join rows=36` — so the
whole error is in that join. Isolating it on the live SF0.5 cluster (:65437, db
`postgres`), same four-table join, varying only the `date_dim` restriction:

| restriction | `date_dim` scan | join below `store` | join **above** `store` | extra factor |
|---|---|---|---|---|
| *(none)* | 73 049 | 1 439 608 | 1 439 608 | **1.0** |
| `d_year = 2000` | 365 | 7 193 | **35** | **≈ 1/205** |
| `d_dom = 15` | 365 | 7 193 | **35** | **≈ 1/205** |
| `d_year = 2000 OR (d_year=1999 AND d_moy=12) OR (d_year=2001 AND d_moy=1)` | 368 | 7 252 | **36** | **≈ 1/201** |
| `d_year > 1999` | 36 889 | 726 987 | **367 128** | **≈ 0.505** |

The `store` join is `ss_store_sk = s_store_sk` against a 12-row relation whose
`s_store_sk` is unique, so it must be **row-preserving**: 7 193 × 12 / 12 =
7 193. It is not. The extra factor tracks the restriction that was **already
applied at the `date_dim` scan** — 0.505 for the inequality (the scan applied
36 889/73 049 = 0.505), ~0.005 for each equality (the scan applied
365/73 049 = 0.005). With no restriction the factor is exactly 1.0.

That is a textbook **double-count of a pushed-down baserestrictinfo**, and it is
precisely the trap `joinResidualSelectivity`'s own header says it prevents
("only conjuncts referencing BOTH sides count. A single-sided conjunct … is
already priced into `EstimateRows(j.Left)`"). PG does not do it: its `store`
join is 2 583 → 2 465, no re-charge.

Note it fires **once**, at the topmost join, not at every join above the scan —
consistent with the restriction surviving as a copy in only that join's
`Predicate` (which is also where `EXPLAIN` renders it as `Filter:`).

### What is ruled out

`joinResidualSelectivity`'s guard is `if exprSide(c, leftWidth) != sideMixed
{ continue }`, and `exprSide` was verified correct in isolation
(throwaway probe): `col = const` → `sideLeft` (1), `col = col` across the width
→ `sideMixed` (3). A one-sided conjunct is therefore skipped by that guard, so
**the leak is not the residual guard as written**. The two remaining candidates
inside `estimateJoin` are

* `joinEquiPairs` → `splitAllEqualitiesForHash` admitting `d_year = 2000` as an
  equi-*pair* (its right operand is a constant), after which the pair loop
  divides by `pairNDistinct` — which for `d_year` on SF0.5 is ≈200, numerically
  indistinguishable from `defaultEqSelectivity` = 0.005 in the equality rows
  above; the `d_year > 1999` row (factor 0.505, neither 1/nd nor 0.005) says at
  least that row is **not** this arm; and
* the residual reaching `clauseSelectivity` by a path that bypasses the
  `exprSide` filter.

Discriminating them is a unit-test question, not a sweep question: build a join
whose left input is a `*Filter` over a scan with the same conjunct left in
`Predicate`, and assert the estimate equals the unfiltered one scaled once.

## 6. Consequences for the filed work

1. **M0127-P5.6-f-iv as filed is closed as refuted.** Damping correlated pairs
   would move goopg *away* from PG (§2, §3) and would be a heuristic dressed in
   an upstream citation.
2. The Q47 regression's real chain is: *pushed-down restriction double-charged*
   (§5) → *v1 sized 18 instead of 7 643* (§4) → *nested loop looks free* →
   *quadratic at runtime*. Only the first link is a PG-divergence; the rest
   follows.
3. `estimateAggregate`'s `child/2` for a multi-key GROUP BY is a **second,
   independent** gap on the same path (PG runs `estimate_num_groups`,
   selfuncs.c). It is not load-bearing for Q47 — with the input corrected to
   ~7 600, `child/2` would still give ~3 800, the same order as PG's 7 643 —
   but it is real and should be filed separately, not folded in.
4. Nothing here changes correctness. Q47 returned its 100 oracle rows in every
   regime; this is cost only.

## 7. Reproduction

```bash
bench/tpcds/server.sh start pg      # PG 18.3 oracle, :65438, db tpcds05
{ echo EXPLAIN; cat bench/tpcds/runtime_goopg/tpcds-data/queries/query47.sql; } \
  | postgres/local_install/bin/psql -h 127.0.0.1 -p 65438 -U ryo -d tpcds05

bench/tpcds/server.sh start sf05    # goopg SF0.5, :65437, db postgres
postgres/local_install/bin/psql -h 127.0.0.1 -p 65437 -U ryo -d postgres -c \
 "EXPLAIN select count(*) from item, store_sales, date_dim, store
   where ss_item_sk = i_item_sk and ss_sold_date_sk = d_date_sk
     and ss_store_sk = s_store_sk and d_year > 1999;"
```

The goopg probes need no `ANALYZE` preamble: SF0.5 column statistics persist
(M0125-0028/-0029), which is why `sf05_capture_plans` has none either.
