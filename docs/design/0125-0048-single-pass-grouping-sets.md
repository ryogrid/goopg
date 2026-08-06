# M0125-0048 — the single-pass grouping-sets aggregate

Status: implemented 2026-08-06
Area: `internal/planner` (lowering), `internal/executor` (aggregate operator)
Supersedes: the UNION-ALL expansion added by M0122-0004 and the source-sharing
CTE hoist added by M0125-0040.

## The defect

SQL:1999 §7.9 *defines* `GROUP BY GROUPING SETS / ROLLUP / CUBE` as the UNION
ALL of one ordinary `GROUP BY` per listed set, and goopg executed that
definition literally. `rewriteGroupingSets` built N sibling `SelectStmt`s and
threaded them through `s.SetOp`; the pre-existing N-ary set-op planner then
planned each one independently.

That is a correct reading of the standard and a poor model of PostgreSQL:

* **The source was read once per set.** M0125-0040 papered over that by
  hoisting `FROM`+`WHERE` into a synthetic materialized CTE (`__gs_src_N`),
  which traded the repeated scans for a full buffer of the source.
* **`GROUPING(...)` was a plan-time literal.** Each branch is a different
  query with a different active set, so `substituteGroupingExpr` replaced the
  call with an `IntegerConst` bitmask. A `GROUPING` argument that was not a
  grouping expression contributed a `1` bit instead of raising PG's 42803.
* **The grand-total level was a branch**, not a grouping set, so EXPLAIN never
  showed anything resembling PG's `Group Key: ()`.
* **Every dimension rolled away had to be substituted to NULL by rewriting the
  target list**, a per-branch AST walk over ~20 expression shapes that returned
  anything it did not recognise unchanged.

PostgreSQL computes every level in ONE pass over the source with one hash table
per hashable set: `nodeAgg.c`'s `AGG_HASHED` / `AGG_MIXED`, set up by
`preprocess_grouping_sets` and `consider_groupingsets_paths` in
`optimizer/plan/planner.c`. The source is read once no matter how many sets
there are, and the grand total is a grouping set like any other.

## The shape that landed

One `Aggregate` plan node covers every level.

```
Aggregate
  GroupExprs    = deduplicated union of every set's expressions  (PG's parse->groupClause)
  GroupingSets  = [][]int — per set, the ASCENDING GroupExprs indices it keeps
  GroupingMasks = [][]int64 — per GROUPING(...) call, its bitmask per set
  schema        = [ group exprs | aggregates | grouping masks | passthrough ]
```

`internal/planner/groupingsets.go` is the whole lowering:

| function | job |
|---|---|
| `prepareGroupingSets` | rewrite `s.GroupBy` to the deduplicated union of the sets; idempotent via `GroupingSetsSpec.Flattened` |
| `groupingSetIndices` | map each expanded set onto `GroupExprs` slots |
| `commonGroupingSlots` | the intersection of all sets — PG's `gset_common` |
| `collectGroupingCalls` | find every distinct `GROUPING(...)` in targets/HAVING/ORDER BY |
| `groupingCallMasks` | its value per set; raises 42803 for a non-grouping argument |

`buildAggregateStage` (`internal/planner/planner.go`) allocates one output
column per `GROUPING(...)` call **before** the target list is resolved, because
`resolveTargetsAfterAggregate` appends functionally-determined passthrough
columns as it discovers them and the executor emits the four regions in the
fixed order above. `resolveExprAfterAggregate` then resolves a `GroupingCall`
to a plain `ColumnRef` into that column.

`aggregateOp.Open` (`internal/executor/operators_join_agg.go`) evaluates every
grouping column once per input row (`evalGroupExprs`) and routes the row into
each set's hash table, keyed by `setGroupKey`. A column not in the current set
is emitted `NULL`. A set with no active columns — an ungrouped aggregate, or a
ROLLUP's grand total — has its group created before the drain, so it emits one
row over empty input.

### Three decisions worth stating

**The mask is an output COLUMN, not an expression over a hidden set-id.** The
bitmask depends only on which set produced the row, never on data — the same
reason PG evaluates a `GroupingFunc` from `AggState->current_set`. Materialising
it as a column means the target list resolves `GROUPING(a,b)` to a `ColumnRef`,
so no new `Expr` node, no new case in either the interpreted or the compiled
evaluator, and no new EXPLAIN formatting. As a side effect the output column is
named `grouping`, which is what PG names it (`FigureColname`'s `GroupingFunc`
case in `parse_target.c`) and what the retired `IntegerConst` rewrite could not
produce.

**The ordinary aggregate is the one-set case, not a separate path.** The
executor synthesises `sets = [[0..n-1]]` when `GroupingSets` is nil, and
`setGroupKey` omits the set prefix when there is only one set — so a plain
`GROUP BY` keys exactly as it did before, through the same loop. `nodeAgg.c`
makes the same unification by giving a plain `Agg` `numsets == 1`. Duplicating
the ~250 lines of shared-state / `finishAgg` handling into a second operator
would have been the sibling-path hazard this project keeps paying for.

