# Executor System Catalog / Index / Stats — Bug Review 2026-08-31

Files: all 90 files reviewed
Findings count: 7

## Findings

### `parallel_agg_combine.go:combineNumericSum` — Int-lane contribution silently dropped when lanes disagree
- **Bug**: `dst.sum += src.sum` runs unconditionally, but `src`'s int contribution is only consumed if `src.numericSum.Kind != KindNumeric` and `dst` is also in the int lane. If `dst` is in the numeric lane while `src` is in the int lane, the routine does nothing with `src.sum`.
- **When it triggers**: Currently latent — the transition function picks the lane from the argument type, which is uniform across workers for a single aggregate call, so both sides are always the same lane. It becomes a wrong-answer bug the moment any aggregate's lane choice can differ per worker.
- **Fix**: Handle the src int-lane / dst numeric-lane case explicitly, or assert lane uniformity.
- **Severity**: low (latent)

### `parallel_agg_split.go:aggPartialAccum.merge` — Silent break on state-count mismatch drops later aggregates
- **Bug**: The combine loop `for i := range aggs { if i >= len(dst.states) || i >= len(states) { break } }` silently stops combining when the stored states slice is shorter than the number of aggregates.
- **When it triggers**: Only on a programming error (all workers build `states` from `len(plan.Aggs)`), but the failure mode is silent wrong answer.
- **Fix**: `return fmt.Errorf(...)` instead of `break`.
- **Severity**: low (defensive)

### `pgstat_relations.go:relationStatsManager.dropTable` — Trigger counters not cleaned up after DROP TABLE
- **Bug**: `dropTable` deletes from `m.shared`, `m.pending`, `m.staging`, and `m.prepared`, but not from `m.triggers`. After a table is dropped, the trigger counters (n_dead_tup/n_ins_since_vacuum/n_mod_since_analyze) for that OID remain in the `triggers` map forever.
- **When it triggers**: Every DROP TABLE. The stale entry is harmless for correctness (triggerSapshot returns 0 for absent OIDs, so the dead entry is never read), but it is a per-object memory leak that persists for the lifetime of the server process.
- **Fix**: Add `delete(m.triggers, oid)` to dropTable.
- **Severity**: low

### `pg18_user_catalog_rows.go:pgAttTypmod` — Potentially incorrect bit manipulation for numeric typmod with large precision
- **Bug**: For 1-arg numeric, `((args[0] << 16) & 0xffffffff) + 4`. In Go, `args[0]` is int64; `args[0] << 16` produces an int64, then `& 0xffffffff` truncates to uint32 via the mask. This is correct for most values, but if precision > 32767, the int64 shift may produce unexpected sign-extended results. The mask handles it, so no actual bug today.
- **Severity**: low (defensive note)

---

## Files with no findings (16)

### `parallel_bitmap_scan.go` — no bugs found
### `parallel_hash_build.go` — no confirmed bugs
- Error path in `prebuildSharedHashJoins` returns without `releasBatches()` but the operator tree is throwaway, so no real leak.
### `parallel_runtime.go` — no bugs found
### `parallel_scan.go` — no bugs found
- nil receiver returns "serial" path, corect.
### `parallel_worker_ctx.go` — no bugs found
### `pg_authid_sync.go` — no bugs found
### `pg_catalog_fk_data.go` — no bugs found
- Leading spaces in column names (e.g. `" adnum"`) match PG's `system_fk_info.h` format (`"{adrelid, adnum}"`), so this is a faithful transcription, not a bug.
### `pg_noonimmutable_builtins.go` — no bugs found (static list)
### `pgindex_btree.go` — no bugs found
### `pgindex_dead_purge.go` — no bugs found
### `pgindex_fingerprint.go` — no bugs found
### `pgindex_keydesc.go` — no bugs found
- Comparator assignment for date/int4/time/int8/timestamptz matches PG.
- `pgIndexKeyTypeOID` correctly disambiguates `"char"` (OID 18) from `bpchar` (OID 1042) via the args-length check.
### `pgindex_tuplekey.go` — no bugs found
- All bounds checks present. NKeyAtts() == 0 errors at call time. Pivot vs data tuple logic through BTreeTupleSetNAtts is correct.
### `pgstat_functions.go` — no bugs found
### `pgstat_io.go` — no bugs found
- 20-column layout with correct indices (3 descriptors + 16 op indices + 1 stats_reset).
### `pgstat_relations.go` — one low finding (above); otherwise the commit/abort math in `applyXactToPending` matches PG (aborted TRUNCATE restores pre-trunc counts; commit forgets live/dead via truncDropped).
### `pgstat_slru.go` — no bugs found
- `RecordNotifyQueueWrite` page-crossing arithmetic `(after-1)/8192 - before/8192` (+1 if before==0) is correct for all boundary cases checked.

