# Performance-review fix tasks — review/260831

The documents under `review/260831/<subsystem>.md` (15 subsystems, 346 findings)
are turned into tasks here. A finding is fixed only when it passes all **three
criteria**:

1. **Benefit** — is there a real, measurable improvement (a hot path, a
   before/after benchmark that moves)? "Wasteful in theory but executed O(1)
   times / once at startup" counts as no benefit.
2. **No regression** — output, plans, on-disk format and error codes stay the
   same. Planner/executor changes are checked with
   `RALPH_PRECOMMIT_SCOPE=units` + `scripts/tpch-spotcheck.sh` + the TPC-DS
   SF0.5 sweep.
3. **Maintainability** — readability is not badly degraded (unsafe code, hand
   unrolling, or a change that adds invariants leans towards reject).

The `verdict` column records the decision: **ADOPT** (fix it) / **REJECT** (do
not, with the reason) / empty (not judged yet).

## How this proceeds

- Priority set = severity high / medium / low-medium (the 83 rows below),
  worked in descending order of expected benefit.
- The 236 severity-low findings are REJECT by default (they fail criterion 1,
  benefit); one that turns out to sit on a hot path and is trivially fixable is
  promoted into the table below. The list and its default verdict live in
  "Appendix A".
- The 27 "no issues found" sections are not findings and are out of scope.
- Every fix gets its own gate run, commit and push.

## Priority set

