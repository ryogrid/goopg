# Executor Operators (part 2) — Code Review 2026-08-31

Files: operators_generated.go, operators_index.go, operators_indexonly.go,
operators_join_agg.go, operators_lockrows.go, operators_material.go,
operators_memoize.go, operators_merge.go, operators_nljoin.go,
operators_ordinality.go, operators_pg_available_wal_summaries.go,
operators_pg_get_catalog_foreign_keys.go, operators_pg_get_publication_tables.go,
operators_pg_get_sequence_data.go, operators_pg_input_error_info.go,
operators_pg_options_to_table.go, operators_pg_partition_tree.go,
operators_pg_sequence_parameters.go, operators_project_set.go,
operators_recursive_cte.go, operators_reindex.go, operators_scalar_func_scan.go,
operators_sequence.go, operators_setop.go, operators_storage.go,
operators_trigger.go, operators_ts_token_type.go, operators_tx.go,
operators_upsert.go, operators_user_srf_scan.go, operators_utility_settings.go,
operators_vacuum.go, operators_vacuum_datfrozenxid.go, operators_verify_heapam.go,
operators_window.go, opnode.go

Findings count: 25

---

### `operators_join_agg.go:applyAgg` — string_agg O(n²) string concatenation
- **Issue**: `string_agg` without ORDER BY accumulates via `st.strResult += delim + sv` (lines 2903, 2932). Each concatenation allocates a new string copying the entire accumulator, yielding O(n²) time and allocation for large groups.
- **Why**: The ORDER BY path (strElems) defers to a `strings.Builder` at the end, but the non-ORDER BY path uses `+=`.
- **Suggestion**: Always accumulate via `strings.Builder` in `applyAgg` and finalize to `strResult` only at the end, or use the `strElems`+`strings.Builder` path for both ORDER BY and non-ORDER BY.
- **Severity**: high

### `operators_window.go:Open` — sort comparator re-evaluates expressions per comparison
- **Issue**: The `sort.SliceStable` comparator calls `evalExpr(pe, o.rows[i], ctx)` and `evalExpr(pe, o.rows[j], ctx)` for every partition key and order key on every comparison (O(n log n × k) evaluations). No memoization or precomputation of key tuples.
- **Why**: The child rows are already materialized; the expression values for a given row are fixed.
- **Suggestion**: Precompute a `[][]Datum` of key tuples once (O(n × k)) and sort by those, avoiding any expression re-evaluation in the comparator.
- **Severity**: medium

### `operators_window.go:Open` — redundant double-copy of child rows
- **Issue**: `slot.Materialize().Row()` returns a deep-copied row; then `dup := make(Row, len(row)); copy(dup, row)` copies it again. Materialize already produces an owned copy.
- **Why**: `Materialize().Row()` returns the embedded row, which is already an independent copy.
- **Suggestion**: Replace both lines with `dup := cloneRow(slot.Materialize().Row())` or just use the MaterializedSlot's row directly.
- **Severity**: low

### `operators_join_agg.go:applyAgg` — `inputIsInt` closure redefined per-row
- **Issue**: In the variance/stddev path, `inputIsInt` is defined as a closure inside the per-row loop (line 3064). The function body is constant — it depends only on `call.InputType.Name`.
- **Why**: Redefining a closure per row costs an allocation per row.
- **Suggestion**: Hoist the boolean check to the top of the per-row loop or compute it once per aggregate call.
- **Severity**: low

### `operators_setop.go:drainSetOpInput` — rowKey computed twice for EXCEPT/INTERSECT rows
- **Issue**: `drainSetOpInput` computes `rowKey(owned)` for every row and stores it in the `counts` map. Then `computeExcept`/`computeIntersect` recompute `rowKey(r)` for every left row. For UNION DISTINCT, the right-side `counts` map is built but never used.
- **Why**: Draining returns both rows and counts; the compute functions re-derive the key.
- **Suggestion**: Return `rowKey` strings alongside rows (or precompute keys in a parallel slice) so the compute functions do not re-derive them.
- **Severity**: low