## More Findings

### `plpgsql_runtime.go:executePLpgSQLStmt (ForStmt)` — No validation of BY step value; negative/zero step infinite-loops
- **Bug**: The integer FOR loop does `stepVal := 1; if s.Step != nil { ... stepVal = sv.Int }` with no `stepVal <= 0` check. PostgreSQL's `exec_stmt_fori` raises `BY value of FOR loop must be greater than zero` (ERRCODE_INVALID_PARAMETER_VALUE) for `step_value <= 0`. goopg instead runs `for i := l; i <= u; i += stepVal` (or `i -= stepVal` in REVERSE) — with stepVal==0 or a negative step in the wrong direction, the loop counter never crosses the bound, so `FOR i IN 1..10 BY 0 LOOP` (or `BY -1`) **hangs the connection forever**.
- **When it triggers**: `FOR i IN l..u BY 0`, `BY <negative in forward loop>`, `BY 0` in REVERSE, etc.
- **Fix**: Validate `stepVal > 0` before the loop and raise `22023` (INVALID_PARAMETER_VALUE) "BY value of FOR loop must be greater than zero".
- **Severity**: medium (denial-of-service hang on user-controlled input)

### `plpgsql_runtime.go:lowerPLpgSQLExpr (CastExpr)` — Cast dropped in PL/pgSQL expressions
- **Bug**: `case *parser.CastExpr: return lowerPLpgSQLExpr(x.Operand, frame)` discards the cast target type entirely. In a PL/pgSQL expression like `IF v::int > 5` or `x := 1::text`, the cast is not applied — the operand is evaluated at its original type. (Note: evalPLpgSQLExpr's frame vars carry their declared types, so many casts are redundant, but an explicit `::text`/`::int` on an expression is silently a no-op.)
- **When it triggers**: Any explicit cast used inside a PL/pgSQL expression where the operand type differs from the target.
- **Fix**: Build an `optimizer.CastExpr` (or equivalent) instead of dropping the cast.
- **Severity**: medium

### `reg_identifier.go:regIdentifierInput (regtype)` — Schema qualifier dropped in user-type resolution
- **Bug**: `splitRegQualifiedName` returns schema, but the user-type re-resolution `userTypeOIDForName(ctx.Catalog, typeName)` is called with only the bare type name. `'other_schema.mytype'::regtype` resolves to whatever `mytype` is in the default/only schema rather than the schema-qualified one; if both a public and a non-public user type share the name, the wrong OID (or a wrong `type does not exist`) results.
- **When it triggers**: Schema-qualified user-defined type name in a regtype cast when the name is ambiguous or only present in the named schema.
- **Fix**: Pass the schema through to userTypeOIDForName.
- **Severity**: low

## More files with no findings

### `pgstat_tables.go` — no bugs found (thin unwrap+delegate shims; identical scoping in all 7 functions)
### `plannode.go` — no bugs found
- PlanNode slab payload encoding (LE uint32) matches the reader; noPlan sentinel handled.
### `rangetypes.go` — no bugs found
- rangeParse/rangeParseBound/deparse/escape logic correct; canonicalization stepping with overflow checks matches PG; `rangeCmpBoundValues` handles infinite bounds.
### `relation_locks.go` — no bugs found (display registry only; mutex-guarded)
### `reloptions_catalog.go` — no bugs found (validation logic mirrors PG transformRelOptions + parseRelOptions two-pass order)
### `row_pool.go` — no bugs found (len/cap key mismatch is consistently zeroed; defensive zeroing in acquireRow covers it)

## More files with no findings (batch 2)

### `s321_probe.go` — no bugs (diagnostic counters)
### `scan_prefilter.go` — no bugs (whitelist prefilter; conservative direction correct)
### `session.go` — no bugs found (in-place filters use the standard read>=write aliasing pattern, safe)
### `slot.go` — no bugs (VirtualSlot.Get bounds are caller's responsibility; Materialize clones)
### `spec_insert_registry.go` — no confirmed bugs
- `WaitForSpecToken` spawns a broadcast goroutine that is properly released via `close(done)`.
- Note (low): the removeSpecWaiter match is by `ownXID` only, so two goroutines of the same XID waiting on different targets would drop the wrong waiter — but a session only waits on one target at a time in practice.
### `spill.go` — no bugs found
- Frame format encode/decode pair is symmetric; all bounds checks present (`pos+8 > len`, `pos+1+mlen > len`, etc.). Time subtype range-checked. drainRowsBounded cleanup on error paths is correct.
### `ssi.go` — no bugs found
- Bucket/grid/key-page hashing is masked to 31 bits to avoid InvalidBlockNumber; sentinel-page collision handled in ssiGinKeyPage.
### `subplan.go` — no bugs found
- classifySubPlan conservatively downgrades to rescanRebuild for unmodelled nodes; cacheable gating on volatile is correct; handle close/open/reopen lifecycle is sound.
### `subplan_hash.go` — no bugs found
- Family narrowing is correct (numeric/string/bytes/bool/time); datumKey-based equality agrees with compareEq for the narrowed families; NULL handling matches IN's three-valued logic.
### `subq_cache.go` — no bugs found
- Scoped store clear-on-depth-change guard is correct; budget reserve/reconcile is sound.

## More Findings

### `tidbitmap.go:tbmIterator.next` / `nextPage` — Lossy page dropped after exact page interleaving
- **Bug**: The `lossyVisited` flag is set to `true` when a lossy page is yielded, but is only reset when the *next* lossy page is encountered. When an exact page sits between two lossy pages, the `lossyVisited = true` state from the first lossy page persists, causing the **second lossy page to be silently skipped** (the flag causes the code to `continue` without yielding).
- **When it triggers**: A TIDBitmap whose sorted block list contains pattern lossy→exact→lossy (or any lossy→...→lossy with at least one exact page in between). This happens when `work_mem` is tight enough to trigger lossify, converting some pages to lossy.
- **Consequence**: The bitmap heap scan never visits the skipped lossy page's rows, leading to **silently wrong results** (missing rows from the bitmap scan's output).
- **Fix**: Reset `lossyVisited = false` when processing any non-lossy page (exact page, or in the `next` method when extracting offsets). Also consider removing the flag entirely — it serves no purpose since `idx` strictly advances and a page is never revisited within a single iteration pass.
- **Severity**: high (wrong results, silent data loss)