| ID | subsystem | sev | finding | verdict | benefit | no regression | maintainability | status |
|---|---|---|---|---|---|---|---|---|
| EC-3 | executor-core | medium | `applyworker.go:decodePgoutputTupleAsRow` — column-name map rebuilt on every row | | | | | [ ] |
| EC-7 | executor-core | medium | `hash_partition.go:computeHashPartitionRowHash` — per-row invariant work on the INSERT partition-routing hot path | | | | | [ ] |
| EC-14 | executor-core | medium | `copy.go:insertSourceRow` — per-row `make(Row, …)` instead of the row pool | | | | | [ ] |
| EC-16 | executor-core | medium | `copy_binary.go:decodeNumericBinary` — dead `fullMantissa` computation before unconditional fallback | | | | | [ ] |
| EC-20 | executor-core | medium | `expr.go:evalCast` — `strings.ToLower(targetType)` on every cast evaluation | | | | | [ ] |
| EO1-1 | executor-operators-1 | medium | `operators.go:sortOp.lessRows` — Sort key expressions re-evaluated on every comparison | REJECT (already done) | — | — | — | [x] no change |
| EO1-13 | executor-operators-1 | medium | `operators_gather_merge.go:lessRows` — Sort key expressions re-evaluated on every heap comparison (promoted from Appendix A) | ADOPT | 4 sources x 2000 rows: 2.63ms -> 1.94ms (1.36x), allocs 42,652 -> 23,988 | comparison rule unchanged verbatim; keys evaluated once, when the row is pulled | same shape as EO2-2 / sortOp | [x] fixed |
| EO1-4 | executor-operators-1 | medium | `operators_analyze.go:analyzeRelationWith` — Decodes every visible tuple but only keeps a fraction | | | | | [ ] |
| EO1-7 | executor-operators-1 | medium | `operators_bitmap.go:bitmapIndexScanOp.buildBitmap` — Allocates a slice per index entry in hot loop | | | | | [ ] |
| EO1-10 | executor-operators-1 | medium | `operators_generate_series.go:generateSeriesOp.Next` — Allocates a Row per emitted value | | | | | [ ] |
| EO1-11 | executor-operators-1 | medium | `operators_fk.go:fkRowMatches` — Per-row linear column-name lookup on every row of a table scan | | | | | [ ] |
| EO2-1 | executor-operators-2 | high | `operators_join_agg.go:applyAgg` — string_agg O(n²) string concatenation | ADOPT | 4000 rows in one group: 34.99ms -> 1.89ms (18.5x), 229MB -> 1.5MB allocated | concatenation order, delimiter placement and the bytea path are identical; the ORDER BY path already used a Builder | one field swapped: `strResult` string -> `strAccum []byte` | [x] fixed |
| EO2-2 | executor-operators-2 | medium | `operators_window.go:Open` — sort comparator re-evaluates expressions per comparison | ADOPT | 4000 rows / 8 partitions: 18.44ms -> 7.66ms (2.4x), allocs 256,905 -> 56,166 | comparison rule unchanged verbatim; only the evaluation moved to once per row | same "precompute keys, sort a permutation" shape as sortOp.sortChunk | [x] fixed |
| EO2-6 | executor-operators-2 | medium | `operators_index.go:Next` / `operators_storage.go:Next` — per-row enum value linear scan | | | | | [ ] |
| EO2-7 | executor-operators-2 | medium | `operators_indexonly.go:decodeRowFromKey` / `operators_indexonly.go:decodeRowFromHeap` — per-row map allocation + O(covered × columns) projection | | | | | [ ] |
| EO2-8 | executor-operators-2 | medium | `operators_generated.go:evalGeneratedExpr` — re-parses expression string per row | | | | | [ ] |
| EO2-9 | executor-operators-2 | medium | `operators_storage.go:checkUniqueIndexesForInsert` / `maintainUniqueIndexesForInsert` — per-row per-index btree open | | | | | [ ] |
| EO2-11 | executor-operators-2 | medium | `operators_join_agg.go:aggregateOp.Open` — per-row allocations for group key values | | | | | [ ] |
| EO2-12 | executor-operators-2 | medium | `operators_merge.go:mergedRow` — per-candidate-pair allocation | | | | | [ ] |
| EO2-14 | executor-operators-2 | medium | `operators_project_set.go:openSelectSrfMode` — per-step output row allocation | | | | | [ ] |
| EO2-24 | executor-operators-2 | medium | `slot.go:asSlot` (callers: nextMerge, recursiveUnionOp, workTableScanOp) — MaterializedSlot allocated per emitted row | | | | | [ ] |
| EO2-25 | executor-operators-2 | medium | `operators_upsert.go:maintainNonArbiterIndexesCapture` — btree re-opened per index per row | | | | | [ ] |
| ES-7 | executor-sys | medium | `plpgsql_runtime.go:rewriteSQLNamedParams` — regexp compiled per argument per invocation | ADOPT | 3-argument rewrite: 29.6us -> 9.5us (3.1x), 16.0KB -> 1.0KB, 152 -> 25 allocs | the compiled pattern is identical; the cache key is the argument name | one `sync.Map`; the key set is bounded by the schema, not by traffic | [x] fixed |
| ES-8 | executor-sys | medium | `plpgsql_runtime.go:executeSQLRoutine` (+procedures/setof) — body re-parsed and re-planned on every call | | | | | [ ] |
| ES-9 | executor-sys | medium | `plpgsql_runtime.go:executePLpgSQLTriggerBody` — trigger body re-parsed on every firing | | | | | [ ] |
| ES-17 | executor-sys | medium | `toast.go:DetoastValue` — full TOAST relation scan per detoasted column | | | | | [ ] |
| OP1-1 | optimizer-1 | medium | `cardinality.go:estimateNumGroups` — per-relation full-tree walk plus subtree re-estimation | | | | | [ ] |
| OP1-2 | optimizer-1 | medium | `cardinality.go:semiPairMatchFraction` — O(n·m) MCV matching nested loop | | | | | [ ] |
| OP1-3 | optimizer-1 | medium | `cardinality.go:EstimateRows` — no memoization on recursive estimates | | | | | [ ] |
| OP2-3 | optimizer-2 | medium | foldconst.go:FoldConstants — Always allocates fresh nodes/slices even when nothing folds | | | | | [ ] |
| OP2-11 | optimizer-2 | medium | joinsearchseam.go:tryPGShapedJoinSearch — searchConsumes rebuilds restrict infos per conjunct | | | | | [ ] |
| OP2-12 | optimizer-2 | medium | joinselectivity.go:examineJoinVar / columnStatsByName — Linear column lookup per operand | | | | | [ ] |
| OP2-14 | optimizer-2 | medium | pathbitmap.go:chooseBitmapAnd — costBitmapTree recomputed inside sort comparator | | | | | [ ] |
| OP2-18 | optimizer-2 | medium | scan_input_rewrite.go:absorbConjunctsIntoSubtree — Re-walks the whole subtree per matching conjunct | | | | | [ ] |
| OP2-20 | optimizer-2 | medium | pushdown.go:pushOneConjunct — Per-conjunct whole-tree walks at every recursion level | | | | | [ ] |
| OP2-29 | optimizer-2 | medium | selectivity.go:clauseSelectivity / clauseSelectivityWithSource — Near-identical duplicated implementations | | | | | [ ] |
| OP2-31 | optimizer-2 | medium | planner.go:tryRangeIndexScan — Whole WHERE predicate resolved twice | | | | | [ ] |
| XL-8 | xlog | medium | iterator.go:readOneAt — record header bytes read twice | | | | | [ ] |
| XL-9 | xlog | medium | iterator.go:readBytesAt / readRecordBytesAt / readSegmentSlice — per-record allocation + per-chunk file open | | | | | [ ] |
| XL-14 | xlog | medium | format.go:encodeRecordXLog / wrapXLogMainData — two allocations per WAL record | | | | | [ ] |
| XL-21 | xlog | medium | pg_assembled_emit.go — envelope + body + mainData allocation chain per PG record | | | | | [ ] |
| XL-24 | xlog | medium | pg_xlog_decode.go:parseXLogRecordData — cloneXLogBytes per block/main-data chunk | | | | | [ ] |
| XL-25 | xlog | medium | pgoutput.go:pgoPhysEpoch — reconstructs the PG epoch per column decode | | | | | [ ] |
| XL-31 | xlog | medium | reader.go:readStreamFrom — stream slice grows without a capacity hint | | | | | [ ] |
| XL-38 | xlog | medium | reorder.go:foldChanges — allocates a copy even when nothing folds | | | | | [ ] |
| XL-39 | xlog | medium | recovery.go:Decode* helpers — defensive tuple copies per record | | | | | [ ] |
| XL-50 | xlog | medium | slots.go:writeSlotLocked — full rewrite + double fsync per slot update | | | | | [ ] |
| XL-68 | xlog | medium | xlog_assemble.go:assembleXLogRecord — header/payload built via repeated append with no capacity hint | | | | | [ ] |
| IN-4 | initdb | medium | `pg_aggregate_view.go:registerPgAggregateView` — pgAggregateInitialEntries() rebuilt on every query | | | | | [ ] |
| IN-8 | initdb | medium | `initdb.go:writeMultiPageHeap` / `writeMultiPageHeapRowsExternal` — hasVarWidthCol recomputed per row (loop invariant) | | | | | [ ] |
| ST-1 | storage | high | `heap.go:CollectDeadHeapSlots` — copies every tuple to inspect only its header | ADOPT | 8282 -> 2622 ns/page (3.2x), 157 -> 0 allocs | header-only read, under the page content lock, tuple never outlives the iteration | reuses the existing `parseHeapTupleAlias` | [x] fixed |
| ST-2 | storage | high | `vm.go:PageAllVisible` / `PageAllFrozen` — same full-tuple copy, only the header is read | ADOPT | 8007 -> 2188 ns/page (3.7x), 157 -> 0 allocs | header-only read, under the page content lock, tuple never outlives the iteration | reuses the existing `parseHeapTupleAlias` | [x] fixed |
| ST-3 | storage | high | `prune.go:pagePruneCore` — `ParseHeapTuple` copy per tuple on the prune/VACUUM path | ADOPT | 8220 -> 3233 ns/page (2.5x), 157 -> 0 allocs | header-only read, under the page content lock, tuple never outlives the iteration | reuses the existing `parseHeapTupleAlias` | [x] fixed |
| ST-4 | storage | medium | `prune.go:pruneChainTip` — per-chain-member `ParseHeapTuple` copy | ADOPT | same as ST-3 (same page loop) | header-only read, under the page content lock, tuple never outlives the iteration | reuses the existing `parseHeapTupleAlias` | [x] fixed |
| ST-5 | storage | medium | `fsm.go:RecordFreeSpace` — grow-one-element-at-a-time to reach a high block number | ADOPT | recording a high block number now costs one allocation (was: one append per block) | what is recorded is identical; skipped blocks stay 0 (= full) | three lines | [x] fixed |
| ST-6 | storage | medium | `fsm.go:GetPageWithFreeSpace` — full linear scan of the block array per lookup | ADOPT | 100k-page relation: GetCandidates 23.6us -> 192ns (123x), GetPageWithFreeSpace 24.4us -> 163ns (150x), one insert round trip 23.7us -> 304ns (78x) | a differential test pins equivalence with the full-scan version, tie-breaking included | one-level summary per 512 blocks; relations of one chunk or less keep the plain scan | [x] fixed |
| ST-8 | storage | medium | `fsm_fork.go:buildFSMTree` — re-parses every built page just to recover the max category | | | | | [ ] |
| ST-10 | storage | medium | `vm_fork.go:parseVMPage` — allocates a full `vmMaxHeapPagesPerPage` slice per page during `ReadVMFork` | | | | | [ ] |
| ST-14 | storage | medium | `lmgr/lockmgr.go:grantedExcept` — O(number of holders) recomputation when the cached mask already has the answer | | | | | [ ] |
| ST-16 | storage | medium | `aio/read_stream.go:refill` — allocates a fresh BlockSize buffer per prefetched block | | | | | [ ] |
| ST-17 | storage | medium | `bufpool.go:Prefetch` — allocates a fresh 8KB buffer per prefetch | | | | | [ ] |
| NB-4 | nbtree-amcheck | low/medium | `pgpage.go:pgFirstDataSlot` — opaque re-decoded on every accessor call inside loops | | | | | [ ] |
| NB-8 | nbtree-amcheck | medium | `btree.go:resetPageItems` — byte-by-byte zeroing of the whole data area on every page rewrite | | | | | [ ] |
| NB-9 | nbtree-amcheck | medium | `btree.go:findChildBlockDirect` / `btree.go:insertItemSorted` — per-probe key copies inside descent/insert binary search | | | | | [ ] |
| NB-10 | nbtree-amcheck | medium | `btree.go:Search` — decodes the whole leaf into a slice before binary-searching it | REJECT (maintainability) | plausible | — | pageItems returns an EXPANDED item list (one entry per heap TID), so items do not map 1:1 to line pointers; a lazy binary search would have to rework that invariant and its VACUUM-side readers | [x] no change |
| NB-11 | nbtree-amcheck | low/medium | `btree.go:EncodeNumericKey` — fresh big.Int allocation per trailing-zero strip iteration | | | | | [ ] |
| NB-15 | nbtree-amcheck | low/medium | `amcheck/heapallindexed.go:fingerprintLeafEntry` — a fresh buffer allocation per element, twice per element | | | | | [ ] |
| NB-17 | nbtree-amcheck | high | `pglz.go:Compress` — brute-force O(n·window·matchlen) match search | ADOPT | 64KiB inputs: random 377ms -> 1.0ms (379x), nodetree 8.1ms -> 0.26ms (32x), text 30.0ms -> 1.5ms (20x) | the output is now byte-identical to upstream pglz_compress (golden test); the ratio worsens slightly (below) | same hash chain + good_match/good_drop as upstream pg_lzcompress.c, with PG defaults | [x] fixed |
| NB-18 | nbtree-amcheck | low/medium | `pglz.go:Decompress` — byte-by-byte run-length copy even for non-overlapping matches | ADOPT | text 78.9us -> 43.6us (1.8x), nodetree 44.6us -> 26.1us (1.7x) | overlapping matches (off < length) keep the byte loop; the round-trip test pins it | one added branch | [x] fixed |
| NB-19 | nbtree-amcheck | low/medium | `backup/basebackup.go:baseBackupStreamer.Write` / `streamBackupManifest` — fresh frame buffer allocated per chunk | | | | | [ ] |
| NB-20 | nbtree-amcheck | low/medium | `backup/basebackup.go:emitBaseBackupTar` / `emitTablespaceTar` — whole-file buffering | | | | | [ ] |
| TA-1 | transam | medium | `manager.go:captureSnapshot` — Full copy of `abortedXIDs` on every snapshot capture | ADOPT | with 10,000 aborts: 15.0us -> 1.13us (13x), 41KB -> 89B per snapshot | honours the existing contract that Aborted is immutable after capture; the insert side became copy-on-write | contained in one insert helper | [x] fixed |
| TA-2 | transam | medium | `manager.go:SnapshotFor` — Deep-`Clone()` of the pinned snapshot on every RR/SSI statement | | | | | [ ] |
| TA-8 | transam | medium | `multixact/store.go:Members` — Allocates + copies the member slice on every call, and takes the global mutex | | | | | [ ] |
| PN-1 | parser-nodes | medium | `internal/parser/adapter.go:mapToken` — Per-token `resolve()` map lookup for every terminal | | | | | [ ] |
| PN-2 | parser-nodes | medium | `internal/parser/adapter.go:next()` — Double `strings.ToLower` per token | | | | | [ ] |
| CP-3 | catalog-postmaster | medium | `catalog.go:InheritanceChildren` / `PartitionChildren` — O(children × tables) lookup by scanning the whole namespace map per child | | | | | [ ] |
| CP-4 | catalog-postmaster | medium | `catalog.go:RoleIsMemberOf` / `IsAdminOfRole` / `HasPrivsOfRole` / `SelectBestAdmin` — BFS re-scans the entire `roleMembers` map per queue level (O(V×E)) | | | | | [ ] |
| CP-5 | catalog-postmaster | medium | `catalog.go:LookupTableByOIDAllDBs` / `tableByOID` — linear scan of all tables per OID lookup | | | | | [ ] |
| UT-1 | utils | medium | `internal/utils/misc/encoding_guc.go:encodingNameToCanonical` — re-cleans the constant encoding table on every call | REJECT (no benefit) | the only caller is the client_encoding GUC check; it is not on any per-row or per-tuple path | — | — | [x] no change |
| UT-9 | utils | medium | `internal/utils/adt/datetime/pg_datetime_format.go` — `fmt.Sprintf` on the per-cell output path | | | | | [ ] |
| UT-14 | utils | medium | `internal/utils/mb/conv.go:DoEncodingConversion` — dead `destBuf` allocation | | | | | [ ] |
| UT-20 | utils | medium | `internal/utils/mmgr/mctx.go:Lookup` — global mutex on the per-datum read path | | | | | [ ] |
| UT-23 | utils | medium | `internal/utils/adt/array/pgarray.go:DecodeElemStyled` — `strings.ToLower(elemName)` recomputed per element | REJECT (no benefit) | `strings.ToLower` only scans (no allocation) when the input is already lower-case, and element names are catalog-normalised | — | — | [x] no change |

