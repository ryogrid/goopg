# InitDB — Code Review 2026-08-31

Files: aio_views.go, auth_bootstrap.go, btree_index_bootstrap.go, catalog_cache.go,
catalog_heap_reload.go, catalog_seed.go, checksum_bootstrap.go, config_seed.go, encoding.go,
information_schema_proc_seed.go, information_schema_proc_sqlbody.go, information_schema_sequences_view.go,
information_schema_tables.go, information_schema_view_oid_pins.go, information_schema_view_seed_data.go,
initdb.go, locale.go, nailed_view_ev_action.go, nailed_view_seed_data.go, open.go,
pg_aggregate_bootstrap.go, pg_aggregate_view.go, pg_amproc_entries.go, pg_cast_bootstrap.go,
pg_collation_bootstrap.go, pg_constraint_bootstrap.go, pg_conversion_bootstrap.go, pg_language_bootstrap.go,
pg_operator_bootstrap.go, pg_opfamily_bootstrap.go, pg_proc_proname_args_nsp_index_bootstrap.go,
pg_proc_seed_data.go, pg_proc_seed_defaults.go, pg_proc_view.go, pg_range_bootstrap.go,
pg_rewrite_bootstrap.go, pg_rewrite_toast_bootstrap.go, pg_rewrite_toast_writer.go, pg_sequences_view.go,
pg_stat_activity_view.go, pg_stat_ssl_gssapi_view.go, pg_tablespace_bootstrap.go, pg_type_bootstrap.go,
pg_type_seed_data.go, pgcontrol.go, pglz.go, recovery_state.go, relcache_init.go, replication_views.go,
standby.go, syncfs_linux.go, syncfs_other.go, system_view_oid_pins.go, timeline.go, wal_bootstrap.go,
wal_io_views.go, xact_recovery.go
Findings count: 14

## Progress

All 57 assigned files reviewed. Files with no findings (data/seed/bootstrap helper files with
straightforward single-pass code): auth_bootstrap.go, catalog_seed.go, config_seed.go, encoding.go,
information_schema_proc_seed.go, information_schema_proc_sqlbody.go, information_schema_sequences_view.go,
information_schema_tables.go, information_schema_view_oid_pins.go, information_schema_view_seed_data.go,
locale.go, nailed_view_ev_action.go, nailed_view_seed_data.go, pg_aggregate_bootstrap.go,
pg_amproc_entries.go, pg_cast_bootstrap.go, pg_collation_bootstrap.go, pg_constraint_bootstrap.go,
pg_conversion_bootstrap.go, pg_language_bootstrap.go, pg_operator_bootstrap.go, pg_opfamily_bootstrap.go,
pg_proc_proname_args_nsp_index_bootstrap.go, pg_proc_seed_data.go, pg_proc_seed_defaults.go,
pg_range_bootstrap.go, pg_rewrite_bootstrap.go, pg_rewrite_toast_bootstrap.go, pg_rewrite_toast_writer.go,
pg_sequences_view.go, pg_stat_ssl_gssapi_view.go, pg_tablespace_bootstrap.go, pg_type_seed_data.go, pglz.go,
recovery_state.go, relcache_init.go, standby.go, syncfs_linux.go, syncfs_other.go,
system_view_oid_pins.go, timeline.go, wal_bootstrap.go, wal_io_views.go, xact_recovery.go.

---

## Findings

### `aio_views.go:registerStatAIOView` / `registerPgStatAIOTargetsView` / `registerPgAiosView` — fmt.Sprintf per cell in virtual view rows
- **Issue**: Every integer column is rendered with `fmt.Sprintf("%d", ...)` (15+ call sites), and `fmt`'s reflection-based formatting is markedly slower than `strconv.FormatUint`. These closures run on every `SELECT * FROM pg_stat_aio` / `pg_stat_aio_targets` / `pg_aios`.
- **Why**: The views are observability-loop targets (`\watch pg_aios`); `fmt.Sprintf` does reflection and allocates through the generic path where `strconv` is specialized and allocation-light. Small result sets make this minor, but it is the hottest rendering path in this package's views.
- **Suggestion**: Replace with `strconv.FormatUint(uint64(x), 10)`; in the row-building loops, reuse a small scratch buffer with `strconv.AppendUint`. Same for `avgLatencyUS`.
- **Severity**: low

