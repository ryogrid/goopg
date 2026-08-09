# Milestone 0130 — Cluster directory compatibility with PG 18.3 + PG physical replication

**Status:** planned
**Filed:** 2026-08-09 (user directive — see the fix_plan Current Priority banner)
**Reference plan:** `.ralph/fix_plan.md` (M0130 section)
**Implementation plan (authoritative task decomposition):**
`docs/design/0130-cluster-dir-compat-and-pg-physical-replication.md`
**Design of record:** `analysis/cluster-dir-level-compat/README.md` (2026-07-26 gap
catalog; 15 gaps, 3 blockers / 7 significant / 5 non-blockers), deferral-ledger rows
#27, #29, #50, #389–#393, #404, and the 2026-07-18 B5 feasibility row.
**Prerequisites:** M-NIGHTLY is the standing filing obligation (highest priority,
unconditional); **M0130 is the top-priority milestone after M-NIGHTLY** (user
directive 2026-08-09). B4 is COMPLETE (B4.6 Stages 1–3b landed 2026-07-18 —
pg_database heap row, OID preservation, RM_DBASE + per-block FPIs, validated on a
real PG 18.3 standby via `TestE2E_FailoverGoopgToPG`). M0113 (pg_index heap),
M0102 (promotion / timeline history / slots), BASE_BACKUP, WalReceiver and the
replcluster harness are all landed.
**Branch:** inherits the current lineage and its discipline (worktrees off pinned
clean HEAD, explicit pathspec staging, guard re-runs after rebase/handoff).

## Background

`analysis/cluster-dir-level-compat/README.md` catalogs every gap that stops a
vanilla PostgreSQL 18.3 from starting against a goopg-created `$PGDATA`, serving
reads, and operating correctly — and conversely stops goopg from starting against a
PG-created one. The formats that are already byte-identical (pg_control 296-byte
payload, WAL page headers, heap/B-tree pages, CLOG/subtrans/multixact SLRU,
relcache init file) and the full replication stack that already exists
(BASE_BACKUP with manifest, IDENTIFY_SYSTEM, TIMELINE_HISTORY, slots with
PG-compatible 200-byte state, START_REPLICATION, WalReceiver `AppendRaw`,
StreamReplayer, SyncRep, promotion, and the E2E family
`TestE2E_PhysicalReplication` / `TestE2E_FailoverGoopgToPG` / `TestE2E_FailoverPGtoGoopg` /
`TestPort_PgBasebackup*`) are the foundation.