## Appendix A — severity-low findings (REJECT by default: criterion 1, benefit)

Executed rarely, saving nanoseconds per call, or on generated/bootstrap paths —
no individually measurable benefit. Only findings that turn out to be on a hot
path are promoted into the priority set.


**executor-core.md**

- `EC-1` `advisory.go:PgLockRows` — fmt.Sprintf for uint32 in row builder
- `EC-2` `advisory.go:acquire` — recursion re-registers waiter and re-probes activity per wake
- `EC-4` `applyworker.go:applyContext` — fresh Context + XID materialization per row
- `EC-5` `applyworker.go:primaryKeyOnlyRow` / `replicaIdentityKeyRow` — per-row nested column lookups
- `EC-6` `applyworker.go:parsePgoutputText` — `string(data)` allocated before type dispatch
- `EC-8` `context.go:waitForRelationLockers` — `time.After` allocates a fresh timer per poll iteration
- `EC-9` `explain_names.go:nodePtr` — `fmt.Sprintf("%p", n)` as map key
- `EC-10` `deferred_exclusion.go:runAllDeferredExclusionChecks` / `deferred_unique.go:runAllDeferredUniqueChecks` — per-check catalog re-resolution
- `EC-11` `copy_csv.go:parseCopyCsvFields` — per-field `string(field)` conversion and byte-at-a-time field building
- `EC-12` `codec.go:pgFloatFromDatum` — `StringValue()` computed then discarded for KindNumeric
- `EC-13` `codec.go:parseIntegerInput` — `strings.ReplaceAll` allocation per integer coercion
- `EC-15` `copy.go:PushBinaryData` / `listedColumns` — per-call rebuild of `listedCols` / `listedColumns()`
- `EC-17` `copy_binary.go:decodeNumericBinaryViaBig` — big.Int reconstruction built then discarded
- `EC-18` `copy_binary.go:datumToCopyBinary` — per-field allocation then re-copy into dst
- `EC-19` `copy_text.go:copyTextToDatum` — `string(raw)` conversion per field per row
- `EC-21` `expr.go:compareDatum` — per-comparison UUID / pg_lsn detection on the string-compare hot path
- `EC-22` `expr.go:evalBinary` — ILIKE lowercases both operands per row
- `EC-23` `expr.go:looksLikePgLSN` — string concatenation for validation loop

**executor-operators-1.md**

- `EO1-2` `operators.go:sortOp.lessRows` — `isRegSortFamilyTypeName` allocates per call via `strings.ToLower`
- `EO1-3` `operators.go:valuesOp.Next` — Allocates a fresh Row per emitted row
- `EO1-5` `operators_analyze.go:computeColumnStats` — MCV split loop O(m²) recomputation
- `EO1-6` `operators_analyze.go:computeColumnStats` — Expanded slice capacity too small, causes repeated reallocation
- `EO1-8` `operators_call.go:callArgTypeCompatible` — Allocates two maps on every CALL
- `EO1-9` `operators_distinct.go:distinctOnOp.Next` — String concatenation in a loop per row
- `EO1-12` `operators_fk.go:fkSetNull` — Nested linear column lookup per matching row
- `EO1-14` `operators_ddl.go:execTruncate` — FK-cascade check rebuilds per-iteration data and re-lowers names

**executor-operators-2.md**

