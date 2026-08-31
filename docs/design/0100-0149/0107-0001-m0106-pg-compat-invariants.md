# 0107-0001 — M0105/M0106 PG-Compatibility Invariants (DO-NOT-BREAK Catalog)

**Status:** accepted
**Date:** 2026-05-20
**Supersedes:** none (new)
**Companion milestone:** [M0107 — Performance Optimization Refactor](../../milestones/0107-performance-optimization-refactor.md)

## Purpose

M0105 (heap page / tuple format parity) and M0106 (relcache init file / pg_control / catalog compatibility) intentionally locked the on-disk and in-memory byte layouts of a number of goopg artefacts to PostgreSQL 18's exact shape so that a vanilla, unmodified PG18 standby can attach to a goopg primary via `pg_basebackup`, start, replay WAL, and serve reads.

M0107 ("Performance Optimization Refactor") rewrites the executor, MVCC manager, activity registry, buffer pool, WAL inserter, and several allocator/runtime sites. **Every M0107 sub-milestone must keep the artefacts listed below byte-identical to upstream PG18.** This document is the single catalog every M0107 sub-milestone's DoD points to; the regression-test column gives the test that will fail loudest if a change crosses the line.

The companion perf-optimize design series already states this constraint at a high level (`docs/design/perf-optimize/00-overview.md` §2.1: "PG on-disk format compatibility is non-negotiable" and `09-migration-and-rollout.md` §10 reviewer-subagent prompt: "Anything that breaks the PG on-disk format invariants"). This doc enumerates the artefacts so each phase's reviewer has a concrete checklist.

Reference policy: per `.ralph/AGENT.md` §"Vanilla PG Compatibility (ABSOLUTE)", goopg may NEVER change PG's behavior to accommodate goopg. All listed artefacts therefore have exactly one canonical layout — PG18's.

---

## 1. On-disk file formats

| Artefact | Locked properties | Upstream reference | Regression test |
|---|---|---|---|
| `global/pg_control` | Exactly 8192 B; ControlFileData fields at PG18 x86_64 offsets (system_identifier@0, pg_control_version@8, catalog_version_no@12, state@16, checkPointCopy@40 [88 B], CRC32C@292); 7896 B zero-pad. | `postgres/src/include/catalog/pg_control.h` | `internal/control/...` byte-layout tests; `TestE2E_FailoverGoopgToPG/async` |
| `pg_wal/000000010000000000000001` (first segment) | 40-B `XLogLongPageHeaderData` (magic `0xD118`, sysid, seg_size) + `XLOG_CHECKPOINT_SHUTDOWN` record carrying an 88-B `CheckPoint` body; PG18 record framing/CRC. | `postgres/src/include/access/xlog_internal.h`, `postgres/src/backend/access/transam/xlog.c` | `internal/wal/...` framing tests; `TestE2E_FailoverGoopgToPG/async` |
| `global/pg_internal.init`, `base/<dboid>/pg_internal.init` | 4-B magic `0x573266`; per-record framing of `RelationData` + `Form_pg_class` + `Form_pg_attribute[]` (`ATTRIBUTE_FIXED_PART_SIZE`-aligned) + index sub-records (opfamily/opcintype/support-proc/indcollation/indoption arrays). | `postgres/src/backend/utils/cache/relcache.c::write_relcache_init_file` | `internal/initdb/relcache_init_test.go`; PG backends "could not open critical system index" PANIC if broken |
| Heap pages (`base/<dboid>/<relfilenode>`, `global/<relfilenode>`) | PageHeaderData layout (`pd_lsn`, `pd_checksum`, `pd_flags`, `pd_lower`, `pd_upper`, `pd_special`, `pd_pagesize_version`, `pd_prune_xid`); ItemIdData 32-bit bitfield `lp_off:15, lp_flags:2, lp_len:15`; HeapTupleHeader bytes (`t_xmin`, `t_xmax`, `t_field3`, `t_ctid`, `t_infomask2`, `t_infomask`, `t_hoff`); null bitmap shift via `BITMAPLEN`; varlena 1-B and 4-B LE encoding; `HEAP_HASVARWIDTH` infomask bit. | `postgres/src/include/storage/bufpage.h`, `itemid.h`, `htup_details.h`, `postgres.h` (varlena) | `internal/access/heap/...` tests; PG-standby segfault if broken |
| B-tree index pages | Block 0 = `BTMetaPageData` (magic `0x053162`, version 4); `BTPageOpaqueData` in special area; `IndexTupleData` key encoding; HIKEY / firstright ordering. | `postgres/src/include/access/nbtree.h` | `internal/access/btree/...` tests; `load_critical_index` PANIC if broken |
| `PG_VERSION` | Three bytes exactly: `0x31 0x38 0x0A` ("18\n"). | `postgres/src/backend/utils/init/miscinit.c::ValidatePgVersion` | initdb sanity test |
| `pg_xact/` (CLOG) | Two-bit-per-xid layout with `TransactionIdGetStatus` semantics; segment file name + slot bit positions match PG. | `postgres/src/backend/access/transam/clog.c` | `internal/mvcc/clog_test.go`; standby cannot replay XACT records if broken |
| `pg_subtrans/` | Sub-transaction parent xid array; segment file name pattern; in-memory cache key. | `postgres/src/backend/access/transam/subtrans.c` | recovery tests |
| `pg_multixact/{members,offsets}/` | MultiXact member + offset SLRU page format. | `postgres/src/backend/access/transam/multixact.c` | M0083 tests |
| Per-relation VM (`<relfilenode>_vm`) | One bit per page (or two for VM v2) at PG-defined offset; `HEAP_XLOG_VISIBLE` record drives updates. | `postgres/src/backend/access/heap/visibilitymap.c` | M0080/M0082 tests |
| Per-relation FSM (`<relfilenode>_fsm`) | Three-level B-tree-of-bytes per `freespace.c`. | `postgres/src/backend/storage/freespace/freespace.c` | M0080/M0082 tests |