**Status at filing (2026-08-09):** Several gaps cataloged in the analysis README
were resolved after its 2026-07-26 date. B5 (retire native WAL kinds for
index/attrdef/view/matview) landed 2026-07-18/19 — kinds 20/21/94/69/102/103 no
longer emit goopg-native records, and zero rmid-128 records appear in the emitted
WAL stream (the `RmgrGoopgCatalog` constant is deliberately kept for classifying
surviving non-catalog kinds like `RecordKindXactAssignment`/`RecordKindXactSubAbort`;
literal deletion would be incorrect + unsafe, per deferral-ledger row 415). The
xl_prev 0-based fix (ledger #29) also landed at HEAD (`internal/wal/writer.go`
documents the −1 conversion). The atomic heap-update WAL gap (ledger #27) may be
partially addressed via pre-assembled `xl_heap_update` envelopes; a verification
pass is needed.

**Remaining blockers for "PG starts against goopg data":** runtime DDL not synced
to catalog heap files (ADD COLUMN → pg_attribute, CREATE SCHEMA → pg_namespace,
etc.), virtual-only pg_class, FSM/VM aggregate files. **Replication gaps:**
hardcoded single timeline, `global/timeline_id` vs pg_control TLI reconciliation,
`recovery.signal` recognized but unimplemented. This milestone closes these
remaining gaps and verifies the already-landed B5/xl_prev work end-to-end.

## Goals

1. **Bidirectional cluster-directory compatibility** — PG 18.3 starts and serves
   against a goopg-created `$PGDATA` (fresh and after a DDL+DML workload), and
   goopg starts and serves against a PG-initdb'd `$PGDATA`.
2. **pg_basebackup-to-PG-standby** — `pg_basebackup` from a goopg primary produces
   a directory a PG 18.3 standby starts from and streams from.
3. **Physical streaming replication goopg → PG 18.3 standby** — DDL and DML replay
   on a real PG standby without FATAL, pg_waldump parses the full stream
   (incl. correct prev-link chain), failover promotes the PG standby and the
   cluster continues on a new timeline.
4. **WAL record fidelity verified** — B5 retirement (kinds 20/21/94/69/102/103)
   already landed; this milestone verifies zero rmid-128 records in the emitted
   stream, documents the keep-the-classify-arms decision, and confirms atomic
   heap-update WAL semantics end-to-end on a PG standby.
5. **Catalog durability for PG visibility** — pg_class heap-backed, all runtime DDL
   paths sync to catalog heaps.
6. **Storage forks PG-shaped** — per-relation `_fsm`/`_vm` fork files instead of the
   aggregate `pg_fsm_state.bin`/`pg_vm_state.bin`.

## Task list (summary — the design/0130 plan doc is authoritative)

| task | what | theme |
|---|---|---|
| S1 | per-relation FSM/VM fork files (retire aggregate bin state) | A — cluster dir format |
| S2 | pg_class heap persistence (retire virtual-only; reverse-start path) | A |
| S3 | catalog heap sync for remaining DDL (ADD COLUMN, CREATE SCHEMA, FDW/server/collation) | A |
| S4 | B5 verification: confirm index/attrdef native WAL kinds retired (kinds 20/21/94/69 landed 2026-07-18) | B — WAL fidelity |
| S5 | B5 verification: confirm view/matview native WAL kinds retired (kinds 102/103 landed 2026-07-19) | B |
| S6 | verify zero rmid-128 records emitted; document keep-the-classify-arms decision (deferral-ledger #415) | B |
| S7 | WAL fidelity verification: confirm xl_prev 0-based at HEAD (ledger #29); audit atomic heap-update completeness (ledger #27) | B |
| S8 | multi-timeline START_REPLICATION + timeline_id/pg_control reconciliation | C — replication |
| S9 | recovery.signal archive recovery (restore_command) | C |
| S10 | PG 18.3 standby E2E: basebackup + streaming + failover + reverse attach | C |

**Filing rule (inherited from M0129):** no task is deferred without a strong reason
recorded in the deferral ledger; every item's subtasks are listed inline in the
fix_plan task body; every non-trivial subsystem lands its design doc (status
`draft` → `accepted`) **within M0130** — a design doc punted past the milestone is a
milestone failure.

## Acceptance bar

1. **PG starts goopg's data dir:** a PG 18.3 `pg_ctl start` succeeds against a
   goopg-initdb'd `$PGDATA` — fresh, and after a workload of CREATE TABLE /
   ADD COLUMN / CREATE SCHEMA / CREATE INDEX / CREATE DATABASE / INSERT / UPDATE /
   DELETE — and serves reads via psql with zero FATAL. Reverse: goopg starts and
   serves against a PG-initdb'd `$PGDATA`.
2. **pg_basebackup:** `pg_basebackup -h <goopg-primary> -D <dir>` completes with a
   manifest; the resulting directory starts as a PG 18.3 standby and catches up.
3. **Streaming:** a PG 18.3 standby of a goopg primary replays the S1/S3/S10 DDL+DML
   workload with no "resource manager with ID 128 not registered" or replay errors;
   rows written on the primary are visible on the standby; `pg_waldump` parses the
   full stream without aborting on an "incorrect prev-link".
4. **Failover:** promoting the PG standby yields a writable primary; goopg's
   `global/timeline_id` and pg_control TLI agree; re-attach on the new timeline
   works.
5. **Catalog:** on a PG started against goopg's data dir, psql `SELECT relname FROM
   pg_class` lists the user tables; catalog DDL from S3 survives restart and
   appears on a standby.
6. **Forks:** per-relation `<relfilenode>_fsm`/`<relfilenode>_vm` files exist after
   checkpoint; PG startup logs no fork errors; a base backup contains them.
7. **rmid-128 verified gone:** pg_waldump over a DDL workload stream reports zero
   records with rmgr ID 128 (already true at HEAD; this milestone adds a
   regression gate). The `RmgrGoopgCatalog` constant is deliberately kept for
   classifying surviving non-catalog kinds; literal deletion would be incorrect.
8. **No regressions:** the existing replication family (`TestE2E_PhysicalReplication`
   + Sync, `TestE2E_FailoverGoopgToPG`, `TestE2E_FailoverPGtoGoopg`,
   `TestE2E_StandbyAttachRoundtrip`, `TestPort_PgBasebackup*`) stays green; UNITS /
   SMOKE / SPOT and the nightly regress suite stay green; `make ralph-state-guard`
   clean.
9. **recovery.signal:** archive recovery via `restore_command` replays to the end
   of WAL and promotes onto a new TLI — or a ledger-recorded verdict naming the
   blocker.

## Required design docs

| doc | status | covers |
|---|---|---|
| `docs/design/0130-cluster-dir-compat-and-pg-physical-replication.md` | created at filing | authoritative task decomposition (all S-tasks) |
| `docs/design/0130-0001-fsm-vm-per-relation-fork-files.md` | draft — **within M0130 (S1, before code)** | PG FSM three-level B-tree-of-bytes + VM bit layouts, fork file write/load, checkpoint persistence, BASE_BACKUP fork tar entries, aggregate-file retirement |
| `docs/design/0130-0002-pg-class-heap-persistence.md` | draft — **within M0130 (S2, before code)** | heap-backed pg_class (bootstrap rows, runtime sync audit, reload pass), reverse-start (goopg from PG heap) |
| `docs/design/0130-0003-catalog-heap-sync-coverage.md` | draft — **within M0130 (S3, before code)** | ADD COLUMN, CREATE SCHEMA/pg_namespace, pg_collation, FDW/server heap rows |
| `docs/design/0130-0004-b5-index-and-attrdef-retirement.md` | draft — **within M0130 (S4, verification)** | B5-A+B verification: kinds 20/21/94/69 already retired at HEAD; document landed state, add regression gates |
| `docs/design/0130-0005-b5-view-matview-retirement.md` | draft — **within M0130 (S5, verification)** | B5-C verification: kinds 102/103 already retired at HEAD; document landed state |
| `docs/design/0130-0006-rmgr-goopg-catalog-retirement.md` | draft — **within M0130 (S6, verification)** | verify zero rmid-128 emitted records; document keep-the-classify-arms decision (deferral-ledger #415) |
| `docs/design/0130-0007-wal-record-fidelity-xlprev-atomic-update.md` | draft — **within M0130 (S7, audit)** | verify xl_prev 0-based at HEAD (ledger #29); audit atomic heap-update completeness (ledger #27) |
| `docs/design/0130-0008-multi-timeline-streaming-and-timeline-reconciliation.md` | draft — **within M0130 (S8, before code)** | TLI source of truth, IDENTIFY_SYSTEM/START_REPLICATION TIMELINE n, promotion TLI bump, TIMELINE_HISTORY |
| `docs/design/0130-0009-recovery-signal-archive-recovery.md` | draft — **within M0130 (S9, before code)** | recovery.signal mode, restore_command, replay-then-promote |
| `docs/design/0130-0010-pg183-standby-e2e-harness.md` | draft — **within M0130 (S10, before code)** | PG-standby harness (pg_basebackup → pg_ctl start → stream → failover), replcluster extension |

Smaller single-function changes may ride the implementation-plan doc per the repo
rule (a design doc is required for every *non-trivial subsystem*; single-function
changes with unit tests may cite this plan instead).
