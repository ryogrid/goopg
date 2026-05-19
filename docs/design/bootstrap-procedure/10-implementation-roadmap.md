# 10 — Implementation Roadmap

**Status:** draft
**Date:** 2026-05-19
**Milestone:** M0106-0010 (vanilla PG18 standby attach against goopg primary)
**Audience:** Claude Code executing one task per Ralph loop.

---

## Scope

This file is the **batched implementation plan** that closes M0106-0010
without further reactive `0106-0010-step3*` iterations. It aggregates the
`partial` / `missing` rows from the "What goopg must produce" sections of
docs [`01-`](01-data-directory-layout.md) … [`09-`](09-streaming-replication-readiness.md)
and [`11-`](11-continuous-maintenance.md) into an ordered task list.

**In scope.**

- An ordered, ~30-task list keyed to concrete Go files under
  `internal/`. Each row names the spec doc that defines correctness,
  the originating `0106-0010-step3*` doc(s) the task supersedes, the
  test that proves the change, and the risk-based gate per
  `.ralph/PROMPT.md`.
- Acceptance criteria gated on `TestE2E_FailoverGoopgToPG/async`.
- A "What this plan does NOT cover" pointer set for the adjacent
  milestones (M0102, M0103, 0102-0002 / -0004, 0030 / 0079 / 0080).
- A roll-up of the 116 superseded `0106-0010-step3*` design docs.

**Out of scope.**

- Per-artefact upstream byte-level detail — see [`01-`](01-data-directory-layout.md)
  through [`09-`](09-streaming-replication-readiness.md).
- Cross-cutting per-operation matrix — see [`11-continuous-maintenance.md`](11-continuous-maintenance.md).

---

## Status snapshot

One row per spec doc. Counts are from each file's "What goopg must
produce" section; "Biggest single gap" is the load-bearing one.

| Doc | `done` | `partial` | `missing` | Biggest single gap |
|---|---:|---:|---:|---|
| [`01-data-directory-layout.md`](01-data-directory-layout.md) | 14 | 2 | 8 | `base/4/` (template0) directory + `PG_VERSION` |
| [`02-pg-control-and-checkpoint.md`](02-pg-control-and-checkpoint.md) | 19 | 8 | 11 | `checkPointCopy` substructure entirely zero |
| [`03-wal-bootstrap-segment.md`](03-wal-bootstrap-segment.md) | 3 | 4 | 5 | First WAL segment `pg_wal/000000010000000000000001` never written |
| [`04-shared-catalog-bootstrap.md`](04-shared-catalog-bootstrap.md) | 3 | 4 | 8 | 16 predefined-role rows in `pg_authid` + 2 `pg_tablespace` rows |
| [`05-local-catalog-bootstrap.md`](05-local-catalog-bootstrap.md) | 2 | 7 | 3 | `pg_proc`: 7 of 3397 rows seeded |
| [`06-bki-derived-catalog-seeds.md`](06-bki-derived-catalog-seeds.md) | 2 | 4 | 10 | `pg_operator` 799 rows, `pg_amop` 860 of 945 rows |
| [`07-system-views-and-pg-rewrite.md`](07-system-views-and-pg-rewrite.md) | 0 | 1 | 6 | 5 of 6 replication views absent; `_RETURN` rules absent |
| [`08-relcache-init-and-version-files.md`](08-relcache-init-and-version-files.md) | 2 | 3 | 3 | `pg_internal.init` index sub-record never emitted |
| [`09-streaming-replication-readiness.md`](09-streaming-replication-readiness.md) | 5 | 4 | 4 | `pg_replslot/*/state` written as JSON, not PG-binary |
| [`11-continuous-maintenance.md`](11-continuous-maintenance.md) | 0 | 4 | 12 | DDL handlers in `internal/executor/` do not emit canonical heap/index WAL |

Totals: **`done` 50, `partial` 41, `missing` 70.** Roughly 70% of the
remaining work is initdb-time seeding; the remainder is runtime
continuous-maintenance plumbing.

---

## Ordering principles

1. **Bottom-up dependency chain.** ControlFile → WAL bootstrap → shared
   catalogs (`global/`) → local catalogs (`base/<dboid>/`) → BKI-derived
   seeds → system views & pg_rewrite → relcache init file → streaming
   replication readiness → continuous-maintenance handlers. A standby
   that FATALs at `ReadControlFile` never reaches the catalog gaps; a
   standby that passes `RelationCacheInitializePhase2` but FATALs in
   Phase 3 never reaches the view gaps. Fix earlier blockers first.
2. **Initdb-time before runtime for the same artefact.** Every spec doc
   pairs an initdb-time inventory with a continuous-maintenance rule.
   Seed the baseline first (deterministic, no concurrency); add the
   runtime mutation rule (WAL emission, SI invalidation, init-file
   unlink) only after the baseline is byte-correct. Otherwise the
   runtime mutation rebases on a wrong baseline and amplifies it.
3. **Package locality.** Group adjacent tasks by package so the
   implementer stays in one area at a time:
   `internal/initdb/` → `internal/wal/` → `internal/server/` →
   `internal/catalog/` → `internal/executor/`. Cross-package tasks
   (e.g. control-file mutation called from checkpointer + basebackup +
   promotion) land last in each package so the helper they call is
   already stable.
4. **One task per Ralph loop.** Each row below names a single Go
   file change small enough to verify in one loop, per
   `.ralph/PROMPT.md` rule "TASKS_COMPLETED_THIS_LOOP must be 0 or 1".
