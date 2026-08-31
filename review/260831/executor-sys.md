# Executor System Catalog / Index / Stats — Code Review 2026-08-31

Files: parallel_agg_combine.go, parallel_agg_split.go, parallel_bitmap_scan.go, parallel_hash_build.go, parallel_runtime.go, parallel_scan.go, parallel_worker_ctx.go, pg18_user_catalog_rows.go, pg_authid_sync.go, pg_catalog_fk_data.go, pg_nonimmutable_builtins.go, pgindex_btree.go, pgindex_dead_purge.go, pgindex_fingerprint.go, pgindex_keydesc.go, pgindex_tuplekey.go, pgstat_functions.go, pgstat_io.go, pgstat_relations.go, pgstat_slru.go, pgstat_tables.go, plannode.go, plpgsql_runtime.go, rangetypes.go, reg_identifier.go, relation_locks.go, reloptions_catalog.go, row_pool.go, s321_probe.go, scan_prefilter.go, session.go, slot.go, spec_insert_registry.go, spill.go, ssi.go, subplan.go, subplan_hash.go, subq_cache.go, subscription_options.go, sys_catalog_btree_multilevel.go, sys_catalog_btree_split.go, sys_catalog_index_insert.go, sys_catalog_postgres_db_mirror.go, sys_pg_aggregate.go, sys_pg_am.go, sys_pg_attrdef.go, sys_pg_auth_members.go, sys_pg_authid.go, sys_pg_cast.go, sys_pg_collation.go, sys_pg_constraint.go, sys_pg_conversion.go, sys_pg_database.go, sys_pg_db_role_setting.go, sys_pg_depend.go, sys_pg_enum.go, sys_pg_event_trigger.go, sys_pg_extension.go, sys_pg_foreign.go, sys_pg_inherits.go, sys_pg_namespace.go, sys_pg_opclass_family.go, sys_pg_operator.go, sys_pg_proc.go, sys_pg_publication.go, sys_pg_range.go, sys_pg_rewrite.go, sys_pg_sequence.go, sys_pg_shdepend.go, sys_pg_statistic_ext.go, sys_pg_subscription.go, sys_pg_tablespace.go, sys_pg_transform.go, sys_pg_ts_config.go, sys_pg_ts_dict.go, tablesample.go, tablespace_options.go, tempfiles.go, tidbitmap.go, time_zone_token.go, toast.go, unistr.go
Findings count: 22

## Findings

### `parallel_bitmap_scan.go:sortBlockNumbers` — Hand-rolled insertion sort (O(n²))
- **Issue**: `init` sorts the block list with a hand-written insertion sort. Worst case is O(n²); the comment allows "a few thousand pages" (≈2M comparisons).
- **Why**: Go's `sort.Slice` is O(n log n), allocation-free, and less code.
- **Suggestion**: `sort.Slice(s.blocks, func(i, j int) bool { return s.blocks[i] < s.blocks[j] })` and delete `sortBlockNumbers`.
- **Severity**: low

### `parallel_agg_combine.go:combineAggRuntime` — `normalizeAggName` recomputed per (worker, group, aggregate)
- **Issue**: `combineAggRuntime` calls `normalizeAggName(name)` (`strings.ToLower(strings.TrimSpace(...))`) on every invocation. In `parallel_agg_split.go:merge` it is called per-group per-aggregate per-worker, but the name is invariant across all groups.
- **Why**: For TPC-H Q1 (4 groups, N workers), the same name is lowercased 4×N times instead of once.
- **Suggestion**: Normalize the aggregate names once (e.g. in `merge` before the loop) and pass the normalized name in.
- **Severity**: low

### `parallel_hash_build.go:parallelBuildLazyHashTable` — Append-driven slice growth with known capacity
- **Issue**: `workerCtxs`/`arenas` grow via `append` though `maxProducers` is known up front.
- **Why**: Each realloc copies the slice header array; pre-sizing avoids it.
- **Suggestion**: Use `make([]*Context, 0, maxProducers)` and `make([]*mmgr.Context, 0, maxProducers)`.
- **Severity**: low

