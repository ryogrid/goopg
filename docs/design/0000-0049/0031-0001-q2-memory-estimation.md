# 0031-0001 — TPC-H Q2 Theoretical Memory Estimation (SF=1, VUSER=1, Ideal GC)

**Status:** draft  
**Parent milestone:** M0031  
**Date:** 2026-05-02

## 1. Objective

Estimate the theoretical lower-bound heap memory footprint of TPC-H Query 2 on goopg at
SF=1 with a single virtual user, assuming Go's GC operates ideally (collects all
unreachable objects immediately). The estimate identifies whether Q2 is fundamentally
executable within the 512 MiB `GOMEMLIMIT` guard, and if not, which operators are
responsible for the excess.

## 2. Query Structure

### 2.1 Q2 SQL (HammerDB variant)

```sql
SELECT s_acctbal, s_name, n_name, p_partkey, p_mfgr, s_address, s_phone, s_comment
FROM part, supplier, partsupp, nation, region
WHERE p_partkey = ps_partkey
  AND s_suppkey = ps_suppkey
  AND p_size = 15
  AND p_type LIKE '%BRASS'
  AND s_nationkey = n_nationkey
  AND n_regionkey = r_regionkey
  AND r_name = 'EUROPE'
  AND ps_supplycost = (
      SELECT min(ps_supplycost)
      FROM partsupp, supplier, nation, region
      WHERE p_partkey = ps_partkey
        AND s_suppkey = ps_suppkey
        AND s_nationkey = n_nationkey
        AND n_regionkey = r_regionkey
        AND r_name = 'EUROPE'
  )
ORDER BY s_acctbal DESC, n_name, s_name, p_partkey;
```

### 2.2 Key properties

- **Outer query**: 5-table comma-FROM join with 8 equality/range conjuncts and one
  correlated scalar subquery in the WHERE clause.
- **Subquery**: 4-table join, correlated on `p_partkey` (resolved via `planSelectWithParent`
  → `resolveColumnRef` walk-up → `OuterColumnRef`). The executor evaluates the subquery
  **per outer row** — no result caching, no unnesting.
- **ORDER BY**: 4 sort keys on the outer result.

### 2.3 Data cardinalities (SF=1, approximate)

| Table     | Rows    | Columns | Key width¹ |
|-----------|---------|---------|-----------|
| region    | 5       | 3       | 3         |
| nation    | 25      | 4       | 4         |
| supplier  | 10,000  | 7       | 7         |
| part      | 200,000 | 9       | 9         |
| partsupp  | 800,000 | 5       | 5         |

¹ Including all columns — the FROM clause exposes all columns to the intermediate join
schema.

**Outer query output**: ~2,000 rows (filtered part × filtered supplier × matching
partsupp records).

**Subquery output per invocation**: 1 scalar value (from `min` aggregate on ~1–4 rows).

**Number of subquery invocations**: equal to the number of outer rows surviving all
pre-subquery conjuncts (~2,000).

## 3. Plan Tree Shape

### 3.1 Initial FROM-clause join chain

`planFromClause` builds a left-deep CROSS-join chain in source order:

```
Cross(Cross(Cross(Cross(part, supplier), partsupp), nation), region)
```

Rendered as a tree (CROSS joins, all children are leaves):

```
Join0 CROSS
├── Join1 CROSS
│   ├── Join2 CROSS
│   │   ├── Join3 CROSS
│   │   │   ├── SeqScan(part)     [200K × 9]
│   │   │   └── SeqScan(supplier) [10K × 7]
│   │   └── SeqScan(partsupp)     [800K × 5]
│   └── SeqScan(nation)           [25 × 4]
└── SeqScan(region)               [5 × 3]
```

### 3.2 Predicate pushdown result

`pushPredicatesIntoCrossJoins` walks each conjunct of the WHERE clause and promotes
CROSS → INNER + HASH where both sides of an equality span the join's schema.
Since there is no equality between `part` and `supplier`, Join3 stays as CROSS.

Conjuncts that fail to push (subquery, single-table filters, or already-promoted
joins) stay on the outer Filter.