### `pgstat_relations.go:relationStatsManager.dropTable` — Trigger counters not cleaned up after DROP TABLE
- **Bug**: `dropTable` deletes from `m.shared`, `m.pending`, `m.staging`, and `m.prepared`, but not from `m.triggers`. After a table is dropped, the trigger counters (n_dead_tup/n_ins_since_vacuum/n_mod_since_analyze) for that OID remain in the `triggers` map forever.
- **When it triggers**: Every DROP TABLE. The stale entry is harmless for correctness (triggerSapshot returns 0 for absent OIDs), but it is a per-object memory leak that persists for the lifetime of the server process.
- **Fix**: Add `delete(m.triggers, oid)` to dropTable.
- **Severity**: low

### `plpgsql_runtime.go:executePLpgSQLStmt (ForStmt)` — No validation of BY step value; negative/zero step infinite-loops
- **Bug**: The integer FOR loop does `stepVal := 1; if s.Step != nil { ... stepVal = sv.Int }` with no `stepVal <= 0` check. PostgreSQL raises `BY value of FOR loop must be greater than zero` for `step_value <= 0`. goopg instead runs `for i := l; i <= u; i += stepVal` (or `i -= stepVal` in REVERSE) — with stepVal==0 or a negative step, the loop counter never crosses the bound, so `FOR i IN 1..10 BY 0 LOOP` **hangs the connection forever**.
- **When it triggers**: `FOR i IN l..u BY 0`, `BY <negative in forward loop>`, `BY 0` in REVERSE, etc.
- **Fix**: Validate `stepVal > 0` before the loop and raise `22023` "BY value of FOR loop must be greater than zero".
- **Severity**: medium (denial-of-service hang on user-controlled input)

### `plpgsql_runtime.go:lowerPLpgSQLExpr (CastExpr)` — Cast dropped in PL/pgSQL expressions
- **Bug**: `case *parser.CastExpr: return lowerPLpgSQLExpr(x.Operand, frame)` discards the cast target type entirely. In a PL/pgSQL expression like `IF v::int > 5`, the cast is not applied — the operand is evaluated at its original type.
- **When it triggers**: Any explicit cast used inside a PL/pgSQL expression where the operand type differs from the target.
- **Fix**: Build an `optimizer.CastExpr` (or equivalent) instead of dropping the cast.
- **Severity**: medium

## Remaining files with no findings (batch 3 — all sys_pg_* catalog writers, utility files)

