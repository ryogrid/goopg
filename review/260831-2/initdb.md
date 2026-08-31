# InitDB — Bug Review 2026-08-31

**Files reviewed**: All 56 files in internal/initdb/ (every file read; logic files read in full, seed/data files read and structurally checked)
**Findings count**: 6

---

### `pgcontrol.go:BackupControlImage` — uint64 underflow on redoLSN == 0
- **Bug**: `lsn0 := redoLSN - 1` underflows to `math.MaxUint64` if `redoLSN == 0`.
- **When it triggers**: If any caller passes redoLSN=0. Currently all callers pass valid >=1 LSNs.
- **Fix**: Guard the subtraction: `if redoLSN == 0 { return nil, error }`.
- **Severity**: low

---

### `catalog_cache.go:readCatalogCache` — Silent partial catalog on TryRegisterUserTable failure
- **Bug**: When `TryRegisterUserTable` fails, the function logs a warning but returns `true, nil` — the caller skips the heap scan, leaving the catalog with missing tables. The cache is reported as "successfully loaded" even when tables failed to register.
- **When it triggers**: On OID collision, schema mismatch, or similar registration error for a cached table. The caller (open.go line 1475) then skips the fallback heap scan.
- **Fix**: Return `false, nil` on any TryRegisterUserTable failure.
- **Severity**: medium

---

### `xact_recovery.go:replayCLogFromWAL` — Latent native-record collision hazard
- **Bug**: The native-record switch on `r.Payload[0]` (for RecordKindXactCommit=8, XactAbort=9, ClogTruncate=33) does not call `xlog.IsGoopgNativeRecord(r)` first. PG-format records whose MainData's first byte coincidentally equals a native RecordKind would be misclassified. The hazard is currently latent because PG-format records typically have `r.Payload == nil` (nativeHeaderMatchesMainData returns false for them). Same pattern in `walHasXactRecords`.
- **When it triggers**: A PG-format record whose first MainData byte collides with a native RecordKind constant. Currently mitigated by the `r.Payload == nil` property of PG-format records, but the guard is documented as mandatory for all scanners of this shape.
- **Fix**: Add `if !xlog.IsGoopgNativeRecord(r) { continue }` before the native switch.
- **Severity**: low (latent)

---

### `initdb.go:mappedLocalCatalogPlaceholderOIDs` — Duplicate OID 3764
- **Bug**: OID 3764 appears twice in the placeholder list:
  - Line 1613: `3764, // pg_ts_template (M0106-0010 step 3co)`
  - Line 1614: `3764, // pg_ts_template (stale — true pg_ts_template OID is 3764)`
  Both are identical OIDs; writing the same empty placeholder twice is harmless (idempotent) but indicates stale commented mappings alongside live ones. (Note: the same OID legitimately appears twice in the local relmap for the stale/alive split, but in a flat placeholder list a duplicate is purely dead weight.)
- **When it triggers**: Always — the duplicate file write is a no-op (same bytes, same destination).
- **Fix**: Remove the stale duplicate entry.
- **Severity**: low (cosmetic — no runtime impact)

---

### `open.go:Open` (RunningXactsFn) — Latent uint32 underflow if Xmax == 0
- **Bug**: The `RunningXactsFn` closure at line 1865 returns `uint32(snap.Xmax - 1)`. If `snap.Xmax == 0`, this underflows to `math.MaxUint32`.
- **When it triggers**: If `txnMgr.FreshSnapshot()` returns `Xmax == 0`, which shouldn't happen after initdb bootstraps transactions (Xmax starts at 3). Latent defensive gap.
- **Fix**: Guard with `if snap.Xmax > 0 { ... uint32(snap.Xmax-1) } else { return xids, 0, 0 }`.
- **Severity**: low (latent)

---

### `information_schema_tables.go:infoSchemaTableRows` — CRLF / non-\N NULL handling
- **Bug**: The TSV parser treats only `\N` as NULL and splits on `\t` with no tolerance for a carriage return. A capture with a trailing `\r` (Windows-edited TSV) would make the last field of each line include `\r`, silently corrupting the `comments` column values. Additionally a literal backslash-N inside a value is not escaped by the capture (standard COPY out) so this is consistent with the capture format — the CRLF concern is the only divergence risk. This is speculative; the embedded TSVs are git-tracked and known to be LF/`\N`-clean.
- **When it triggers**: Only if a TSV is re-captured with CRLF line endings.
- **Fix**: Trim a trailing `\r` per line.
- **Severity**: low (data-hygiene, not currently triggering)

---

### Checked and confirmed NOT bugs (verified against PG 18.3 oracle data)