```
Sort (s_acctbal DESC, n_name, s_name, p_partkey)
  Project (8 targets)
    Filter (remaining: s_suppkey=ps_suppkey, p_size=15,
            p_type LIKE '%BRASS', r_name='EUROPE',
            ps_supplycost=SUBQUERY)
      Join0 INNER HASH (n_regionkey=r_regionkey)
      ├── Join1 INNER HASH (s_nationkey=n_nationkey)
      │   ├── Join2 INNER HASH (p_partkey=ps_partkey)
      │   │   ├── Join3 CROSS ← REMAINS AS CROSS (no equality between part/supplier)
      │   │   │   ├── SeqScan(part)
      │   │   │   └── SeqScan(supplier)
      │   │   └── SeqScan(partsupp)
      │   └── SeqScan(nation)
      └── SeqScan(region)
```

### 3.3 Impact of join-order reordering

If ANALYZE has run (stats present), `reorderCommaFromByCardinality` permutes the
FROM list to join small tables first. The greedy NN algorithm would start with
`region(5)` → `nation(25)` → `supplier(10K)` → `partsupp(800K)` (or `part` depending
on edge connectivity). This eliminates the bottom CROSS join entirely and produces a
much more compact plan.

**Without ANALYZE** (no `Stats.RowCount`), the source-order CROSS joins remain.
This is the default state after a fresh data load.

### 3.4 Subquery plan

The inner subquery is planned via `planSubqueryExpr` → `planSelectWithParent` → `Plan()`.
It goes through the same FROM-clause and pushdown pipeline:

```
Aggregate (min(ps_supplycost))
  Filter (r_name='EUROPE')
    Join INNER HASH (n_regionkey=r_regionkey)
    ├── Join INNER HASH (s_nationkey=n_nationkey)
    │   ├── Join INNER HASH (p_partkey=ps_partkey)
    │   │   ├── Join INNER HASH (s_suppkey=ps_suppkey)
    │   │   │   ├── Cross(partsupp, supplier) ← CROSS?
    │   │   │   └── nation
    │   │   └── ... ?
    │   └── ...
    └── region
```

Wait — the subquery's FROM list is `partsupp, supplier, nation, region`. There's no
explicit `part` in the subquery FROM. The correlation is via `p_partkey` which resolves
to the outer query's `part` column (an `OuterColumnRef`). In the subquery's FROM:
- `s_suppkey = ps_suppkey`: connects supplier ↔ partsupp
- `s_nationkey = n_nationkey`: connects supplier ↔ nation
- `n_regionkey = r_regionkey`: connects nation ↔ region
- `r_name = 'EUROPE'`: single-table filter on region

The edges: partsupp — supplier — nation — region. The reorder (with stats) would be:
region(5) → nation(25) → supplier(10K) → partsupp(800K).

