# 0038-0001 — Multi-Way Hash Join Operator

**Status:** draft
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