**The output sort is per SET first, then by key within the set.** That
reproduces exactly what the retired expansion emitted — one sorted block per
branch, in declaration order — so the row ORDER of every grouping-sets query is
unchanged by the rewrite. It is not PG's order (see Divergences).

## Semantics captured from PostgreSQL 18.3

Every expected value below was captured live from the reference cluster on port
65432 on 2026-08-06 and is pinned in
`internal/executor/grouping_sets_single_pass_test.go`.

| probe | PG 18.3 | goopg |
|---|---|---|
| `ROLLUP(dept)` over an EMPTY table | 1 row `(NULL, 0)` | same |
| plain `GROUP BY dept` over an EMPTY table | 0 rows | same |
| `GROUPING SETS (())` over an EMPTY table | 1 row | same |
| bare `GROUPING(dept)` column name | `grouping` | same |
| `GROUPING SETS ((dept),(dept))` | `a,b,a,b` — duplicates are NOT merged | same |
| `GROUPING(region)` under `GROUP BY ROLLUP(dept)` | ERROR 42803 | same message |
| `SELECT id, name … GROUP BY ROLLUP(id)`, id a PK | ERROR 42803 | same |
| `GROUP BY dept, ROLLUP(region)` cross product | 5 rows | byte-identical |
| CUBE + `GROUPING(a,b)`, sub-SELECT, UNION-ALL branch, DISTINCT, expression key, `ORDER BY GROUPING(...)`, two-arg GROUPING in HAVING, join+filter, `ROLLUP((a,b))` multi-column unit, LIMIT | — | all 10 byte-identical |
| FILTER, `count(DISTINCT …)`, window over the result, self-join aliases | — | all byte-identical |

The functional-dependency row is the one this change had to *learn*. PostgreSQL
builds `groupClauseCommonVars` from `gset_common` — the intersection of the
expanded sets — and hands **that** list, not the whole GROUP BY, to
`check_functional_grouping` (`src/backend/parser/parse_agg.c`,
`parseCheckAggregates`). Under `ROLLUP(id)` the grand-total level groups by
nothing, so a primary key cannot determine `name` and the query is an error even
though the same target list is legal under `GROUP BY id`. goopg's
`isColumnFunctionallyDetermined` now consults `aggregateSurface.groupCommonSlots`
for exactly this.

## Measured effect

TPC-DS SF0.5 plan channel, 99 queries: `same=91 changed=8`. The eight are
`Q5 Q14 Q18 Q22 Q27 Q67 Q77 Q80` — precisely the eight queries in the corpus
that use `ROLLUP`, and **no query without a grouping-set construct moved**.

Q27 is representative: the plan goes from a `CTE __gs_src_606` carrying the
whole five-table join plus a three-branch Append, to

```
Limit
  -> Sort
    -> HashAggregate (2 keys, 3 grouping sets)
      -> Nested Loop (INNER)
         …
```

— one aggregate over one scan of the source, the same node count as PG's
`MixedAggregate` over one `Seq Scan`.

Row counts against the git-tracked PG oracle: `PASS=8 MISMATCH=0` for those
eight queries. Runtime, S-cold, is a side effect rather than the goal but is
worth recording: Q67 82s→17s, Q18 37s→9s, Q22 21s→4s, Q27 31s→10s.

## Divergences deferred (ledger rows dated 2026-08-06)

1. **Emission order without ORDER BY.** goopg emits set-by-set in declaration
   order; PG's `AGG_MIXED` emits the sorted level(s) and the hashed levels in
   its own order (grand total first for `ROLLUP(dept,region)`). The row SETS are
   identical and neither order is guaranteed without `ORDER BY`, but a test that
   pins PG's literal output would fail.
2. **No per-set key detail lines in EXPLAIN.** PG prints one
   `Hash Key:`/`Group Key:` line per set, including the bare `Group Key: ()`.
   goopg has no `Group Key:` line for ANY aggregate, so this is a facet of the
   pre-existing gap the M0125-0039 line records rather than a new one; the set
   count rides on the existing `(N keys)` suffix instead.
3. **Everything is hashed.** PG chooses per set between hashing and sorting
   under `work_mem` (`consider_groupingsets_paths`), and labels the mixed case
   `MixedAggregate`. goopg has one grouped implementation, a hash aggregate with
   no spill, so a wide grouping-sets query holds every level's hash table at
   once.

## Retired in this change

* `rewriteGroupingSets`, `substituteGroupingExpr`, `groupingBitmask`
  (`internal/planner/planner.go`)
* `internal/planner/groupingsets_share.go` and its test — the whole
  `shareGroupingSetsSource` / `__gs_src_N` hoist. The item that filed this work
  said the single-pass aggregate REPLACES it rather than extending it, and with
  the source read once there is no source to share.
* `GOOPG_GS_SHARE_SOURCE`, moved to `flagProvenanceRetired` (not deleted) so
  older benchmark artefacts that carry it stay attributable.

Parallel aggregation refuses a grouping-sets node
(`aggregateSplitIsSafe`): the set a group belongs to is not part of the key the
partial accumulator merges on, so a split would fold every level together.