### `pg18_user_catalog_rows.go:buildUserPGAttributeRow` — `strings.ToLower` on every column's type name
- **Issue**: The SERIAL remap `switch strings.ToLower(col.Type.Name)` runs on every column write, even for already-lowercase names.
- **Why**: `strings.ToLower` allocates a new string each call; the common case needs no allocation.
- **Suggestion**: Use `strings.EqualFold` (allocation-free) or a manual case-insensitive match.
- **Severity**: low

### `pgindex_btree.go:arbiterKey` — redundant `pgIndexKeyColumns` / `EqualFold` per key column
- **Issue**: `arbiterKey` re-resolves `pgIndexKeyColumns(idx)` on every upsert row and compares column names with `strings.EqualFold`, even though index/table column names are already stored lowercased by the parser.
- **Why**: Column names are canonical; `==` suffices and skips the fold. `pgIndexKeyColumns` itself does an O(cols) `findColumnByName` per column.
- **Suggestion**: Compare with `==`; cache resolved key columns alongside the memoised descriptor (`ctx.pgKeyDescCache`).
- **Severity**: low

### `pgindex_keydesc.go:pgIndexKeyTypeOID` — double case-normalisation of the same string
- **Issue**: Inside the `"char"` case, `strings.EqualFold(col.Type.Name, "char")` re-folds a name the switch already `strings.ToLower`-ed.
- **Why**: The switch already lowercased; a plain `==` is sufficient and avoids a second alloc.
- **Suggestion**: Track the lowercased value and compare with `==`.
- **Severity**: low

### `plpgsql_runtime.go:rewriteSQLNamedParams` — regexp compiled per argument per invocation
- **Issue**: `regexp.MustCompile(...)` runs inside the loop over `argNames`, so an N-parameter SQL function compiles N regexps on EVERY call. `executeSQLRoutine`/`executeSQLProcedure`/`evalSQLFunctionSetof` call this per invocation.
- **Why**: Regexp compilation is expensive; the per-name patterns are fixed for the routine's lifetime. Also each `ReplaceAllStringFunc` rescans the whole body.
- **Suggestion**: Precompile once (cache the patterns on the routine, or build a single alternation regexp).
- **Severity**: medium

### `plpgsql_runtime.go:executeSQLRoutine` (+procedures/setof) — body re-parsed and re-planned on every call
- **Issue**: Each invocation runs `parser.Parse(body)`, `optimizer.Plan`, `Build` for every statement.
- **Why**: PG compiles a SQL-language function's body once (SPI plan cache); goopg reparses/replans on every call. For a hot function this is significant repeated work.
- **Suggestion**: Cache the parsed/planned statement list (and the named-param rewrite) on `catalog.Routine` at CREATE time, invalidated on REPLACE.
- **Severity**: medium

### `plpgsql_runtime.go:executePLpgSQLTriggerBody` — trigger body re-parsed on every firing
- **Issue**: `plpgsql.Parse(r.Body)` runs for every trigger invocation (per row for a per-row trigger), and `executePLpgSQLRoutine`/`evalPLpgSQLFunctionSetof` also re-parse the PL/pgSQL body on every call.
- **Why**: Parsing is pure repeated work across invocations of the same routine.
- **Suggestion**: Cache the parsed `plpgsql.Block` on the routine (invalidate on REPLACE).
- **Severity**: medium

### `plpgsql_runtime.go:dispatchStoredRoutineByLanguage` — `strings.ToLower` on language/return-type per call
- **Issue**: `strings.ToLower(r.Language)` and `strings.ToLower(r.ReturnType.Name)` allocate on every routine dispatch.
- **Why**: Language and return type are fixed per routine; lowercasing is redundant work on a hot path (functions called per row).
- **Suggestion**: Normalise at CREATE time and store canonical lowercase forms on `catalog.Routine`.
- **Severity**: low