### `operators_index.go:Next` / `operators_storage.go:Next` — per-row enum value linear scan
- **Issue**: Both `indexScanOp.Next` (lines 620-634) and `seqScanOp.Next` (lines 2178-2194) scan `et.Values` linearly per row to map an enum label to its sort order. For a table with enum columns this is a per-row O(values) scan.
- **Why**: The enum label→sort-order mapping is stable per scan.
- **Suggestion**: Build a `map[string]int16` per enum column in `openPrep`/`Open` (like `colInfo` caching already done for type names). Use one map lookup per row instead of the linear scan.
- **Severity**: medium

### `operators_indexonly.go:decodeRowFromKey` / `operators_indexonly.go:decodeRowFromHeap` — per-row map allocation + O(covered × columns) projection
- **Issue**: The multi-column fast path allocates a `map[string]Datum` per row (`make(map[string]Datum)`) and `projectCovered` re-looks up each covered column. `decodeRowFromHeap` uses a nested loop over Covered × Table.Columns per row.
- **Why**: Covered columns are fixed per scan; the projection can be done by index alone.
- **Suggestion**: Precompute a covered→key-column ordinal array once in `openPrep`; write directly to the output Row with no map. For the heap path, precompute a covered→table-column ordinal.
- **Severity**: medium

### `operators_generated.go:evalGeneratedExpr` — re-parses expression string per row
- **Issue**: `computeGeneratedColumns` calls `evalGeneratedExpr` per row, which does `parser.Parse("SELECT " + exprStr)` every time. The expression is fixed per column.
- **Why**: Parsing a full SQL statement per row is orders of magnitude above the cost of the arithmetic.
- **Suggestion**: Parse once at DDL/Open time, cache the parsed AST by column, and build a column-name→index map for ColumnRef resolution.
- **Severity**: medium

### `operators_storage.go:checkUniqueIndexesForInsert` / `maintainUniqueIndexesForInsert` — per-row per-index btree open
- **Issue**: Both functions loop over indexes on the table and call `openIndexBTree(ctx, idx, idxRel)` for each index, per row. For a bulk INSERT into a table with multiple unique indexes, this re-opens the btree handle (including root-page fetch and options recomputation) on every row.
- **Why**: The btree handle is not cached across rows.
- **Suggestion**: Cache the opened btree handles per index in the operator state (like `upsertOp.leafTrees` does for partitioned arbiter trees). Reuse across rows within the same statement.
- **Severity**: medium

### `operators_storage.go:writeHeapRowReturning` — `tryAppendToBlock` function defined inside the outer function body
- **Issue**: `tryAppendToBlock` is a closure defined inside `writeHeapRowReturning` and `writeHeapRowReturningPG`. Each call to `writeHeapRowReturning` allocates this closure. Written to the same buffer pool, the same chain of NBlocks checks, FSM queries, and extension logic are repeated.
- **Why**: The closure captures `tuple`, `logHeap`, `logDel` etc. and is defined per call to the outer function, allocating on every heap row write.
- **Suggestion**: Extract the closure into a method on a helper struct that captures the shared state once, or restructure to avoid the per-write closure allocation.
- **Severity**: low

### `operators_join_agg.go:aggregateOp.Open` — per-row allocations for group key values
- **Issue**: `evalGroupExprs` does `make(Row, 0, n)` and `make([]string, 0, n)` on every input row, plus `datumKey(v)` per group column (allocating a string per column per row).
- **Why**: The values are consumed immediately; buffers could be reused.
- **Suggestion**: Reuse per-operator scratch buffers for `vals` and `parts`, truncating each iteration. The `parts` strings decay after `setGroupKey` copies them into the map key.
- **Severity**: medium

