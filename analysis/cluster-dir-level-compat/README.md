# goopg Cluster Directory: PG 18.3 Startup Compatibility Gap Analysis

**Date:** 2026-07-26
**Target:** PostgreSQL 18.3 (`PG_CONTROL_VERSION=1800`, `CATALOG_VERSION_NO=202506291`)
**goopg branch:** `tpcds-error-fix`

## 1. Purpose & Methodology

### 1.1 Purpose

This document catalogs every gap that would prevent a vanilla, unmodified PostgreSQL 18.3 server from starting up against a goopg-created `$PGDATA` directory, serving reads, and operating correctly. It covers both **directory structure** and **byte-level data format** compatibility within each file.

### 1.2 Scope

**In scope:**
- Every file and directory goopg creates at `initdb` time
- File-internal binary format compatibility (pg_control, WAL records, heap pages, B-tree pages, SLRU segments, catalog row encodings)
- Runtime state that goopg does or does not persist to disk
- goopg's currently implemented feature set (not aspirational PG features)

**Out of scope:**
- PG features goopg does not implement and that PG does not check at startup (e.g., GiST/GIN/BRIN index formats, TOAST, large objects)
- SQL-level behavioral differences between goopg and PG
- Performance differences
- Protocol-level compatibility

### 1.3 Methodology

For each cluster artifact, we answer five questions:
1. **Does goopg create it?** — Directory or file existence
2. **Is it populated?** — Does goopg write meaningful data, or is the file/directory an empty placeholder?
3. **Is the format compatible?** — Can PG parse goopg's bytes?
4. **What does PG check at startup?** — FATAL, WARNING, or silent acceptance
5. **What is the operational impact?** — Degradation, data loss, or none

Gaps are classified as:
- **❌ BLOCKER** — PG startup would FATAL
- **⚠️ SIGNIFICANT** — PG starts but misbehaves (wrong results, missing data, degraded performance)
- **🔵 DEFERRED** — Documented in `.ralph/deferral_ledger.md` with a planned resolution path
- **— N/A** — Feature not implemented; directory exists as a compatibility placeholder

### 1.4 References

Key design documents and source files:

| Resource | Path |
|---|---|
| PG compat invariants catalog | `docs/design/0107-0001-m0106-pg-compat-invariants.md` |
| Deferral ledger | `.ralph/deferral_ledger.md` |
| pg_control builder | `internal/initdb/pgcontrol.go` |
| Runtime control file | `internal/control/pgcontrol.go` |
| initdb main | `internal/initdb/initdb.go` |
| Storage manager (relpath) | `internal/storage/smgr.go` |
| FSM aggregate | `internal/storage/fsm.go` |
| VM aggregate | `internal/storage/vm.go` |
| WAL format | `internal/wal/format.go` |
| Relcache init file | `internal/initdb/relcache_init.go` |
| Startup wiring | `internal/initdb/open.go` |
| PG 18.3 control file | `postgres/src/include/catalog/pg_control.h` |
| PG 18.3 relpath | `postgres/src/common/relpath.c` |
| PG 18.3 initdb | `postgres/src/bin/initdb/initdb.c` |

---

## 2. Directory Tree — Side-by-Side

All 23 subdirectories from goopg's `initdb.Subdirs` (`internal/initdb/initdb.go:87-114`) compared against PG 18.3's `subdirs[]` (`postgres/src/bin/initdb/initdb.c:231-255`).

| Directory | PG Purpose | goopg Creates? | goopg Populates? | Format Compat? | Status |
|---|---|---|---|---|---|
| `base/` | Per-database data (default tablespace) | ✅ | ✅ | ✅ (see §6) | ✅ |
| `global/` | Shared system catalogs | ✅ | ✅ | ✅ (see §6) | ✅ |
| `pg_wal/` | Write-ahead log | ✅ | ✅ | ⚠️ (see §4) | ⚠️ |
| `pg_wal/archive_status/` | WAL archiving markers | ✅ | — (lazy) | — | ✅ |
| `pg_wal/summaries/` | WAL summary files (PG18) | ✅ | — (lazy) | — | ✅ |
| `pg_xact/` | Transaction commit status (CLOG) | ✅ | ✅ | ✅ (see §5.1) | ✅ |
| `pg_subtrans/` | Subtransaction parent tracking | ✅ | ✅ placeholder | ✅ (see §5.2) | ✅ |
| `pg_multixact/members/` | MultiXact member data | ✅ | ✅ placeholder | ✅ (see §5.3) | ✅ |
| `pg_multixact/offsets/` | MultiXact offset data | ✅ | ✅ placeholder | ✅ (see §5.3) | ✅ |
| `pg_commit_ts/` | Commit timestamps (SLRU) | ✅ | — (empty) | — (feature off) | — |
| `pg_dynshmem/` | Dynamic shared memory segments | ✅ | — (ephemeral) | — | ✅ |
| `pg_logical/` | Logical decoding state | ✅ | — (empty) | — | — |
| `pg_logical/snapshots/` | Logical decoding snapshots | ✅ | — (empty) | — | — |
| `pg_logical/mappings/` | Logical decoding mapping files | ✅ | — (empty) | — | — |
| `pg_notify/` | LISTEN/NOTIFY queue (SLRU) | ✅ | — (empty) | — | — |
| `pg_replslot/` | Replication slot state | ✅ | ⚠️ (see §10.D) | ⚠️ | ⚠️ |
| `pg_serial/` | Serializable transaction state (SLRU) | ✅ | — (empty) | — | — |
| `pg_snapshots/` | Exported snapshot files | ✅ | — (ephemeral) | — | ✅ |
| `pg_stat/` | Permanent statistics file | ✅ | — (empty) | ❌ (see §10.B) | ⚠️ |
| `pg_stat_tmp/` | Temporary statistics | ✅ | — (empty) | — | ✅ |
| `pg_tblspc/` | Tablespace symlinks | ✅ | ⚠️ (see §8) | ⚠️ | ⚠️ |
| `pg_twophase/` | Two-phase commit state files | ✅ | — (empty) | — | — |

**Verdict:** All 23 directories that PG expects are created. The compatibility gaps are in what's inside them, not in directory existence.

**Structural differences in how directories are created:**
- **`pg_wal/`**: PG 18.3 creates `pg_wal/` separately via `create_xlog_or_symlink()` (initdb.c:2953), NOT in its `subdirs[]` array. goopg includes `pg_wal/` directly in its `Subdirs` list (initdb.go:90). Functionally equivalent — the directory exists at the same path.
- **`base/1/`**: PG 18.3 lists `base/1` in its `subdirs[]` (initdb.c:246). goopg does NOT include it in `Subdirs` — instead, `CreatePerDatabaseScaffolding()` creates `base/1/`, `base/4/`, and `base/5/` during database bootstrap (initdb.go:817). Functionally equivalent — template1 directory exists.

---

## 3. Config & Control Files

### 3.1 PG_VERSION