## 2. Byte-compatible Go struct layouts

These Go structs are intentionally byte-equivalent to upstream C structs on x86_64 Linux. Any rename / reorder / padding change is a regression.

| Go struct | Source file | Mirrors C struct | Key sizes |
|---|---|---|---|
| `ControlFileData` | `internal/control/pgcontrol.go`, `internal/initdb/pgcontrol.go` | `postgres/src/include/catalog/pg_control.h::ControlFileData` | 296 B active payload + CRC@292; total record 8192 B |
| `Form_pg_class` (34 columns) | `internal/initdb/initdb.go` (`pgClassColDefs`), `internal/executor/codec.go` | `postgres/src/include/catalog/pg_class.h::FormData_pg_class` | Column order + types fixed by PG18 |
| `Form_pg_attribute` (25 columns) | `internal/initdb/initdb.go` (`pgAttrEntriesForRel`), `internal/initdb/relcache_init.go` | `postgres/src/include/catalog/pg_attribute.h::FormData_pg_attribute` | First 20 cols form the `ATTRIBUTE_FIXED_PART_SIZE` block |
| `RelationData` (relcache entry) | `internal/initdb/relcache_init.go` | `postgres/src/include/utils/rel.h::RelationData` | `sizeof` extracted from PG18 DWARF; `rd_id`, `rd_node`, `rd_rel`, `rd_att` offsets fixed |
| `CheckPoint` (XLOG body) | `internal/wal/...` | `postgres/src/include/catalog/pg_control.h::CheckPoint` | 88 B |
| `HeapTupleHeaderData` | `internal/access/heap/...` | `postgres/src/include/access/htup_details.h::HeapTupleHeaderData` | 23 B header + bitmap |
| `ItemIdData` | `internal/access/heap/...` | `postgres/src/include/storage/itemid.h::ItemIdData` | 32-bit bitfield |
| `PageHeaderData` | `internal/storage/...` | `postgres/src/include/storage/bufpage.h::PageHeaderData` | 24 B |
| `XLogPageHeaderData` / `XLogLongPageHeaderData` | `internal/wal/...` | `postgres/src/include/access/xlog_internal.h` | 24 B / 40 B |
| `BTMetaPageData` | `internal/access/btree/...` | `postgres/src/include/access/nbtree.h::BTMetaPageData` | per PG18 |
| `BTPageOpaqueData` | `internal/access/btree/...` | `postgres/src/include/access/nbtree.h::BTPageOpaqueData` | per PG18 |

## 3. Catalog heap-tuple row layouts

For all of the nailed catalogs and their indexes, the row bytes must decode under PG18's tuple parser.

