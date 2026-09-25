# M0145-0008d: grouping follows PG's processed_groupClause

Status: landed 2026-09-24 `e32442db1`. Movement: yes (CATEGORIES-EXCL-MATCH,
TPC-H Q18, see Measurement). Follow-ups: M0145-0008h (planner crash on the
upstream `agg_sort_order` query), M0145-0008i (multi-relation GROUP BY
pruning).
Task: `.ralph/fix_plan.md` M0145-0008d (Kind: impl, Parent: M0145-0008).
Evidence: `analysis/m0145/m0145-0008d/`.

## PG

1. `transformGroupClauseExpr` (`parse_clause.c:2424-2438`): a GROUP BY item
   whose target entry ORDER BY also names takes a copy of ORDER BY's
   SortGroupClause, i.e. its sort operator (direction) and `nulls_first`.
   Its position in ORDER BY does not matter.
2. `preprocess_groupclause` (`planner.c:2828`): the GROUP BY items forming a
   prefix of ORDER BY, compared with `equal()` on expression and direction,
   move to the front in ORDER BY's order. The rest follow in written order.
   With no match the written order stands.

The result is `root->group_pathkeys` and the sort a GroupAggregate consumes.
EXPLAIN prints it as the `Group Key:` order, for hashed aggregates too.

## goopg

- `internal/optimizer/groupclause.go`:
  - `processedGroupOrder` is the single derivation of both steps.
  - `buildGroupClause` turns it into `Aggregate.GroupClause`: positions into
    `GroupExprs` plus DESC / NULLS FIRST. nil means the default.
  - `groupClauseKeys` reads the clause back, falling back to written ASC.
- `GroupExprs` is never reordered, for the reason `GroupKeyOrder` gives:
  every output binding is fixed to the written position.
- Consumers:
  - `groupKeysSortKeys` covers the serial sorted aggregate and the parallel
    worker / Gather Merge sorts.
  - The presorted-aggregate `group_pathkeys` follow the clause.
  - `groupingEmissionPathkeys` and `aggregateEmissionPathkeys`, the sibling
    pair, translate child key j as clause position j, emitted at output
    position `clause[j].Pos`.
  - EXPLAIN's `Group Key:` order follows the clause (an index-driven
    `GroupKeyOrder` still wins).
  - The presearch `groupClauseItems` now calls the same derivation, which also
    fixes its non-prefix direction (`ORDER BY count(*) DESC, g DESC` groups
    on g DESC).
- `buildAggregateStage` carries each key's written index through
  `pruneUselessGroupByColumns`. PG prunes before `preprocess_groupclause`,
  so a pruned key never takes part in the prefix.
- Matching uses `parserSortExprEqual`, like the presearch pathkeys. PG
  matches by target entry, which also pairs equal non-column expressions
  (ledgered).

## Verification

- Probe (`probe.txt`): five `enable_hashagg = off` plans (direction copy,
  prefix reorder, partial prefix, `NULLS LAST`, non-prefix copy) and two
  ordered results. The goopg output is identical to PG 18.3's and is pinned
  as `TestExplainGroupClauseFollowsOrderBy`.
- Gates: units; tpch-spotcheck (Q12=2, Q13=33); sf025 96/96; acceptance arm
  24 MATCH (Q18 13.17 s against 13.43 s); fire-set PASS; ea-ratchet PASS
  52/52.
- Tests updated for the behaviour change:
  - `TestDeriveQueryPathkeys`: non-prefix direction copy.
  - The deep-indent fixture: `ORDER BY 2 DESC` became single-sort, so the
    fixture now orders by the aggregate. PG uses a backward index scan
    there, which goopg does not generate for grouping (ledgered).

## Measurement

| corpus | match | categories changed |
|---|---|---|
| TPC-H | 3 → 3 | Q18: aggregation-strategy 3 → 4, sort-strategy 8 → 9, rendering 4 → 3 |
| TPC-DS SF0.25 | 4 → 4 | none |

Q18 groups on five keys and orders by `o_totalprice DESC, o_orderdate`. Two
of the keys form the ORDER BY prefix, so the processed clause leads with them
and a GroupAggregate over one Gather Merge sort wins, which is PG's own rule
at work. PG instead prunes the group list to `(c_custkey, o_orderkey)` with
multi-relation `remove_useless_groupby_columns`: `c_custkey` and `o_orderkey`
are primary keys that determine the other three. Then nothing matches the
ORDER BY prefix and HashAggregate + Sort wins. goopg prunes only single-relation
GROUP BYs, so the Q18 category change belongs to that gap (M0145-0008i).

## Found along the way

The upstream `aggregates.out:3158` query, `SELECT array_agg(c1 ORDER BY c2),
c2 FROM agg_sort_order WHERE c2 < 100 GROUP BY c1 ORDER BY 2` with c1 a
primary key, panics the planner (`assertSortInputTargetCoversKeys`: the Sort
input target drops sort-key column c2) and the whole server exits. It
reproduces at HEAD without this change. Filed as M0145-0008h with an S2
escalation.
