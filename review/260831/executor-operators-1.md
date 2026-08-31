# Executor Operators (part 1) — Code Review 2026-08-31

Files: operators.go, operators_analyze.go, operators_bitmap.go, operators_bt_index_check.go, operators_call.go, operators_checkpoint.go, operators_cluster.go, operators_cte_dml.go, operators_ddl.go, operators_ddl_database_acl.go, operators_ddl_default_privileges.go, operators_ddl_parameter_acl.go, operators_ddl_partition.go, operators_ddl_role_membership.go, operators_distinct.go, operators_explain.go, operators_explain_format.go, operators_fk.go, operators_from_regexp_matches.go, operators_from_regexp_split_to_table.go, operators_from_unnest.go, operators_gather.go, operators_gather_merge.go, operators_generate_series.go
Findings count: 14

---

### `operators.go:sortOp.lessRows` — Sort key expressions re-evaluated on every comparison
- **Issue**: Each comparison in the sort re-evaluates every ORDER BY key expression via `evalSortKeyValue` for both rows. With N rows and O(N log N) comparisons, each key expression is evaluated O(N log N) times instead of O(N).
- **Why**: `lessRows` calls `evalSortKeyValue(k.Expr, a, ctx)` and `evalSortKeyValue(k.Expr, b, ctx)` per comparison. The same row's values are recomputed every time it participates in a comparison. PG solves this via SortSupport (precomputed abbreviated keys).
- **Suggestion**: Precompute sort-key datums per row at materialization time (in `Open` when rows are buffered), then compare precomputed values. This is `O(N)` evaluations of key expressions instead of `O(N log N)`.
- **Severity**: medium

### `operators.go:sortOp.lessRows` — `isRegSortFamilyTypeName` allocates per call via `strings.ToLower`
- **Issue**: `isRegSortFamilyTypeName` calls `strings.ToLower(name)` on every invocation, allocating a new string. In `evalSortKeyValue`, this is called on every comparison whenever the key is a `CastExpr` with a reg*-family target type. Each sort comparison that hits a reg* cast pays an allocation.
- **Why**: The target type name is a constant property of the plan — it could be lowered once at plan construction time.
- **Suggestion**: Store the lowercased target type name on the `CastExpr` node, or make `isRegSortFamilyTypeName` use a case-insensitive comparison that does not allocate (e.g., compare against a pre-lowered set).
- **Severity**: low

### `operators.go:valuesOp.Next` — Allocates a fresh Row per emitted row
- **Issue**: `row := make(Row, len(exprs))` allocates a new `Row` slice on every `Next()` call. For a VALUES clause with many rows, this is O(rows) allocations.
- **Why**: The slot is returned to the parent and must outlive the call. However, the row is immediately consumed by the parent (values are typically projected once) and could be reused.
- **Suggestion**: Reuse a scratch row buffer (like `projectOp.out` or `resultOp.out`), materializing only when the slot lifetime requires it.
- **Severity**: low

### `operators_analyze.go:analyzeRelationWith` — Decodes every visible tuple but only keeps a fraction
- **Issue**: Every visible tuple is decoded into a full `Row` (`make(Row, len(tbl.Columns))` + `DecodeRowIntoMctxPGTuple`), but only the reservoir sample (~target*300 = ~30,000 rows) is actually kept. For a large table, millions of tuples are decoded then discarded.
- **Why**: The reservoir sampling decision (line 573: `j := rng.Int63n(seen+1)`) is made before the row is needed. The decode is unconditional on every visible tuple.
- **Suggestion**: Run the reservoir decision first (increment `seen`, roll the dice), then only decode the row if it is selected for the reservoir. The `totalBytes` accounting for `AvgWidth` uses the tuple header + data length, not the decoded row, so it does not depend on the decode.
- **Severity**: medium

### `operators_analyze.go:computeColumnStats` — MCV split loop O(m²) recomputation
- **Issue**: The MCV admission loop (lines 902-929) recomputes `remaining` from scratch each iteration by summing all admitted buckets' counts: `for k := 0; k <= mcvCount; k++ { remaining -= buckets[k].count }`. This is O(mcvCount²) nested accumulation.
- **Why**: A running sum of admitted counts would eliminate the inner loop.
- **Suggestion**: Maintain a `sumAdmitted` variable, subtracting `buckets[mcvCount].count` from `remaining` once per iteration instead of recomputing from scratch.
- **Severity**: low (mcvCap is bounded by statsTarget, default 100)

### `operators_analyze.go:computeColumnStats` — Expanded slice capacity too small, causes repeated reallocation
- **Issue**: `make([]Datum, 0, nonNull-(nonNull-len(nonMCV)))` simplifies to `len(nonMCV)` capacity, but the slice is filled with each value repeated `b.count` times. The actual length is the sum of counts of all non-MCV buckets, which is typically much larger than `len(nonMCV)`. The slice grows and reallocates repeatedly.
- **Why**: The capacity expression is mathematically wrong — it should be the total count of non-MCV values.
- **Suggestion**: Precompute the sum of non-MCV bucket counts and use that as the capacity.
- **Severity**: low

### `operators_bitmap.go:bitmapIndexScanOp.buildBitmap` — Allocates a slice per index entry in hot loop
- **Issue**: The `scanFn` closure (line 142-144) creates `[]storage.ItemPointer{ptr}` on every invocation. For a bitmap index scan over millions of index entries, this is millions of slice header allocations.
- **Why**: `tbmAddTuples` accepts `[]storage.ItemPointer`, but the call site has only one TID at a time.
- **Suggestion**: Add a `tbmAddOne(tid, recheck)` method that avoids the slice allocation, or hoist a reusable 1-element slice outside the closure.
- **Severity**: medium

