# Executor Operators (part 2) — Bug Review 2026-08-31

Files: All 36 files in the assignment list were read.
Findings count: 8 (7 open + 1 contested)

---

### `operators_indexonly.go:Rescan` — range-scan strictness operators (LowOp/HighOp) ignored; both bounds always inclusive
- **Bug**: In the `default:` (range scan) branch of `Rescan`, `RangeScanWithPosLeafFilter` is called with `false, false` (both inclusive), regardless of the actual `plan.LowOp`/`plan.HighOp` strictness. The sibling `indexScanOp.Rescan` correctly passes `o.plan.LowOp == parser.OpGt` and `o.plan.HighOp == parser.OpLt`. For a strict `WHERE x > 5`, the index-only scan returns `x >= 5` (including the boundary value), while the index scan correctly excludes it. Additionally, the composite-index padding logic for the low bound is entirely missing (the index scan pads lo when `LowOp == OpGt`, and pads hi only when `HighOp != OpLt`; the IOS pads hi unconditionally when `hiBytes != nil` and never pads lo).
- **When it triggers**: any range scan (`WHERE x > 5`, `WHERE x < 10`, `WHERE x >= 5 AND x < 10`, etc.) on a composite index, and any range scan with a strict inequality on any index, when the planner chooses an index-only scan over an index scan. Returns wrong row set (too many or too few rows).
- **Fix**: pass the actual LowOp/HighOp exclusive flags; mirror the composite-padding logic from indexScanOp.
- **Severity**: **high**

### `operators_sequence.go:seqState.nextVal` — int64 overflow wraps to negative values instead of erroring
- **Bug**: `next := cur + s.increment` uses int64 addition with no overflow guard. When `current` is near `math.MaxInt64` and `increment > 0` (or near MinInt64 with negative increment), `next` wraps past the boundary; the `next > s.max` / `next < s.min` checks then compare a wrapped (negative/positive) value and fail, so the sequence silently starts issuing wrapped negative (or huge positive) values forever instead of raising `2200H` "reached maximum/minimum value". PG avoids this by doing the boundary math in uint64/checked form.
- **When it triggers**: a bigint sequence advanced past its max (or min) — e.g. `SELECT nextval('s') FROM generate_series(1, 9223372036854775807)`.
- **Fix**: detect overflow (`if cur > s.max - s.increment` for positive increment) and apply the max/cycle logic against the not-yet-wrapped next value.
- **Severity**: medium

### `operators_project_set.go:openSelectSrfMode` — generate_series loop can overflow int64 and spin forever
- **Bug**: `for v := start; v <= stop; v += step` (step>0) and `for v := start; v >= stop; v += step` (step<0) do no overflow guard. With `start` near `math.MaxInt64` and positive step, `v += step` wraps to a very negative value, the `v <= stop` test stays true, and the loop never terminates. Same for the negative-step arm wrapping upward.
- **When it triggers**: any large-bound generate_series where the last `v += step` wraps past MaxInt64/MinInt64; the executor hangs.
- **Fix**: guard the increment (`if v > stop-step { break }`) before wrapping.
- **Severity**: medium

### `operators_recursive_cte.go:recursiveUnionOp.Open` — phase state not reset on re-open
- **Bug**: `Open()` only opens the anchor; it does not reset `initDone`, `done`, `outIdx`, `depth`, `output`, `working`, `seen`. `Close()` nulls output/working/seen but leaves `initDone`/`done`/`outIdx`/`depth` stale. A second Open (rescan of a recursive CTE node, e.g. inside a nested loop or an executed-twice subplan) resumes from stale state: `initDone` already true means the anchor is never re-read, and `outIdx` may exceed the (now empty) output → immediate EOF.
- **When it triggers**: WITH RECURSIVE under a rescanning parent.
- **Fix**: reset all phase fields in `Open()` (and in `Close()`).
- **Severity**: medium

### `opnode.go:limitOpNext` — FETCH FIRST 0 ROWS WITH TIES panics indexing nil tieKeyVals
- **Bug**: When `withTies` and `limitCount == 0`, the first call enters `s.emitted >= s.limitCount`, pulls a child row, and compares `datumEquals(v, s.tieKeyVals[i])` against `s.tieKeyVals` which was never populated (the `s.emitted == s.limitCount` block only runs after a row is emitted) — nil slice index panic.
- **When it triggers**: `FETCH FIRST 0 ROWS WITH TIES` reaches execution; crashes the backend instead of returning 0 rows.
- **Fix**: guard `s.emitted == 0 && s.limitCount == 0` → return EOF without pulling/comparing; populate tieKeyVals on the first emitted row for ties.
- **Severity**: medium

