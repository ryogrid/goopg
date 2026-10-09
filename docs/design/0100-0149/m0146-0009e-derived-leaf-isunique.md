# M0146-0009e — isunique + leaf-tuples propagation for FROM-clause derived leaves

Parent: M0146-0009c recon (analysis/m0146/m0146-0009c). Kind: impl.

## Problem

TPC-DS Q83 joins three grouped derived leaves (`sr_items`, `cr_items`,
`wr_items`, each `GROUP BY i_item_id`). Goopg estimated the merge joins at
`rows=1` while PG 18.3 estimates `rows=10` on identical leaf estimates
(20 / 10 groups). The gap was not the selectivity formula — it is
`eqjoinsel_inner`'s `(1-nf)(1-nf)/max(nd1,nd2)` on both sides — but what
fed it: `nd` came out as `DEFAULT_NUM_DISTINCT=200` in goopg versus the
leaf row count in PG.

## Upstream behavior (selfuncs.c `examine_variable` → `examine_simple_variable`)

Two independent facts goopg was not supplying for a Var operand over a
FROM-clause derived leaf (`*CTEScan`, `*SubqueryScan`):

1. `vardata->rel = find_base_rel(root, var->varno)` — **unconditional**
   (selfuncs.c:5331). A leaf var's `tuples`/`rows` are the LEAF rel's own
   estimates, even when the leaf has no catalog statistics at all.
   Consequence: `get_variable_numdistinct`'s statless arms see the leaf's
   size — `nd = min(leaf.tuples, 200)` for a statless var, `nd = tuples`
   for a unique one.
2. RTE_SUBQUERY / RTE_CTE arm (:5865-5883): when the body's top query has
   a **lone** `groupClause` or `distinctClause` and the probed output
   column is that key, `vardata->isunique = true` — then `nd = (1 -
   nullfrac) * tuples` unconditionally ("assume it is unique no matter
   what pg_statistic says"). `setOperations`, `groupingSets`, multi-key
   grouping, and non-Var outputs all punt — the arm's early exits are the
   behavior, not an accident.

The arm's third clause — recursing `examine_simple_variable(subroot, var)`
for a passthrough output var to recover the base column's real statistics
— is NOT ported here; see the deferral ledger row. It is the
`cteColPassthrough` class (Q95's `ws_wh.ws_order_number`), orthogonal to
the lone-key marking.

## Why goopg estimated 1

Q83's leaves are inlined CTEs (`*CTEScan`) / derived tables
(`*SubqueryScan`) whose bindings carry a **synthetic** `catalog.Table`
(planner.go:4564 — name + column names, no statistics). That made
`resolveJoinVarColumn` SUCCEED, so examination took the catalog `ok` path
with `v.tuples = baseRows = 0` — the `!ok` arm's `subqueryUniqueOutput`
channel, which covers only pulled ANY-subquery leaves, was never reached.
Zero tuples → `DEFAULT_NUM_DISTINCT=200` → sel `0.005` → `20*10*0.005 = 1`.

## Change

| file | change |
|---|---|
| `cte_stats_synthesis.go` | `cteOutputColStats.unique`; shared classifier `loneKeyPositions(body, width)` — Aggregate lone `GroupExprs` (keys occupy output positions `[0, len(GroupExprs))`), lone-key `DistinctOn`, single-column `Distinct`; peels Filter/Sort/Limit, the `*Project` position remap, and pushed-qual `*SubqueryScan` labels. |
| `cardinality.go` | `baseRelInfo.{leafTuples, leafRows, uniqueOutCols}` — per-column uniqueness, replacing the leaf-wide bool for this class. |
| `joinsearchseam.go` | prefix-leaf init: `isSubplanLeaf(scans[i])` → `leafTuples = EstimateRows(scans[i])`, `leafRows = EstimateRows(leaves[i])` (leaf-local quals applied), `uniqueOutCols = derivedLeafUniqueCols(scans[i])`. |
| `joinselectivity.go` | `examineJoinVar`: `ok` path overrides tuples/rows with the leaf's estimates when `leafTuples > 0` and marks `isUnique` via `uniqueOutCols[cr.Name]`; `!ok` arm generalized — `rel = leaf` tuples for derived/table-less leaves plus per-column `isUnique` (`subqueryUniqueOutput` kept for the pulled-ANY path). `derivedLeafUniqueCols` maps body-output positions to the leaf's own (post-alias) output names. |

The classifier runs once per CTE through the existing
`plannedCTE.outputStats()` memoization; `*SubqueryScan` leaves classify
their `Child` directly with the same `loneKeyPositions`, so both leaf kinds
share one rule source.

## Correctness invariants

- Fail-closed: every unrecognized shape (set ops, grouping sets, multi-key
  grouping, bare SeqScan bodies, VALUES, function scans) yields no marking,
  exactly upstream's punts.
- Uniqueness is a structural property of the body plan — never guessed
  from cardinalities.
- `leafRows` applies the leaf-local `*Filter` wrapper, matching PG's
  `rel->rows` (post-baserestrictinfo) while `leafTuples` is the raw
  `rel->tuples`.
- The catalog path is untouched for base relations: `leafTuples` stays 0
  and `uniqueOutCols` nil.

## Witnesses

- Q83 standalone probe (`sr_items ⋈ cr_items`): `rows=1 → 10`, matching
  PG's `rows=10` for the identical relset.
- Q83 plan: merge chain now 5 / 2 (was 1).
- `make ea-ratchet`: PASS, 52 baseline findings fixed — all four Q83
  flagged relsets plus the Q78/Q85 leaf-level relsets whose vars now carry
  real leaf tuples.
- Q95's `cte:ws_wh` semijoin stays `rows=1` — PG estimates the same node
  `rows=1` (self-join body has no lone key; the underestimate is the
  driving filtered chain, PG-shared).