### `operators_call.go:callArgTypeCompatible` — Allocates two maps on every CALL
- **Issue**: `intFamily` and `numFamily` map literals are allocated on every call to `callArgTypeCompatible`, which is called for every candidate routine during CALL resolution.
- **Why**: The maps are identical on every invocation.
- **Suggestion**: Move the maps to package-level `var` declarations.
- **Severity**: low

### `operators_distinct.go:distinctOnOp.Next` — String concatenation in a loop per row
- **Issue**: `key += datumKey(row[idx]) + "\x00"` concatenates strings per key column per row, allocating a fresh string each time (DISTINCT ON over a large pre-sorted stream).
- **Why**: String `+=` in a loop is O(n²) in the number of key columns and allocates on every append.
- **Suggestion**: Use a `strings.Builder` (reused across rows or reset per row) to build the key.
- **Severity**: low

### `operators_generate_series.go:generateSeriesOp.Next` — Allocates a Row per emitted value
- **Issue**: `SlotFromRow(nil, Row{val})` allocates a new `Row` slice on every call. `generate_series(1, 1000000)` performs 1M small heap allocations. Same for `generateSubscriptsOp.Next`.
- **Why**: The row content is a single scalar that changes each call, so a scratch row could be reused.
- **Suggestion**: Keep a reusable `o.scratch []Datum` of length 1 and mutate `scratch[0]` each call before `SlotFromRow`.
- **Severity**: medium

### `operators_fk.go:fkRowMatches` — Per-row linear column-name lookup on every row of a table scan
- **Issue**: `fkRowMatches` (and `fkColValues`, and `scanRefTableForDetachedPartitionMatch`'s inline equivalent) scans `cols` linearly with `strings.EqualFold` per FK column for **every** row visited in the parent/child scans (`scanRelForMatch`, `scanRelForFKMatch`, `fullTableFKCheckRel`, `detectInFlightChildInsert`).
- **Why**: The FK column name → column index mapping is fixed for the whole scan but re-derived per tuple, so a scan over N rows does N×len(cols) case-insensitive string compares.
- **Suggestion**: Precompute the FK column indexes once per scan (like `scanRefTableForDetachedPartitionMatch` already does for `parentIdx` — its child-side lookup is the inconsistent half) and index into `row` directly.
- **Severity**: medium

### `operators_fk.go:fkSetNull` — Nested linear column lookup per matching row
- **Issue**: For each matching row, `for _, fkCol := range fk.Columns { for i, c := range cols { ... } }` re-scans all columns with `EqualFold` to null out the FK columns.
- **Why**: The affected column indexes are constant for the operation.
- **Suggestion**: Compute the column indexes once before the page scan loop.
- **Severity**: low

### `operators_gather_merge.go:gatherMergeOp.lessRows` — Sort key expressions re-evaluated on every heap comparison
- **Issue**: Same shape as `sortOp.lessRows`: each row's key expressions are evaluated afresh for every heap sift it participates in. Every source row is heap-adjusted O(log n) times.
- **Why**: Key evaluation is repeated work per row; the keys could be materialized once per row when a source's `cur` is advanced.
- **Suggestion**: Precompute key datums per row in `advance` and compare those.
- **Severity**: low

### `operators_ddl.go:execTruncate` — FK-cascade check rebuilds per-iteration data and re-lowers names
- **Issue**: The BFS FK check loops over every set entry × every table in the database, building a fresh `tblNames` slice (with `strings.ToLower` allocations per ancestor) on each `other` iteration, and re-lowers each FK's `RefTable` name. `sortedTruncateTableSet`'s sort comparator also calls `strings.ToLower(entries[i].tbl.Name)` on every comparison — O(n log n) lowercasing of the same strings.
- **Why**: All of the lowercased names and ancestor chains are loop-invariant.
- **Suggestion**: Precompute lowered names and ancestor-name sets once per table; lower the sort keys once before sorting.
- **Severity**: low (DDL, one-shot, but the work is trivially hoistable)

### `operators_explain_format.go` / `operators_explain.go`
- No significant waste: formatting code runs once per EXPLAIN; per-call builders/sorted-key slices are negligible at that frequency.

## Files with no issues found

- **operators_checkpoint.go** (38 lines): Trivial, one-shot side effect. No wasteful patterns.
- **operators_bt_index_check.go**: One-shot amcheck path. No significant waste.
- **operators_cluster.go**: Double call to `IndexesOnTable` in `markTableClusterIndex` is minor (one-time, not a hot path). Not worth flagging.
- **operators_cte_dml.go**: Row cloning is inherent to the materialization contract. No wasteful patterns.
- **operators_ddl_database_acl.go**, **operators_ddl_default_privileges.go**, **operators_ddl_parameter_acl.go**, **operators_ddl_role_membership.go**: One-shot GRANT/REVOKE path; the per-statement `make([]string,…)` and map allocations are negligible.
- **operators_ddl_partition.go**: Partition/bound validation is one-shot DDL; the small nested scans and `strings.ToLower` calls are not worth changing.
- **operators_from_regexp_matches.go**, **operators_from_regexp_split_to_table.go**: Tiny SRF adapters; rows are materialized once at Open. No issues.
- **operators_from_unnest.go**: Per-row `make(Row, len(arrays))` in the multi-arg path is bounded by the array size at Open time; negligible.
- **operators_gather.go**: Batch/flush design is sound; `MaterializeForTransfer` per row is the ownership boundary, not waste.