- `EO2-3` `operators_window.go:Open` — redundant double-copy of child rows
- `EO2-4` `operators_join_agg.go:applyAgg` — `inputIsInt` closure redefined per-row
- `EO2-5` `operators_setop.go:drainSetOpInput` — rowKey computed twice for EXCEPT/INTERSECT rows
- `EO2-10` `operators_storage.go:writeHeapRowReturning` — `tryAppendToBlock` function defined inside the outer function body
- `EO2-13` `operators_ordinality.go:Next` — per-row Fresh Row allocation
- `EO2-15` `operators_index.go:currentTID` / `operators_lockrows.go:drainAndStamp` — RelFileNode recomputed per row
- `EO2-16` `operators_lockrows.go:parseRowCTID` — fmt.Sscanf per row
- `EO2-17` `operators_generated.go:datumToString` / `evalGenBinary` — fmt.Sprintf + big.Int Exp per op
- `EO2-18` `operators_join_agg.go:finalizeGroup` — rebuilds slotCount map per group
- `EO2-19` `operators_generated.go:applyDefaultsForMissing` — strings.EqualFold column resolution loop
- `EO2-20` `operators_sequence.go:seqKey` — fmt.Sprintf per lookup
- `EO2-21` `operators_storage.go:seqScanOp.Next` — fmt.Sprintf for resjunk ctid per row
- `EO2-22` `operators_memoize.go:Next` — per-row datumKey purely for byte accounting
- `EO2-23` `operators_recursive_cte.go:rowKey` — full string rendering per row for UNION dedup

**executor-sys.md**

- `ES-1` `parallel_bitmap_scan.go:sortBlockNumbers` — Hand-rolled insertion sort (O(n²))
- `ES-2` `parallel_agg_combine.go:combineAggRuntime` — `normalizeAggName` recomputed per (worker, group, aggregate)
- `ES-3` `parallel_hash_build.go:parallelBuildLazyHashTable` — Append-driven slice growth with known capacity
- `ES-4` `pg18_user_catalog_rows.go:buildUserPGAttributeRow` — `strings.ToLower` on every column's type name
- `ES-5` `pgindex_btree.go:arbiterKey` — redundant `pgIndexKeyColumns` / `EqualFold` per key column
- `ES-6` `pgindex_keydesc.go:pgIndexKeyTypeOID` — double case-normalisation of the same string
- `ES-10` `plpgsql_runtime.go:dispatchStoredRoutineByLanguage` — `strings.ToLower` on language/return-type per call
- `ES-11` `row_pool.go:acquireRow` — rows zeroed twice (acquire + release)
- `ES-12` `slot.go:VirtualSlot.Materialize` — double clone of the row
- `ES-13` `pgstat_io.go:fetchIOStatRows` — `ioTracksObject` evaluated once per row AND once per op
- `ES-14` `pgstat_relations.go:recordRelInsert/Update/Delete/Truncate` — two mutex acquisitions in the autocommit path
- `ES-15` `tidbitmap.go:tbmIterator.next` — map lookup + index arithmetic per emitted offset
- `ES-16` `tidbitmap.go:tbmLossify` — O(n) scan per degradation (O(n²) worst case)
- `ES-18` `tablesample.go:tsmHashAny` — allocates a byte buffer per call
- `ES-19` `sys_catalog_btree_multilevel.go:buildBulkSysBtreeLayoutVariable` — recomputes the tail sum per leaf (O(n·leaves))
- `ES-20` `sys_catalog_postgres_db_mirror.go:mirrorCatalogRelToPostgresDB` — pins+scans every block of every mirrored catalog on every DDL
- `ES-21` `pgstat_functions.go:record` — global mutex on the tracked-call hot path
- `ES-22` `reg_identifier.go:regOutShared` — linear scan over all user collations per regcollation render
- `ES-23` `reloptions_catalog.go:validateRelOptionNames` — options split twice and re-sorted needlessly

**optimizer-1.md**

- `OP1-4` `cardinality.go:indexScanRows` — linear column lookup inside the key loop
- `OP1-5` `costbitmap.go:computeBitmapPagesLooped` — results computed then discarded
- `OP1-6` `costindex.go:indexTupleWidth` — allocation + nested scan per call
- `OP1-7` `cost_funcs.go:hashJoinCost` — recomputes per-row geometry
- `OP1-8` `createplanroot.go:missingBindingCoords` — sorting an already-sorted slice
- `OP1-9` `createplanindex.go:createIndexScanPlan` (index-only arm) — linear schema search per covered column
- `OP1-10` `createplanjoin.go:baseRelLayout` — linear name scan for narrowed leaves
- `OP1-11` `exists_to_any.go:existsToAny` — predicate split twice
- `OP1-12` `exprwalk.go:exprChildSlots` — fresh slice allocation per node per walk
- `OP1-13` `exprwalk.go:exprSelfKey` — fmt.Sprintf for plain scalars
- `OP1-14` `enclosingtree.go:enclosingNodeScopeOf` / `walkEnclosingTree` — allocations on the assertion walk

**optimizer-2.md**

- `OP2-1` groupagg_presorted.go:applyPresortedAggregateRule — Repeated grouppathkeys copy per candidate
- `OP2-2` groupagg_presorted.go:aggregateSortlist — O(n²) duplicate detection
- `OP2-4` groupagg_indexorder.go:applyIndexOrderedGroupingRule — Per-candidate map allocations inside index loop
- `OP2-5` groupagg_indexorder.go:buildIndexOrderedScan — tableColumnByName linear scan per referenced column
- `OP2-6` groupby_alias_key.go / groupingsets.go — Reflective key walks rebuild strings repeatedly
- `OP2-7` join_exec_keys.go:ExecHashKeyPlan / ExecMergeKeyPlan — Two-pass loop over pairs with repeated predicate scan
- `OP2-8` join_hashkey.go:hashKeyIsInt64 — Redundant ToLower
- `OP2-9` joinrestrict.go:relidsOfExpr — Linear scan over cumOffsets per column ref
- `OP2-10` joinsearchlevel.go:joinIsLegal / joinOrderRestricted — Iterate whole joinInfoList per pair with nested subset checks
- `OP2-13` joinrelsize.go:bestProvableKey — Rebuilds equated map on every outer iteration
- `OP2-15` path.go:comparePaths — Allocates a slice per dominance comparison
- `OP2-16` parallel.go:findPartialSubtree — Repeated drivingScan re-walks of the same subtree
- `OP2-17` pathbitmap.go:matchBitmapIndexQuals / buildOneParameterizedBitmapPath — columnStatsByName linear lookup per matched column
- `OP2-19` reduce_outer_joins.go:applyDemotion — Per-join map allocations and repeated ON-clause walks
- `OP2-21` qual_canonical.go:processDuplicateOrs — Recomputes strictParserExprKey repeatedly
- `OP2-23` selectivity.go:rangeOpSelectivity — histCmp recomputed for each MCV entry
- `OP2-24` selectivity.go:histogramOpSelectivity — histCmp recomputed in loop and recursion
- `OP2-25` unnest.go:unnestSubqueriesInPlan — countSublinksInExpr re-walks predicate every loop iteration
- `OP2-26` unnest.go:planCloneSupported — Walked twice (node walk + walkPlanExprs)
- `OP2-27` unnest.go:collectUnnestParamsAndResiduals — Repeated walkExprTree per conjunct and three full-plan walks
- `OP2-28` selectivity.go:eqSelectivityForColumn — MCV list scanned twice per call
- `OP2-32` planner.go:tryPromoteOrderedIndexOnlyScan — idxColSet map and table-column linear scan per index
- `OP2-33` planner.go:parserExprKey — fmt.Sprintf used for simple integer formatting
- `OP2-34` planner.go:exprEqual — Full expression tree walk to build keys for every comparison

**xlog.md**

