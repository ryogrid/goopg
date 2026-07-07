# 0038-0001 — Multi-Way Hash Join Operator

**Status:** accepted
**Parent milestone:** M0038
**Date:** 2026-05-02

## 1. Objective

Replace chains of N binary hash joins with a single `MultiHashJoin` operator
that builds hash tables from N-1 small tables and probes one large fact table,
performing chain-lookups through the hash tables in a single pass. This
eliminates N-1 intermediate result sets.

## 2. Current Behaviour (Binary Hash Join Chain)

Q2's bushy plan produces a chain of 4 binary hash joins:

```
HashJoin(p_partkey = ps_partkey)             ← binary join #4
  ├── SeqScan(part) (200K rows)
  └── HashJoin(s_suppkey = part.ps_suppkey)  ← binary join #3
        ├── SeqScan(partsupp) (800K rows)
        └── HashJoin(s_nationkey = n_nationkey) ← binary join #2
              ├── SeqScan(supplier) (10K rows)
              └── HashJoin(n_regionkey = r_regionkey) ← binary join #1
                    ├── SeqScan(nation) (25 rows)
                    └── SeqScan(region) (5 rows)
```

Each binary join level:
1. `drainRows` copies ALL child output (build side)
2. Builds hash table from those rows
3. Lazy-probes from the other child
4. Each `Next()` call returns one joined row

The problem: at level #3, `drainRowsBounded` on the child (level #2's output)
copies ~800K rows × ~17 columns. Even with spill-to-disk, these rows are
re-read and re-hashed at level #3.

## 3. MultiHashJoin Operator

### 3.1 Plan node

```go
type MultiHashKey struct {
    LeftTable  int   // index into Tables[]
    LeftCol    int   // column index in left table's schema
    RightTable int
    RightCol   int
}

type MultiHashJoin struct {
    pos        int
    Tables     []Node          // N child plan nodes
    Keys       []MultiHashKey  // N-1 equijoin edges
    ProbeTable int             // which child drives the probe loop
    Filters    []Expr          // residual WHERE filters
    schema     Schema          // concatenated output schema
}
```

### 3.2 Operator

```go
type multiHashJoinOp struct {
    plan      *planner.MultiHashJoin
    children  []Operator
    hashTbls  []map[string]Row   // one hash table per build child
    probeOp   Operator           // the child that drives probing
    keyExprs  []*KeyAccess       // how to compute hash keys
    filters   []planner.Expr     // residual filters
    nulls     []Row              // null-padded rows per child, for concat
    schema    planner.Schema
    ctx       *Context
}

type KeyAccess struct {
    buildTable int     // which hash table to use
    buildCol   int     // column index in that table's row
    srcTable   int     // where the lookup key comes from
    srcCol     int     // column index in the source row (probe or prior build)
}
```

### 3.3 Build phase (`Open()`)

1. Open all children.
2. For each child EXCEPT the probe table:
   - `drainRowsBounded(child, workMem)` — spill if needed.
   - For each row, compute the equijoin key column and insert into a hash table:
     `hashTbl[datumKey(row[keyCol])] = row`.
   - Close the child.
3. Keep the probe child open for streaming.

Q2 example:
- Build HT from supplier (key: s_suppkey → Row)
- Build HT from nation (key: n_nationkey → Row)
- Build HT from region (key: r_regionkey → Row)
- Probe: partsupp (streaming)

### 3.4 Probe phase (`Next()`)

```go
func (o *multiHashJoinOp) Next() (Row, error) {
    probeRow, err := o.probeOp.Next()
    if err == EOF { return nil, EOF }

    // Chain lookup through hash tables.
    // Start with the probe row's known columns.
    currentRow := probeRow

    for _, k := range o.keyExprs {
        // Determine the key value from the current state.
        keyVal := currentRow[k.srcCol]  // e.g., ps_suppkey from partsupp
        match, ok := o.hashTbls[k.buildTable][datumKey(keyVal)]
        if !ok {
            return o.nextLazySkip() // INNER semantics: skip on no match
        }
        // Append matched columns to output.
        currentRow = concatRows(currentRow, match)
    }

    // Apply residual filters.
    for _, f := range o.filters {
        ok, _ := evalExpr(f, currentRow, o.ctx) // simplified
        if !ok { return o.nextLazySkip() }
    }

    return currentRow, nil
}
```

### 3.5 Optimization: chain-lookup order

The hash tables are probed in order of the join chain. For Q2:

```
Probe: partsupp.ps_suppkey
  → HT_supplier[ps_suppkey] → get supplier row {s_suppkey, s_name, s_nationkey, s_acctbal, ...}
                                 └── s_nationkey
  → HT_nation[s_nationkey]  → get nation row {n_nationkey, n_name, n_regionkey}
                                 └── n_regionkey
  → HT_region[n_regionkey] → get region row {r_regionkey, r_name}
  → apply filter: r_name = 'EUROPE'
```