- **Location:** `$PGDATA/PG_VERSION` and `base/<dbOid>/PG_VERSION` (for OIDs 1, 4, 5)
- **goopg writes:** `"18\n"` (three bytes: `0x31 0x38 0x0A`)
- **PG expects:** first whitespace-delimited token parsed as major version integer → compared to `PG_MAJORVERSION_NUM` (18)
- **Format compatibility:** ✅ PG's `ValidatePgVersion()` (`postgres/src/backend/utils/init/miscinit.c:1770`) accepts this
- **Status:** ✅ **COMPATIBLE**

### 3.2 postgresql.conf / postgresql.auto.conf

- **goopg writes:** `config.SampleConfig()` output for `postgresql.conf`; empty header comment for `postgresql.auto.conf`
- **PG behavior:** PG reads and parses these at startup. Unknown GUCs produce WARNING, not FATAL.
- **Potential issues:**
  - goopg-registered GUCs (e.g., `io_method`, `io_workers`, `io_max_concurrency`, `allow_in_place_tablespaces`) are unknown to PG → PG emits warnings but continues
  - PG-expected GUCs not registered in goopg (e.g., `effective_io_concurrency` — see `.ralph/deferral_ledger.md` row `0009-readstream`) → goopg's config file omits them; PG uses its compiled-in defaults
- **Status:** ✅ **COMPATIBLE** (warnings only, no startup failure)

### 3.3 pg_hba.conf / pg_ident.conf

- **goopg writes:** minimal HBA (`trust` all), empty ident map
- **PG behavior:** parses these at startup. Format is standard.
- **Status:** ✅ **COMPATIBLE**

### 3.4 global/pg_control

- **Location:** `global/pg_control`
- **Physical format:** 8192 bytes total, 296 bytes active payload, CRC32C at offset 292, 7896 bytes zero-pad
- **goopg builder:** `internal/initdb/pgcontrol.go:buildPgControl()`

#### Field-by-field comparison (x86_64 little-endian)

| Offset | Size | Field | goopg Value | PG 18.3 Expected | Match? |
|---|---|---|---|---|---|
| 0 | 8 | `system_identifier` | Random uint64 | Cluster-unique random ID | ✅ |
| 8 | 4 | `pg_control_version` | 1800 | `PG_CONTROL_VERSION` = 1800 | ✅ |
| 12 | 4 | `catalog_version_no` | 202506291 | `CATALOG_VERSION_NO` | ✅ |
| 16 | 4 | `state` | 1 (`DB_SHUTDOWNED`) | 1 = clean shutdown | ✅ |
| 20 | 4 | *(padding)* | 0 | implicit | ✅ |
| 24 | 8 | `time` | `now.Unix()` | pg_time_t | ✅ |
| 32 | 8 | `checkPoint` | `pgInitCheckpointLSN` | Last checkpoint LSN | ✅ |

**CheckPoint struct (embedded at offset 40–127, 88 bytes):**

| Offset | Size | Field | goopg Value | PG 18.3 Match? |
|---|---|---|---|---|
| 40 | 8 | `redo` | `pgInitCheckpointLSN` | ✅ |
| 48 | 4 | `ThisTimeLineID` | 1 (`BootstrapTimeLineID`) | ✅ |
| 52 | 4 | `PrevTimeLineID` | 1 | ✅ |
| 56 | 1 | `fullPageWrites` | 1 (GUC default) | ✅ |
| 57 | 3 | *(padding)* | 0 | ✅ |
| 60 | 4 | `wal_level` | 1 (`replica`, from GUC) | ✅ |
| 64 | 8 | `nextXid` | 3 (`FirstNormalTransactionId`) | ✅ |
| 72 | 4 | `nextOid` | 10000 (`FirstGenbkiObjectId`) | ✅ |
| 76 | 4 | `nextMulti` | 1 (`FirstMultiXactId`) | ✅ |
| 80 | 4 | `nextMultiOffset` | 0 | ✅ |
| 84 | 4 | `oldestXid` | 3 | ✅ |
| 88 | 4 | `oldestXidDB` | 1 (template1) | ✅ |
| 92 | 4 | `oldestMulti` | 1 | ✅ |
| 96 | 4 | `oldestMultiDB` | 1 | ✅ |
| 100 | 4 | *(padding)* | 0 | ✅ |
| 104 | 8 | `time` | `now.Unix()` | ✅ |
| 112 | 4 | `oldestCommitTsXid` | 0 (`InvalidTransactionId`) | ✅ |
| 116 | 4 | `newestCommitTsXid` | 0 (`InvalidTransactionId`) | ✅ |
| 120 | 4 | `oldestActiveXid` | 0 (`InvalidTransactionId`) | ✅ |
| 124 | 4 | *(padding to 8-byte boundary)* | 0 | ✅ |

**Note:** PG 18.3's `CheckPoint` struct ends at `oldestActiveXid` (84 bytes + 4 padding = 88 bytes). PG 19+ adds `dataChecksumState` (uint32) at offset 124. goopg correctly targets PG 18.3 layout.

**Remaining ControlFileData fields:**

| Offset | Size | Field | goopg | PG 18.3 | Match? |
|---|---|---|---|---|---|
| 128 | 8 | `unloggedLSN` | 1000 (`FirstNormalUnloggedLSN`) | ✅ |
| 136 | 8 | `minRecoveryPoint` | 0 | ✅ |
| 144 | 4 | `minRecoveryPointTLI` | 0 | ✅ |
| 148 | 4 | *(padding)* | 0 | ✅ |
| 152 | 8 | `backupStartPoint` | 0 | ✅ |
| 160 | 8 | `backupEndPoint` | 0 | ✅ |
| 168 | 1 | `backupEndRequired` | 0 (false) | ✅ |
| 169 | 3 | *(padding)* | 0 | ✅ |
| 172 | 4 | `wal_level` | from GUC | ✅ |
| 176 | 1 | `wal_log_hints` | 0 | ✅ |
| 177 | 3 | *(padding)* | 0 | ✅ |
| 180 | 4 | `MaxConnections` | from GUC | ✅ |
| 184 | 4 | `max_worker_processes` | from GUC | ✅ |
| 188 | 4 | `max_wal_senders` | from GUC | ✅ |
| 192 | 4 | `max_prepared_xacts` | from GUC | ✅ |
| 196 | 4 | `max_locks_per_xact` | from GUC | ✅ |
| 200 | 1 | `track_commit_timestamp` | 0 (false) | ✅ |
| 201 | 3 | *(padding)* | 0 | ✅ |
| 204 | 4 | `maxAlign` | 8 | ✅ |
| 208 | 8 | `floatFormat` | `math.Float64bits(1234567.0)` | ✅ |
| 216 | 4 | `blcksz` | 8192 | ✅ |
| 220 | 4 | `relseg_size` | 131072 (1 GiB) | ✅ |
| 224 | 4 | `xlog_blcksz` | 8192 | ✅ |
| 228 | 4 | `xlog_seg_size` | 16777216 (16 MiB) | ✅ |
| 232 | 4 | `nameDataLen` | 64 | ✅ |
| 236 | 4 | `indexMaxKeys` | 32 | ✅ |
| 240 | 4 | `toast_max_chunk_size` | 1996 | ✅ |
| 244 | 4 | `loblksize` | 2048 | ✅ |
| 248 | 1 | `float8ByVal` | 1 (true) | ✅ |
| 249 | 3 | *(padding)* | 0 | ✅ |
| 252 | 4 | `data_checksum_version` | 1 if enabled, else 0 | ✅ |
| 256 | 1 | `default_char_signedness` | 1 (true) | ✅ |
| 257 | 32 | `mock_authentication_nonce` | Random bytes | ✅ |
| 289 | 3 | *(padding)* | 0 | ✅ |
| 292 | 4 | `crc` | CRC32C over [0, 292) | ✅ |
| 296 | 7896 | *(zero pad)* | 0 | ✅ |