- `XL-1` append_xlog_payload.go:appendXLogPayload — no significant issues
- `XL-2` archive_recovery.go:RunArchiveRecovery — repeated string allocation per segment
- `XL-3` archive_restore.go:RestoreArchivedFile — two-pass replace, low impact
- `XL-4` checkpointer.go:runCheckpoint / volumeTriggerFires — no significant issues
- `XL-5` classifier.go:xminFromTuple — could use encoding/binary
- `XL-6` insert_pos_publish.go:reserveAndPublish — no significant issues
- `XL-7` mem_ring_concurrent.go:WriteReserved / PublishUpTo / AdvanceWindow — no significant issues
- `XL-10` iterator.go:Next — page-header loop recomputes header size per iteration
- `XL-11` insertion_tracker.go — no significant issues
- `XL-12` mem_ring.go:Append / ReadAt — no significant issues
- `XL-13` decoder.go — no significant issues
- `XL-15` format.go:unwrapXLogMainData — defensive copy that could alias
- `XL-16` format.go:FormatSegmentNameTLI — fmt.Sprintf for a fixed-width hex name
- `XL-17` format_detect.go — no significant issues
- `XL-18` index_am_refusal.go:preflightIndexAMRecords — no significant issues
- `XL-19` insert_pos.go:reserve / reserveLocked — no significant issues
- `XL-20` padded_mutex.go — no significant issues
- `XL-22` pg_assembled_emit.go:heapHeaderPlusData / EncodeHeapInsertPG — duplicate slice construction
- `XL-23` pg_xact_parse.go — no significant issues
- `XL-26` pgoutput.go:appendCString — `[]byte(s)` conversion before append
- `XL-27` pgoutput.go:writeRelation — 'd' replica-identity + flag byte per column
- `XL-28` pgoutput_decoder.go — minor defensive copies
- `XL-29` predict_emitted_size.go / predict_xlog_record_len.go — no significant issues
- `XL-30` publish_visibility.go — no significant issues
- `XL-32` reader.go:readAllPageAware — header extracted and decoded twice per record
- `XL-33` reader.go:openSegmentFile — FormatSegmentName computed twice on the slow path
- `XL-34` reader.go:isPreallocatedTail — 64 KiB stack array per call
- `XL-35` reader_early_end.go — no significant issues
- `XL-36` recovery_cache.go — no significant issues
- `XL-37` relmap.go — no significant issues
- `XL-40` recovery.go:redoHeapPageForBlock — page buffer allocated per call
- `XL-41` repllog.go — no significant issues
- `XL-42` replmon.go — no significant issues
- `XL-43` reserve_emitted.go — no significant issues
- `XL-44` retention.go — no significant issues
- `XL-45` rmgr_map.go — no significant issues
- `XL-46` segment_pad.go:buildSegmentPadRecord — two allocations per pad
- `XL-47` segment_pad_emit.go — no significant issues
- `XL-48` seq_log.go — no significant issues
- `XL-49` slot_decoder.go — no significant issues
- `XL-51` slots.go:readSlotFile — double string/[]byte conversion on JSON fallback
- `XL-52` slots_pg.go — no significant issues
- `XL-53` snapshot.go — no significant issues
- `XL-54` stream_replayer.go — no significant issues
- `XL-55` stripe_append.go / stripe_append_emitted.go / stripe_writer_core.go — no significant issues
- `XL-56` subscriber_mon.go — no significant issues
- `XL-57` sync_linux.go / sync_other.go — no significant issues
- `XL-58` syncrep.go:rule.satisfied (ANY mode) — allocates + sorts on every release check
- `XL-59` syncrep_parse.go — no significant issues
- `XL-60` tail_publisher.go — no significant issues
- `XL-61` timeline_history.go — no significant issues
- `XL-62` wal_buffer.go — no significant issues
- `XL-63` wal_buffer_publish_tail.go — no significant issues
- `XL-64` wal_write_lock.go — no significant issues
- `XL-65` writer.go:Append / tryAppend — predictXLogRecordLen + AppendXLogPayload predict again
- `XL-66` writer.go:walSyncStage — allocates and sorts a dirty-segment slice per flush barrier
- `XL-67` writer.go:detectWritePos / loadState — repeated directory scans at startup
- `XL-69` xlog_emit.go — no significant issues
- `XL-70` xlog_page.go / xlog_record.go — no significant issues

**initdb.md**

- `IN-1` `aio_views.go:registerStatAIOView` / `registerPgStatAIOTargetsView` / `registerPgAiosView` — fmt.Sprintf per cell in virtual view rows
- `IN-2` `catalog_cache.go:catalogCachePath` — fmt.Sprint for the dbOid
- `IN-3` `checksum_bootstrap.go:stampClusterChecksums` / `checksumRelationData` — whole-file read + full copy + write
- `IN-5` `pg_proc_view.go:registerPgProcView` — repeated typeNameToOIDStr/ToLower work per row
- `IN-6` `pg_stat_activity_view.go:numericPIDOrNull` — range loop over runes
- `IN-7` `replication_views.go:formatLSN` / `formatStringList` — fmt.Sprintf + string concatenation in per-row view rendering
- `IN-9` `initdb.go:pgClassRow` — per-row linear scans through nail lists
- `IN-10` `initdb.go:textArrayBytes` — []byte conversion per element
- `IN-11` `pgcontrol.go:buildPgControl` — walLevelInt(cfg) called twice
- `IN-12` `open.go:Open` — pg_control read twice at startup
- `IN-13` `btree_index_bootstrap.go:pgBuildBtreeBulkLoadVariable` — O(n²) remaining-size scan
- `IN-14` `pg_type_bootstrap.go:pgTypeBootstrapEntryMap` / `pgTypeOIDsUsedByNailedAttrs` — entry sets rebuilt across bootstrap phases

**storage.md**

- `ST-7` `fsm.go:GetCandidates` — re-allocates the kept-candidates slice on every call
- `ST-9` `fsm_fork.go:parseFSMPage` — always allocates a `fsmSlotsPerPage` slice even when only `maxCat` is needed
- `ST-11` `vm_fork.go:WriteVMFork` — dead `numPages == 0` branch
- `ST-12` `bufmap.go:compact` — unpacks a BufferTag and re-hashes instead of hashing the packed key
- `ST-13` `bufmap.go:Lookup` — `in.mask` re-loaded as a field access on every probe iteration
- `ST-15` `lmgr/deadlock.go:findLockCycle` — linear scan of `visited` slice per recursion step
- `ST-18` `bufpool.go:maybeEmitFPI` / `MarkDirtyForceFPI` / `MarkDirtyChangeRecord` / `MarkDirtyLogicalChange` — independent 8KB `make+copy` per FPI
- `ST-19` `page.go:InitPage` — manual zeroing loop instead of `clear`
- `ST-20` `aio/method_iouring_linux.go:completeOne` — identical `if/else` branches
- `ST-21` `checksum.go:pageChecksumBlock` — `if off == 8` branch evaluated in the innermost loop
- `ST-22` `smgr.go:relPath`/`relDir`/`sharedOrPerDBRelDir` and `fsm_fork.go:RelForkPath`, `file/pgtemp.go:FilePattern` — `fmt.Sprintf`/`fmt.Sprint` for integer formatting
- `ST-23` `smgr.go:extendBatch` — per-block `IsNew` re-check on identical copies

**nbtree-amcheck.md**

- `NB-1` `pgnewroot.go:PGRestorePageData` — per-item allocation just to append alignment padding
- `NB-2` `pgnewroot.go:PGParseRestorePageData` — double slice + double copy to reverse the item order
- `NB-3` `pgformat.go:WritePGMetaPage` — redundant zeroing of alignment hole and tail padding
- `NB-12` `btree.go:pinNewOrRecycled` — manual zeroing loop for recycled pages