**Without stats**, the initial CROSS chain is in source order: partsupp(800K), supplier(10K),
nation(25), region(5). The bottom Cross(partsupp, supplier) stays as CROSS (no direct
equality — `p_partkey = ps_partkey` references the outer `p_partkey` via OuterColumnRef
and CANNOT be pushed into the subquery's join tree because it spans the subquery boundary).

Actually: `p_partkey = ps_partkey` WHERE `p_partkey` is an OuterColumnRef. In the
subquery's WHERE, this is a binary equality between an outer column and the subquery's
own `ps_partkey`. At pushdown time, `walkColumnRefs` encounters `OuterColumnRef` → calls
`onOuter()` → `classifyConjunctSide` returns `sideOutOfScope`. **This conjunct cannot
be pushed into any Join in the subquery**. It stays on the subquery's Filter.

So the subquery plan (source order, no stats):

```
Aggregate MIN (single scalar output; group-by: none → "__all__")
  Filter (p_partkey=ps_partkey via OuterColumnRef AND r_name='EUROPE')
    Join INNER HASH (n_regionkey=r_regionkey)
    ├── Join INNER HASH (s_nationkey=n_nationkey)  
    │   ├── Join INNER HASH (s_suppkey=ps_suppkey)
    │   │   ├── Cross(partsupp, supplier) ← 800K × 10K = 8 BILLION rows
    │   │   │   ├── SeqScan(partsupp) [800K × 5]
    │   │   │   └── SeqScan(supplier)  [10K × 7]
    │   │   └── nation                 [25 × 4]
    │   └── ... 
    └── region                         [5 × 3]
```

**This is the critical problem.** The subquery contains a CROSS join of partsupp (800K)
× supplier (10K) = 8 billion intermediate rows. Even with ideal GC, this alone requires:

- `drainRows` copies: 800K rows × 5 Datum + 10K rows × 7 Datum ≈ negligible
- `runNestedLoop`: 8B × concatRows(12 Datum) = 8B × 12 × ~80 bytes/Datum = **~7.68 TB**

This is way beyond the 512 MiB GOMEMLIMIT. The join would OOM immediately during the
CROSS product enumeration in `runNestedLoop`.

### 3.5 Reality check: the query DID run

The user reports Q2 _did_ execute (memory grew then WSL2 crashed). This suggests one of:
1. ANALYZE was run, enabling join-order reordering that eliminates the CROSS joins.
2. The plan tree is not as analyzed above (I may have misread the pushdown logic).

Given that the fix_plan.md reports earlier successful schema builds and data loads, and
the HammarDB TPC-H scripts may run ANALYZE as part of the TPC-H build phase, it's
plausible that stats exist. With stats, both outer and subquery use reordered joins:
region → nation → supplier → partsupp → part (outer) and region → nation → supplier →
partsupp (inner). No CROSS joins remain.

## 4. Memory Model

### 4.1 Datum size

```
type Datum struct {
    Kind          DatumKind  // int (8 bytes)
    Int           int64      // 8 bytes
    Bool          bool       // 1 byte + padding
    String        string     // 16 bytes (ptr + len)
    Bytes         []byte     // 24 bytes (ptr + len + cap)
    Time          time.Time  // 24 bytes (wall + ext + loc ptr)
    IntervalMonths int32     // 4 bytes
    IntervalDays   int32     // 4 bytes
    NumericMantissa int64    // 8 bytes
    NumericScale    int8     // 1 byte + padding
}
```

Estimated size: ~104 bytes (with alignment padding). We use 100 bytes as a conservative
round figure.

A `Row` is `[]Datum` — a 24-byte slice header plus backing array. A row of N columns
carries N × 100 bytes in the Datum array plus the slice header.

### 4.2 Per-operator footprint model

| Operator | Peak in Open() | Retained after Close() | Per-invocation GC'able |
|----------|---------------|----------------------|----------------------|
| **SeqScan** | None (pins buffer pool pages; pages are reclaimed on Unpin) | `o.pinned` nilled | N/A |
| **joinOp** | `leftRows + rightRows + o.rows` (all copies via drainRows + concatRows) | `o.rows` NOT nilled | `leftRows`, `rightRows`, hash maps (local vars) |
| **sortOp** | `o.rows` (child's full output, NOT copied) | `o.rows` NOT nilled | N/A |
| **aggregateOp** | `groups map + order []string + o.rows` | `o.rows` NOT nilled | `groups`, `order` (local vars) |
| **projectOp** | None (streaming — one Row per Next()) | None | Per-row `out` row |
| **filterOp** | None (streaming) | None | N/A |
| **windowOp** | `o.rows` (child's full output, deep-copied) | `o.rows` NOT nilled | N/A |

### 4.3 Subquery re-Build cost

`evalSubquery` in `internal/executor/expr.go:617-662` calls `Build(x.Plan)` for every
outer row. `Build` recursively constructs a new operator tree. Each invocation allocates:

| Object | Count per invocation | Size estimate |
|--------|---------------------|---------------|
| `joinOp` structs | N (one per join in subquery) | ~200 bytes each |
| `sortOp` struct | 0–1 | ~100 bytes |
| `aggregateOp` struct | 1 | ~100 bytes |
| `filterOp` / `projectOp` structs | 1 each | ~100 bytes each |
| `SeqScanOp` structs | 1 per table | ~100 bytes each |
| Other small structs (`ctx` copies, etc.) | c. 5–10 | ~500 bytes |

Total Build allocation: **~2–4 KB per subquery invocation** (operator structs only).

For ~2,000 invocations: ~4–8 MB. Negligible — the operator structs are GC'd when
`subqueryImpl` returns (the `op` variable goes out of scope after `defer Close()`).

### 4.4 The critical retained-memory issue

Here is the crux. When `subqueryImpl` calls `op.Open(ctx)`:

```go
func subqueryImpl(x *planner.SubqueryExpr, ctx *Context) (Datum, error) {
    op, err := Build(x.Plan)        // fresh operator tree
    op.Open(ctx)                     // opens and buffers
    defer func() { _ = op.Close() }() // closes
    row, err := op.Next()
    ...
    return val, nil
    // op goes out of scope → GC can collect op (and all its fields)
}
```

The `op` variable is local to `subqueryImpl`. When the function returns, `op` is no
longer reachable. The operator's fields — `o.rows`, hash maps, etc. — are all rooted
in `op`, so they become unreachable. **With ideal GC, all per-invocation memory is
collectable.**

However, this ideal case is for each individual subquery invocation. The critical
question is: **does Go's GC collect these between invocations, or does the heap fill
up before the next GC cycle?**

### 4.5 GC timing and heap pressure

Go's GC uses a concurrent mark-sweep with a pacing algorithm. With `GOMEMLIMIT=512MiB`,
the GC target heap is 512 MiB. The GC is triggered when live heap size doubles from the
previous GC (by default `GOGC=100`). This means:

1. First GC: triggered when heap reaches ~2× live set
2. Subsequent GCs: triggered when heap grows by 100% from the last GC's live set

The issue with Q2 is the **allocation rate**, not just the live set. Each subquery
invocation allocates:
- `joinOp.Open()`: `leftRows` (drainRows), `rightRows` (drainRows), `o.rows` (result)
- `aggregateOp.Open()`: `groups` map, `order` slice, `o.rows`
- Hash join maps: `map[string][]Row`

For the reordered plan (no CROSS joins), the subquery processes:
- PARTSUPP rows filtered by `p_partkey`: ~4 rows per invocation
- SUPPLIER rows: ~10K (cached in hash, but re-drained each time)
- NATION: 25 rows
- REGION: 5 rows

The subquery hash join builds on the smaller side. For `n_regionkey=r_regionkey` on
region(5) and the left side, the hash map stores ~5 keys × ~1–5 rows = tens of rows.
Similarly `s_nationkey=n_nationkey` builds on nation(25). The `s_suppkey=ps_suppkey`
join builds on the smaller of the wrapped sides.

**Total allocation per subquery invocation (reordered, with stats):**
- Supplier drainRows: 10K × 7 × (100+24) ≈ 8.7 MB (Datum backing + slice header)
- Parts-supp drainRows: ~4 rows × 5 Datum ≈ 2 KB
- Nation drainRows: 25 × 4 Datum ≈ 10 KB
- Region drainRows: 5 × 3 Datum ≈ 1.5 KB
- Hash maps + concatRows: ~500 KB
- Total: ~10 MB per invocation

With 2,000 invocations: **20 GB allocated**. Even with ideal GC, if the GC doesn't run
frequently enough (every subquery invocation allocates 10 MB, and GC only triggers at
100% heap growth after GC mark), the heap can grow to 20 GB before all subqueries
complete. With `GOMEMLIMIT=512MiB`, this would OOM-kill the process.

**The subquery-per-row evaluation with drainRows is the root cause of the memory
explosion.** Each invocation drains fully-buffered join children (including 10K-supplier
rows) that, while collectable, are allocated faster than GC can reclaim them.

## 5. Total Memory Estimate

### 5.1 Assumption: ANALYZE stats present (reordered join, no CROSS)

**Outer query (invoked once):**

| Operator | Peak allocation | Retained after Close() |
|----------|----------------|----------------------|
| SeqScan(region) | 0 (buffer pool) | 0 |
| ... (all 5 SeqScans) | 0 | 0 |
| Join2 (part × partsupp) | leftRows+drain: ~200K×9 + 800K×5 ≈ 590 MB | `o.rows`: ~2000×21 ≈ 4 MB |
| Join1 (+ nation) | drainRows: 2000×21 + 25×4 ≈ 0.3 MB | `o.rows`: ~2000×25 ≈ 5 MB |
| Join0 (+ region) | drainRows: 2000×25 + 5×3 ≈ 0.3 MB | `o.rows`: ~2000×28 ≈ 5.6 MB |
| Filter | 0 (streaming) | 0 |
| Project | 0 (streaming) | 0 |
| Sort | ~2000×8 ≈ 160 KB | `o.rows`: ~2000×8 ≈ 160 KB |
| **Outer total** | **~600 MB peak** (dominated by SeqScan drain for hash build) | **~15 MB** (all operators retain o.rows) |

**Subquery (invoked ~2,000 times):**

| Per-invocation allocation | Size | 2,000× total |
|---------------------------|------|-------------|
| SeqScan drains (4 tables) | ~10 MB | 20 GB |
| Hash maps + concatRows | ~500 KB | 1 GB |
| Operator struct Build | ~3 KB | 6 MB |
| **Subquery total (allocated)** | **~10.5 MB each** | **~21 GB** |

21 GB allocated over 2,000 invocations. With `GOMEMLIMIT=512MiB`, the GC must collect
aggressively. The live set at any point is bounded by one subquery invocation (~10 MB)
plus the outer query retained (~15 MB) = **~25 MB live**. But the allocation rate
(~10 MB per outer row) outpaces GC.

**The query is theoretically executable if GC runs after every ~10–50 subquery
invocations, keeping the heap under 512 MiB.** But Go's default GC pacing may not be
aggressive enough, and retained operator state from the outer query (~15 MB) is never
freed until the outer plan's Run() returns.

### 5.2 Without stats (CROSS joins)

| Component | Peak allocation | Feasibility |
|-----------|---------------|-------------|
| Outer CROSS(part, supplier) | 200K × 10K × 16 = 2B rows × 100 bytes = **200 GB** | Impossible |
| Subquery CROSS(partsupp, supplier) | 800K × 10K × 12 = 8B rows × 100 bytes = **800 GB** | Impossible |

**Without ANALYZE stats, Q2 is fundamentally un-executable** — the CROSS joins alone
require hundreds of GB of memory. The OOM crash is not surprising.

## 6. Lower-Bound Conclusion

| Scenario | Minimum memory needed | Fits in 512 MiB? |
|----------|----------------------|-----------------|
| Without ANALYZE stats | > 200 GB (CROSS joins) | **No** |
| With ANALYZE stats, ideal GC | ~25 MB live + ~10 MB/subquery alloc → need aggressive GC | **Marginally** (depends on GC pacing) |
| With ANALYZE stats, subquery cached | ~25 MB live + ~10 MB cached subquery result | **Yes** |

**Dominant contributors:**
1. **Lack of ANALYZE stats → CROSS joins** (immediate OOM, #1 cause).
2. **Subquery per-row re-execution** (allocation rate exceeds GC reclaim rate, #2 cause).
3. **`drainRows` copies all children** (makes every join fully-buffering, preventing
   streaming execution).
4. **Operator `o.rows` not nilled on Close()** (retained outer query memory, #3 cause).

## 7. Reference

- `internal/executor/expr.go:617-662` — `evalSubquery` / `subqueryImpl`
- `internal/executor/operators_join_agg.go:40-75,653-667` — `joinOp.Open()`, `drainRows`
- `internal/planner/planner.go:297-308,2379-2394,610-656` — planSelect, planSubqueryExpr, join algo selection
- `internal/planner/pushdown.go:1-49` — `pushPredicatesIntoCrossJoins`
- `internal/planner/joinorder.go:60-139` — `reorderCommaFromByCardinality`