### `operators_pg_options_to_table.go:Open` — lateral binding uses ctx.OuterRows instead of bound slot (BindLateralOuter)
- **Bug**: unlike the other lateral SRFs (pg_get_sequence_data, pg_get_publication_tables, verify_heapam), this op reads `ctx.OuterRows[len-1]` in Open and never implements `BindLateralOuter`, so the executor's `lateralBindable` dispatch never binds it. Under a `Join.Lateral` whose outer row is pushed via BindLateralOuter (the opNodeOperator path), the argument evaluates against a stale/missing outer row.
- **When it triggers**: pg_options_to_table in a lateral correlated position (the pg_dump usage) where the driver only sets the bound slot.
- **Fix**: add a `outerSlot SlotView` field + `BindLateralOuter` like the sibling SRFs and evaluate via `evalExprSlot(arg, o.outerSlot, ctx)`.
- **Severity**: low

### `operators_generated.go:evalGenExpr` ColumnRef silently returns NULL on out-of-range column index
- **Bug**: `strings.EqualFold(col.Name, x.Column) && i < len(row)` — the `i < len(row)` check is in the wrong order (after EqualFold). If the column name matches but the column index `i` is >= len(row), the function silently returns NullDatum instead of an error. Also `computeGeneratedColumns` and `applyDefaultsForMissing` write `row[i]` for every generated/default column slot without first checking `i < len(row)`, so a caller passing a short row panics.
- **When it triggers**: a caller passes a row shorter than the column list (unlikely in practice, but silently produces wrong generated values on the ColumnRef path instead of erroring).
- **Fix**: check `i < len(row)` before the EqualFold; add bounds guard before `row[i] = val` in the two callers.
- **Severity**: low

### `operators_utility_settings.go:nextShow` — SHOW ALL returns 2 columns vs PG's 3 (name, setting, description)
- **Bug**: PG's `ShowAllGUCConfig` (guc_funcs.c:456) emits `(name, setting, description)`; goopg emits `(name, value)` rows. The output schema has 2 columns where PG has 3.
- **When it triggers**: `SHOW ALL` via the executor path.
- **Fix**: emit a third `description` column (empty string is fine for goopg).
- **Severity**: low

---

## Files reviewed — all 36

| File | Status | Notes |
|------|--------|-------|
| operators_generated.go | reviewed | 1 finding (low) |
| operators_index.go | reviewed | no bugs found |
| operators_indexonly.go | reviewed | **1 finding (high)** |
| operators_join_agg.go | reviewed | no bugs found |
| operators_lockrows.go | reviewed | no bugs found |
| operators_material.go | reviewed | no bugs found |
| operators_memoize.go | reviewed | no bugs found |
| operators_merge.go | reviewed | no bugs found |
| operators_nljoin.go | reviewed | no bugs found |
| operators_ordinality.go | reviewed | no bugs found |
| operators_pg_available_wal_summaries.go | reviewed | no bugs found |
| operators_pg_get_catalog_foreign_keys.go | reviewed | no bugs found |
| operators_pg_get_publication_tables.go | reviewed | no bugs found |
| operators_pg_get_sequence_data.go | reviewed | no bugs found |
| operators_pg_input_error_info.go | reviewed | no bugs found |
| operators_pg_options_to_table.go | reviewed | 1 finding (low) |
| operators_pg_partition_tree.go | reviewed | no bugs found |
| operators_pg_sequence_parameters.go | reviewed | no bugs found |
| operators_project_set.go | reviewed | 1 finding (medium) |
| operators_recursive_cte.go | reviewed | 1 finding (medium) |
| operators_reindex.go | reviewed | no bugs found |
| operators_scalar_func_scan.go | reviewed | no bugs found |
| operators_sequence.go | reviewed | 1 finding (medium) |
| operators_setop.go | reviewed | no bugs found |
| operators_storage.go | reviewed | no bugs found |
| operators_trigger.go | reviewed | no bugs found |
| operators_ts_token_type.go | reviewed | no bugs found |
| operators_tx.go | reviewed | no bugs found |
| operators_upsert.go | reviewed | no bugs found |
| operators_user_srf_scan.go | reviewed | no bugs found |
| operators_utility_settings.go | reviewed | 1 finding (low) |
| operators_vacuum.go | reviewed | no bugs found |
| operators_vacuum_datfrozenxid.go | reviewed | no bugs found |
| operators_verify_heapam.go | reviewed | no bugs found |
| operators_window.go | reviewed | no bugs found |
| opnode.go | reviewed | 1 finding (medium) |