### `operators_merge.go:mergedRow` — per-candidate-pair allocation
- **Issue**: For each (target, source) ON-condition evaluation, `mergedRow` does `make(Row, len(tgt)+len(src))` + two copies. The same pattern repeats in `applyMod`'s EPQ loop and step 2b.
- **Why**: The combined row is consumed immediately by `evalExpr` and never retained; a single reusable buffer would serve all evaluations in a given loop.
- **Suggestion**: Hoist one scratch Row per loop and overwrite it per pair.
- **Severity**: medium

### `operators_ordinality.go:Next` — per-row Fresh Row allocation
- **Issue**: `ordinalityOp.Next` does `make(Row, len(childRow)+1)` + copy per row; `rowsFromOp.Next` builds a fresh `combined` slice per call.
- **Why**: The row is consumed by the next operator immediately; a reusable buffer would avoid the churn.
- **Suggestion**: Keep a per-operator scratch Row buffer (grown as needed) and overwrite it per Next.
- **Severity**: low

### `operators_project_set.go:openSelectSrfMode` — per-step output row allocation
- **Issue**: For each SRF step, `outRow := make(Row, n)` + `copy(outRow, otherVals)` + per-step `evalRow` allocation. A million-step generate_series allocates a million Row slices.
- **Why**: Output rows are appended to `o.rows` and consumed later; the stage buffer could be reused.
- **Suggestion**: Reuse one scratch `outRow`/`evalRow` buffer across steps within a child row; allocate the retained copy only on `append`.
- **Severity**: medium

### `operators_index.go:currentTID` / `operators_lockrows.go:drainAndStamp` — RelFileNode recomputed per row
- **Issue**: `indexScanOp.currentTID` calls `o.ctx.Catalog.RelFileNode(o.plan.Table)` per invocation (operators_index.go:748). `lockRowsOp.drainAndStamp` calls `o.ctx.Catalog.RelFileNode(lk.Table)` per drained row. Both were already resolved at Open.
- **Why**: The RelFileNode is invariant for the operator lifetime.
- **Suggestion**: Cache the RelFileNode in a field and return it directly.
- **Severity**: low

### `operators_lockrows.go:parseRowCTID` — fmt.Sscanf per row
- **Issue**: `fmt.Sscanf` (reflection-based) is used to parse the `(block,offset)` tid string per row in the resjunk-ctid path.
- **Why**: `fmt.Sscanf` is ~10x slower than manual parsing.
- **Suggestion**: Parse manually (find `(`, split on `,`, `strconv.ParseInt` both fields).
- **Severity**: low

### `operators_generated.go:datumToString` / `evalGenBinary` — fmt.Sprintf + big.Int Exp per op
- **Issue**: `datumToString` uses `fmt.Sprintf("%d", d.Int)`; `evalGenBinary` builds `new(big.Int).Exp(big.NewInt(10), ...)` per scale alignment.
- **Why**: Generated-column eval is per-row; `strconv.FormatInt` and a cached power-of-ten table avoid the allocs.
- **Suggestion**: Use `strconv.FormatInt`; precompute/cache `10^k` big.Ints.
- **Severity**: low

### `operators_join_agg.go:finalizeGroup` — rebuilds slotCount map per group
- **Issue**: `finalizeGroup` recomputes `slotCount` (loop over all Aggs) and allocates `distinctSlotStates` for every group. Both depend only on `o.plan.Aggs`, which is fixed.
- **Why**: For many groups with shared user-agg slots, this is repeated identical work.
- **Suggestion**: Compute `slotCount` once in Open and store it.
- **Severity**: low

### `operators_generated.go:applyDefaultsForMissing` — strings.EqualFold column resolution loop
- **Issue**: `evalGenExpr` resolves `ColumnRef` by name via a `strings.EqualFold` loop over all columns. On a table with many columns this is per-reference O(columns).
- **Why**: The column index is fixed per schema.
- **Suggestion**: Build a name→index map once (e.g. at parse time or Open time).
- **Severity**: low

