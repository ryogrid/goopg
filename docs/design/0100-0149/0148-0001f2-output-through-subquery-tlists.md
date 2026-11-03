# M0148\-0001f2 — EXPLAIN VERBOSE Output: subquery columns deparse through the child's targetlist

**Parent:** M0148\-0001f1 · **Status:** landed 2026\-11\-04; closed (residue in
M0148\-0001f3)

## Divergence

aggregates.sql, a join over a LATERAL aggregate subquery whose Subquery Scan
PG removes:

```
PG 18.3                                              goopg (before)
Sort                                                 Sort
  Output: s1.s1, s2.s2, (sum((s1.s1 + s2.s2)))         Output: s1.s1, s2, sm
  ->  Nested Loop                                      ->  Nested Loop
        Output: s1.s1, s2.s2, (sum((s1.s1 + s2.s2)))         Output: s1.s1, s2, sm
```

goopg printed the subquery's output names. PG deparses each column through
the plan below it to the expression that computes it.

## PostgreSQL

Once `trivial_subqueryscan` has removed the Subquery Scan, the subquery's
columns above the join are OUTER/INNER Vars. ruleutils deparses them
through the child plan's targetlist (`get_special_variable` → `resolve_special_varno`):

- the Agg's grouping Var prints as `s2.s2`;
- its aggregate prints as `sum((s1.s1 + s2.s2))`, parenthesised because it
  is not a plain Var.

## Change

`explainOutputNames` (operators_explain.go): an output column with no
relation of its own (SourceTableIdx 0, a subquery output) is resolved by
`explainDeparseThroughChildren`.

- **The walk** goes down through:
  - a Join, choosing the side that holds the column;
  - pass\-through nodes: Sort, Incremental Sort, Limit, Filter,
    Materialize, Gather, Gather Merge;
  - renaming Projects.
- **At an Aggregate** it deparses `aggOutputKeyExpr` and parenthesises a
  non\-Var. The Aggregate is entered (`reg.enter`) while formatting, so the
  expression's column references resolve in the aggregate's own query level.
  Without that, a non\-lateral subquery printed `sum(f1.a)` for the inner
  `ft.a`.
- **Anything else reports no match** and keeps the name: a kept Subquery
  Scan (PG prints the RTE alias there), an expression target, grouping
  sets.

## Verification

- `TestExplainVerboseOutputThroughSubquery` pins PG 18.3's whole plan for
  the aggregates.sql query.
- A scratch\-PG probe matches the lateral case. A non\-lateral grouped
  subquery join now prints PG's expressions; its remaining differences are
  residue.
- **Regress runner, HEAD → change, 19 suites.** aggregates 48 → 31 lines
  (unmatched PG `Output:` lines 2 → 0); the single\-suite run is down to the
  v\_pagg\_test SRF hunk (M0148\-0001i2). The other 18 suites are
  byte\-identical.
- `TestPort_RegressSuite`, `TestPort_PgDump*` and `TestPort_Isolation*`
  pass. The TPC gates show no change.

## Remaining (M0148\-0001f3)

- **Sort\-input expressions.** PG evaluates a non\-volatile select\-list
  expression below the Sort (make\_sort\_input\_target); goopg evaluates
  it above. This is a plan\-shape difference.
- **Join targetlists.** Join inputs and joins carry columns not needed
  above them: `Output: f1.a, f1.b, ft.b, …` where PG prints
  `f1.a, ft.b, …` (use\_physical\_tlist / build\_path\_tlist).
  `Inner Unique: true` is missing, and some inner nodes print no Output.
- **Older items.** The 2026\-11\-02 ledger items: casts and typed literals,
  SRF RTIDs, Result above an aggregate.