### `row_pool.go:acquireRow` — rows zeroed twice (acquire + release)
- **Issue**: `acquireRow` zeroes every slot after `releaseRow` already zeroed it on return.
- **Why**: Double writes over the same memory; one of the two is redundant given the pool's own contract.
- **Suggestion**: Drop the zeroing on one side (acquire can rely on release's invariant, or vice versa).
- **Severity**: low

### `slot.go:VirtualSlot.Materialize` — double clone of the row
- **Issue**: `Materialize` calls `s.Row()` (allocates a fresh pooled Row and copies each column) and then `cloneRowOwned` on the result — a second deep copy including a fresh backing slice.
- **Why**: `Row()` already produced an independent row; the second copy is redundant allocation + memcpy.
- **Suggestion**: Build the owned row directly from `s.cols`/sources (single pass with cloneRowOwned semantics).
- **Severity**: low

### `pgstat_io.go:fetchIOStatRows` — `ioTracksObject` evaluated once per row AND once per op
- **Issue**: `ioTracksObject(bt, obj, ctxIdx)` is called in the row loop, then `ioTracksOp` re-calls it (its first line) for each of the 8 ops — so the object-tracked decision runs 9× per (bt,obj,ctx) triple.
- **Why**: Redundant function evaluation; `ioTracksOp` could take the already-computed object result as a parameter.
- **Suggestion**: Pass `ioTracksObject`'s result into `ioTracksOp`, or hoist the object check out of the op loop.
- **Severity**: low

### `pgstat_relations.go:recordRelInsert/Update/Delete/Truncate` — two mutex acquisitions in the autocommit path
- **Issue**: Each `recordRel*` calls `relStats.recordX(...)` (locks) and then, when not in an explicit txn, `relStats.commitXact(id)` (locks again).
- **Why**: Two lock/unlock pairs per autocommit DML statement; the fold could run under the same critical section.
- **Suggestion**: Add a combined record+commitXact entry point that holds `m.mu` once.
- **Severity**: low

### `tidbitmap.go:tbmIterator.next` — map lookup + index arithmetic per emitted offset
- **Issue**: For an exact page, every emitted offset does `tbm.entries[it.blocks[it.idx-1]]` — a map lookup per TID returned.
- **Why**: The block and its `pageEntry` are constant for the whole page; they can be cached on the iterator.
- **Suggestion**: Cache the current block and `*pageEntry` (plus recheck) when entering an exact page, reuse them while draining `it.offsets`.
- **Severity**: low

### `tidbitmap.go:tbmLossify` — O(n) scan per degradation (O(n²) worst case)
- **Issue**: To find the heaviest exact page it re-scans all entries each time it degrades one; if many pages must degrade, the whole operation is O(n²).
- **Why**: `maxEntries` is usually near the final count so few iterations occur, but a tight budget can degrade many pages.
- **Suggestion**: Maintain a max-heap of exact pages by popcount, or degrade in one pass picking the worst each iteration with a running candidate list.
- **Severity**: low

### `toast.go:DetoastValue` — full TOAST relation scan per detoasted column
- **Issue**: `DetoastValue` scans every block of the TOAST relation for the matching `chunk_id`. `DetoastRow` calls it once per `KindToastPointer` column, so a row with N toasted columns does N full scans.
- **Why**: The TOAST relation is unordered w.r.t. chunk_id, so each detoast must rescan; the row-level caller could scan once and collect all requested OIDs.
- **Suggestion**: Add a multi-OID variant that scans the TOAST relation once, gathering all chunks for every pointer in the row.
- **Severity**: medium

### `tablesample.go:tsmHashAny` — allocates a byte buffer per call
- **Issue**: `tsmHashAny` does `make([]byte, 4*len(words))` per invocation; BERNOULLI calls it per scanned tuple, SYSTEM per block.
- **Why**: A fresh heap allocation on every sampled-tuple decision; the words arrays are tiny (1–3 uint32s).
- **Suggestion**: Use a stack array (`var b [12]byte`) and slice it, or keep a pre-sized reusable buffer.
- **Severity**: low

### `sys_catalog_btree_multilevel.go:buildBulkSysBtreeLayoutVariable` — recomputes the tail sum per leaf (O(n·leaves))
- **Issue**: Each outer packing iteration recomputes `remaining` by re-summing all tuples from `pos` to the end; with L leaves the total work is O(n·L).
- **Why**: `totalNeeded` is already computed once at the top; `remaining` could be maintained by subtraction as leaves are packed.
- **Suggestion**: Track a running `remaining` and subtract each leaf's bytes, or compute the leaf boundary with a prefix sum.
- **Severity**: low

### `sys_catalog_postgres_db_mirror.go:mirrorCatalogRelToPostgresDB` — pins+scans every block of every mirrored catalog on every DDL
- **Issue**: The mirror walks (pins, locks, compares) EVERY block of EVERY relfile in `mirroredCatalogOIDs()` on each DDL that touches any of them, even though only 1–2 pages changed.
- **Why**: `bytes.Equal` avoids the copy/WAL cost but the per-block pin/lock/unlock still happens for all blocks. Amplified because each write site calls the mirror for several relfiles.
- **Suggestion**: Track dirty blocks during the DDL (or record the touched block range) and mirror only those; at minimum hoist the block-walk to skip untouched files via a per-file dirty marker.
- **Severity**: low

### `pgstat_functions.go:record` — global mutex on the tracked-call hot path
- **Issue**: With `track_functions='all'`, every function invocation takes the process-global `functionStatsManager.mu`.
- **Why**: Default `'none'` gates the path off, but when enabled a per-backend pending map could be lock-free (each backend owns its session's pending bucket) with the lock only on flush/read.
- **Suggestion**: Key pending state per session and use a lock-free or sharded counter per backend; the global lock is only needed at flush.
- **Severity**: low

### `reg_identifier.go:regOutShared` — linear scan over all user collations per regcollation render
- **Issue**: The `regcollation` arm iterates `im.ListUserCollations()` linearly to match an OID before falling back to the builtin name.
- **Why**: O(#user collations) per rendered regcollation value; a name→OID map lookup would be O(1).
- **Suggestion**: Use an OID-indexed map for user collations (or the existing `ResolveIndexColumnCollationName` path without the list scan).
- **Severity**: low

### `reloptions_catalog.go:validateRelOptionNames` — options split twice and re-sorted needlessly
- **Issue**: `validateRelOptionNames` copies+`sort.Strings`, then `validateRelOptionNamesInOrder` runs two passes each calling `splitRelOptionName(full)` again on the same strings.
- **Why**: Each option name is parsed twice per validation call; the namespace/name split is a pure function of the string.
- **Suggestion**: Precompute the split once into `(ns, name, qualified)` triples and validate over the triples.
- **Severity**: low

## Files reviewed with no findings

The following files were read in full and contain no processing that clearly meets the review criteria (mostly static data tables, small fixed-size builders, already-reused buffers, or single-shot DDL paths where the work is inherent):

- parallel_runtime.go, parallel_scan.go, parallel_worker_ctx.go
- pg_authid_sync.go, pg_catalog_fk_data.go (static data), pg_nonimmutable_builtins.go (map built once at init)
- pgindex_dead_purge.go, pgindex_fingerprint.go, pgindex_tuplekey.go
- pgstat_slru.go, pgstat_tables.go
- plannode.go, rangetypes.go, scan_prefilter.go, session.go, spill.go, ssi.go
- subplan.go, subplan_hash.go, subq_cache.go, subscription_options.go
- sys_catalog_btree_split.go, sys_catalog_index_insert.go
- sys_pg_aggregate.go, sys_pg_am.go, sys_pg_attrdef.go, sys_pg_auth_members.go, sys_pg_authid.go, sys_pg_cast.go, sys_pg_collation.go, sys_pg_constraint.go, sys_pg_conversion.go, sys_pg_database.go, sys_pg_db_role_setting.go, sys_pg_depend.go, sys_pg_enum.go, sys_pg_event_trigger.go, sys_pg_extension.go, sys_pg_foreign.go, sys_pg_inherits.go, sys_pg_namespace.go, sys_pg_opclass_family.go, sys_pg_operator.go, sys_pg_proc.go, sys_pg_publication.go, sys_pg_range.go, sys_pg_rewrite.go, sys_pg_sequence.go, sys_pg_shdepend.go, sys_pg_statistic_ext.go, sys_pg_subscription.go, sys_pg_tablespace.go, sys_pg_transform.go, sys_pg_ts_config.go, sys_pg_ts_dict.go
- tablespace_options.go, tempfiles.go, time_zone_token.go, unistr.go

Note: `s321_probe.go` is a temporary diagnostic file (env-gated); its `s321Dump` string concatenation loop is only active under `GOOPG_S321_PROBE=1` and is intended to be removed, so it was not counted as a finding.