5. **Risk gates dominate over speed.** Every WAL / lock / replication
   /concurrency change runs the matching unit suite plus a
   race-focused test before the loop completes
   (`.ralph/PROMPT.md` §"Required risk-based gates").

---

## Task list

Spec-doc paths are relative to `docs/design/`. Step-3* originating
docs are relative to `docs/design/`. "Risk gate" is the
`.ralph/PROMPT.md` category applied to that change.

| # | Task | Files (`internal/`) | Spec doc | Originating step-3* doc | Test | Risk gate |
|---:|---|---|---|---|---|---|
|  1 | Fill `checkPointCopy` substructure (redo, TLI×2, nextXid=3, nextOid=10000, nextMulti=1, oldestXid=3, oldestXidDB=1, oldestMulti=1, oldestMultiDB=1, fullPageWrites, wal_level, time) in `buildPgControl`. | `initdb/pgcontrol.go` | `bootstrap-procedure/02-pg-control-and-checkpoint.md` | `0106-0010-step3*` (none — net-new from spec aggregation) | `initdb/pg_control_test.go` (new): assert offsets 32..127 match expected. | wal/replication |
|  2 | Set `unloggedLSN = FirstNormalUnloggedLSN = 1000` and pipe live GUCs (`MaxConnections`, `max_worker_processes`, `max_wal_senders`, `max_prepared_xacts`, `max_locks_per_xact`, `wal_level`) into ControlFile from `internal/config` instead of hard-coded constants. | `initdb/pgcontrol.go` | `bootstrap-procedure/02-pg-control-and-checkpoint.md`, `09-streaming-replication-readiness.md` | — | `initdb/pg_control_test.go` extended. | wal/replication |
|  3 | Add `updateControlFile(dataDir, fn func(*ControlFileData)) error` helper; thread it through `wal/checkpointer.go::runCheckpoint`, `server/basebackup.go::handleBaseBackup`, and the (future) promotion path. | `initdb/pgcontrol.go` (move to `control/pgcontrol.go`), `wal/checkpointer.go`, `server/basebackup.go` | `bootstrap-procedure/02-pg-control-and-checkpoint.md` §"Recommended Go API" | — | `internal/control/control_test.go` extended; `wal/checkpointer_test.go`. | wal/replication |
|  4 | Add `WriteBootstrapWAL(dataDir, sysID, now) error` writing `pg_wal/000000010000000000000001`: 40-byte long page header + `XLOG_CHECKPOINT_SHUTDOWN` record (114 B total), zero-pad to `wal_segment_size`, `fsync` before `writePgControl`. | `initdb/wal_bootstrap.go` (new), `initdb/initdb.go` | `bootstrap-procedure/03-wal-bootstrap-segment.md` | — | `initdb/wal_bootstrap_test.go` (new): byte-diff vs vanilla `pg_basebackup` first segment. | wal/replication |
|  5 | Add `excludeFiles` and `excludeDirContents` tables and table-driven exclusion in basebackup; ship the 11 missing entries (`pg_internal.init*` prefix, `backup_label`, `tablespace_map`, `backup_manifest`, `postgresql.auto.conf.tmp`, `current_logfiles.tmp`, plus 6 dir-contents) and demote the inline `pg_replslot` prefix-strip. | `server/basebackup.go` | `bootstrap-procedure/09-streaming-replication-readiness.md` | — | `server/basebackup_test.go` extended; `internal/testport/e2e_failover_*` smoke. | wal/replication |
|  6 | Bind the `XLOG_PARAMETER_CHANGE` redo path on the standby side to `updateControlFile` so a goopg standby imprints replayed GUC echoes. | `wal/recovery.go`, `control/pgcontrol.go` | `bootstrap-procedure/02-pg-control-and-checkpoint.md`, `09-streaming-replication-readiness.md` | — | `wal/recovery_test.go` extended (table-driven `XLOG_PARAMETER_CHANGE` replay). | wal/replication |
|  7 | Replace JSON slot file writer with PG-binary `state` (magic `0x1051CA1`, version 5, CRC32C, `ReplicationSlotPersistentData`); keep `state.tmp` + atomic `rename` + parent-dir `fsync`. | `wal/slots_pg.go` (new), `wal/slots.go` | `bootstrap-procedure/09-streaming-replication-readiness.md` | — | `wal/slots_test.go` extended: CRC self-check, magic+version assertion, round-trip. | wal/replication |
|  8 | Add `createPerDatabaseScaffolding(dboid, name)` writing `base/<dboid>/` directory and `base/<dboid>/PG_VERSION = "18\n"`; emit for OIDs 1 (template1), 4 (template0), 5 (postgres). | `initdb/initdb.go` | `bootstrap-procedure/01-data-directory-layout.md`, `08-relcache-init-and-version-files.md` | — | `initdb/initdb_test.go` extended: `base/{1,4,5}/PG_VERSION` exist with `"18\n"`. | parser/planner/executor |
|  9 | Write `postgresql.auto.conf` two-line `ALTER SYSTEM` header at initdb. | `initdb/initdb.go` (extend `SampleFiles`) | `bootstrap-procedure/01-data-directory-layout.md` | — | `initdb/initdb_test.go`. | parser/planner/executor |
| 10 | Seed `pg_database` OID 4 (`template0`, `datistemplate=true`, `datallowconn=false`) plus its leaf entries in `pg_database_oid_index` (2672) and `pg_database_datname_index` (2671). | `initdb/initdb.go::bootstrapPostgresDatabase`, `initdb/btree_index_bootstrap.go` | `bootstrap-procedure/04-shared-catalog-bootstrap.md`, `08-relcache-init-and-version-files.md` | `0106-0010-step3cs-pg-database-oid-index-populated.md`, `0106-0010-step3ct-pg-database-pg18-row-layout.md`, `0106-0010-step3dh-pg-database-datname-index.md` | `initdb/pg_database_*_test.go`. | parser/planner/executor |
| 11 | Seed 16 predefined `pg_authid` rows (`pg_database_owner`, `pg_read_all_data`, … `pg_signal_autovacuum_worker`); rewrite `rolpassword`/`rolvaliduntil` as NULL; set `HEAP_XMIN_FROZEN`. Update `pg_authid_oid_index` (2677) and `pg_authid_rolname_index` (2676) leaves. | `initdb/initdb.go::bootstrapPostgresRole`, `initdb/btree_index_bootstrap.go::bootstrapPgAuthidIndexes` | `bootstrap-procedure/04-shared-catalog-bootstrap.md` | `0106-0010-step3cx-pg-authid-os-user-and-indexes.md`, `0106-0010-step3de-pg-authid-heap-rolname-byte-layout.md`, `0106-0010-step3dg-pg-authid-rolname-index-name-typed-descriptor.md` | `initdb/pg_authid_heap_row_test.go`, `pg_authid_indexes_test.go`. | parser/planner/executor |
| 12 | Seed 2 default `pg_tablespace` rows (1663 `pg_default`, 1664 `pg_global`) and update `pg_tablespace_oid_index` (2697) and `pg_tablespace_spcname_index` (2698) leaves. | `initdb/sharedcatalog.go` (new), `initdb/btree_index_bootstrap.go` | `bootstrap-procedure/04-shared-catalog-bootstrap.md` | `0106-0010-step3ch-pg-tablespace-nailed-rel.md`, `0106-0010-step3cr-pg-class-reltablespace-shared.md` | `initdb/pg_class_reltablespace_test.go` extended; new `pg_tablespace_heap_test.go`. | parser/planner/executor |
| 13 | Wire `pg_auth_members_oid_index` (6303) and `pg_auth_members_grantor_index` (6302) into the critical-shared-index loop so the empty placeholders match vanilla. | `initdb/btree_index_bootstrap.go` | `bootstrap-procedure/04-shared-catalog-bootstrap.md` | `0106-0010-step3z-pg-auth-members-role-member-index.md` | `initdb/pg_auth_members_*_index_test.go`. | parser/planner/executor |
| 14 | Expand `bootstrapPgProcTuples` from 7 AM-handler rows to the full `pg_proc.dat` row set (~3397 rows); add an embedded `pg_proc.dat`-derived inventory; populate `proallargtypes`/`proargnames`/`proargmodes` arrays with PG18 byte layout. | `initdb/pg_proc_view.go` (extend), `initdb/initdb.go::bootstrapPgProcTuples` | `bootstrap-procedure/05-local-catalog-bootstrap.md`, `06-bki-derived-catalog-seeds.md` | `0106-0010-step3a-pg-proc-bootstrap.md`, `0106-0010-step3da-pg-type-io-regproc-oids.md`, `0106-0010-step3dc-pg-proc-io-regproc-heap-rows.md`, `0106-0010-step3db-pg-proc-oid-index-populated.md` | `initdb/pg_proc_bootstrap_test.go`, `pg_proc_oid_index_test.go`. | parser/planner/executor |
| 15 | Expand `bootstrapPgTypeTuples` from ~25 to ~612 rows (112 base + ~500 derived array / multirange / rowtype); fix `typalign` byte-offset (Step 3cq). | `initdb/pg_type_bootstrap.go`, `initdb/initdb.go` | `bootstrap-procedure/05-local-catalog-bootstrap.md`, `06-bki-derived-catalog-seeds.md` | `0106-0010-step3cq-pg-type-heap-canonical-typalign.md`, `0106-0010-step3cz-pg-type-oid-index-populated.md` | `initdb/pg_attribute_attalign_offset_test.go`, new `pg_type_heap_test.go`. | parser/planner/executor |
| 16 | Seed `pg_operator` (799 rows) heap + indexes (2688, 2689). | `initdb/pg_operator_bootstrap.go` (new), `initdb/btree_index_bootstrap.go` | `bootstrap-procedure/06-bki-derived-catalog-seeds.md` | `0106-0010-step3bl-pg-operator-oprname-l-r-n-index.md` | `initdb/pg_operator_oprname_l_r_n_index_test.go`. | parser/planner/executor |
| 17 | Complete `pg_amop` seed: add cross-type rows for `text_ops` (1994), `datetime_ops` (434), `numeric_ops` (1988), plus hash/gist/gin/spgist/brin tail (target 945 rows). | `initdb/initdb.go::pgAmopInitialEntries` | `bootstrap-procedure/06-bki-derived-catalog-seeds.md` §"Cross-type opfamily rows" | `0106-0010-step3c-pg-amop-amproc-bootstrap.md`, `0106-0010-step3d-pg-amop-amproc-pinned-opfamily-fix.md`, `0106-0010-step3e-pg-amproc-sortsupport-equalimage.md`, `0106-0010-step3h-pg-amop-amproc-crosstype-integer.md`, `0106-0010-step3y-pg-amop-fam-strat-index.md` | `initdb/pg_amop_bootstrap_test.go`, `pg_amop_fam_strat_index_test.go`. | parser/planner/executor |
| 18 | Complete `pg_amproc` seed: cross-type cmp procs for text / datetime / numeric, plus hash/gist/gin support functions (target 714 rows). | `initdb/initdb.go::pgAmprocInitialEntries`, `initdb/btree_index_bootstrap.go` | `bootstrap-procedure/06-bki-derived-catalog-seeds.md` | `0106-0010-step3cw-pg-amproc-fam-proc-index.md`, `0106-0010-step3e-pg-amproc-sortsupport-equalimage.md` | `initdb/pg_amproc_bootstrap_test.go`, `pg_amproc_fam_proc_index_test.go`. | parser/planner/executor |
| 19 | Seed `pg_opclass` (177 rows) heap + index (2687); add `pgOpfamilyInitialEntries()` for `pg_opfamily` (146 rows) heap + indexes (2754, 2755). | `initdb/initdb.go::pgOpclassInitialEntries`, new `pg_opfamily_bootstrap.go` | `bootstrap-procedure/06-bki-derived-catalog-seeds.md` | `0106-0010-step3b-pg-opclass-bootstrap.md`, `0106-0010-step3ad-pg-opclass-am-name-nsp-index.md`, `0106-0010-step3bm-pg-opfamily-nailed-rel.md`, `0106-0010-step3bn-pg-opfamily-am-name-nsp-index.md`, `0106-0010-step3bo-pg-opfamily-oid-index.md`, `0106-0010-step3l-pg-opclass-oid-index-tuples.md` | `initdb/pg_opclass_bootstrap_test.go`, `pg_opfamily_*_test.go`. | parser/planner/executor |
| 20 | Seed `pg_cast` (235 rows) heap + indexes (2660, 2661). | `initdb/pg_cast_bootstrap.go` (new) | `bootstrap-procedure/06-bki-derived-catalog-seeds.md` | `0106-0010-step3aa-pg-cast-nailed-rel.md`, `0106-0010-step3ab-pg-cast-oid-index.md`, `0106-0010-step3ac-pg-cast-source-target-index.md` | `initdb/pg_cast_nailed_test.go`, `pg_cast_*_index_test.go`. | parser/planner/executor |
| 21 | Seed `pg_collation` (7 BKI rows) heap + indexes (3164, 3085). | `initdb/pg_collation_bootstrap.go` (new) | `bootstrap-procedure/06-bki-derived-catalog-seeds.md` | `0106-0010-step3ae-pg-collation-name-enc-nsp-index.md`, `0106-0010-step3af-pg-collation-oid-index.md` | `initdb/pg_collation_*_index_test.go`. | parser/planner/executor |
| 22 | Seed `pg_conversion` (128 rows) heap + indexes (2668, 2669, 2670). | `initdb/pg_conversion_bootstrap.go` (new) | `bootstrap-procedure/06-bki-derived-catalog-seeds.md` | `0106-0010-step3ag-pg-conversion-nailed-rel.md`, `0106-0010-step3ah-pg-conversion-default-index.md`, `0106-0010-step3ai-pg-conversion-oid-index.md`, `0106-0010-step3aj-pg-conversion-name-nsp-index.md` | `initdb/pg_conversion_*_test.go`. | parser/planner/executor |
| 23 | Seed `pg_aggregate` (161 rows) heap + index (2650); ensure each row's `aggfnoid` resolves into the expanded `pg_proc` set from task 14. | `initdb/pg_aggregate_bootstrap.go` (new) | `bootstrap-procedure/06-bki-derived-catalog-seeds.md` | `0106-0010-step3w-pg-aggregate-nailed-rel.md`, `0106-0010-step3x-pg-aggregate-fnoid-index.md` | `initdb/pg_aggregate_*_test.go`. | parser/planner/executor |
| 24 | Seed `pg_range` (6 rows) + the 6 multirange `pg_type` peers; add range `pg_cast` rows; add indexes (3542, 2228). | `initdb/pg_range_bootstrap.go` (new) | `bootstrap-procedure/06-bki-derived-catalog-seeds.md` | `0106-0010-step3bz-pg-range-nailed-rel.md` | `initdb/pg_range_nailed_test.go`. | parser/planner/executor |
| 25 | Seed `pg_language` (3 BKI rows) heap + indexes (2681, 2682). | `initdb/pg_language_bootstrap.go` (new) | `bootstrap-procedure/06-bki-derived-catalog-seeds.md` | `0106-0010-step3bj-pg-language-name-index.md`, `0106-0010-step3bk-pg-language-oid-index.md` | `initdb/pg_language_*_test.go`. | parser/planner/executor |
| 26 | Backfill the residual nailed-rel placeholders surfaced by the step-3a..3cp chain (`pg_default_acl`, `pg_enum`, `pg_event_trigger`, `pg_extension`, `pg_foreign_data_wrapper`, `pg_foreign_server`, `pg_foreign_table`, `pg_parameter_acl`, `pg_partitioned_table`, `pg_publication`, `pg_publication_namespace`, `pg_publication_rel`, `pg_replication_origin`, `pg_sequence`, `pg_statistic`, `pg_statistic_ext`, `pg_statistic_ext_data`, `pg_subscription_rel`, `pg_transform`, `pg_ts_*`, `pg_user_mapping`, `pg_db_role_setting`, `pg_shseclabel` schema fix) — already implemented as empty heap + index placeholders; this task audits the residual gaps and adds whichever placeholder indexes are still missing from `bootstrapMappedLocalCatalogHeaps`. | `initdb/initdb.go`, `initdb/btree_index_bootstrap.go` | `bootstrap-procedure/05-local-catalog-bootstrap.md`, `06-bki-derived-catalog-seeds.md` | All `0106-0010-step3ak..3cp` docs (see "Superseded step-3* docs"). | `initdb/pg_*_nailed_test.go`, `pg_*_oid_index_test.go`. | parser/planner/executor |
| 27 | Seed `pg_proc` rows 3099, 6118, 6169, 6248, 3781 (SRFs backing the remaining 5 replication views) with full PG18 `proallargtypes` / `proargnames` arrays. | `initdb/pg_proc_view.go` | `bootstrap-procedure/07-system-views-and-pg-rewrite.md` | `0106-0010-step3dj-pg-proc-stat-get-wal-receiver.md`, `0106-0010-step3dk-pg-proc-3317-out-args-arrays.md`, `0106-0010-step3di-segv-chain-eliminated-pg-stat-wal-receiver-missing.md`, `0106-0010-step3dd-segv-backtrace-ld-preload.md`, `0106-0010-step3df-segv-backtrace-si-addr-and-registers.md` | `initdb/pg_proc_view_test.go`, `pg_proc_outargs_test.go`. | parser/planner/executor |
| 28 | Seed `pg_class` + `pg_attribute` + `pg_type` (composite rowtype) rows for the 5 remaining replication views (`pg_stat_replication` 12102, `pg_stat_recovery_prefetch` 12103, `pg_stat_subscription` 12104, `pg_replication_slots` 12105, `pg_stat_replication_slots` 12106). | `initdb/relcache_init.go::nailedLocalRels`, `initdb/aio_views.go` extended | `bootstrap-procedure/07-system-views-and-pg-rewrite.md` | `0106-0010-step3dl-pg-stat-wal-receiver-view-pg-class.md` | `initdb/aio_views_test.go` extended. | parser/planner/executor |
| 29 | Add `replicationViewRewriteEntries()` emitting `_RETURN` rule tuples (8-col PG18 layout) for the 5 remaining views into `pg_rewrite` (2618) and `pg_rewrite_rel_rulename_index` (2693); embed `.dat` ev_action captures. | `initdb/pg_rewrite_bootstrap.go`, new `pg_*_ev_action.dat` files | `bootstrap-procedure/07-system-views-and-pg-rewrite.md` §"ev_action encoding" | `0106-0010-step3dm-pg-rewrite-schema-fix.md` | `initdb/pg_rewrite_bootstrap_test.go`, `pg_rewrite_schema_test.go`. | parser/planner/executor |
| 30 | Fix `writeRelcacheInitFile`: emit exactly 5 shared / 4 local rels + 6 / 7 critical indexes (trailing-count check `relcache.c:6524-6534`); write the index sub-record (pg_index tuple, opfamily, opcintype, support, indcollation, indoption, opcoptions) for every index entry. Drop the `chmod 0o400` so PG can rewrite. | `initdb/relcache_init.go` | `bootstrap-procedure/08-relcache-init-and-version-files.md` | — (supersedes the older `docs/design/0106-0001-relcache-init-file-format.md`) | `initdb/relcache_init_test.go` (new): magic byte, record count, reader round-trip via a vanilla-PG18 `load_relcache_init_file` simulator. | wal/replication |
| 31 | Add `internal/catalog/RelcacheInitFileUnlink(dataDir, dboid)` and `WithRelCacheInitLock(fn)`; funnel every PG-canonical nailed-rel DDL through them; emit commit-record `RelcacheInitFileInval=true`. | `catalog/relcache_inval.go` (new), `executor/operators_ddl.go`, `executor/operators_vacuum.go`, `wal/recovery.go` | `bootstrap-procedure/08-relcache-init-and-version-files.md`, `11-continuous-maintenance.md` | — | `catalog/relcache_inval_test.go` (new); `wal/recovery_test.go` extended for `ProcessCommittedInvalidationMessages` redo. | wal/replication |
| 32 | Add `internal/catalog/PgCanonicalHeapInsert(rel, row)` + `PgCanonicalBtreeInsert(rel, key, tid)` + `RelationMapUpdateMap(dboid, relid, relfilenode, shared)` helpers; funnel `internal/executor/operators_ddl.go` DDL paths through them so `CREATE TABLE` / `CREATE INDEX` / `CREATE VIEW` / `CREATE FUNCTION` / `CREATE TYPE` / `CREATE TRIGGER` emit `XLOG_HEAP_INSERT` + `XLOG_BTREE_INSERT_LEAF` + `XLOG_RELMAP_UPDATE` and queue `CacheInvalidateHeapTuple` SI messages. | `catalog/canonical.go` (new), `executor/operators_ddl.go` | `bootstrap-procedure/05-local-catalog-bootstrap.md`, `06-bki-derived-catalog-seeds.md`, `11-continuous-maintenance.md` | `0106-0010-step3f-pg-index-empty-page.md`, `0106-0010-step3g-pg-index-form-encoder.md`, `0106-0010-step3i-null-bitmap-encoding.md`, `0106-0010-step3j-relnatts-indnatts-alignment.md`, `0106-0010-step3k-btree-metapage-encoding.md`, `0106-0010-step3n-pg-index-indkey-pg18-attnum-fixes.md`, `0106-0010-step3o-pg-attribute-relid-attnum-index-tuples.md`, `0106-0010-step3p-pg-index-indexrelid-index-tuples.md`, `0106-0010-step3q-pg-index-indexrelid-and-indrelid-split.md`, `0106-0010-step3r-pg-index-2678-2679-pg18-oid-correction.md`, `0106-0010-step3s-index-tuple-block-id-encoding.md`, `0106-0010-step3m-pg-class-oid-index-tuples.md`, `0106-0010-step3au-multi-leaf-btree-prereq.md`, `0106-0010-step3av-multi-leaf-btree-bulk-load.md`, `0106-0010-step3az-multi-leaf-btree-hikey-and-pnone.md`, `0106-0010-step3ba-multi-leaf-btree-hikey-firstright.md` | `catalog/canonical_test.go` (new); existing `executor/*_test.go` extended with WAL-byte capture assertions. | wal/replication |
| 33 | Add a primary-side `ReportParameters` entry point in `wal/parameter_change.go` that, on postmaster start and `SIGHUP`, diffs the 8 GUC fields and emits `XLOG_PARAMETER_CHANGE` + `updateControlFile`. | `wal/parameter_change.go` (new), `wal/checkpointer.go` | `bootstrap-procedure/09-streaming-replication-readiness.md`, `02-pg-control-and-checkpoint.md` | — | `wal/parameter_change_test.go` (new). | wal/replication |
| 34 | Wire `wal.WriteHistory` into the primary-initiated promotion path (post-recovery TLI bump, `pg_promote()` SQL function) — already wired for standby-initiated promotion. | `wal/recovery.go`, `cmd/goopg/standby.go` | `bootstrap-procedure/09-streaming-replication-readiness.md` | — | `internal/testport/e2e_failover_*` extended. | wal/replication |
| 35 | E2E gate run: `TestE2E_FailoverGoopgToPG/async` end-to-end. Acceptance: standby reaches hot standby, `pg_stat_wal_receiver.status = 'streaming'`, no FATAL on any of the spec-doc error chains. | `testport/e2e_failover_goopg_to_pg_test.go` | All of `bootstrap-procedure/` | — | `go test -v -run TestE2E_FailoverGoopgToPG/async ./internal/testport/`. | wal/replication |