**Verification:** PG 18.3's `ControlFileData` has no `slru_pages_per_segment` field — that was added in a later PG version. goopg's layout at 296 bytes matches PG 18.3 exactly. The `CheckPoint` struct at 88 bytes also matches PG 18.3 (no `dataChecksumState` field yet).

- **Status:** ✅ **COMPATIBLE** — byte-identical to PG 18.3

### 3.5 global/system_identifier

- **goopg creates:** `global/system_identifier` — 8-byte little-endian uint64 (the same value written into pg_control offset 0)
- **PG behavior:** PG embeds the system identifier ONLY inside `pg_control`. It never opens or reads `global/system_identifier`.
- **Impact:** Harmless extra file. PG ignores it.
- **Status:** ✅ **BENIGN** — goopg-specific extra file, PG ignores

---

## 4. WAL (pg_wal/)

### 4.1 Directory Structure

| Artifact | goopg | PG 18.3 | Status |
|---|---|---|---|
| `pg_wal/` directory | ✅ Created | Required | ✅ |
| `pg_wal/archive_status/` | ✅ Created (empty) | Created by initdb; `.ready`/`.done` files appear lazily | ✅ |
| `pg_wal/summaries/` | ✅ Created (empty) | Created by initdb; summary files appear lazily (PG 18 new feature) | ✅ |

### 4.2 Bootstrap WAL Segment

- **File:** `pg_wal/000000010000000000000001`
- **goopg creates via:** `internal/initdb/wal_bootstrap.go:WriteBootstrapWAL()`
- **Size:** 16 MiB (16777216 bytes)
- **Segment naming formula:** `%08X%08X%08X` → timeline=1, segment=1 → `00000001 00000000 00000001`
- **First page:** 40-byte `XLogLongPageHeaderData` with magic `0xD118`, system identifier, segment size, timeline ID
- **Record payload:** `XLOG_CHECKPOINT_SHUTDOWN` with 88-byte `CheckPoint` body
- **Remainder:** zero-filled (PG interprets trailing zeros as end-of-WAL)

**Format compatibility:**
- ✅ Segment naming matches PG 18.3
- ✅ Long page header (magic, sysid, seg_size, TLI) matches `XLogLongPageHeaderData`
- ✅ `XLOG_CHECKPOINT_SHUTDOWN` record framing correct
- ✅ CheckPoint body (88 bytes) matches PG 18.3 field-for-field

### 4.3 WAL Record Format

goopg's WAL record encoding produces PG-compatible `XLogRecord` headers:

| Field | Size | Format | Status |
|---|---|---|---|
| `xl_tot_len` | 4 bytes | Total record length including header | ✅ |
| `xl_xid` | 4 bytes | Transaction ID | ✅ |
| `xl_prev` | 8 bytes | Previous record LSN | ⚠️ **See below** |
| `xl_info` | 1 byte | RMGR-specific info bits | ✅ |
| `xl_rmid` | 1 byte | Resource manager ID | ⚠️ **See below** |
| *(padding)* | 2 bytes | — | ✅ |
| `xl_crc` | 4 bytes | CRC32C over record data | ✅ |

