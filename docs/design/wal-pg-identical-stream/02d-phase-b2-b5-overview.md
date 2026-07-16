# 02d — Phase-B2–B5 overview (application map)

| | |
|---|---|
| Status | draft — pending agent review |
| Date | 2026-07-16 |
| Scope | Application tables + risk deltas for the remaining conversion groups. Every conversion follows [02b](02b-catalog-conversion-recipe.md) verbatim; this doc only records what differs per group. Deliberately thin — it gains detail as each group starts. |
| Parent | [02-catalog-heap-journaling.md](02-catalog-heap-journaling.md) §8.3–§8.5, §7 |

## 1. B2 — type/operator families

| Catalog | Header | Indexes | Main DDLs | Bespoke records to die |
|---|---|---|---|---|
| pg_type (non-composite rows) | catalog/pg_type.h | 2703 oid, 2704 typname+nsp | CREATE TYPE/DOMAIN | CreateDomain, CreateRangeType(type half), enum/range creators' type rows |
| pg_enum | catalog/pg_enum.h | 3502 oid, 3503 typid+label, 3534 typid+sortorder | CREATE TYPE AS ENUM, ALTER TYPE ADD VALUE | CreateEnumType group |
| pg_range | catalog/pg_range.h | 3542 rngtypid | CREATE TYPE AS RANGE | CreateRangeType(range half) |
| pg_operator | catalog/pg_operator.h | 2688 oid, 2689 name+args+nsp | CREATE OPERATOR | CreateOperator |
| pg_opclass / pg_opfamily / pg_amop / pg_amproc | catalog/pg_opclass.h etc. | per header | CREATE OPERATOR CLASS/FAMILY | CreateOperatorClass, CreateAmOpMember, … |
| pg_cast | catalog/pg_cast.h | 2660 oid, 2661 src+tgt | CREATE CAST | CreateCast |
| pg_conversion | catalog/pg_conversion.h | 2668/2669/2670 | CREATE CONVERSION | CreateConversion |
| pg_collation | catalog/pg_collation.h | 3085 oid, 3164 name+enc+nsp | CREATE COLLATION | CreateCollation |
| pg_aggregate | catalog/pg_aggregate.h | 2650 fnoid | CREATE AGGREGATE | CreateAggregate group |

**Risk delta**: pg_type rows for composites interlock with pg_class/pg_attribute
(a composite type row is created by CREATE TABLE, already Family-1) — the B2
conversion must not double-write those rows; only standalone type DDLs convert.
pg_type is already runtime heap-backed for reads (`RegisterRealTable`), so B2
begins with a catalog whose read model needs no swap.

## 2. B3 — extension/config catalogs

| Catalog | Notes |
|---|---|
| pg_ts_dict / pg_ts_config / pg_ts_parser / pg_ts_template (+ config_map) | four scanners die together (tsdict/tsconfig recovery files) |
| pg_transform | low-traffic → candidate for true heap-read |
| pg_event_trigger | low-traffic → true heap-read candidate |
| pg_publication / pg_publication_rel / pg_publication_namespace / pg_subscription / pg_subscription_rel | pubsub_ddl_recovery.go dies; pg_subscription is SHARED (moves to B4 if global/ is not ready) |
| pg_statistic_ext (+ _data) | statistics_ddl_recovery.go dies |
| pg_constraint / pg_attrdef / pg_depend | **pg_depend arrives here** — unblocks the B1 ledger rows (sequence OWNED BY, schema-owner dependencies). Highest-fanout catalog of the group. |

**Risk delta**: pg_depend rows are written by nearly every DDL — its conversion
is a cross-cutting emit-site sweep, not a single-command swap; stage it last in
B3 with its own sub-plan.

## 3. B4 — shared catalogs (`global/`)

pg_database, pg_authid, pg_auth_members, pg_tablespace, pg_foreign_data_wrapper,
pg_foreign_server, pg_user_mapping (+ pg_subscription if deferred from B3).

- Heap writes target `global/<oid>` (`RelFileLocator{spc=pg_global, db=0}`);
  `catalogReloadDesc.Shared=true` scans `global/` once, not per-DB.
- Cross-DB visibility (doc 02 risk R4) follows from the single relfile — the
  per-connection virtual-catalog scoping must read shared registries, not per-DB
  copies.
- **pg_authid special case**: the bespoke byte-level writer `SyncPgAuthidFile`
  (`internal/executor/pg_authid_sync.go:219`) is retired in favor of the
  standard heap path; role DDL records (RoleState/DropRole/GrantRoleMembership)
  die here.
- The postgres-DB mirror shim (`sys_catalog_postgres_db_mirror.go`) is retired
  once every shared catalog is heap-backed (doc 02 §6).

## 4. B5 — retirement

Preconditions: B1–B4 complete, no bespoke catalog record emitted anywhere.

1. Delete `RmgrGoopgCatalog = 128` and the `default:` arm's remaining catalog
   kinds from `internal/wal/rmgr_map.go` + `recovery.go`.
2. Retire `IsGoopgNativeRecord`'s catalog-record uses (the guard exists for
   scanners; the last scanner died in B4).
3. Final gates: grep-zero on every retired symbol; full suite; `pg_waldump`
   whole-stream decode shows ONLY real PG rmgrs; real-PG-standby e2e with a DDL
   workload covering every group.

## 5. Deferral-ledger index (rows created by Phase B)

| Created in | Row | Resumes in |
|---|---|---|
| B0 | B0.4 relmap writer + XLOG_RELMAP_UPDATE (if deferred) | first relfilenode-changing op or bootstrap byte-parity gate |
| B1 | pg_sequence counter state on kind 65 (XLOG_SEQ_LOG flip) | B1.3b |
| B1 | sequence OWNED BY / schema-owner pg_depend rows | B3 (pg_depend) |
| B1 | pg_proc TOAST threshold note (if hit) | B2/B3 varlena work |