- `pg_conversion_bootstrap.go:pgConversionInitialEntries` — conproc 4358/4359 reused across all UTF8↔WIN conversions is **intentional**: PG's own `pg_conversion.dat` names `utf8_to_win`/`win_to_utf8` for every Windows encoding. Verified against `pg_conversion.dat` and `pg_proc.dat`.
- `pg_range_bootstrap.go:pgRangeInitialEntries` — opclass OIDs (1978, 3125, 3128, 3127, 3122, 3124) all match `pg_opclass.dat`.
- `pgcontrol.go:buildPgControl` — all 88 CheckPoint struct bytes and subsequent ControlFileData fields match PG18's `pg_control.h` layout at the stated offsets.
- `encoding.go` — `pgEncNames` slice index == encoding ID; bounds checks correct; client-only encodings (35..41) correctly rejected by `pgValidServerEncoding`.
- `pg_proc_proname_args_nsp_index_bootstrap.go:pgBuildBtreeBulkLoadVariable` — leaf packing, high-key reservation, and downlink numbering verified; the multi-leaf btree layout is internally consistent.
- `pg_rewrite_toast_writer.go` — `buildVarattExternalOnDisk`/`externalizeVarlenaPayload` chunking and the va_rawsize/va_extinfo encoding match `toast_save_datum` semantics.
- `config_seed.go:replaceGUCValue` — prefix matching, `=` search, and inline-comment preservation are correct; no off-by-one in the `j+len(name)` bounds check.
- `auth_bootstrap.go:resolveAuthMethods` — ident↔peer cross-map correctly fires only when host==local (matches upstream parse-time behavior).
- `pglz.go` — compression-pays threshold and fallback are correct.
- `checksum_bootstrap.go` — block-index math (`i * BlockSize`) and PageSetChecksumCopy application correct; WAL files correctly excluded.
- `recovery_state.go` — online-checkpoint detection and DB_IN_CRASH_RECOVERY stamping match upstream semantics.
- `timeline.go`, `standby.go`, `wal_bootstrap.go`, `pglz.go`, `locale.go`, `catalog_seed.go` — no logic errors found.

---

### Files with no bugs found

All 56 reviewed files had no bug findings beyond those listed above. Files with confirmed-clean reviews:

- aio_views.go
- auth_bootstrap.go
- btree_index_bootstrap.go
- catalog_cache.go
- catalog_heap_reload.go
- catalog_seed.go
- checksum_bootstrap.go
- config_seed.go
- encoding.go
- information_schema_proc_seed.go
- information_schema_proc_sqlbody.go
- information_schema_sequences_view.go
- information_schema_tables.go
- information_schema_view_oid_pins.go
- information_schema_view_seed_data.go
- initdb.go
- locale.go
- nailed_view_ev_action.go
- nailed_view_seed_data.go
- open.go
- pg_aggregate_bootstrap.go
- pg_aggregate_view.go
- pg_amproc_entries.go
- pg_cast_bootstrap.go
- pg_collation_bootstrap.go
- pg_constraint_bootstrap.go
- pg_conversion_bootstrap.go
- pg_language_bootstrap.go
- pg_operator_bootstrap.go
- pg_opfamily_bootstrap.go
- pg_proc_proname_args_nsp_index_bootstrap.go
- pg_proc_seed_data.go
- pg_proc_seed_defaults.go
- pg_proc_view.go
- pg_range_bootstrap.go
- pg_rewrite_bootstrap.go
- pg_rewrite_toast_bootstrap.go
- pg_rewrite_toast_writer.go
- pg_sequences_view.go
- pg_stat_activity_view.go
- pg_stat_ssl_gssapi_view.go
- pg_tablespace_bootstrap.go
- pg_type_bootstrap.go
- pg_type_seed_data.go
- pgcontrol.go
- pglz.go
- recovery_state.go
- relcache_init.go
- replication_views.go
- standby.go
- syncfs_linux.go
- syncfs_other.go
- system_view_oid_pins.go
- timeline.go
- wal_bootstrap.go
- wal_io_views.go
- xact_recovery.go

---

## Summary

| # | File:Function | Severity | Description |
|---|---------------|----------|-------------|
| 1 | `pgcontrol.go:BackupControlImage` | low | uint64 underflow on redoLSN == 0 |
| 2 | `catalog_cache.go:readCatalogCache` | medium | Silent partial catalog on register failure |
| 3 | `xact_recovery.go:replayCLogFromWAL` | low | Latent native-record collision hazard |
| 4 | `initdb.go:mappedLocalCatalogPlaceholderOIDs` | low | Duplicate OID 3764 in placeholder list |
| 5 | `open.go:Open` (RunningXactsFn) | low | Latent uint32 underflow if Xmax == 0 |
| 6 | `information_schema_tables.go:infoSchemaTableRows` | low | CRLF line-ending hygiene (data capture) |
