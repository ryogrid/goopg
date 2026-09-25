# M0145-0008ab: the derived ANY_subquery unique-ifies and estimates as PG's does

Status: **LANDED 2026-09-25** (`f8fb6d64c`). Task: `.ralph/fix_plan.md`
M0145-0008ab (Kind: impl, Parent: M0145-0008aa). Evidence:
`analysis/m0145/m0145-0008ab/`.

## Problem

M0145-0008aa pulls a non-simple ANY body up as one derived semi-side leaf,
PG's `ANY_subquery` RTE. On TPC-H Q18 the leaf still behaved unlike PG's in
two ways:
- **Estimate.** goopg estimated the semi join at 750K of 1.5M `orders` rows,
  which is the 0.5 default: the derived leaf has no catalog table, so
  `examineJoinVar` found no statistics. PG estimates 117K.
- **Unique-ify.** PG can unique-ify the grouped side for free and join it as
  an inner join. goopg never built that path for a pulled leaf.

## What PG does, and what was ported

1. **`examine_simple_variable`, RTE_SUBQUERY arm**
   (`postgres/src/backend/utils/adt/selfuncs.c`). A Var of a subquery is
   `isunique` when it is the sub-select's only DISTINCT column or only GROUP BY
   column. `get_variable_numdistinct` then treats it as unique over the rel's
   rows, so `eqjoinsel_semi` gets nd2 = the rel's rows.
   - Port: `subqueryOutputIsUnique` (`internal/optimizer/query_distinct.go`) is
     computed at pull-up and carried as `jtPulledBody.uniqueOutput`, then
     `baseRelInfo.subqueryUniqueOutput`.
   - `examineJoinVar`'s unresolved arm sets `joinVarStats.isUnique` with the
     leaf's own tuples and rows.
   - `getVariableNumDistinct` applies upstream's override:
     `stadistinct = -1 * (1 - nullfrac)`.
2. **`create_unique_path`'s `UNIQUE_PATH_NOOP`**
   (`postgres/src/backend/optimizer/util/pathnode.c:1955-1975`). When the RHS
   subquery is distinct for the semi_rhs_exprs
   (`query_supports_distinctness` / `query_is_distinct_for`,
   `postgres/src/backend/optimizer/plan/analyzejoins.c:1079`, `:1117`), the
   unique path is the subpath itself, with the same rows and cost.
   - Port: `queryIsDistinctForFirstColumn` ports the rule for a one-column
     body:
     - DISTINCT / DISTINCT ON;
     - GROUP BY where every item is the target;
     - an aggregate or HAVING body with no GROUP BY;
     - set operations with no ALL anywhere in the tree;
     - a target-list SRF defeats the rule except under DISTINCT.
   - The answer is stamped on the SJInfo (`SemiRhsDistinct`), because goopg's
     rel carries no parse tree.
   - The pulled SJInfo's `SemiRhsExprs` are in problem space
     (`SemiRhsProblemSpace`). `createPulledUniquePath` maps them through the
     leaf's `baseOffset` and returns the subpath.
3. **`populate_joinrel_with_paths`, JOIN_SEMI arm**
   (`postgres/src/backend/optimizer/path/joinrels.c:991-1014`). PG adds the
   JOIN_UNIQUE_INNER paths beside the plain SEMI paths whenever the inner rel
   is exactly the RHS and unique-ifiable. goopg's `jointypeForDirection`
   returned the unique side only when the plain SEMI direction was illegal.
   - Port: `addPathsToJoinrel` now runs `addPathsForJointype` a second time
     with `uniqueSideInner`. The JOIN_UNIQUE_OUTER twin is the swapped
     direction, which the existing fallback already admits.

Two rules are deliberately separate, as upstream's are. The estimator's
`isunique` and the unique path's `query_is_distinct_for` disagree on
HAVING-only and UNION bodies: those are distinct but not `isunique`.
`TestQueryIsDistinctForFirstColumn` pins both rules side by side, including
the cases where a false "yes" would return duplicate rows:
- two GROUP BY keys;
- GROUP BY on another column;
- UNION ALL anywhere in the tree;
- an SRF under GROUP BY.

A bare GROUP BY name is matched structurally, never through an output alias,
because PG resolves it to an input column first.

## Scope kept out

A pulled **base-table** RHS still declines unique-ification. Doing it
faithfully needs PG's other NOOP arm (`relation_has_unique_index_for`,
pathnode.c:1940) and the HASH method (ledger M0142-0008c-1a). Offering goopg's
paid Sort+Unique where PG takes a free or hashed path would create
divergences of goopg's own making. Ledgered.

## Measured

- **TPC-H fire set: Q18 only.**
  - Semi-join estimate: 43,450 per worker, about 135K in total, against PG's
    117K (29,376 × 4). It was 750K.
  - Final estimate: 539K rows at cost 382K, against PG's 470K at 404K. It
    was 3.0M rows at cost 1.52M.
  - Join order now matches PG's spine: `orders ⋈ HashAggregate(lineitem)`
    first, then an index nested loop into `lineitem`. Plans:
    `analysis/m0145/m0145-0008ab/q18-{baseline,candidate,candidate-pg}.plan.txt`.
- **Remaining Q18 differences:**
  - goopg elects the Hash **Semi** Join; PG elects the unique-inner Hash
    Join. The trace shows the unique-inner candidates are generated and lose
    on cost. Filed as M0145-0008ad.
  - `customer` is reached through Memoize + index rather than PG's Parallel
    Hash.
  - GroupAggregate rather than HashAggregate on top.
- **Values:** acceptance arm 24 MATCH; Q18 5.89 s → 4.84 s.
- **TPC-DS:** fire set has no changed query at SF0.25 or SF1, and the SF0.25
  sweep shows 99/99 shapes the same.
- **EA-RATCHET:** 52 → 52.

## Gates (staged code `f8fb6d64c`)

- units: PASS.
- `tpch-spotcheck`: PASS.
- acceptance arm: 24 MATCH.
- `tpcds-sf025`: 96 PASS, `MISMATCH=0 TIMEOUT=0`.
- fire-set (SF0.25 + SF1): PASS, fires none.
- TPC-H fire set: fires Q18, no timeouts.
- `make ea-ratchet`: PASS.
- lineage guard: OK.
- pgbench smoke: PASS.

Movement: none. TPC-H match 3 → 3, and every category move is within ±1.