---

## Acceptance criteria

The roadmap is complete when **every** predicate below holds for a
single `TestE2E_FailoverGoopgToPG/async` run (E2E driver at
`internal/testport/e2e_failover_goopg_to_pg_test.go:298`).

- **Vanilla PG18 standby attaches via `pg_basebackup -X stream -R`** against a goopg
  primary without any `could not open directory ...` / `could not stat
  ...` / `unexpected file ...` server-log entries (verifies
  [`01-`](01-data-directory-layout.md), [`09-`](09-streaming-replication-readiness.md)).
- **`ReadControlFile` does not FATAL** on the standby: no
  `pg_control_version`, `catalog_version_no`, `maxAlign`,
  `floatFormat`, `blcksz`, `nameDataLen`, CRC, or
  `CheckRequiredParameterValues` failure (verifies
  [`02-`](02-pg-control-and-checkpoint.md)).
- **`RelationCacheInitializePhase2` completes** on the standby without
  `could not open critical system index` (verifies
  [`04-`](04-shared-catalog-bootstrap.md), [`08-`](08-relcache-init-and-version-files.md)).
- **`RelationCacheInitializePhase3` completes** on the standby without
  `cache lookup failed for type/proc/opclass` (verifies
  [`05-`](05-local-catalog-bootstrap.md), [`06-`](06-bki-derived-catalog-seeds.md),
  [`08-`](08-relcache-init-and-version-files.md)).