**transam.md**

- `TA-3` `manager.go:WaitForXID / WaitForSlotsToCommit` — A goroutine + channel spawned per wait
- `TA-4` `manager.go:xidActiveWithSubxact / xidInProgress` — Full proc-array scan (up to 1024 atomic loads) per check, repeated in wait loops
- `TA-5` `clog.go:DidCommit` — Allocates a `visited` map on every call, even when never needed
- `TA-6` `clog.go:firstRetrainedSLRUXID` — Re-implements `parseSLRUSegName` inline
- `TA-7` `clog_bufferpool.go:evictVictimLocked` — O(nslots) full LRU scan per page fault
- `TA-9` `multixact/store.go:CreateFromMembers` — Sorts/copies before the dedup lookup
- `TA-10` `ssi_conflict.go:coveringPredicateLockTags` — Heap slice allocation per conflict-in check
- `TA-11` `subxact_slru.go:SetParent / GetParent` — Open/stat/truncate/write/fsync (or open/read) per XID under a global mutex
- `TA-12` `subxact_visibility.go:RestoreFromSLRU / Truncate` — `nParents.Store` inside the loop
- `TA-13` `subxact_visibility.go:isCurrentTxXID` — Multiple RLock acquisitions per tuple on the subxact hot path
- `TA-14` `clog_bufferpool.go:readPageFromDisk` — Manual zero-fill loop (minor)

**parser-nodes.md**

- `PN-3` `internal/parser/adapter.go:setValueAtoms()` — Redundant `strings.Join` allocations
- `PN-4` `internal/parser/base_yylex.go:substFor()` — Linear scan on every token
- `PN-5` `internal/parser/lexer.go:next()` — Inline closure allocation in numeric literal path
- `PN-6` `internal/parser/lexer.go:tryQuoteContinuation()` — Redundant string continuation scan
- `PN-7` `internal/parser/interval.go:parseIntervalTimeToken` — `strings.Split` allocation
- `PN-8` `internal/parser/interval.go:parseDateFields` — `strings.Split` allocation
- `PN-9` `internal/parser/interval.go:expandIntervalField` — Repeated `strings.ContainsRune` and `strings.IndexByte`
- `PN-10` `internal/nodes/datum.go:stripEraSuffix` — `strings.ToUpper` allocation in date/time parsing
- `PN-11` `internal/nodes/datum.go:parseTZOffsetSeconds` — Redundant `ContainsRune` before `Split`
- `PN-12` `internal/nodes/datum.go:parseTimeFields` — `strings.Split` allocation
- `PN-13` `internal/nodes/datum.go:numericVar.text()` — Separate `strings.Builder` for fractional part
- `PN-14` `internal/nodes/outfuncs.go:outDatum` — Per-byte `strconv.Itoa` for datum bytes
- `PN-15` `internal/nodes/datum.go:formatTimestamptzUTC` / `formatTimestamp` / `formatTime` — `fmt.Sprintf` for integer formatting
- `PN-16` `internal/parser/select.go:tryParseParenJoin` / `parseRangeVar` — `fmt.Sprintf` for synthetic alias
- `PN-17` `internal/parser/select.go:parseBetweenTail` — Inline closures for each BETWEEN
- `PN-18` `internal/parser/analyzer/analyzer.go:orderBySubstitution` — Double loop over targets

**catalog-postmaster.md**

- `CP-1` `encoding.go:EncodingNameToID` — Re-computes `cleanConvEncodingName` on canonical names inside a loop on every call
- `CP-2` `catalog.go:PGStatsRowsForDBOid` / `PGClassRowsForDBOid` — `c.ns(dbOid)` re-resolved on every loop iteration
- `CP-6` `catalog.go:PGClassRowsForDBOid` and other virtual-row builders — `fmt.Sprintf("%d", …)` for every numeric cell
- `CP-7` `routines.go:LookupByOID` — linear OID scan of all routines per call
- `CP-8` `pubsub.go:pubMapKey`/`subMapKey` — `fmt.Sprintf` + re-lowercasing on every map access
- `CP-9` `query.go:handleQuery` — redundant string normalizations per simple-query message
- `CP-10` `statement_log.go:leadingKeyword` — re-slices/trims the SQL on each classification
- `CP-12` `autovacuum/launcher.go:tick` / `needsVacuum` / `needsAnalyze` — `params()` GUC-parse recomputed per table, `NextXID` re-read per table
- `CP-15` `messages.go:WriteStartupMessage` — redundant double-buffer allocation per startup
- `CP-17` `auth.go:Method.String` / `ConnType.String` — linear map scan per error-message call

**utils.md**

- `UT-2` `internal/utils/misc/guc.go:canonicalizeFrom` — redundant identical branches in the `nativeStr` closure
- `UT-3` `internal/utils/misc/guc.go:parseBoolish` — allocates two strings per call
- `UT-4` `internal/utils/misc/session.go:Set/SetStartup/SetInternal` — repeated lowercasing of the same name
- `UT-5` `internal/utils/misc/timestamptz_out.go:TimestampToTimestampTZ` — redundant re-copy of the UTC wall clock
- `UT-10` `internal/utils/adt/datetime/interval_format.go:FormatInterval` — Sprintf-based time-of-day assembly
- `UT-11` `internal/utils/adt/datetime/timeofday.go:ParseTimeOfDay` — lowercase allocation just to test for "allballs"
- `UT-12` `internal/utils/adt/datetime/timeofday.go:CanonicalizeTimeToken` — zero-pad via `strconv.Itoa(1_000_000_000 + nsec)[1:]`
- `UT-15` `internal/utils/mb/euc_kr.go:euc_kr_to_utf8` — per-character `dec.Bytes` allocations
- `UT-17` `internal/utils/activity/activity.go:goroutineID` — `runtime.Stack` + string conversion per call
- `UT-21` `internal/utils/mmgr/mctx.go:growChunk` — O(n) chunk-tail copy on every chunk growth

**cmd.md**

- `CM-1` `cmd/validate-ralph-state/main.go:loadStatus` — JSON decoded twice for the same bytes
- `CM-2` `cmd/validate-ralph-state/main.go:loadProgress` — JSON decoded twice for the same bytes
- `CM-3` `cmd/validate-ralph-state/main.go:autoRepair` — `parseTimestamp` called twice on the same values
- `CM-4` `cmd/tpch-runner/main.go:drainRows` — per-row slice allocation in the EXPLAIN path
- `CM-5` `cmd/diag/main.go:main` — per-row slice allocation
- `CM-6` `cmd/tpch-runner/main.go:selectQueries` — dead sorted-copy computation
- `CM-7` `cmd/gen-pg-operator-data/main.go` — pg_operator.dat parsed twice (two full passes)

## Appendix B — "no issues found" sections (out of scope)