| Catalog | Locked row format | Notes |
|---|---|---|
| `pg_class` | 34-column `Form_pg_class` row; varlena fields (`relacl` aclitem[], `reloptions` text[], `relpartbound` pg_node_tree) use binary `ArrayType` encoding (not JSON). | `internal/executor/codec.go` is the byte-emitter. |
| `pg_attribute` | 25-column `Form_pg_attribute` row; `attlen`, `attalign`, `attnotnull`, `attstattarget` exactly per PG18; `nocachegetattr` macro and `VARATT_IS_1B_E` checks must hold on the bytes. | |
| `pg_proc` | 30-column form; `proargtypes` as `oidvector` (4-B varlena header + 20-B ArrayType header + 4N-B OID payload); `prosrc` varlena. | AM handler rows: heap_tableam_handler=3, bthandler=330, … |
| `pg_type` | Per PG18 schema; entries for the nailed types must be present and byte-correct. | |
| `pg_index` | Per PG18; `indkey`, `indcollation`, `indoption` arrays encoded as PG oidvector/int2vector. | |
| `pg_opclass`, `pg_am`, `pg_amop`, `pg_amproc` | Per PG18; opclass/opfamily/operator/support-proc OIDs must be the PG18-canonical numeric values. | |
| `pg_rewrite`, `pg_trigger` | Per PG18; varlena `pg_node_tree` columns. | |
| Shared catalogs (`pg_database`, `pg_authid`, `pg_auth_members`, `pg_shseclabel`, `pg_subscription`) | Per PG18; live under `global/`. | |

## 4. Critical B-tree indexes (must be present + byte-correct)

Local (per-DB) critical indexes — needed for `load_critical_index` at PG backend startup:

- `pg_class_oid_index` (OID 2662)
- `pg_class_relname_nsp_index` (OID 2663)
- `pg_attribute_relid_attnam_index` (OID 2658)
- `pg_attribute_relid_attnum_index` (OID 2659)
- `pg_type_oid_index` (OID 2703)
- `pg_index_indexrelid_index` (OID 2679)
- `pg_opclass_oid_index` (OID 2687)

Shared critical indexes (under `global/`):

- `pg_database_oid_index` (OID 2672)
- `pg_authid_oid_index` (OID 2828)
- `pg_authid_rolname_index` (OID 2676)
- `pg_auth_members_role_member_index` (OID 2694)
- `pg_auth_members_member_role_index` (OID 2695)
- `pg_replication_origin_roiident_index` (OID 6001 — when wired)

Leaf-page byte layout, key-tuple ordering (HIKEY / firstright), and opclass alignment must match PG18.

## 5. WAL record formats

Goopg writes PG-canonical WAL so a PG18 standby can replay it.

| Record | Locked properties | Source |
|---|---|---|
| `XLOG_CHECKPOINT_SHUTDOWN` / `XLOG_CHECKPOINT_ONLINE` | 88-B `CheckPoint` body (redo, ThisTimeLineID, PrevTimeLineID, fullPageWrites, wal_level, nextXid [FullTransactionId u64], nextOid, nextMulti, oldestMulti, …). | `postgres/src/backend/access/transam/xlog.c::CreateCheckPoint` |
| `XLOG_HEAP_INSERT`, `XLOG_HEAP_UPDATE`, `XLOG_HEAP_DELETE` | Canonical heap mutation records — block reference frames + per-record offset/info fields per PG18. | `postgres/src/backend/access/heap/heapam.c` |
| `XLOG_HEAP2_MULTI_INSERT`, `XLOG_HEAP2_LOCK_UPDATED`, `XLOG_HEAP2_VISIBLE`, `XLOG_HEAP2_FREEZE_PAGE`, `XLOG_HEAP_HOT_UPDATE` | Per PG18. | same |
| `XLOG_XACT_COMMIT` / `XLOG_XACT_ABORT` | Per PG18; required so standby's mvcc state advances. | `postgres/src/backend/access/transam/xact.c` |
| `XLOG_BTREE_INSERT_*`, `XLOG_BTREE_SPLIT_*`, `XLOG_BTREE_NEWROOT`, `XLOG_BTREE_MARK_PAGE_HALFDEAD`, `XLOG_BTREE_UNLINK_PAGE`, `XLOG_BTREE_REUSE_PAGE`, `XLOG_BTREE_META_CLEANUP`, `XLOG_BTREE_VACUUM` | Per PG18. | `postgres/src/backend/access/nbtree/nbtxlog.c` |

The byte-emitter for WAL is `internal/wal/`. M0107 may change *how* a record is staged (e.g., per-stripe insert locks per `perf-optimize/07-wal-fsm-insert.md`), the FSM consultation pattern, or the buffer-pool path that produces the FPI — but the resulting bytes on disk must remain identical.

## 6. Per-phase risk callouts for M0107

These callouts make the constraint concrete for each M0107 sub-milestone. Reviewers verify these as part of the phase's DoD.

**M0107-0001 (Phase A — `mctx`)**: only changes in-process allocator shape. The byte-emitter sites (`internal/executor/codec.go`, `internal/initdb/relcache_init.go`, `internal/wal/...`) must not be moved, renamed, or have their output bytes changed. `internal/mctx` provides a pool that those sites can borrow scratch space from; their per-byte output stays identical.