- **WAL receiver connects** and consumes the first WAL segment without
  any `xlp_magic`, `xlp_seg_size`, `xlp_xlog_blcksz`, or `xlp_sysid`
  mismatch in `XLogPageRead` (verifies [`03-`](03-wal-bootstrap-segment.md)).
- **`SELECT * FROM pg_stat_wal_receiver` returns 1 row** with non-null
  `pid` and `status = 'streaming'` while the receiver is up; the same
  shape is returned for the other five replication views (verifies
  [`07-`](07-system-views-and-pg-rewrite.md)).
- **The standby remains a healthy hot standby for ≥ 60 seconds**:
  `pg_is_in_recovery()` returns `t`; the standby accepts read-only
  client connections; no FATAL appears in the log during the sustain
  window (verifies the runtime side of [`02-`](02-pg-control-and-checkpoint.md),
  [`07-`](07-system-views-and-pg-rewrite.md), [`08-`](08-relcache-init-and-version-files.md),
  [`09-`](09-streaming-replication-readiness.md)).
- **`TestE2E_FailoverGoopgToPG/async` passes** end-to-end without any
  reactive `0106-0010-step3*` follow-up loop being required.

---

## What this plan does NOT cover

- **Logical replication apply** (vanilla PG18 publisher → goopg
  subscriber). Tracked under M0103; design in
  `docs/design/0103-*`.