- `EO1-15` (executor-operators-1) `operators_explain_format.go` / `operators_explain.go`
- `EO2-26` (executor-operators-2) Files with no significant findings
- `OP2-22` (optimizer-2) relfromjoinlist.go / relsize.go / reduce_outer_joins.go / searchedtree.go / predicate_implication.go / pathkeys.go / pathkeysindex.go / pathparam.go / pathparamindex.go — No material efficiency issues found
- `OP2-30` (optimizer-2) plan.go / view_dml.go / view_privilege.go / walk_export.go / with.go / specialjoin.go / small_dimension.go / subplan_cost.go / subplan_lower.go / subplan_lower_walk.go / tuplefraction.go — No material efficiency issues found
- `NB-5` (nbtree-amcheck) `lpdead_kill.go:KillItems` — no issue
- `NB-6` (nbtree-amcheck) `dead_purge.go:purgeDeadHeapPointers` — no significant issue
- `NB-7` (nbtree-amcheck) `parse_err_dump.go` / `latch_release.go` / `pgsplit.go` / `pgpagedel.go` / `pgnewroot.go`(rest) — no issues
- `NB-13` (nbtree-amcheck) `btree.go` — no further issues
- `NB-14` (nbtree-amcheck) `bulkload.go` — no significant issue
- `NB-16` (nbtree-amcheck) `amcheck/verify_heapam.go`, `verify_heapam_relation.go`, `verify_nbtree.go`, `verify_nbtree_unique.go`, `heapallindexed_relation.go`, `heapallindexed_heapscan.go` — no significant issues
- `NB-21` (nbtree-amcheck) `backup/basebackup.go` — no further issues
- `NB-22` (nbtree-amcheck) `commands/vacuum/vacuum.go` — no significant issues
- `CP-11` (catalog-postmaster) `cancel.go` / `eof_watch.go` / `plancache.go` / `notify.go` / `twophase.go` / `txn_verb.go` — no significant waste found
- `CP-13` (catalog-postmaster) `dispatch.go` / `dispatch_extended.go` / `extended.go` / `server.go` / `copy.go` / `role_ddl.go` / `database_ddl.go` / `grant_ddl.go` — no significant waste found on hot paths
- `CP-14` (catalog-postmaster) `replication_util.go` / `applylauncher.go` / `tablesync.go` / `tablesync_manager.go` / `walreceiver.go` / `logicalreceiver.go` / `logicalwalsender.go` / `walsender.go` — no significant waste found
- `CP-16` (catalog-postmaster) `frame.go` / `protocol.go` / `replication.go` / `messages.go` (rest) — no significant waste
- `CP-18` (catalog-postmaster) `exchange.go` / `parser.go` / `userstore.go` / `scram.go` / `saslprep.go` — no significant waste
- `CP-19` (catalog-postmaster) `gls.go` / `gls_fallback.go` / `gls_linkname.go` / `runtimeshim/*` — no significant waste
- `UT-6` (utils) `internal/utils/misc/datestyle.go` — no issues found
- `UT-7` (utils) `internal/utils/misc/defaults.go`, `sample.go`, `version.go` — no issues found
- `UT-8` (utils) `internal/utils/misc/parser.go` — no significant issues
- `UT-13` (utils) `internal/utils/adt/datetime/adjust_typmod.go`, `interval_typmod.go`, `era.go`, `monthname.go`, `normalize.go` — no issues found
- `UT-16` (utils) `internal/utils/mb/latin1.go`, `latin2.go`, `wchar.go`, `euc_jp.go` — no significant issues
- `UT-18` (utils) `internal/utils/activity/registry.go`, `stats/counter.go` — no issues found
- `UT-19` (utils) `internal/utils/errcodes/codes.go` — no issues found
- `UT-22` (utils) `internal/utils/adt/similarto/similarto.go` — no issues found
- `UT-24` (utils) `internal/utils/adt/array/pgarray.go` — otherwise no issues found

## Progress log

### ST-1 / ST-2 / ST-3 / ST-4 — ADOPT (`ParseHeapTuple` -> `parseHeapTupleAlias`)

Four page loops on the VACUUM / prune / visibility-map paths read nothing but
`HeapTuple.Header`, yet called the copying `ParseHeapTuple`, throwing away two
allocations (Data and Bitmap) per tuple. Every caller holds the page pinned
under an exclusive content lock and the returned tuple never outlives the
iteration (the contract on `VacuumHeapPageBySlots`, and the `slot.Lock()` /
`slot.Unlock()` span in `vacuum.go`), so the existing no-copy decode
`parseHeapTupleAlias` — introduced for the read hot path in M0092-0006 — is a
drop-in replacement.

Measured (200 tuples on an 8KB page,
`internal/storage/headeronly_decode_bench_test.go`):

| bench | before | after |
|---|---|---|
| `CollectDeadHeapSlots` | 8282 ns/op, 3768 B, 157 allocs | 2622 ns/op, 0 B, 0 allocs |
| `PageAllVisible` | 8007 ns/op, 3769 B, 157 allocs | 2188 ns/op, 0 B, 0 allocs |
| `pagePruneCore` | 8220 ns/op, 3768 B, 157 allocs | 3233 ns/op, 0 B, 0 allocs |

- **Benefit**: yes. VACUUM was discarding a full copy of every tuple on every
  page it scanned. 2.5x to 3.7x.
- **No regression**: yes. The values read are identical, and the benchmarks pin
  zero allocations permanently.
- **Maintainability**: yes. One function swapped plus a comment saying why; no
  new invariant.

### EO2-1 — ADOPT (string_agg's O(n^2) concatenation)

Without ORDER BY, `string_agg` accumulated with `st.strResult += delim + piece`,
reallocating and recopying the whole accumulator per input row — O(n^2) bytes
for one group. The ORDER BY path already deferred concatenation through
`strElems` + `strings.Builder`, so only the unordered path had been left behind.
It now appends into a byte slice and is stringified once in `finishAgg`. bytea
mode simply accumulates raw bytes, so it needs no separate treatment
(`byteaOutMode` still renders at the end). `strResult` stays as the BIT(n)
width tag for bit_and/bit_or/bit_xor.

Measured (`internal/executor/string_agg_accum_bench_test.go`, 4000 rows of 26
bytes in one group):

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| before | 34,986,204 | 229,731,898 | 16,349 |
| after | 1,890,655 | 1,535,984 | 12,272 |

- **Benefit**: yes. 18.5x, and 150x fewer bytes allocated; the term that was
  quadratic in the row count is gone.
- **No regression**: yes. Concatenation order, delimiter placement and the
  bytea GUC path are unchanged; the existing string_agg tests stay green.
- **Maintainability**: yes. One field replaced, and the unordered path now
  follows the same "assemble once at the end" rule as the ordered one.

### NB-17 — ADOPT (PGLZ Compress: hash chain)

`Compress` scanned the whole history window from every position (up to 4096
candidates times the match length, per position). On incompressible input
(random) the window was rescanned in full every time: 377 ms for 64 KiB.
Upstream `postgres/src/common/pg_lzcompress.c` uses a 4-byte hash
(`pglz_hist_idx`) with history chains (`PGLZ_HISTORY_SIZE` 4096) and the
`good_match` / `good_drop` early exit (defaults 128 / 10%), which is what was
ported here. The hash function, the constants and the cut-off rule are
upstream's; no goopg-specific tuning was added.

Measured (`internal/access/common/pglz/compress_bench_test.go`, 64 KiB inputs):

| blob | before ns/op | after ns/op | speedup |
|---|---|---|---|
| random | 377,273,400 | 995,478 | 379x |
| nodetree | 8,146,757 | 257,828 | 32x |
| text | 29,989,230 | 1,528,340 | 20x |

The compression ratio worsens slightly because of the early exit: nodetree
0.0381 -> 0.0439, text 0.1588 -> 0.1688. That is the same trade upstream PG
makes by default — and the output is now BYTE-IDENTICAL to what real
`pglz_compress()` produces, which the old brute-force search was not.
`TestCompressMatchesUpstreamPGLZ` pins that against golden files generated by
linking PG 18.3's `pg_lzcompress.c` into a frontend harness, and
`TestCompressRatioAndRoundTrip` pins the ratio bounds and the round trip.
Incompressible input is rejected by the callers in both variants, so nothing
changes there.