Output row = concat(partsupp columns, supplier columns, nation columns, region columns).

### 3.6 Memory model

| Component | Size (Q2) |
|-----------|-----------|
| HT_supplier (10K rows × 7 cols) | ~7 MB |
| HT_nation (25 rows × 4 cols) | ~10 KB |
| HT_region (5 rows × 3 cols) | ~2 KB |
| probe state (1 row at a time) | ~1 KB |
| **Total** | **~7 MB** |

Compare to binary join chain: N-1 intermediate result sets of ~800K rows each → ~3-4 GB.

## 4. Planner Integration

### 4.1 Chain detection

In `planSelect`, after bushy DP builds the join tree, walk the tree looking for
chains of binary hash joins where:
- Each join is `JoinAlgoHash`, `JoinTypeInner`.
- The chain forms a connected subgraph where each join adds exactly one new
  base table (no reuse).
- At least 3 tables in the chain (to justify the optimization).

```go
func detectMultiWayChain(node Node) *MultiHashJoin {
    // Walk the bushy join tree.
    // Collect all SeqScan leaf nodes reachable via a chain of
    // JoinAlgoHash nodes.
    // If the chain touches ≥ 3 base tables, extract the chain
    // into a MultiHashJoin node.
}
```

### 4.2 Rewrite

Replace the detected chain of binary joins with a single `MultiHashJoin`.
The parent binary join (if any) now points to the `MultiHashJoin` as one child.

Q2 result:
```
Join(p_partkey = ps_partkey)          
  ├── SeqScan(part)
  └── MultiHashJoin{partsupp, supplier, nation, region}  ← 4 tables, 1 operator
        probe=partsupp, build=[supplier, nation, region]
```

## 5. Implementation Plan

| Step | Files | Description |
|------|-------|-------------|
| 1 | `internal/planner/plan.go` | Add `MultiHashJoin`, `MultiHashKey` plan types |
| 2 | `internal/planner/bushy.go` | `detectMultiWayChain` — walk bushy tree, extract chains |
| 3 | `internal/planner/planner.go` | Wire chain detection after bushy DP pass |
| 4 | `internal/executor/multi_hash_join.go` | `multiHashJoinOp` — build, probe, chain-lookup |
| 5 | `internal/executor/executor.go` | `Build` dispatch for `*planner.MultiHashJoin` |
| 6 | `internal/executor/multi_hash_join_test.go` | Unit tests: chain-lookup correctness, INNER semantics, filter application |
| 7 | `analysis/tpch-multi-way-hash-join-results.md` | TPC-H Q2 verification |

## 6. Verification

- `TestMultiHashJoinChainLookup`: 3 tables (A→B→C via equijoins), verify output.
- `TestMultiHashJoinNoMatch`: probe row with no match → skip (INNER semantics).
- `TestMultiHashJoinFilter`: residual filter correctly drops rows.
- `TestMultiHashJoinQ2PlanShape`: Q2 plan contains `MultiHashJoin` node.
- `TestBuildTPCHQueries`: 22/22 TPC-H queries execute (no regressions).
- Q2 at SF=1 partial data: peak RSS ≤ 10 GB, duration ≤ 120s.

## 7. Reference

- `internal/executor/operators_join_agg.go` — `joinOp`
- `internal/planner/bushy.go` — bushy DP join tree construction
- `internal/executor/operator.go` — `Operator` interface
- `internal/executor/executor.go:21-169` — `Build()` dispatch
- `analysis/tpch-spill-hash-join-results.md` — Q2 24.8 GB RSS

## 8. Follow-up: correlated EXISTS/subquery outer-refs not remapped after the MHJ rewrite (2026-07-07, AI-20260707-000712-005)

**Symptom:** TPC-H Q21 (nightly sweep) failed with `pq: invalid input syntax
for type numeric: "slyly bold packages haggle against the instructions"
(22P02)` — an `l_comment` (text) value being fed into a numeric comparison.
Minimal repro needed all three of: (a) ≥3 FROM-clause tables so bushy DP fires
the `MultiHashJoin` rewrite (§4), (b) a `WHERE`-level `EXISTS`/`NOT EXISTS` or
scalar subquery correlated back to one of those tables, (c) the correlated
column NOT be the first column of its source table (so a wrong-index read
lands on a different, differently-typed column instead of coincidentally
reading the right one).