- **Reverse direction physical replication** (vanilla PG18 primary →
  goopg standby). Tracked under M0102; the byte-decoding side already
  lives in `internal/wal/recovery.go` + `internal/wal/stream_replayer.go`
  but is not exercised by `TestE2E_FailoverGoopgToPG/async`.
- **Promotion mechanics beyond timeline switch** — fast-failover,
  cascade promotion, `pg_promote()` SQL function semantics. Tracked
  under `0102-0002-promotion-rpc.md` and
  `0102-0004-promotion-replication.md`.
- **DDL replay corner cases** — concurrent `REINDEX`, partition
  attach/detach replay, generated-column rewrite. Tracked under
  `docs/design/0030-*`, `0079-*`, `0080-*`.
- **`information_schema` view set** and the rest of `system_views.sql`
  beyond the 6 replication views in [`07-`](07-system-views-and-pg-rewrite.md).
  Follow-up after task 29.
- **`pg_import_system_collations`** (the SQL-phase libc/ICU collation
  enumerator that adds ~800 rows on a stock PG18 cluster). Outside the
  bootstrap-procedure scope; tracked separately because the row set
  depends on host-OS locale presence.
- **WAL archiving / WAL summarisation runtime producers** (the
  `pg_wal/archive_status/*.ready` writer, the walsummarizer
  worker). The directories are present at initdb; the runtime
  emitters are tracked in [`03-`](03-wal-bootstrap-segment.md)
  §"Continuous maintenance" as deferred.