One consequence, recorded because it looks like a regression and is not: the
seeded pg_rewrite TOAST heap in a fresh data directory grows from 127 to 131
chunks (32 -> 33 pages), because the same values compress slightly less well.
`TestPgRewriteToastPairIndexRowAndFiles` was re-pinned accordingly.

- **Benefit**: yes. 20x to 380x; the TOAST write path pays that CPU directly.
- **No regression**: yes, and stronger than before — the stream is now what PG
  itself emits.
- **Maintainability**: yes. The code now corresponds 1:1 with upstream, so it
  is easier to check against it, not harder.

### NB-18 — ADOPT (PGLZ Decompress: one copy for non-overlapping matches)

`Decompress` expanded every match one byte at a time. Only an overlapping match
(`off < length`) needs the run-length expansion; anything else is a single
`append`. `dst` is pre-sized to `rawSize`, so neither branch reallocates.

| blob | before ns/op | after ns/op | speedup |
|---|---|---|---|
| text | 78,934 | 43,559 | 1.8x |
| nodetree | 44,567 | 26,109 | 1.7x |
| random | 100,496 | 98,790 | 1.0x (almost no matches) |

- **Benefit**: yes. 1.7x to 1.8x on TOAST reads.
- **No regression**: yes. The overlapping path is the old code, and the
  round-trip test pins the result.
- **Maintainability**: yes. One branch.

### EO1-1 — REJECT (already done)

`sortOp` has precomputed its key values into `o.keyvals` since M0134-0191, and
the actual sort runs through `lessKeyVals` (comparing precomputed Datums).
`lessRows` survives only as a defensive fallback for the case where
`len(o.keyvals) != len(rows)`, which the normal path never hits. The symptom
the finding describes does not exist in the current code.

### EO2-2 — ADOPT (precompute the window input sort keys)

`windowOp.Open`'s comparator called `evalExpr` on both sides for every PARTITION
BY / ORDER BY expression on every comparison — O(n log n) evaluations per
expression instead of one per row. It now follows `sortOp.sortChunk`: evaluate
once per row, sort a permutation over the precomputed keys. The comparison rule
(the `compareSortDatums` call order, `decided`, the `Desc` flip) is unchanged
verbatim.

Measured (`internal/executor/window_sortkey_bench_test.go`, 4000 rows over 8
partitions, `row_number() OVER (PARTITION BY g ORDER BY t)`):

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| before | 18,435,533 | 8,989,178 | 256,905 |
| after | 7,660,551 | 4,784,936 | 56,166 |

- **Benefit**: yes. 2.4x and 4.6x fewer allocations, widening with row count.
- **No regression**: yes. Only the evaluation site moved; the window tests stay
  green.
- **Maintainability**: yes. It now matches the shape sortOp already uses.

### EO1-13 — ADOPT (precompute the Gather Merge heap keys)

`gatherMergeOp.lessRows` evaluated the sort keys on both sides of every heap
comparison, so each output row cost O(keys x log sources) evaluations. `advance`
now evaluates them once when it pulls the row, into `gmSource.curKeys`, and the
heap compares Datums. NULL placement, `Desc` and the `compareDatum` ordering
rule are unchanged verbatim — this comparator agreeing with `sortOp`'s is
exactly what makes Gather Merge correct, so the rule itself was not touched.

Measured (`internal/executor/gather_merge_keys_bench_test.go`, 4 sources x 2000
rows, keys being two column references — the cheapest possible expressions):

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| before | 2,632,359 | 1,023,654 | 42,652 |
| after | 1,935,229 | 1,151,443 | 23,988 |

- **Benefit**: yes, 1.36x here, and more for keys heavier than a column
  reference (casts, function calls).
- **No regression**: yes. B/op rises slightly because each front row now holds
  its key array; the number of evaluations and of allocations both fall.
- **Maintainability**: yes. Same approach as EO2-2 / sortOp.

### NB-10 — REJECT (maintainability)

`BTree.Search` does decode the whole leaf through `pageItems` before binary
searching it. But `pageItems` returns an EXPANDED item list (one entry per heap
TID), so item indexes do not map 1:1 onto line pointers. Making the binary
search decode lazily would mean reworking that expansion rule and the
`pageItemsWithDead` / VACUUM readers that assume they are looking at the same
list — a change deep into nbtree's invariants. The benefit is plausible, but
criterion 3 (do not badly degrade maintainability) is not met, so this is left
alone for now.

### ES-7 — ADOPT (cache the PL/pgSQL named-parameter regexps)

`rewriteSQLNamedParams` called `regexp.MustCompile` once per argument, so
calling a PL/pgSQL function compiled as many regexps as it has arguments on
every invocation — a fixed cost unrelated to what the body does. The pattern is
now cached in a `sync.Map` keyed by argument name; the pattern itself is
unchanged verbatim. The key set is the set of argument identifiers in the
database's routines, so it is bounded by the schema, not by traffic.

Measured (`internal/executor/plpgsql_namedparam_bench_test.go`, 3 arguments):

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| before | 29,582 | 15,960 | 152 |
| after | 9,498 | 1,028 | 25 |

- **Benefit**: yes. 3.1x; a fixed cost proportional to the call count is gone.
- **No regression**: yes. The compiled pattern is identical and the plpgsql
  tests stay green.
- **Maintainability**: yes. One helper function.

### ST-5 / ST-6 — ADOPT (FSM lookups scaled with the relation)

`GetCandidates` / `GetPageWithFreeSpace` scanned every registered page of the
relation. The INSERT path calls this to choose a target page, so **the cost of
one insert grew with the size of the table** — 23.6 us per lookup on a
100k-page (800 MB) relation. On an append-heavy relation every page but the
tail is full, so almost the whole scan was reading zeroes.

The FSM now keeps a one-level summary of the maximum free space per 512 blocks
(`chunkMax`) and skips a chunk wholesale when its maximum is below what the
caller asked for. It is the same idea as PG's FSM tree, flattened to one level
because the reader only ever asks "could there be anything here at all". The
summary is maintained exactly by the writer (`RecordFreeSpace`) under the write
lock; readers only consult it, still under the shared lock, and fall back to the
plain scan when there is no summary. Relations of one chunk or less keep no
summary and behave exactly as before, because for them the scan is already
cheaper than the bookkeeping. ST-5 is a second issue in the same function:
recording a high block number appended one block at a time, and now grows in one
allocation.

Measured (`internal/storage/fsm_lookup_bench_test.go`, only the last 4 pages
have room):

| bench | before | after |
|---|---|---|
| `GetCandidates`, 64 pages | 49.9 ns | 63.5 ns |
| `GetCandidates`, 100k pages | 23,590 ns | 192 ns |
| `GetPageWithFreeSpace`, 64 pages | 22.4 ns | 27.3 ns |
| `GetPageWithFreeSpace`, 100k pages | 24,400 ns | 163 ns |
| insert round trip (get + record), 64 pages | 77-87 ns | 77 ns |
| insert round trip, 100k pages | 23,650 ns | 304 ns |

- **Benefit**: yes. 78x to 150x on large relations. Small relations pay 5-14 ns
  more per read (one extra map lookup for `chunkMax`); the full insert round
  trip is unchanged.
- **No regression**: yes. `TestFSMChunkMaxMatchesFullScan` pins equivalence with
  a reference implementation copied from the old code over 2000 rounds of
  random updates, including the "ties go to the lower block number" rule.
- **Maintainability**: yes. The summary has exactly one owner (the writer), and
  a reader with no summary falls back to the old full scan — so a bug there
  costs speed, never correctness.