### `operators_sequence.go:seqKey` — fmt.Sprintf per lookup
- **Issue**: `seqKey` uses `fmt.Sprintf("%d:%s", ...)` per call. Called from `LookupSequence`, `evalNextval`, `evalCurrval`, etc. — on the hot INSERT path for every serial column.
- **Why**: `fmt.Sprintf` is slower than `strconv` + manual concatenation.
- **Suggestion**: Use `strconv.FormatUint` + `strings.Builder` or precompute the dbOid prefix.
- **Severity**: low

### `operators_storage.go:seqScanOp.Next` — fmt.Sprintf for resjunk ctid per row
- **Issue**: `fmt.Sprintf("(%d,%d)", block, slot)` is used per row to format the ctid string (operators_index.go:712, operators_storage.go:2258).
- **Why**: `fmt.Sprintf` is slower than `strconv`.
- **Suggestion**: Use `strconv.AppendInt` into a `[]byte` buffer, or precompute a per-block format string.
- **Severity**: low

### `operators_memoize.go:Next` — per-row datumKey purely for byte accounting
- **Issue**: In fill mode, `len(datumKey(cp[i]))` is called per datum per row solely to estimate byte size — this allocates a string and then discards it.
- **Why**: The byte budget is an approximation; datum size can be estimated from Kind/type without rendering a key string.
- **Suggestion**: Use a size-estimation helper based on `d.Kind` and `d.Len()` (if available).
- **Severity**: low

### `operators_recursive_cte.go:rowKey` — full string rendering per row for UNION dedup
- **Issue**: `rowKey` renders every column datum via `d.Format()` and concatenates into a `strings.Builder`, producing a heap string per row for UNION dedup.
- **Why**: String-form canonicalization is the simplest correct cross-type dedup, but per-row `Format` calls allocate per datum.
- **Suggestion**: For a faster path, hash columns into an int64/struct key where kinds are uniform; keep the string path for mixed-kind rows. At minimum, reuse the `strings.Builder` buffer across rows.
- **Severity**: low

### `slot.go:asSlot` (callers: nextMerge, recursiveUnionOp, workTableScanOp) — MaterializedSlot allocated per emitted row
- **Issue**: `asSlot` calls `SlotFromRow`, which allocates a fresh `*MaterializedSlot` on every `Next()` call. Many siblings (indexScanOp, materializeOp) embed a reusable slot.
- **Why**: Slot reuse is the established pattern in this package.
- **Suggestion**: Give each of these operators an embedded `MaterializedSlot` field and re-point it per Next.
- **Severity**: medium

### `operators_upsert.go:maintainNonArbiterIndexesCapture` — btree re-opened per index per row
- **Issue**: On every inserted row, `maintainNonArbiterIndexesCapture` (and `maintainNonArbiterIndexesForUpdate`) loops over the table's non-arbiter indexes and calls `openIndexBTree` per index — even though the arbiter tree is already cached in `o.arbiterTree`/`o.leafTrees`.
- **Why**: Only the arbiter handles are cached; secondary indexes are re-opened per row.
- **Suggestion**: Cache the non-arbiter tree handles alongside `leafTrees` (keyed by index OID) and reuse them across rows.
- **Severity**: medium

### Files with no significant findings
- operators_material.go, operators_nljoin.go, operators_trigger.go, operators_tx.go,
  operators_ts_token_type.go, operators_user_srf_scan.go, operators_utility_settings.go,
  operators_vacuum.go, operators_vacuum_datfrozenxid.go, operators_verify_heapam.go,
  operators_reindex.go, operators_scalar_func_scan.go,
  operators_pg_available_wal_summaries.go, operators_pg_get_catalog_foreign_keys.go,
  operators_pg_get_publication_tables.go, operators_pg_get_sequence_data.go,
  operators_pg_input_error_info.go, operators_pg_options_to_table.go,
  operators_pg_partition_tree.go, operators_pg_sequence_parameters.go,
  opnode.go