**M0107-0002 (Phase B — pointer-free Datum)**: changes `Datum` from 64 B (3 GC-traced fields) to 24 B (no GC fields). Wire format is unchanged (`perf-optimize/02-datum-pointer-free.md` §"Wire format unchanged"). Crucially: when a `Datum` is *written* into a heap tuple via `internal/executor/codec.go`, the emitted bytes are by varlena/integer/numeric rules — those rules and their output bytes must be byte-identical pre- and post-Phase B. Add a goldens test pinning the byte output for each scalar type if one doesn't already exist.

**M0107-0003 (Phase C — concrete-type executor)**: replaces `Operator`/`TupleSlot` interfaces with concrete sum-types. Pure in-memory refactor. The hot-write call chain (insertOp / updateOp / deleteOp) still emits identical WAL bytes and identical heap-page mutations.

**M0107-0004 (Phase D1 — ProcArray + XidGen + CLOG bank locks)**: replaces `Manager.mu` with per-slot atomics and per-bank `RWMutex` over CLOG pages. The CLOG on-disk page format (2 bits per xid, segment naming, slot bit positions) is unchanged — only the *in-memory* bank lock geometry changes. PG18 standby replay must still see byte-identical XACT_COMMIT / XACT_ABORT records.

**M0107-0005 (Phase D2 — per-backend activity)**: pure in-memory; `pg_stat_activity` is a runtime view, not on-disk state. No on-disk effect.

**M0107-0006 (Phase D3 — lock-free bufpool)**: replaces 128-partition mutex with lock-free open-addressing hash. The page bytes the bufpool serves are unchanged; only the lookup/eviction protocol changes. PG-compat heap, btree, VM, FSM page bytes remain the byte-emitter's output, never mutated by the bufpool itself.

**M0107-0007 (Phase D4 — WAL stripe locks + FSM-distributed inserts)**: highest-risk for byte regression. Replaces single `appendMu` with 8 stripes and changes tail-page selection to consult FSM + bufmap. The WAL record framing, CRC, page-header layout, and per-record block-reference frames must remain byte-identical to PG18. The FSM-driven page selection changes *which* page receives an insert, but the on-page heap-tuple bytes still go through `internal/access/heap/...` and the WAL record still goes through `internal/wal/...` — both byte-emitters must remain unchanged. Add an integration test that diffs pre/post-Phase-D4 WAL segment bytes for a fixed pgbench workload (modulo timestamps).

**M0107-0008 (Phase D5 — runtime internals via `//go:linkname`)**: linkname targets (`nanotime`, `PinP`/`UnpinP`, `semacquire`/`semrelease`) only touch goroutine scheduling and timing. No on-disk effect.

## 7. Regression gate every M0107 sub-milestone must pass

For each M0107 sub-milestone, the DoD includes:

1. **PG-standby attach E2E:** `TestE2E_FailoverGoopgToPG/async` PASS.
2. **PG-side TAP suite for the affected subsystem** still PASS (the sub-milestone reviewer picks the relevant subset from `docs/test-port/postgres-oracle-port-status.csv`).
3. **Byte-layout regression tests** in `internal/initdb/...`, `internal/control/...`, `internal/wal/...`, `internal/access/heap/...`, `internal/access/btree/...` all PASS.
4. **No new entry under** "On-disk file formats" / "Byte-compatible Go struct layouts" / "Catalog heap-tuple row layouts" / "WAL record formats" tables **above is silently modified**. Any deliberate change requires a follow-up milestone (M01xx-NNNN) that updates this catalog in the same PR.

`make ralph-state-guard` keeps the fix_plan + design index + milestone index consistent; it is the operational hook for catching missing index updates.

## 8. Source-of-truth files (read-only audit trail for M0107 reviewers)

These files are the byte-emitters. M0107 phases may call into them, but must not change their output bytes:

- `internal/control/pgcontrol.go` — ControlFileData encode + CRC32C
- `internal/initdb/pgcontrol.go` — initdb's pg_control writer
- `internal/initdb/relcache_init.go` — RelationData / Form_pg_class / Form_pg_attribute encoder
- `internal/initdb/initdb.go` — bootstrap entry; catalog seed; pg_class/pg_attribute heap rows
- `internal/executor/codec.go` — varlena / ArrayType encoders (aclitem[], text[], oidvector, pg_node_tree)
- `internal/wal/` — all WAL record encoders (CheckPoint, heap, btree, xact)
- `internal/access/heap/` — HeapTupleHeader, ItemId, page header
- `internal/access/btree/` — BTMetaPageData, BTPageOpaqueData, IndexTuple
- `internal/storage/` — PageHeaderData, page-checksum

If a M0107 phase needs to *touch* one of these (e.g., to change its caller, factor out an interior helper, or feed it from `mctx`), the phase's PR must include a goldens test that pins the byte output before AND after the change.