**⚠️ `xl_prev` is 1-based (deferral entry #29):** goopg's restart-seed bug causes `prevRecPtr` to be 1-based instead of 0-based. This does NOT prevent PG from replaying WAL (the prev-link is used for chain validation, not for recovery logic), but `pg_waldump` aborts chain traversal at the second record with an "incorrect prev-link" error.

**⚠️ RmgrGoopgCatalog=128 (deferral entries B4 series):** goopg registers a custom resource manager (ID 128) for DDL operations (CREATE/ALTER/DROP TABLE, etc.). A PG 18.3 binary encountering WAL records with `xl_rmid=128` cannot parse them — PG's `RmgrTable` has no entry at index 128. Impact depends on PG's WAL replay mode:
- In crash recovery: PG FATALs on unknown rmgr ID
- In standby mode: PG cannot replay DDL WAL, falling behind the primary

The B4/B5 series (`.ralph/deferral_ledger.md` entries #395–#403) progressively retires goopg-specific WAL kinds in favor of PG-native heap mutation records. B4.6 Stage 1+2 landed; remaining B4.6 stages 3-4 and B5 are deferred.

**⚠️ WAL atomicity gap (deferral entry #27):** goopg's heap update path calls `PageAddHeapTuple` then separately emits a WAL record. PG records both old+new tuple data in a single atomic WAL record (`log_heap_update`). On crash between page mutation and WAL flush, goopg leaves an inconsistent page.

### 4.4 Resource Manager Compatibility

| RMGR ID | Name | goopg Status | PG Can Parse? |
|---|---|---|---|
| 0 | `XLOG` | ✅ Checkpoint, FPI records | ✅ |
| 1 | `XACT` | ✅ Commit/abort records | ✅ |
| 9 | `HEAP2` | ✅ Multi-insert/visible/freeze/prune/vacuum | ✅ |
| 10 | `HEAP` | ✅ Insert/update/delete/hot-update/lock | ✅ |
| 11 | `BTREE` | ✅ Insert/split/newroot/delete/vacuum | ✅ |
| 128 | `GoopgCatalog` | ⚠️ DDL journaling (B4 series retiring) | ❌ |

### 4.5 WAL Segment Lifecycle

- **Preallocation:** goopg zero-fills unused WAL bytes. PG interprets zeroed page headers as end-of-WAL. ✅ Compatible.
- **Segment recycling:** goopg's `checkpointer.go` handles WAL segment removal/recycling based on `min_wal_size`/`max_wal_size` GUCs. ✅ Compatible.

---

## 5. SLRU-Based Subsystems

### 5.1 pg_xact/ (CLOG — Commit Log)

- **Directory:** `pg_xact/`
- **SLRU parameters:** 2 bits per XID, 32768 XIDs per 8 KiB page, 32 pages per segment
- **Segment naming:** `%04X` (e.g., `0000`, `0001`)
- **goopg init:** `bootstrapCLog()` (`initdb.go:6252`) enables SLRU mirror at `pg_xact/`, marks `BootstrapTransactionId`(1) and `FrozenTransactionId`(2) as committed → creates `pg_xact/0000`
- **Runtime writes:** CLOG transactions write 2-bit status codes (`IN_PROGRESS=0`, `COMMITTED=1`, `ABORTED=2`, `SUB_COMMITTED=3`) matching PG's `TRANSACTION_STATUS_*` constants
- **CLOG truncation:** checkpoint `TruncateCLOGFn` removes old segments
- **Format compatibility:** ✅ PG's `StartupCLOG()` reads goopg's SLRU pages correctly
- **Status:** ✅ **COMPATIBLE**

### 5.2 pg_subtrans/ (Subtransaction Tracking)

- **Directory:** `pg_subtrans/`
- **Format:** 4-byte parent `TransactionId` per XID slot, 2048 XIDs per page, 32 pages per segment
- **goopg init:** `bootstrapSLRUPlaceholders()` creates `pg_subtrans/0000` (8 KiB zero page)
- **Runtime writes:** `SubxactMap` through `RegisterSubXid` → persisted via SLRU mirror
- **Format compatibility:** ✅ PG's `StartupSUBTRANS()` reads goopg's segment files correctly
- **Status:** ✅ **COMPATIBLE**

### 5.3 pg_multixact/ (MultiXact — Members and Offsets)

- **Directories:** `pg_multixact/members/`, `pg_multixact/offsets/`
- **goopg init:** `bootstrapSLRUPlaceholders()` creates `pg_multixact/members/0000` and `pg_multixact/offsets/0000` (8 KiB zero pages)
- **Runtime writes:** goopg does **NOT** write multixact data at runtime (multixact is engine-first only, not wired to tuple locking). The placeholder segments satisfy PG startup checks.
- **Format compatibility:** ✅ Placeholder pages are valid SLRU pages (zero content = no multixacts exist)
- **Operational gap:** If a PG process running against goopg's data dir were to create multixacts (e.g., via `SELECT FOR SHARE` with multiple lockers), the SLRU segments would need to be written correctly. This is a future concern.
- **Status:** ✅ **COMPATIBLE** for current goopg feature scope

### 5.4 Unused SLRU Directories

These directories exist as empty compatibility placeholders. PG does not FATAL on empty SLRU directories — they are only accessed when the corresponding feature is enabled.

| Directory | Feature | goopg GUC Default | PG Behavior at Startup |
|---|---|---|---|
| `pg_commit_ts/` | Commit timestamps | `track_commit_timestamp=off` | Not accessed |
| `pg_notify/` | LISTEN/NOTIFY | Not implemented | Not accessed |
| `pg_serial/` | Serializable SI | Not implemented | Not accessed |
| `pg_logical/snapshots/` | Logical decoding | Not implemented | Not accessed |
| `pg_logical/mappings/` | Logical decoding | Not implemented | Not accessed |

**Status:** ✅ **COMPATIBLE** — empty directories are valid when features are disabled

---

## 6. Catalog Storage (base/ and global/)

### 6.1 Architecture Overview

goopg uses a **two-mechanism catalog architecture**:

1. **In-memory catalog** (`catalog.InMemory`): Go structs (`map[string]*Table`) — the primary runtime catalog. All query planning and execution reads from here.
2. **Heap-backed copy**: Selected bootstrap catalogs are written as PG-format heap files at init time (`bootstrapSystemCatalogs()`, `bootstrapSharedCatalogPlaceholders()`). Runtime DDL operations optionally call `syncTableToCatalogHeap` to write updated rows to heap files for PG standby consumption.

**Key architectural differences from PG:**
- PG's catalog is heap-first: heap files are the single source of truth; relcache is derived from them
- goopg's catalog is memory-first: Go structs are authoritative; heap files are a sync target
- `pg_class` is **virtual** — rows are generated on-the-fly by `VirtualRows` builder, not read from a heap file
- `pg_attribute` is **heap-backed** — rows are stored in `base/<dbOid>/1249`

### 6.2 Shared Catalogs (global/)

Files goopg creates under `global/` at init time:

| RelOID | Catalog | Format | Bootstrap Method |
|---|---|---|---|
| 1213 | `pg_tablespace` | Heap + B-tree indexes | `bootstrapPgTablespaceTuples` + index bootstraps |
| 1214 | `pg_shdepend` | Heap (empty placeholder) | `bootstrapSharedCatalogPlaceholders` |
| 1260 | `pg_authid` | Heap + indexes (2676, 2677) | B4.5: real heap row (`.ralph/deferral_ledger.md` #399) |
| 1261 | `pg_auth_members` | Heap + indexes (2694, 2695) | B4.3: real heap row (#397) |
| 1262 | `pg_database` | Heap + indexes (2671, 2672) | B4.6 Stage 1+2: real heap row (#401–#402) |
| 2964 | `pg_db_role_setting` | Heap | B4.2: real heap row (#395) |
| 3592 | `pg_shdescription` | Heap (empty placeholder) | `bootstrapSharedCatalogPlaceholders` |
| 6000 | `pg_replication_origin` | Heap (empty placeholder) | `bootstrapSharedCatalogPlaceholders` |
| 6100 | `pg_subscription` | Heap | B4.4: real heap row (#398) |
| 6243 | `pg_parameter_acl` | Heap (empty placeholder) | `bootstrapSharedCatalogPlaceholders` |

**B4 series status:** B4.1 (pg_tablespace), B4.2 (pg_db_role_setting), B4.3 (pg_auth_members), B4.4 (pg_subscription), B4.5 (pg_authid), and B4.6 stages 1–2 (pg_database heap row + OID preservation) have landed. Remaining: B4.6 stages 3–4 (WAL_LOG + RM_DBASE + standby E2E), then B5 (retire RmgrGoopgCatalog=128).

**Remaining gaps:**
- ⚠️ **pg_tablespace no heap visibility** — goopg maintains a runtime registry but does not expose pg_tablespace rows via on-disk heap for PG standby resolver
- 🔵 Additional shared catalogs (`pg_shdepend`, `pg_shdescription`, `pg_replication_origin`, `pg_parameter_acl`) are empty placeholder pages at init time

### 6.3 Per-Database Catalogs (base/<dbOid>/)

goopg creates catalog files under `base/1/` (template1), `base/4/` (template0), and `base/5/` (postgres) during init.

**Bootstrap catalog files** (~25 heap + ~30 index files created by individual `bootstrap*()` functions):

Core system catalogs with populated rows:
- `1247` — `pg_type` (built-in type entries)
- `1249` — `pg_attribute` (column definitions for system catalogs)
- `1255` — `pg_proc` (built-in functions)
- `1259` — `pg_class` (catalog table descriptors — virtual in goopg, heap-backed for PG compat)
- `2615` — `pg_namespace` (schema entries)
- `2610` — `pg_index` (index metadata)
- `2601` — `pg_am` (access methods: heap, btree)
- `2602` — `pg_amop` (access method operators)
- `2603` — `pg_amproc` (access method support procs)
- `2616` — `pg_opclass` (operator classes)
- `2617` — `pg_operator` (operators)
- `2618` — `pg_rule` (rewrite rules — virtual)
- `2619` — `pg_statistic` (column statistics — empty placeholder at init; **populated at runtime by ANALYZE via `persistStatsToPGStatistic`**)

Plus ~30 **mapped local catalog placeholders** (`bootstrapMappedLocalCatalogHeaps`, `initdb.go:1453`): empty 8 KiB pages for catalogs that PG expects to exist but goopg does not yet populate at init (e.g., `pg_default_acl` (826), `pg_attrdef` (2604), `pg_constraint` (2606), `pg_trigger` (2620), `pg_rewrite` (2618), etc.)

**Critical gaps:**

- **❌ DDL durability — ADD COLUMN (deferral entry #404):** `ALTER TABLE ADD COLUMN` does NOT call `syncTableToCatalogHeap`, so the new column's `pg_attribute` row is never written to the heap file or WAL. It disappears on restart.
- **⚠️ Catalog coverage:** goopg implements 13 of 64 `pg_catalog` tables (`docs/parity-dashboard.md`). Many PG-required catalogs exist only as empty placeholder pages. If PG attempts to scan them at startup (e.g., `load_critical_index` for `pg_constraint_oid_index`), it may fail.
- **⚠️ Per-database namespace:** goopg's catalog is ONE shared `map[string]*Table` per process. `RelFileNode.DBOid` is often hardcoded to `catalog.DefaultDBOid` (5) regardless of which database a connection attached to. (Multi-loop epic: `.ralph/deferral_ledger.md` entries #245–#254.)
- **⚠️ `pg_statistic` runtime persistence — partial:** `ANALYZE` writes rows to `pg_statistic` heap via `persistStatsToPGStatistic()` (`operators_analyze.go:179`), and `loadStatisticsFromHeap()` (`open.go:1447`) restores them at startup. However, stats are also stored as per-connection in-memory `Stats` fields — other connections within the same process may not see freshly-analyzed stats until the catalog is re-read. The `pg_stat/pgstat.stat` cumulative statistics file is never written (separate from `pg_statistic`). See §10.B for full analysis.

### 6.4 Heap Page / Tuple Format

**Page layout** (`PageHeaderData`, 24 bytes):
- ✅ `pd_lsn` (8 bytes) — LSN of last page modification
- ✅ `pd_checksum` (2 bytes) — data checksum (if enabled)
- ✅ `pd_flags` (2 bytes) — page flags
- ✅ `pd_lower` (2 bytes) — offset to start of free space
- ✅ `pd_upper` (2 bytes) — offset to end of free space
- ✅ `pd_special` (2 bytes) — offset to special space
- ✅ `pd_pagesize_version` (2 bytes) — page size and layout version
- ✅ `pd_prune_xid` (4 bytes) — oldest unpruned XID

**Item pointer** (`ItemIdData`, 4 bytes, 32-bit bitfield):
- ✅ `lp_off:15` — item offset
- ✅ `lp_flags:2` — item flags (`LP_UNUSED=0`, `LP_NORMAL=1`, `LP_REDIRECT=2`, `LP_DEAD=3`)
- ✅ `lp_len:15` — item length

**Heap tuple header** (`HeapTupleHeaderData`, 23 bytes + null bitmap):
- ✅ `t_xmin` (4 bytes) — inserting XID
- ✅ `t_xmax` (4 bytes) — deleting/locking XID
- ✅ `t_field3` (4 bytes) — command ID or XID overlay
- ✅ `t_ctid` (6 bytes) — current or new tuple ID (`ItemPointerData`: block number in two 16-bit halves + 16-bit offset `ip_posid`)
- ✅ `t_infomask2` (2 bytes) — attribute count and flag bits
- ✅ `t_infomask` (2 bytes) — flag bits (`HEAP_HASNULL`, `HEAP_HASVARWIDTH`, `HEAP_XMIN_COMMITTED`, etc.)
- ✅ `t_hoff` (1 byte) — header size including null bitmap

**Varlena encoding:**
- ✅ 1-byte header for values ≤ 126 bytes (`0x00..0x7E` = length-1)
- ✅ 4-byte header for values > 126 bytes (`0x80000000 | length-4`)
- ✅ `HEAP_HASVARWIDTH` infomask bit set when varlena columns present

**Catalog row formats:**
- ✅ `Form_pg_class` (34 columns) — byte-equivalent to PG 18.3
- ✅ `Form_pg_attribute` (25 columns) — byte-equivalent to PG 18.3
- ✅ Null bitmap encoding follows PG `BITMAPLEN` / `offsetof` rules
- ✅ `relacl` (aclitem[]), `reloptions` (text[]), `proargtypes` (oidvector) use PG binary `ArrayType` encoding

**Status:** ✅ **COMPATIBLE** — heap page and tuple formats are PG 18.3 byte-identical. Verification via `docs/design/0107-0001-m0106-pg-compat-invariants.md` §1–§3.

### 6.5 B-tree Index Format

- ✅ **Meta page (block 0):** `BTMetaPageData` — magic `0x053162`, version 4
- ✅ **Special area:** `BTPageOpaqueData` — page type, siblings, level, cycle ID
- ✅ **Index tuple:** `IndexTupleData` header + key attributes, PG-compatible NULL-bitmap word
- ✅ **Key ordering:** HIKEY (high key) at first-right position
- ✅ **Critical indexes present:** `pg_class_oid_index` (2662), `pg_attribute_relid_attnam_index` (2658), `pg_attribute_relid_attnum_index` (2659), `pg_type_oid_index` (2703), `pg_database_oid_index` (2672), `pg_authid_oid_index` (2828), `pg_authid_rolname_index` (2676), `pg_auth_members_role_member_index` (2694), `pg_auth_members_member_role_index` (2695)

**Status:** ✅ **COMPATIBLE** — B-tree page and tuple format matches PG 18.3

---

## 7. Heap Forks (FSM, VM, Init)

### 7.1 Free Space Map (FSM)

- **PG format:** Per-relation fork file `<relfilenode>_fsm` in three-level B-tree-of-bytes format (`postgres/src/backend/storage/freespace/freespace.c`). One byte per heap page indicating free space fraction (0–255 = 0%–100%).
- **goopg format:** Aggregate file `global/pg_fsm_state.bin` in goopg-custom binary format:
  - Magic: `0x66534D31` ("fSM1")
  - Version: uint32 (1)
  - NumRels: uint32
  - Per-relation: `{DBOid: uint32, RelOid: uint32, NBlocks: uint32, FreeBytes: []uint16}`
  - Created/saved by `Runtime.SaveFSM()` (`open.go:3041`)
- **Gap:** goopg does NOT create per-relation `_fsm` fork files. PG would see no FSM for any relation.
- **Impact on PG:** PG falls back to sequential scan for free-space search on insert (no FSM). Startup NOT blocked; operational degradation only.
- **❌ FORMAT MISMATCH** — goopg's aggregate `pg_fsm_state.bin` is unreadable by PG. Missing per-relation `_fsm` fork files.
- **Deferral reference:** `.ralph/deferral_ledger.md` — `0009-readstream` row notes FSM fork infrastructure exists but uses aggregate format

### 7.2 Visibility Map (VM)

- **PG format:** Per-relation fork file `<relfilenode>_vm` with one bit per heap page (or two bits for VM v2) at PG-defined offset. Updated via `HEAP_XLOG_VISIBLE` WAL records.
- **goopg format:** Aggregate file `global/pg_vm_state.bin` in goopg-custom binary format:
  - Magic: `0x764D5331` ("vMS1")
  - Version: uint32 (1)
  - NumRels: uint32
  - Per-relation: `{DBOid: uint32, RelOid: uint32, NBlocks: uint32, Bits: []byte}`
  - Created/saved by `Runtime.SaveVM()` (`open.go:3028`)
- **Gap:** goopg does NOT create per-relation `_vm` fork files. PG would see no VM for any relation.
- **Impact on PG:** PG cannot perform index-only scans (requires VM to verify all-visible pages). Sequential scans still work. Startup NOT blocked; operational degradation only.
- **❌ FORMAT MISMATCH** — goopg's aggregate `pg_vm_state.bin` is unreadable by PG. Missing per-relation `_vm` fork files.

### 7.3 Init Fork

- **PG format:** Per-relation `<relfilenode>_init` fork file for unlogged relations. Created at relation creation time; used at crash recovery to reset the main fork to empty state.
- **goopg:** Format is PG-compatible when created. Init fork creation is on-demand.
- **Status:** ⚠️ **PARTIAL** — format is compatible when used, but goopg's unlogged relation support may be incomplete

---

## 8. Tablespaces

### 8.1 Directory Structure

- **goopg init:** `pg_tblspc/` directory created but empty
- **goopg runtime:** `CREATE TABLESPACE` creates `pg_tblspc/<oid>/PG_18_202506291/<dbOid>/` when `allow_in_place_tablespaces` GUC is enabled
- **smgr.relDir():** correctly routes relation files to `pg_tblspc/<TblOid>/<versionDir>/<DBOid>/<RelOid>`

### 8.2 Gaps

- **⚠️ Symlink vs in-place directories:** PG normally stores tablespaces as *symlinks*: `pg_tblspc/<oid>` → `/path/to/actual/tablespace`. goopg uses `allow_in_place_tablespaces` to create real directories inside `pg_tblspc/`. PG startup scans for symlinks — if it finds a real directory instead, it may not recognize it as a tablespace.
- **⚠️ `pg_tablespace` heap visibility:** goopg maintains a runtime tablespace registry, but does not expose `pg_tablespace` catalog rows via on-disk heap for PG standby resolver.
- **Deferral reference:** `.ralph/deferral_ledger.md` entry #45 (foundation landed); `.ralph/deferral_ledger.md` entry #244 (physical relocation landed); version subdirectory, BASE_BACKUP tar emission, and heap visibility deferred.

---

## 9. goopg-Specific Files (No PG Equivalent)

Files PG would encounter in goopg's data directory but has no code to parse:

| File | Format | PG Behavior | Risk |
|---|---|---|---|
| `global/system_identifier` | 8-byte little-endian uint64 | PG does not open this file; sysid loaded from `pg_control[0:8]` | None |
| `global/pg_fsm_state.bin` | goopg custom binary (see §7.1) | PG ignores unrecognized files in `global/` | PG sees no per-relation `_fsm` → falls back to seq scan |
| `global/pg_vm_state.bin` | goopg custom binary (see §7.2) | PG ignores unrecognized files in `global/` | PG sees no per-relation `_vm` → no index-only scans |
| `global/pg_goopg_catalog_cache.json` | JSON text (catalog snapshot for fast restart) | PG ignores unrecognized files in `global/` | None |
| `base/<dbOid>/pg_goopg_catalog_cache.json` | JSON text (per-DB catalog snapshot) | PG ignores unrecognized files in `base/<dbOid>/` | None |

---

## 10. Unpopulated Directories — Startup vs Operational Impact

Directories goopg creates at init time but leaves empty (or with placeholder pages only). This section classifies them by what happens when PG starts against goopg's data directory.

### A. Safely Empty — PG Treats Empty as Normal Initial State

These directories are expected to be empty after a clean initdb. PG creates content lazily.

| Directory | What PG Stores | When Populated | PG Startup Check |
|---|---|---|---|
| `pg_dynshmem/` | Dynamic shared memory segments (mmap'd files) | Ephemeral — created/removed by backends at runtime | No check |
| `pg_snapshots/` | Exported transaction snapshot files | Created by `pg_export_snapshot()`, cleaned up at transaction end | No check |
| `pg_wal/archive_status/` | `.ready` and `.done` marker files for WAL archiver | Created by archiver process at runtime | No check |
| `pg_wal/summaries/` | WAL summary files (PG 18 new feature) | Created by WAL summarizer process | No check |

### B. Empty but PG Would Rebuild — Acceptable Startup, Degraded Operation

These directories contain data PG normally persists across restarts. PG starts without them but operates degraded until data is rebuilt.

#### pg_stat/ and pg_stat_tmp/

- **PG stores:** `pg_stat/pgstat.stat` — permanent cumulative statistics file containing:
  - Per-table row counts, live/dead tuple counts, changes since last analyze
  - Per-index scan counts, tuple fetch counts
  - Per-function call counts
  - Autovacuum trigger thresholds
- **goopg behavior:** Never writes to `pg_stat/` or `pg_stat_tmp/`. Statistics live in-process only (Go structs in `activity.Registry`).
- **Impact on PG startup:** PG stats collector starts with empty statistics. Autovacuum launcher sees zero dead tuples → no vacuum triggered until stats accumulate. Autoanalyze sees zero changes → no auto-analyze until stats accumulate. **NOT a startup blocker.**
- **Impact on query planning:** All `pg_stat_*` and `pg_statio_*` views return zeros. PG's cost model falls back to default selectivity estimates.

#### pg_statistic Catalog Table (Per-Column Statistics)

- **PG stores:** Per-column statistics (`stanullfrac`, `stawidth`, `stadistinct`, `stakindN`, `staopN`, `stanumbersN`, `stavaluesN`) in `pg_statistic` heap tuples (OID 2619). These survive restarts and are the planner's primary statistics source. PG also stores cumulative table/index statistics in `pg_stat/pgstat.stat`.
- **goopg behavior:**
  1. `ANALYZE` computes statistics and calls `persistStatsToPGStatistic()` (`operators_analyze.go:179`) — this **DOES write** `pg_statistic` heap rows to disk
  2. `loadStatisticsFromHeap()` (`open.go:1447`) restores stats from `pg_statistic` heap rows at startup — stats **DO survive restart**
  3. `ANALYZE` also calls `SetTableStats()` to update the in-memory `Stats` fields. These in-memory stats are what the query planner reads during execution
  4. In-memory stats are **per-connection** ([memory: `goopg_analyze_per_connection_stats`]) — `ANALYZE` in one connection updates the in-memory `Stats` but other connections' planners do not see the update until they re-read from the catalog heap (or until restart + `loadStatisticsFromHeap`)
  5. `pg_stat/pgstat.stat` is **never created** — the cumulative statistics file is empty
- **Impact after restart:**
  - `loadStatisticsFromHeap()` restores stats from `pg_statistic` heap rows → planner has statistics from the last `ANALYZE` that ran before shutdown
  - All `pg_stat_*` / `pg_statio_*` views return zeros (29+ views registered with honest-0 values, `.ralph/deferral_ledger.md` entries #314–#324) because `pg_stat/pgstat.stat` is absent
- **Impact on PG:** PG would find `pg_statistic` heap rows written by goopg's `ANALYZE`. If `loadStatisticsFromHeap` had run before shutdown, the heap rows are current. PG's planner can read them via normal `pg_statistic` catalog scans. The missing `pg_stat/pgstat.stat` means PG's cumulative statistics views return zeros until its own stats collector populates them.
- **Status:** ⚠️ **SIGNIFICANT** — cumulative stats file (`pg_stat/pgstat.stat`) not written; per-connection stats visibility gap within running process. `pg_statistic` heap rows ARE persisted and restored at startup.

### C. Feature Not Implemented — No Data To Persist

These directories are empty because goopg does not implement the corresponding feature. If a user were to use the feature via PG running against goopg's data directory, the lack of persisted state would cause data loss on restart.

| Directory | PG Persists | goopg Feature | Data Loss If Feature Used via PG |
|---|---|---|---|
| `pg_twophase/` | Per-prepared-transaction state files (named by GXID, e.g., `0000000000000017`). Contain serialized transaction state for `COMMIT PREPARED` / `ROLLBACK PREPARED`. | Not implemented | ❌ Prepared transactions LOST on restart — PG would see no state file and treat them as rolled back |
| `pg_notify/` | SLRU-based queue of pending LISTEN/NOTIFY notifications. Survives clean shutdown and restart. | Not implemented | ❌ Pending async notifications silently lost on restart |
| `pg_serial/` | SLRU-based serializable transaction conflict tracking (SERIALIZABLE isolation). Needed for SSI to detect serialization anomalies across restarts. | Not implemented | ❌ Serializable transactions could see anomalies after restart |
| `pg_logical/snapshots/` | SLRU-based logical decoding snapshot data (replication slot state) | Not implemented | ❌ Logical replication slots lose snapshot state |
| `pg_logical/mappings/` | Logical decoding relation mapping files (OID→name mappings for output plugins) | Not implemented | ❌ Logical replication slots lose relation mappings |
| `pg_commit_ts/` | SLRU-based commit timestamps (one timestamp per XID, stored as 64-bit microsecond values) | `track_commit_timestamp=off` (default) | ⚠️ Harmless while GUC stays off; if turned on, commit timestamp data lost on restart |

**Note:** All of these are **future concerns** — goopg does not implement the features, so there is no data to lose. The risk only materializes if someone runs PG against goopg's data directory AND uses these features.

### D. Potentially Problematic — Partial Support, Persistence Unclear

#### pg_replslot/

- **goopg infrastructure:** `wal.OpenSlots(abs)` is called at startup (`open.go:1626`), so goopg HAS replication slot infrastructure.
- **Unclear:** Whether slot state (`restart_lsn`, `confirmed_flush_lsn`) is correctly persisted to `pg_replslot/<name>/state` by goopg's slot implementation.
- **Risk:** If physical replication slots were created against goopg and state is not persisted, a restart would lose the slot's WAL position — a standby would need a full re-sync.
- **Status:** ⚠️ **NEEDS VERIFICATION** — not a startup blocker but a potential data-loss risk for replication users

---

## 11. Prioritized Gap Summary

### ❌ BLOCKERS (PG won't start or will FATAL quickly)

| # | Gap | Category | Detail |
|---|---|---|---|
| 1 | **RmgrGoopgCatalog=128 WAL records** | WAL | PG cannot parse goopg's DDL WAL records (custom rmgr ID 128). In crash recovery: FATAL. B4/B5 series aims to retire these. |
| 2 | **Missing catalog heap rows** | Catalog | Runtime DDL (ADD COLUMN, CREATE SCHEMA, FDW/server/collation DDL) not synced to heap files. PG sees stale/missing catalog data. Entry #404 + #50 in deferral ledger. |
| 3 | **pg_class is virtual** | Catalog | PG may try to heap-scan `pg_class` at startup (for relcache invalidation or `load_critical_index`). goopg's `pg_class` rows are generated on-the-fly, not stored in a heap file. |

### ⚠️ SIGNIFICANT (PG starts but has operational issues)

| # | Gap | Category | Detail |
|---|---|---|---|
| 4 | **FSM/VM aggregate files** | Storage | goopg uses custom `pg_fsm_state.bin`/`pg_vm_state.bin` instead of per-relation `_fsm`/`_vm` fork files. PG: no FSM → falls back to seq scan for inserts; no VM → no index-only scans. |
| 5 | **xl_prev 1-based** | WAL | `pg_waldump` chain traversal broken at second record. Does not block recovery replay. Entry #29. |
| 6 | **WAL atomicity** | WAL | Heap update + WAL emission not atomic; crash between them leaves inconsistent page. Entry #27. |
| 7 | **Per-database catalog namespace** | Catalog | Single shared `map[string]*Table`; DBOid hardcoded to DefaultDBOid/5. Entries #245–#254. |
| 8 | **Catalog coverage (13/64)** | Catalog | Many `pg_catalog` tables exist only as empty placeholder pages. PG queries against missing catalogs fail. |
| 9 | **`pg_tablespace` heap visibility** | Catalog | Runtime registry only; no on-disk heap rows for PG standby. Entry #45. |
| 10 | **ANALYZE statistics — partial persistence** | Statistics | `pg_statistic` heap rows ARE written by `persistStatsToPGStatistic()` and restored by `loadStatisticsFromHeap()` at startup. Gaps: (a) in-memory `Stats` fields are per-connection — other connections don't see fresh ANALYZE results until restart or heap re-read; (b) `pg_stat/pgstat.stat` cumulative stats file is never created → `pg_stat_*` views return zeros. |

### 🔵 NON-BLOCKERS (empty directories / features not implemented)

| # | Gap | Category | Detail |
|---|---|---|---|
| 11 | `pg_twophase/` empty | 2PC | PREPARE TRANSACTION not implemented. Empty dir = no prepared xacts. |
| 12 | `pg_notify/`, `pg_serial/`, `pg_logical/`, `pg_commit_ts/` empty | Various | Features not implemented. Empty SLRU dirs are valid when features disabled. |
| 13 | `pg_stat/`, `pg_stat_tmp/` empty | Statistics | PG rebuilds cumulative stats at startup (separate from `pg_statistic` catalog gap #10). |
| 14 | goopg-specific extra files | Misc | `global/system_identifier`, `pg_goopg_catalog_cache.json`, `pg_fsm_state.bin`, `pg_vm_state.bin` — PG ignores unrecognized files. |
| 15 | `pg_tblspc/` in-place vs symlink | Tablespaces | goopg uses real directories; PG expects symlinks. Future concern. |

---

## 12. What Actually Works (Current PG-Compat Regions)

For balance, here is what goopg **already** produces in PG 18.3-compatible format:

| Artifact | Format | Validated By |
|---|---|---|
| `PG_VERSION` | `"18\n"` | `ValidatePgVersion()` |
| `global/pg_control` | 8192 bytes, 296-byte payload, all fields at PG18 offsets, CRC32C correct | `docs/design/0107-0001` §1 |
| `global/pg_internal.init` | Relcache init file, magic `0x573266`, `RelationData` + `Form_pg_class` + `Form_pg_attribute[]` | PG backends can `load_relcache_init_file` |
| `base/<dbOid>/pg_internal.init` | Per-DB relcache init file (copy of base/1/) | Same as above |
| Heap pages | `PageHeaderData` (24B), `ItemIdData` (4B), `HeapTupleHeader` (23B + bitmap), varlena 1B/4B LE | `docs/design/0107-0001` §1 |
| B-tree pages | `BTMetaPageData` (magic `0x053162`, v4), `BTPageOpaqueData`, `IndexTupleData` | `docs/design/0107-0001` §1 |
| `pg_xact/` (CLOG) | 2-bit-per-XID SLRU, segment naming `%04X`, page/segment sizes match | `StartupCLOG()` |
| `pg_subtrans/` | 4-byte parent XID per slot SLRU | `StartupSUBTRANS()` |
| `pg_multixact/` | Members + offsets SLRU placeholders | Valid zero-page SLRU format |
| WAL segment naming | `%08X%08X%08X`, 16 MiB segments | PG `XLogFileNameById` |
| WAL page headers | `XLogLongPageHeaderData` (40B), magic `0xD118`, sysid, seg_size, TLI | `pg_waldump` accepts |
| `XLOG_CHECKPOINT_SHUTDOWN` | 88-byte `CheckPoint` body, all fields at PG18 offsets | PG recovery accepts |
| Per-relation main fork | `<relfilenode>` naming, `base/<dbOid>/` or `global/` location | `GetRelationPath()` compatible |
| Catalog boot rows | `Form_pg_class` (34 cols), `Form_pg_attribute` (25 cols), `Form_pg_proc` (30 cols) PG byte-equivalent | `docs/design/0107-0001` §3 |
| Segmented relations | Per `relseg_size` = 131072 blocks = 1 GiB | PG reads `.1`, `.2` segment suffixes |

---

## 13. References

### Deferral Ledger Entries
- **#27** — WAL atomicity gap (heap + WAL split writes)
- **#29** — xl_prev 1-based vs 0-based
- **#32** — CLOG in-memory architecture (Part B/C deferred)
- **#45** — Tablespace foundation (version subdir deferred)
- **#50** — Schema DDL durability gap (pg_namespace)
- **#245–#254** — Per-database catalog namespace (multi-loop epic)
- **#310** — Default GUCs (stubs only)
- **#314–#324** — pg_stat_* views (honest-0 values)
- **#389–#393** — Collation, FDW, foreign server registries (in-memory only)
- **#395–#403** — B4 series (shared catalog heap journaling)
- **#404** — ADD COLUMN durability gap
- **0009-readstream** — `effective_io_concurrency` GUC deferred, ReadStream unwired

### Design Documents
- `docs/design/0107-0001-m0106-pg-compat-invariants.md` — Master catalog of PG-compatible byte layouts
- `docs/design/0014-0001-xlog-page-and-segment-layout-compat.md` — WAL page/segment format
- `docs/design/0014-0003-recovery-streaming-and-compat-validation.md` — Recovery/streaming
- `docs/design/0101-0001-wal-page-header-compat-default.md` — WAL page header default
- `docs/design/0095-0003-in-place-tablespace.md` — In-place tablespace
- `docs/design/0122-0017-database-ddl-drop-guards.md` — Database DDL guards
- `docs/design/0122-0018-per-database-catalog-namespace.md` — Per-DB catalog namespace
- `docs/parity-dashboard.md` — 13/64 pg_catalog tables implemented

### Key Source Files
- `internal/initdb/pgcontrol.go` — `buildPgControl()` — pg_control field emitter
- `internal/control/pgcontrol.go` — Runtime `ControlFileData` struct + `UpdateControlFile()`
- `internal/initdb/initdb.go` — `Subdirs`, `SampleFiles`, `Init()`, bootstrap functions
- `internal/initdb/open.go` — Startup wiring, `Open()`, `SaveFSM()`, `SaveVM()`
- `internal/storage/smgr.go` — `relDir()`, `relPath()`, `Manager.PrefetchBlock()`
- `internal/storage/fsm.go` — Custom FSM aggregate format
- `internal/storage/vm.go` — Custom VM aggregate format
- `internal/wal/format.go` — WAL record encoding, `XLogRecord`, rmgr dispatch
- `internal/initdb/relcache_init.go` — `pg_internal.init` writer
- `internal/initdb/wal_bootstrap.go` — `WriteBootstrapWAL()`
- `internal/mvcc/clog.go`, `clog_bufferpool.go` — CLOG SLRU implementation

### PG 18.3 Source References
- `postgres/src/include/catalog/pg_control.h` — `ControlFileData`, `CheckPoint` structs
- `postgres/src/common/relpath.c` — `GetRelationPath()`, fork name mapping
- `postgres/src/bin/initdb/initdb.c` — `subdirs[]`, `setup_config()`, `bootstrap_template1()`
- `postgres/src/include/access/xlog_internal.h` — `XLogLongPageHeaderData`
- `postgres/src/include/access/xlogrecord.h` — `XLogRecord` struct
- `postgres/src/backend/access/transam/clog.c` — CLOG SLRU format
- `postgres/src/backend/utils/init/miscinit.c` — `ValidatePgVersion()`

### Memories
- `goopg_analyze_per_connection_stats` — ANALYZE stats are per-connection and lost on reconnect
- `goopg_catalog_ddl_durability_two_mechanisms` — Two-mechanism catalog persistence architecture
- `goopg_pg_class_virtual_pg_attribute_heap` — pg_class virtual / pg_attribute heap distinction
- `per_connection_virtual_catalog_scoping` — Per-DB virtual catalog scoping