### `catalog_cache.go:catalogCachePath` — fmt.Sprint for the dbOid
- **Issue**: `fmt.Sprint(dbOid)` (reflection-based) is used to build the path on every cache write/read/unlink.
- **Why**: `strconv.FormatUint(uint64(dbOid), 10)` is cheaper and allocation-light.
- **Suggestion**: Use `strconv.FormatUint`.
- **Severity**: low

### `checksum_bootstrap.go:stampClusterChecksums` / `checksumRelationData` — whole-file read + full copy + write
- **Issue**: Each relation file is `os.ReadFile`-ed in full, then `checksumRelationData` makes a second full-size `make([]byte, len(raw))` + `copy`, and `os.WriteFile` adds a third allocation for the write path. Peak memory is ~2–3× the largest relfile.
- **Why**: initdb-time only, but the largest relfiles are tens of MB and the outer copy is unnecessary — `PageSetChecksumCopy` copies each block internally anyway, so writing the checksummed blocks directly into `raw` (a fresh read buffer) before the single `WriteFile` yields the same bytes with one less full-file allocation.
- **Suggestion**: Check-sum blocks in place in `raw` (or stream block-by-block) and drop the `out` copy.
- **Severity**: low

### `pg_aggregate_view.go:registerPgAggregateView` — pgAggregateInitialEntries() rebuilt on every query
- **Issue**: The `VirtualRows` closure calls `pgAggregateInitialEntries()`, which allocates a fresh 161-element `[]pgAggregateEntry` on **every** scan of `pg_aggregate`. The data is a compile-time constant and never mutates.
- **Why**: Any query touching pg_aggregate (pg_dump's `dumpAgg`, `\d` probes) re-materializes the whole table; cost is per-query allocation, not one-time init.
- **Suggestion**: Memoize with `sync.OnceValue(func() []pgAggregateEntry { return pgAggregateInitialEntries() })` — the same pattern already used for `pgProcSeedDefaultsTrees` and `pgProcOIDNameIndex` in this package.
- **Severity**: medium

### `pg_proc_view.go:registerPgProcView` — repeated typeNameToOIDStr/ToLower work per row
- **Issue**: `typeNameToOIDStr` (a ~60-case switch over `strings.ToLower(t)`), `langNameToOIDStr`, `strings.Join(argOIDs, " ")`, and the two `modes` scans in `pgArgModesLiteral`/`pgAllArgTypesLiteral` run per routine row, per query of `pg_proc`. `strings.ToLower` allocates a new string per arg-type call; the built-in-stub branch re-runs `strings.Fields(b.argTypes)` on the same constant string per stub row.
- **Why**: pg_proc is one of the most-queried catalog views; the arg-OID conversion allocates once per arg per row. Costs compound for clusters with many functions.
- **Suggestion**: Precompute the lowercase→OID mapping once (inputs are canonical), precompute the `builtinProcs` argcounts/argtypes at init, and merge the two `modes` scans.
- **Severity**: low

### `pg_stat_activity_view.go:numericPIDOrNull` — range loop over runes
- **Issue**: `for _, r := range pid` decodes UTF-8 runes to test ASCII digits; a byte loop is faster and sufficient.
- **Why**: Runs once per backend row per pg_stat_activity query — tiny, but trivially improvable.
- **Suggestion**: `for i := 0; i < len(pid); i++` testing `pid[i]`.
- **Severity**: low

### `replication_views.go:formatLSN` / `formatStringList` — fmt.Sprintf + string concatenation in per-row view rendering
- **Issue**: `formatLSN` uses `fmt.Sprintf("%X/%X", hi, lo)` and `formatStringList` builds with `out += x` in a loop; both run per row of the replication views.
- **Why**: `strconv.AppendUint(buf, x, 16)` into a small stack buffer avoids the fmt reflection path; `strings.Builder` avoids O(n²) concatenation when a subscription has many publications (per row of `pg_subscription`).
- **Suggestion**: Rewrite `formatLSN` with `strconv.AppendUint` into a `[16]byte` scratch; rewrite `formatStringList` with `strings.Builder`.
- **Severity**: low

### `initdb.go:writeMultiPageHeap` / `writeMultiPageHeapRowsExternal` — hasVarWidthCol recomputed per row (loop invariant)
- **Issue**: `if hasVarWidthCol(cols) { … }` is evaluated inside the per-row loop, but `cols` never changes. `hasVarWidthCol` linearly scans the column list (10–32 entries) for every row, so pg_proc's 3397 rows cost 3397 redundant scans (and likewise pg_attribute's ~600 rows, pg_operator's 799, pg_amop's ~400, pg_cast's 235, …).
- **Why**: The result is identical for every row; only the first evaluation matters. This is a textbook hoistable loop invariant.
- **Suggestion**: Compute `hasVarWidth := hasVarWidthCol(cols)` once before the loop.
- **Severity**: medium

### `initdb.go:pgClassRow` — per-row linear scans through nail lists
- **Issue**: `pgClassRow` calls `pgClassRelnamespaceFor(rel.OID)` and `isNailedToastRelOID(rel.OID)` per row. Each call re-runs `nailedToastPairs()` and (for the namespace lookup) `infoSchemaTables()` and `informationSchemaViewSeedRels()` — the last rebuilds and scans a 65-element slice — to answer a single OID. With ~200 pg_class rows this is repeated allocation + O(rows × 65) scanning.
- **Why**: initdb-time only and small absolute cost, but the lookups are pure functions of the OID and could be answered from prebuilt maps built once (all the inputs are compile-time tables).
- **Suggestion**: Build `map[uint32]…` caches for toast-pair/namespace/view-OID membership once and reuse across all rows (and across the sibling index bootstrappers that call the same helpers).
- **Severity**: low

### `initdb.go:textArrayBytes` — []byte conversion per element
- **Issue**: `copy(buf[eoff+4:], []byte(s))` converts each string to `[]byte`; `copy` accepts a string source directly, avoiding the allocation.
- **Why**: Called per pg_proc row with non-nil ArgNames (the 11 information_schema helpers today) — small, but the fix is a one-word change.
- **Suggestion**: `copy(buf[eoff+4:], s)`.
- **Severity**: low

### `pgcontrol.go:buildPgControl` — walLevelInt(cfg) called twice
- **Issue**: `walLevelInt(cfg)` (a registry lookup + `strings.ToLower` allocation) is evaluated at both offset 60 and offset 172 for the same `cfg`.
- **Why**: Redundant identical work; the value is a pure function of the registry.
- **Suggestion**: Hoist `walLevel := walLevelInt(cfg)` and write it in both places.
- **Severity**: low

### `open.go:Open` — pg_control read twice at startup
- **Issue**: `control.ReadControlFile(abs)` is called once for `checksumsEnabled` (line ~301) and again inside `beginRecovery(abs)` (~line 354), so the 8 KB control file is read, parsed, and CRC-verified twice on every start.
- **Why**: Redundant file read + parse on the startup path; the second caller only needs the already-decoded struct.
- **Suggestion**: Read once in `Open` and pass the `*control.ControlFileData` (or the checksum flag + redo/state fields) into `beginRecovery`.
- **Severity**: low

### `btree_index_bootstrap.go:pgBuildBtreeBulkLoadVariable` — O(n²) remaining-size scan
- **Issue**: The leaf-grouping loop recomputes `remaining := Σ (len(t)+4)` from `pos` to the end on every group (the `for i := pos; …` inner loop). For pg_proc's ~3400 tuples / ~50 leaves this is ~n²/2 ≈ 100 K cheap additions, and it is O(n²) in general — unlike `pgBuildBtreeBulkLoad`/`Sized`, which derive the count arithmetically.
- **Why**: initdb-time only, so the absolute cost is small, but a running total (decrement the sum as tuples are consumed) makes it O(n) with no logic change.
- **Suggestion**: Track a single running `remaining` value and subtract the consumed group's bytes each iteration.
- **Severity**: low

### `pg_type_bootstrap.go:pgTypeBootstrapEntryMap` / `pgTypeOIDsUsedByNailedAttrs` — entry sets rebuilt across bootstrap phases
- **Issue**: `pgTypeBootstrapEntryMap()` is re-run by `bootstrapPgTypeTuples`, `bootstrapPgTypeOidIndex`, and `bootstrapPgTypeTypnameNspIndex`; each call rebuilds the map from `pgTypeAllEntries()` (all 300+ pg_type rows) plus `pgTypeOIDsUsedByNailedAttrs()` (which itself re-allocates maps/slices and re-iterates every nailed rel × attr, twice per map build) plus the RelType/domain/table-type loops. The ~3407-entry pg_proc seed list is likewise rebuilt by each of the three pg_proc bootstrappers.
- **Why**: The sets are compile-time-derived pure functions of the seed data; recomputing them per bootstrap phase is duplicate work (all three call sites run during the same `Init`).
- **Suggestion**: Compute each once (e.g. `sync.OnceValue` or package-level lazily-initialized map) and share across the heap writer and index bootstrappers; also memoize `pgTypeOIDsUsedByNailedAttrs`.
- **Severity**: low
