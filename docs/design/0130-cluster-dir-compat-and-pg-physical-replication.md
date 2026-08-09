# M0130 Implementation Plan — Cluster-directory compatibility with PG 18.3 + PG physical replication

**Status:** planned
**Date:** 2026-08-09
**Milestone:** `docs/milestones/0130-cluster-dir-compat-and-pg-physical-replication.md`
**Design of record:** `analysis/cluster-dir-level-compat/README.md` (2026-07-26 gap catalog)
**Convention:** this is the authoritative task decomposition for M0130; each
S-task may ride this doc or have its own `0130-NNNN-*.md` sub-design doc (listed
in the milestone's Required design docs table).
**Decomposition source:** the 15-gap catalog in `analysis/cluster-dir-level-compat/README.md`
(3 blockers / 7 significant / 5 non-blockers), deferral-ledger rows #27, #29,
#50, #389–#393, #404, and the 2026-07-18 B5 feasibility row.

## Positioning

M0129 closed 2026-08-09. Per user directive 2026-08-09, priority is:
M-NIGHTLY (standing filing obligation, unconditional) first, then **M0130**,
then every remaining unchecked item top-to-bottom (currently M0119, then
M0122). The `.ralph/fix_plan.md` banner is the sole ordering authority.

## Ordering principle

Cluster-directory format first (S1–S3), because goals 1–2 are the cheapest
independent wins. B5 verification (S4→S5→S6) follows as audit/documentation
work — the implementation landed 2026-07-18/19 but the milestone adds regression
gates and end-to-end confirmation. S7 audits already-landed WAL fidelity fixes
(xl_prev 0-based, atomic heap-update). S8/S9 are independent implementation
tasks. S10 is last — it is the acceptance vehicle for everything.

## Common gate vocabulary

Same as M0129's binding list: UNITS (`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`),
SMOKE (the git-hook pgbench), SPOT (`scripts/tpch-spotcheck.sh`),
DS05 (TPC-DS SF0.5 regression gate), PLAN (`make plan-gate`),
RACE (`make race-gate`), SIBLING (encode↔decode, fast-path↔interpreted,
column-lookup↔star-expansion), `make ralph-state-guard`.
Plus replication-specific gates: `TestE2E_FailoverGoopgToPG` (the existing
real-PG-standby harness), the `TestPort_PgBasebackup*` family,
`TestE2E_PhysicalReplication(+Sync)`, `TestPort_WALPgWaldumpCompat` family,
and `pg_waldump` from the `./postgres/` oracle tree.

---

## Theme A — Cluster directory format compatibility

### S1 — Per-relation FSM/VM fork files (est ~2 loops)

**Sources:** analysis README §7.1/§7.2, gap #4; `internal/storage/fsm.go`,
`internal/storage/vm.go`, `internal/initdb/open.go:3028/3041`.

**Subtasks:**
- S1.1 Fork-file writers: PG `<relfilenode>_fsm` (three-level B-tree of bytes
  per `freespace.c`, `FSM_FORKNUM` naming via `relpath.c` fork names) and
  `<relfilenode>_vm` (bitmap per `visibilitymap.c`, `VM_FORKNUM`), one file
  per relation under `base/<dbOid>/` and `global/` for shared rels.
- S1.2 Checkpoint-time persistence: `SaveFSM()`/`SaveVM()` emit fork files;
  startup load in `open.go` replaces the aggregate-bin load.
- S1.3 BASE_BACKUP streams `_fsm`/`_vm` forks (tar entries with fork suffix).
- S1.4 Retire `pg_fsm_state.bin`/`pg_vm_state.bin` (stop writing; delete on
  next init).
- S1.5 Guards: PG 18.3 start against goopg data dir logs no fork errors; a
  base backup contains forks; insert path uses the FSM fork.

**Gates:** UNITS + RACE + `TestPort_PgBasebackup*` + real-PG-startup probe + SMOKE.
**Design doc:** `docs/design/0130-0001-fsm-vm-per-relation-fork-files.md`.

### S2 — pg_class heap persistence (est ~2 loops)

**Sources:** analysis README §6.3 gap #3; memory `goopg_pg_class_virtual_pg_attribute_heap`;
`catalog.go` VirtualRows; `syncTableToCatalogHeap`.

**Subtasks:**
- S2.1 Audit + complete bootstrap pg_class rows in `base/<dbOid>/1259` (every
  catalog and initial user state).
- S2.2 Runtime sync completeness audit: every CREATE/ALTER/DROP path keeps
  1259 current.
- S2.3 Reverse-start path: goopg bootstraps its in-memory catalog from a
  PG-created 1259/1249 heap (goal 1 reverse direction).
- S2.4 Guards: real PG on goopg data dir — psql `SELECT relname FROM pg_class`
  sees user tables; goopg on PG-initdb'd data dir serves.

**Gates:** UNITS + SMOKE + both-direction start guards.
**Design doc:** `docs/design/0130-0002-pg-class-heap-persistence.md`.

### S3 — Catalog heap sync coverage for remaining DDL (est ~2 loops)

**Sources:** analysis README gaps #2/#9; ledger #404 ADD COLUMN, #50 CREATE
SCHEMA, #389–#393 collation/FDW/server.

**Subtasks:**
- S3.1 ADD COLUMN arm calls `syncTableToCatalogHeap` (deferral #404;
  `operators_ddl.go`) + WAL.
- S3.2 CREATE SCHEMA → pg_namespace (2615) heap rows.
- S3.3 User collations → pg_collation (3456) heap rows.
- S3.4 FDW/server → pg_foreign_data_wrapper (2328) / pg_foreign_server (1417)
  heap rows.
- S3.5 Verify pg_tablespace heap rows are complete (B4.1 landed — confirm,
  don't rebuild).
- S3.6 Guards: each DDL survives restart and replays on a real PG standby
  (after S6).

**Gates:** UNITS + SurvivesRestart family + standby DDL replay.
**Design doc:** `docs/design/0130-0003-catalog-heap-sync-coverage.md`.

---

## Theme B — WAL record fidelity for PG standby consumption

### S4 — B5 verification: index/attrdef native WAL kinds (est ~1 loop)

**Sources:** ledger 2026-07-18 B5 FEASIBILITY row; landed commits `eb88b8a2` (B5-A+B) 2026-07-18.

**Status at HEAD:** Kinds 20/21 (CREATE/DROP INDEX) are already heap-backed via
M0113; kind 94 (RENAME INDEX) resyncs pg_index; kind 69 (pg_attrdef) is
heap-backed. The emit sites and `index_ddl_recovery.go`/`attrdef_ddl_recovery.go`
were deleted in the July commits. Zero goopg-native records are emitted for
these kinds.

**Subtasks:**
- S4.1 Audit: grep-verify zero emit sites remain for kinds 20/21/94/69.
- S4.2 Verify `record_kind_rmgr_mapping_test.go` classifies surviving kinds
  correctly.
- S4.3 Regression gate: extend WAL test to confirm no regressions in index/attrdef
  DDL replay on a PG standby.

**Gates:** UNITS + wal suite + `WALPgWaldumpCompat` + SurvivesRestart.
**Design doc:** `docs/design/0130-0004-b5-index-and-attrdef-retirement.md`.

### S5 — B5 verification: view/matview native WAL kinds (est ~1 loop)

**Sources:** same ledger row; landed commit `2697504f` (B5-C) 2026-07-19.

**Status at HEAD:** Kinds 102/103 (CREATE VIEW/MATVIEW) are retired;
`view_ddl_recovery.go`/`matview_ddl_recovery.go` are deleted; runtime pg_rewrite
writer, text ev_action, rule OIDs, and `loadViewsFromHeap` reload pass are
landed. Canonical node-tree fidelity is explicitly deferred to M0123.

**Subtasks:**
- S5.1 Audit: grep-verify zero emit sites remain for kinds 102/103.
- S5.2 Verify view/matview DDL replays on a PG standby without errors.
- S5.3 Confirm the M0123 deferral is ledger-recorded with a resume point.

**Gates:** UNITS + wal + standby E2E with view/matview DDL.
**Design doc:** `docs/design/0130-0005-b5-view-matview-retirement.md`.

### S6 — Verify zero rmid-128 records emitted; document keep-the-classify-arms decision (est ~1 loop)

**Sources:** deferral-ledger row 415 (2026-07-18: "B5 COMPLETE: rmid-128 retired
from the EMITTED stream … LANDED as DOCUMENTATION, not a literal deletion").

**Status at HEAD:** Zero goopg-catalog WAL records are emitted (all 6 kinds
retired). The `RmgrGoopgCatalog` constant is deliberately KEPT — it classifies
surviving non-catalog kinds (`RecordKindXactAssignment`, `RecordKindXactSubAbort`)
pinned by `record_kind_rmgr_mapping_test.go:53-66`. Literal deletion of the
constant and dispatch arms would be incorrect and unsafe (per ledger #415).

**Subtasks:**
- S6.1 Verify `pg_waldump` over a full DDL workload stream reports zero records
  with rmgr ID 128.
- S6.2 Document the keep-the-classify-arms decision in the design doc (cite
  ledger #415, the test pin, and the surviving non-catalog kinds).
- S6.3 Add a regression gate: `grep` for rmid-128 in emitted WAL must stay zero.

**Gates:** UNITS + SMOKE + the extended E2E + full-suite.
**Design doc:** `docs/design/0130-0006-rmgr-goopg-catalog-retirement.md`.

### S7 — WAL fidelity audit: verify xl_prev 0-based + atomic heap-update completeness (est ~1 loop)

**Sources:** ledger #29, #27; `internal/wal/writer.go:1385-1395`,
`internal/wal/recovery.go:277-290`.

**Status at HEAD:** S7.1 (xl_prev 0-based, ledger #29) is already fixed —
`internal/wal/writer.go` documents the −1 conversion that prevents the
restart-seed bug. S7.2 (atomic heap-update, ledger #27) may be addressed via
pre-assembled `xl_heap_update` envelopes (`RecordKindHeapUpdate` documented as
"the M0080-0002 atomic non-HOT … XLOG_HEAP_UPDATE" at `recovery.go:277-290`);
a verification pass is needed to confirm completeness.

**Subtasks:**
- S7.1 Verify pg_waldump chain traversal across a segment boundary at HEAD
  (confirm the already-landed −1 conversion works).
- S7.2 Audit the heap-update WAL path: confirm `RecordKindHeapUpdate` carries
  both old+new tuple images and the page mutation happens after WAL flush.
- S7.3 If S7.2 reveals a remaining gap, scope the fix (likely single-loop);
  otherwise document the verified-clean state.

**Gates:** UNITS + `WALPgWaldumpCompat` + standby update replay.
**Design doc:** `docs/design/0130-0007-wal-record-fidelity-xlprev-atomic-update.md`.

---

## Theme C — Physical replication PG compatibility

### S8 — Multi-timeline START_REPLICATION + timeline reconciliation (est ~2 loops)

**Sources:** `internal/server/replication.go:161/:427`, `internal/initdb/timeline.go`.

**Subtasks:**
- S8.1 TLI source of truth = pg_control `CheckPoint.ThisTimeLineID`;
  `global/timeline_id` written on promote and read at startup, reconciled
  (never diverged).
- S8.2 IDENTIFY_SYSTEM returns the real TLI (not hardcoded "1").
- S8.3 START_REPLICATION TIMELINE n accepted for n ≤ current TLI (remove the
  single-timeline rejection at `replication.go:427`).
- S8.4 TIMELINE_HISTORY serves the current timeline's history file.
- S8.5 Promotion bumps TLI (1→2→…), updates pg_control, rewrites
  `global/timeline_id`; timeline file naming `0000000<N>...` throughout.
- S8.6 Guards: failover to PG standby → PG promotes → goopg re-attaches on
  the new timeline.

**Gates:** UNITS + `TestE2E_FailoverGoopgToPG` + new multi-TLI guard test.
**Design doc:** `docs/design/0130-0008-multi-timeline-streaming-and-timeline-reconciliation.md`.

### S9 — recovery.signal archive recovery (est ~2 loops)

**Sources:** `internal/initdb/standby.go:35`; `docs/design/0005-0002`.

**Subtasks:**
- S9.1 `recovery.signal` triggers archive-recovery mode: single-pass replay of
  WAL segments fetched via `restore_command`, then promote.
- S9.2 `restore_command` GUC + segment fetch plumbing.
- S9.3 Promote at end of recovery onto a new TLI — or a ledger-recorded
  verdict narrowing PITR scope.

**Gates:** UNITS + archive-recovery guard test (replay to end + promote).
**Design doc:** `docs/design/0130-0009-recovery-signal-archive-recovery.md`.

### S10 — PG 18.3 standby E2E: basebackup + streaming + failover + reverse (est ~2 loops)

**Sources:** `TestE2E_PhysicalReplication`, `TestPort_PgBasebackup*`,
`TestE2E_FailoverGoopgToPG`, `replcluster` harness.

**Subtasks:**
- S10.1 pg_basebackup from goopg primary → PG 18.3 `pg_ctl start` as standby →
  catch-up + read verify.
- S10.2 DDL+DML streaming workload verified on the PG standby (requires S4–S7).
- S10.3 Failover: promote the PG standby, verify writes + TLI switch + re-attach.
- S10.4 Reverse: PG primary → goopg standby on a PG-created data dir (goal 1
  reverse + goal 3 reverse).

**Gates:** the new E2E + the entire existing replication family + UNITS/SMOKE.
**Design doc:** `docs/design/0130-0010-pg183-standby-e2e-harness.md`.

---

## Dependencies

1. **M-NIGHTLY** — standing filing obligation; ordering only, not a block.
2. **B4 COMPLETE** — verified at start (B4.6 Stages 1–3b landed 2026-07-18).
3. **B5 COMPLETE** — all 6 native WAL kinds retired 2026-07-18/19; zero rmid-128
   records emitted. S4/S5/S6 verify, document, and add regression gates.
4. **M0113 pg_index heap infra** (landed) — B5 reused.
5. **M0102 promotion / timeline-history / slots / SyncRep** (landed) —
   S8/S10 reuse.
6. **BASE_BACKUP, WalReceiver `AppendRaw`, StreamReplayer, replcluster harness**
   (landed) — S1/S10 reuse.
7. **Internal ordering:** S4+S5 independent verification tasks; S6 after S4+S5
   (aggregate gate); S10 last.