**Root cause:** `buildMHJPosMap` (bushy.go) computes a position map from the
*pre-rewrite* schema (tables concatenated in OID-sorted order — see
`buildMHJPosMap`'s "Sort by OID to get FROM-order" step) to the
`MultiHashJoin`'s own table order (`mh.Tables`, driven by
`ProbeTable`/chain-lookup order, generally *not* OID order). Every
Filter/Project/Sort/Aggregate expression sitting above the rewritten
`MultiHashJoin` is walked through this map by `remapByPosMap` so its
`ColumnRef.Index` values track the new layout.

`remapByPosMap`'s switch had no case for `*ExistsExpr` / `*SubqueryExpr` /
`*ArraySubqueryExpr`. Both are evaluated as an inline correlated subplan at
filter/leaf-eval time (`evalExistsExpr`/`evalSubquery`, `internal/executor/expr.go`)
— the executor pushes the *current* outer row (already in the new,
post-rewrite column order, since it comes straight from
`multiHashJoinOp.virtualOut`) onto `ctx.OuterRows`, and the inner plan's
`OuterColumnRef.Index` indexes directly into that row. Since the index was
never translated, it silently pointed at the *pre-rewrite* column position
inside the *post-rewrite* row — for Q21's `l1.l_suppkey` reference this landed
on `l_comment` a few columns off, and the resulting text value blew up the
`NOT LIKE`/numeric comparison it was checked against.

Contrast with `InExpr` (already handled, deliberately left as a no-op per the
existing "already remapped" comment): correlated `IN`/`= ANY` subqueries are
unnested into a Semi/Anti join by `unnestExistsExpr` *before* bushy DP runs,
so by the time `remapByPosMap` sees an `InExpr` it is almost always the
non-correlated/residual shape and genuinely needs no outer-ref fix-up. EXISTS
and scalar subqueries have no such unnesting pass and reach this point with
their `OuterColumnRef`s intact.

**Fix:** `internal/planner/bushy.go` — `remapByPosMap` now dispatches
`*ExistsExpr`/`*SubqueryExpr`/`*ArraySubqueryExpr` to a new
`remapOuterRefsInSubplan(node, depth, posMap)`, which walks the subquery's
inner plan via the existing `walkPlanExprs` node-tree walker and remaps any
`OuterColumnRef` whose `.Level` matches the current nesting `depth` (starting
at 1 for the immediate outer scope, incrementing across further nested
Exists/Subquery/In so a doubly-nested correlated reference is translated by
the correct enclosing scope's posMap, not the wrong one).

**Verification:**
- New minimal repro (`select ... from supplier, lineitem l1, orders where ...
  and exists (...)/not exists (...)`) — previously errored, now returns rows.
- Correlated scalar subquery variant (`(select count(*) from lineitem l2
  where ...)` in the SELECT list of a 3-way join) — same failure mode,
  same fix, confirmed via manual repro.
- `scripts/pg-oracle-diff.sh` byte-for-byte match against vanilla PostgreSQL
  18.3 on a small synthetic dataset built specifically to include the exact
  `l_comment` string that broke Q21 (to rule out coincidental correctness).
- Full TPC-H Q21 via `tmp/tpch-runner -queries 21`: `OK elapsed=91.97s
  rows=370` (was: hard error at ~21-192s depending on server warmth).
- `go test ./internal/planner/... ./internal/executor/...`: PASS, no
  regressions.
- Reverting just this hunk (`git stash push -- internal/planner/bushy.go`)
  restores the exact original failure — confirms the fix is load-bearing, not
  incidental.

**Deferred / discovered, NOT part of this fix (see `.ralph/deferral_ledger.md`
and `.ralph/fix_plan.md`):** while validating via `scripts/tpch-spotcheck.sh`,
Q13 was found failing independently (`operator NOT LIKE requires string
operands (got left.Kind=5 right.Kind=3)` — a `Time`-kind value read where
`o_comment` (String) was expected, inside a `LEFT OUTER JOIN ... ON ... AND
o_comment NOT LIKE '...'` plan whose `Filter: (o_comment NOT LIKE ...)` gets
pushed onto the bare `orders` seq-scan). Confirmed via `git stash` that this
reproduces identically with this section's fix removed — it is a pre-existing,
unrelated regression (almost certainly a sibling case of the same class of
bug: an ON-clause conjunct's `ColumnRef.Index` not being correctly shifted
when pushed down onto a single table's own scan schema, cf. the M0110-0003
LEFT JOIN inner-only pushdown fix in
`internal/planner/pushdown.go`/`shiftColumnRefsBy`). Not fixed here to keep
this loop's task scope to the assigned Q21 item; filed as a new top-priority
fix_plan.md entry since it blocks the mandatory Q12/Q13 spot-check gate for
every subsequent executor/planner change.