---

## Superseded step-3* docs

The 116 `docs/design/0106-0010-step3*.md` reactive design docs below
are superseded by the
[`docs/design/bootstrap-procedure/`](.) doc set and the task table
above. They remain in the tree as historical provenance only — future
Ralph loops should read the bootstrap-procedure files plus this
roadmap, not the individual step-3* files.

Each task in the table above names the originating step-3* doc(s) it
supersedes; the complete enumeration is:

- `0106-0010-step3a-pg-proc-bootstrap.md`
- `0106-0010-step3aa-pg-cast-nailed-rel.md`
- `0106-0010-step3ab-pg-cast-oid-index.md`
- `0106-0010-step3ac-pg-cast-source-target-index.md`
- `0106-0010-step3ad-pg-opclass-am-name-nsp-index.md`
- `0106-0010-step3ae-pg-collation-name-enc-nsp-index.md`
- `0106-0010-step3af-pg-collation-oid-index.md`
- `0106-0010-step3ag-pg-conversion-nailed-rel.md`
- `0106-0010-step3ah-pg-conversion-default-index.md`
- `0106-0010-step3ai-pg-conversion-oid-index.md`
- `0106-0010-step3aj-pg-conversion-name-nsp-index.md`
- `0106-0010-step3ak-pg-default-acl-nailed-rel.md`
- `0106-0010-step3al-pg-default-acl-role-nsp-obj-index.md`
- `0106-0010-step3am-pg-default-acl-oid-index.md`
- `0106-0010-step3an-pg-enum-nailed-rel.md`
- `0106-0010-step3ao-pg-enum-oid-index.md`
- `0106-0010-step3ap-pg-enum-typid-label-index.md`
- `0106-0010-step3aq-pg-enum-typid-sortorder-index.md`
- `0106-0010-step3ar-pg-event-trigger-nailed-rel.md`
- `0106-0010-step3as-pg-event-trigger-evtname-index.md`
- `0106-0010-step3at-pg-event-trigger-oid-index.md`
- `0106-0010-step3au-multi-leaf-btree-prereq.md`
- `0106-0010-step3av-multi-leaf-btree-bulk-load.md`
- `0106-0010-step3aw-pg-extension-nailed-rel.md`
- `0106-0010-step3ax-pg-extension-oid-index.md`
- `0106-0010-step3ay-pg-extension-name-index.md`
- `0106-0010-step3az-multi-leaf-btree-hikey-and-pnone.md`
- `0106-0010-step3b-pg-opclass-bootstrap.md`
- `0106-0010-step3ba-multi-leaf-btree-hikey-firstright.md`
- `0106-0010-step3bb-pg-foreign-data-wrapper-nailed-rel.md`
- `0106-0010-step3bc-pg-foreign-data-wrapper-name-index.md`
- `0106-0010-step3bd-pg-foreign-data-wrapper-oid-index.md`
- `0106-0010-step3be-pg-foreign-server-nailed-rel.md`
- `0106-0010-step3bf-pg-foreign-server-name-index.md`
- `0106-0010-step3bg-pg-foreign-server-oid-index.md`
- `0106-0010-step3bh-pg-foreign-table-nailed-rel.md`
- `0106-0010-step3bi-pg-foreign-table-relid-index.md`
- `0106-0010-step3bj-pg-language-name-index.md`
- `0106-0010-step3bk-pg-language-oid-index.md`
- `0106-0010-step3bl-pg-operator-oprname-l-r-n-index.md`
- `0106-0010-step3bm-pg-opfamily-nailed-rel.md`
- `0106-0010-step3bn-pg-opfamily-am-name-nsp-index.md`
- `0106-0010-step3bo-pg-opfamily-oid-index.md`
- `0106-0010-step3bp-pg-parameter-acl-nailed-rel.md`
- `0106-0010-step3bq-pg-parameter-acl-parname-index.md`
- `0106-0010-step3br-pg-parameter-acl-oid-index.md`
- `0106-0010-step3bs-pg-partitioned-table-nailed-rel.md`
- `0106-0010-step3bt-pg-partitioned-table-partrelid-index.md`
- `0106-0010-step3bu-pg-publication-nailed-rel.md`
- `0106-0010-step3bv-pg-publication-pubname-index.md`
- `0106-0010-step3bw-pg-publication-oid-index.md`
- `0106-0010-step3bx-pg-publication-namespace-nailed-rel.md`
- `0106-0010-step3by-pg-publication-rel-nailed-rel.md`
- `0106-0010-step3bz-pg-range-nailed-rel.md`
- `0106-0010-step3c-pg-amop-amproc-bootstrap.md`
- `0106-0010-step3ca-pg-replication-origin-nailed-rel.md`
- `0106-0010-step3cb-pg-sequence-nailed-rel.md`
- `0106-0010-step3cc-pg-statistic-ext-data-nailed-rel.md`
- `0106-0010-step3cd-pg-statistic-ext-nailed-rel.md`
- `0106-0010-step3ce-pg-statistic-nailed-rel.md`
- `0106-0010-step3cf-pg-subscription-indexes.md`
- `0106-0010-step3cg-pg-subscription-rel-nailed-rel.md`
- `0106-0010-step3ch-pg-tablespace-nailed-rel.md`
- `0106-0010-step3ci-pg-transform-nailed-rel.md`
- `0106-0010-step3cj-pg-ts-config-map-nailed-rel.md`
- `0106-0010-step3ck-pg-ts-config-nailed-rel.md`
- `0106-0010-step3cm-pg-ts-dict-nailed-rel.md`
- `0106-0010-step3cn-pg-ts-parser-nailed-rel.md`
- `0106-0010-step3co-pg-ts-template-nailed-rel.md`
- `0106-0010-step3cp-pg-user-mapping-nailed-rel.md`
- `0106-0010-step3cq-pg-type-heap-canonical-typalign.md`
- `0106-0010-step3cr-pg-class-reltablespace-shared.md`
- `0106-0010-step3cs-pg-database-oid-index-populated.md`
- `0106-0010-step3ct-pg-database-pg18-row-layout.md`
- `0106-0010-step3cu-pg-db-role-setting-nailed-rel.md`
- `0106-0010-step3cv-pg-shseclabel-pg18-schema.md`
- `0106-0010-step3cw-pg-amproc-fam-proc-index.md`
- `0106-0010-step3cx-pg-authid-os-user-and-indexes.md`
- `0106-0010-step3cy-e2e-standby-log-capture-and-type-23-cache-miss.md`
- `0106-0010-step3cz-pg-type-oid-index-populated.md`
- `0106-0010-step3d-pg-amop-amproc-pinned-opfamily-fix.md`
- `0106-0010-step3da-pg-type-io-regproc-oids.md`
- `0106-0010-step3db-pg-proc-oid-index-populated.md`
- `0106-0010-step3dc-pg-proc-io-regproc-heap-rows.md`
- `0106-0010-step3dd-segv-backtrace-ld-preload.md`
- `0106-0010-step3de-pg-authid-heap-rolname-byte-layout.md`
- `0106-0010-step3df-segv-backtrace-si-addr-and-registers.md`
- `0106-0010-step3dg-pg-authid-rolname-index-name-typed-descriptor.md`
- `0106-0010-step3dh-pg-database-datname-index.md`
- `0106-0010-step3di-segv-chain-eliminated-pg-stat-wal-receiver-missing.md`
- `0106-0010-step3dj-pg-proc-stat-get-wal-receiver.md`
- `0106-0010-step3dk-pg-proc-3317-out-args-arrays.md`
- `0106-0010-step3dl-pg-stat-wal-receiver-view-pg-class.md`
- `0106-0010-step3dm-pg-rewrite-schema-fix.md`
- `0106-0010-step3e-pg-amproc-sortsupport-equalimage.md`
- `0106-0010-step3f-pg-index-empty-page.md`
- `0106-0010-step3g-pg-index-form-encoder.md`
- `0106-0010-step3h-pg-amop-amproc-crosstype-integer.md`
- `0106-0010-step3i-null-bitmap-encoding.md`
- `0106-0010-step3j-relnatts-indnatts-alignment.md`
- `0106-0010-step3k-btree-metapage-encoding.md`
- `0106-0010-step3l-pg-opclass-oid-index-tuples.md`
- `0106-0010-step3m-pg-class-oid-index-tuples.md`
- `0106-0010-step3n-pg-index-indkey-pg18-attnum-fixes.md`
- `0106-0010-step3o-pg-attribute-relid-attnum-index-tuples.md`
- `0106-0010-step3p-pg-index-indexrelid-index-tuples.md`
- `0106-0010-step3q-pg-index-indexrelid-and-indrelid-split.md`
- `0106-0010-step3r-pg-index-2678-2679-pg18-oid-correction.md`
- `0106-0010-step3s-index-tuple-block-id-encoding.md`
- `0106-0010-step3t-pg-namespace-index-seeds.md`
- `0106-0010-step3u-pg-attribute-null-attoptions.md`
- `0106-0010-step3v-pg-shseclabel-reltype.md`
- `0106-0010-step3w-pg-aggregate-nailed-rel.md`
- `0106-0010-step3x-pg-aggregate-fnoid-index.md`
- `0106-0010-step3y-pg-amop-fam-strat-index.md`
- `0106-0010-step3z-pg-auth-members-role-member-index.md`

Total: **116 docs superseded**. New work tracked through this file and
the bootstrap-procedure doc set.