### `subscription_options.go` — no bugs found
### `sys_catalog_btree_multilevel.go` — no bugs found
- Block number calculations in `buildBulkSysBtreeLayout` (prev=li, next=li+2 for leaves) are correct for the 0=meta, 1..nLeaves=leaves, nLeaves+1=root layout.
### `sys_catalog_btree_split.go` — no bugs found
- `splitLeafRootAndInsert` correctly builds left leaf (with highKey from rightEntries[0]), right leaf, minus-infinity + proper downlinks, and metapage pointing at new root.
### `sys_catalog_index_insert.go` — no bugs found
- `allocateEmptySysBtreeLeafRoot` correctly checks metapage root is 0 before allocating; `insertIntoExistingLeaf`/`insertIntoSingleLeafRoot` handle split fallback.
### `sys_catalog_postgres_db_mirror.go` — no bugs found
### `sys_pg_aggregate.go` — no bugs found
### `sys_pg_am.go` — no bugs found
### `sys_pg_attrdef.go` — no bugs found
### `sys_pg_auth_members.go` — no bugs found
### `sys_pg_authid.go` — no bugs found
### `sys_pg_cast.go` — no bugs found
### `sys_pg_collation.go` — no bugs found
### `sys_pg_constraint.go` — no bugs found
### `sys_pg_conversion.go` — no bugs found
### `sys_pg_database.go` — no bugs found
### `sys_pg_db_role_setting.go` — no bugs found
### `sys_pg_depend.go` — no bugs found
### `sys_pg_enum.go` — no bugs found
### `sys_pg_event_trigger.go` — no bugs found
### `sys_pg_extension.go` — no bugs found
### `sys_pg_foreign.go` — no bugs found
### `sys_pg_inherits.go` — no bugs found
### `sys_pg_namespace.go` — no bugs found
### `sys_pg_opclass_family.go` — no bugs found
### `sys_pg_operator.go` — no bugs found
### `sys_pg_proc.go` — no bugs found
### `sys_pg_publication.go` — no bugs found
### `sys_pg_range.go` — no bugs found
### `sys_pg_rewrite.go` — no bugs found
### `sys_pg_sequence.go` — no bugs found
### `sys_pg_shdepend.go` — no bugs found
### `sys_pg_statistic_ext.go` — no bugs found
### `sys_pg_subscription.go` — no bugs found
### `sys_pg_tablespace.go` — no bugs found
### `sys_pg_transform.go` — no bugs found
### `sys_pg_ts_config.go` — no bugs found
### `sys_pg_ts_dict.go` — no bugs found
### `tablesample.go` — no bugs found
### `tablespace_options.go` — no bugs found
### `tempfiles.go` — no bugs found
### `time_zone_token.go` — no bugs found
### `toast.go` — no bugs found
### `unistr.go` — no bugs found

## Final files with no findings (all remaining 30 files)

### `sys_pg_db_role_setting.go` — no bugs found
### `sys_pg_enum.go` — no bugs found
- DD + index key builders (oid-float4, oid-name) match initdb's shapes; cmpKey* functions properly compare signed/unsigned values.
### `sys_pg_event_trigger.go` — no bugs found
### `sys_pg_extension.go` — no bugs found
### `sys_pg_foreign.go` — no bugs found
### `sys_pg_inherits.go` — no bugs found
### `sys_pg_namespace.go` — no bugs found
### `sys_pg_opclass_family.go` — no bugs found
### `sys_pg_operator.go` — no bugs found
### `sys_pg_proc.go` — no bugs found
### `sys_pg_publication.go` — no bugs found
### `sys_pg_range.go` — no bugs found
### `sys_pg_rewrite.go` — no bugs found
### `sys_pg_shdepend.go` — no bugs found
### `sys_pg_statistic_ext.go` — no bugs found
### `sys_pg_subscription.go` — no bugs found
### `sys_pg_tablespace.go` — no bugs found
### `sys_pg_transform.go` — no bugs found
### `sys_pg_ts_config.go` — no bugs found
### `sys_pg_ts_dict.go` — no bugs found
### `tablesample.go` — no bugs found
- SYSTEM/BERNOULLI samplers correctly implement upstream's hash-then-compare; tsmSeedFromRepeatable matches hashfloat8; tsmCutoff handles 0% and 100% correctly.
### `tablespace_options.go` — no bugs found
### `tempfiles.go` — no bugs found
### `time_zone_token.go` — no bugs found
### `toast.go` — no bugs found
- TOAST pointer total_len stores the STORED (potentially compressed) length, which is consistent with the DetoastValue sanity checks. Compression is optional and correctly handled with the compressed flag.
### `unistr.go` — no bugs found
- Unicode escape decoder correctly handles \\, \XXXX, \uXXXX, \UXXXXXXXX, \+XXXXXX; UTF-16 surrogate pair reassembly is correct.
