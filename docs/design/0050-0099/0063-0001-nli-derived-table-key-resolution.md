# Design 0063-0001 — NLI derived-table outer key resolution (Q8 / Q15b)

| field      | value |
| ---------- | ----- |
| status     | draft |
| date       | 2026-05-07 |
| milestone  | 0063 — TPC-H residual long-tail v2 |
| supersedes | — |

## 1. Problem

Two TPC-H queries return zero rows when they should not, while
their canonical row counts on PostgreSQL are 2 (Q8) and 1
(Q15b). The post-M0062 final sweep
(`analysis/tpch-m0062-final-baseline-2026-05-07.md`) shows:

| Query | Result | Canonical |
| ----- | ------ | --------- |
| Q8    | OK 188.23 s, 0 rows | 2 rows |
| Q15b  | OK 25.08 s, 0 rows  | 1 row  |

The reproducer probe is far smaller than either query:

```sql
SELECT count(*) FROM supplier, (SELECT 1 AS x) v
 WHERE s_suppkey = v.x;
-- NLI on  → 0
-- NLI off → 1   (SET enable_nestloop_index = off)
```

Both Q8 and Q15b have plans that contain a NestedLoopIndexJoin
whose **outer** is a derived table (a `(SELECT …) AS v` rangevar
or, for Q15b, the `revenue0` view body materialised as
`Project(Aggregate(Filter(SeqScan)))`). When NLI is on, the
inner `IndexScan` produces zero matches.

This is the *symmetric counterpart* of M0062-0006: the Q9 fix
(`commit 09e24d1`) handled the case where NLI's parent had
ColumnRefs into an *MHJ-output schema*, by extending
`buildBindingsPosMap.collect` to traverse `*NestedLoopIndexJoin`
and to preserve `IndexScan.Alias`. The remaining bug is on the
*inner* side: the IndexScan's `Key` expression — built from the
NLI's outer-side ColumnRef — does not bind correctly to the
outer row at runtime when that outer is a derived table.

## 2. Hypothesis (to verify in implementation)

`internal/planner/nl_index_join.go::nliRewrite` constructs:

```go
inner := &IndexScan{
    Table:  innerScan.Table,
    Index:  idx,
    schema: innerScan.Output(),
    Key:    keys[0],     // ← outer-side ColumnRef
}
```

The `Key` is an outer-side `*ColumnRef`. At runtime, the executor
binds the outer row via `BindOuter(row)`
(`internal/executor/operators_index.go:138-146`) and
`lookupKey()` (line 328) calls `evalExpr(o.plan.Key, o.outerRow,
o.ctx)`.

For a normal SeqScan outer, `o.outerRow` is a row of width =
`len(SeqScan.Output())`, and the Key's `ColumnRef.Index` is in
that range — works.

For a derived-table outer of shape `Project(Values(...))`, the
runtime row is shaped by Project's targets — width = number of
projection targets. The Key's `ColumnRef.Index` was assigned at
plan time against the *FROM-clause cumulative offset*, not
against the Project's local schema. If the planner's binding
offset for the derived table is non-zero (because `supplier`
preceded it in `from supplier, (SELECT 1 AS x) v`), the Key
holds an Index of `len(supplier.Output()) + 0 = 7`, but
`o.outerRow` is the 1-column Project output, so `o.outerRow[7]`
is out of bounds → falls through to `IsNull()` and returns no
matches.

`buildBindingsPosMap` could remap the Key's Index, but only
when the plan tree allows it to traverse: bucket A is **NLI
where the Outer is itself the *non-MHJ* containing the derived
table**, and `applyJoinTreePosMap` may not reach into that
outer's local scope.

## 3. Critical code paths

| Path | File:line |
| ---- | --------- |
| NLI rewrite + Key construction | `internal/planner/nl_index_join.go::nliRewrite` (~line 350-370) |
| `pickInnerSide` (decides outer vs inner) | `internal/planner/nl_index_join.go::pickInnerSide` (line 632) |
| `BindOuter` and `lookupKey` | `internal/executor/operators_index.go` (line 138, 328) |
| Bindings posMap collect (already handles `*NestedLoopIndexJoin`) | `internal/planner/bushy.go::buildBindingsPosMap` (post-M0062-0006 patch) |

## 4. Proposed change

Two-part fix, both in the planner. The order matters:

1. **In `nliRewrite`, refresh the outer-side Key index by
   NAME.** After choosing `outerKey *ColumnRef`, re-bind its
   `Index` against the chosen outer node's
   `outerNode.Output()`, by Name. If the Name appears once in
   that schema, set `Index = position-in-outer-schema`. The
   pre-existing `reresolveJoinByName`
   (`internal/planner/bushy.go::reresolveJoinByName`) already
   provides the `findUnique` primitive — it can be lifted to
   shared scope and reused.

2. **In `applyJoinTreePosMap` (`bushy.go:1503`), descend into
   the NLI's `Outer` with the outer's *local* posMap.** The
   M0062 fix made the Right side of Semi/Anti joins isolated
   subquery scopes; NLI's Outer for derived-table cases is
   similarly an isolated scope. The current walker uses the
   query-wide bindings posMap which lacks the derived-table
   binding. We need to either build a posMap for the NLI's
   outer subtree alone, or extend the bindings list to include
   the derived table.

The simpler approach is (1). (2) is a fallback if Name-based
re-resolution misses any Q8 / Q15b shape.

## 5. Reproducer + acceptance

Reproducer (must return `1`):

```sql
SELECT count(*) FROM supplier, (SELECT 1 AS x) v
 WHERE s_suppkey = v.x;
-- expected: 1 (the supplier row with s_suppkey = 1)
```

Acceptance:

- The reproducer returns 1 with NLI on AND with NLI off
  (currently 0 vs 1; post-fix should be 1 in both).
- Q8 returns ≥ 1 row on SF=1 (canonical TPC-H Q8 result is
  2 rows for the BRAZIL/AMERICA/ECONOMY ANODIZED STEEL
  combination; we accept any positive count).
- Q15b returns 1 row (the supplier with the maximum
  total_revenue from the `revenue0` view).
- A new test in `internal/planner/nl_index_join_test.go`
  pins the rewrite for a derived-table outer.
- A new end-to-end test in `internal/testutil/tpch/`
  exercises the reproducer pattern against a real cluster
  with NLI on.
- `go test ./...` PASS.

## 6. Risks & rollback

- The `enable_nestloop_index` GUC kill-switch
  (`internal/planner/nl_index_join.go::nliEnabled`) remains
  the rollback path. If a regression appears after merge,
  set the GUC default to off and reopen.
- Name-based re-resolution can silently miss when the same
  column name appears twice in the outer schema (self-join
  shape). The M0062-0002 IndexScan Alias plumbing already
  addresses the self-join case at the bindings level; the
  Name lookup here scopes to a single subtree's output,
  which is unique by construction in v0.

## 7. Out of scope

- Q9's NLI/MHJ remap fix is already in (commit `09e24d1`).
  This design only handles the *outer-side derived-table*
  variant.
- General multi-level subquery decorrelation (covered by
  M0063-0003).
