# goopg Fix Plan

Roadmap derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md` (§10 "Definition of
Done (Initial Milestone)"). Pick the topmost unchecked item **unless the Current
Priority banner below or a dependency forces another order**.

## Notes / rules

- This is the authoritative TODO list for Ralph. Update it after every meaningful
  change (tick boxes, add newly-discovered follow-ups). ONE item per loop;
  decompose any item larger than a single agent invocation.
- Every non-trivial subsystem must land with (or just before) a design doc under
  `docs/design/<id>-NNNN-*.md` **and** a `docs/design/README.md` index entry —
  hard requirement, same loop.
- Deferrals: never close a task silently with a forward reference. Append one row
  to `.ralph/deferral_ledger.md` (`date | task-id | landed | deferred | resume
  point | why`) and leave the fix_plan item unchecked. **The ledger is the source
  of truth for every "DEFERRED" note below** — consult it for full context/resume
  points.
- Completed milestones are archived under `completed_milestones/` (latest:
  `completed_fix_plan_008.md`); they are reference-only, NOT actionable, and must
  not be copied back here.

## Current Priority (per 2026-06-20 directive)

Work order: **M0117 → M0118** (both complete + archived), then resume **M0110**
(its **M0119-0004/0005/0006/0007** spinoffs are the active, in-progress form of
that work), with **M0095** parked (blocked on logical decoding). **M0120 / M0121
are CLOSED** (2026-07-04) and archived. Policy: fix blockers in place; do NOT
defer unless genuinely compelling (then record a ledger row); commit + push at
every clean, green (build + pre-commit) checkpoint.

**Next up:** the `updateViaIndex` partition/inheritance-child fan-out
discovery is DONE (2026-07-04, design `docs/design/root-0026-update-via-index-inheritance-fanout.md`,
deferral-ledger row flipped to `resolved` — indexed `UPDATE parent SET ...
WHERE indexed_col = X` now correctly falls through to the multi-table
SeqScan path when `tbl` has partition/inheritance children, instead of
silently missing rows that live only in a child). It surfaced a SELECT-side
twin, still open: a bare `SELECT ... WHERE indexed_col = X` (planner
`IndexScan`, `indexScanOp`) has the identical parent-vs-children fan-out gap
(deferral ledger, task-id `M0119-0004 (root-0026 follow-up, this loop)`) —
bound it with a plain two-table INHERITS regression test first, then either
restrict the read-side IndexScan the same way this fix restricts
`updateViaIndex`, or extend the planner's Append/child-probe machinery to
the index-eligible case. Otherwise continue the M0119-0004 pg_dump
catalog-view parity battery / next unresolved DU-002 slice from
`.ralph/deferral_ledger.md`, or pick up an M0122 backlog item.

## Archived — complete (see `completed_milestones/completed_fix_plan_009.md`)

M0117 (CLOG ↔ PostgreSQL subsystem alignment), M0118 (Upstream Isolation Spec
Suite Pass-Through), M0120 (WordPress WP-CLI verification execution + evidence),
M0121 (WordPress WP-CLI verification remediation).

## Archived — complete (see `completed_milestones/completed_fix_plan_008.md`)

M0096 (RC isolation feature impl + spec pass), M0100 (RC isolation runtime
closure / 21-spec pass), M0102 (heterogeneous streaming-replication +
SIGKILL-failover E2E), and the two completed Maintenance fixes
(MAINT-STATEGUARD-RECONCILE, MAINT-TPCH-RELOAD). Earlier milestones:
`completed_fix_plan_001.md` .. `completed_fix_plan_007.md`.

---

## M0095 — Client-Tools TAP Test Porting (filed 2026-05-12)

Design: `docs/design/0095-0003-*`. Goal: port the client-tools-tap suite and the
engine features its `t.Skip`'d scripts need. (`pg_ctl` 001–004 already PASS.)

- [x] **M0095-0002** — `pg_walsummary/002` ported (added `pg_stat_io` virtual view,
      `pg_available_wal_summaries()`; `TestPort_PgWalsummary002Blocks` PASS).
- [ ] **M0095-0003** — `pg_basebackup` 010/011/020 PASS (backup execution,
      `-X stream`/`-X fetch`, manifest + SHA-family checksums, in-place tablespace,
      `READ_REPLICATION_SLOT`). **Remaining:** `030 recvlogical` — blocked on logical
      decoding (not implemented; tracks with the logical-replication milestone / D-004).
      Deferred: on-disk `pg_tablespace` heap visibility (independent shared-catalog
      runtime write — see ledger). **Not actionable until logical decoding lands.**

## M0110 — Additional TAP Test Porting (beyond M0094/M0095) (filed 2026-05-22)

Scope = `docs/test-port/upstream-tap-coverage.md` tests not covered by M0094
(recovery/subscription) or M0095. Tags: SHOULD_PASS / BUG_FIX / UNIMPLEMENTED.
Already complete within M0110 (detail in git history): **M0110-0004** pg_resetwal
(RW-001..004 PASS), **M0110-0007 / M0110-0010** B-tree split & vacuum sibling
prev-link fixes.

- [ ] **M0110-0001 — pg_dump TAP** — `001_basic` ported (DU-001, CLI-only).
      `002–010` (schema dump, dump/restore round-trip, parallel, filter-file,
      connstr) DEFERRED on broad catalog-view parity + round-trip; being advanced
      one catalog gap at a time via the self-promoting
      `TestPort_PgDumpConnectionSetup` guard (CSV row DU-002, slice-by-slice).
      Design `0110-0001-pg-dump-tap-port.md`. Resume = next gap in pg_dump's
      getter battery (latest blocker tracked in `.ralph/working_set.md` / ledger).
- [ ] **M0110-0002 — pg_waldump TAP** — `001_basic` CLI tier ported (WD-001);
      WAL-format readability guarded by W-001 (`TestPort_WALPgWaldumpCompat`).
      **Remaining (WD-002, deferred):** `002_save_fullpage` — needs goopg to emit
      PG-decodable FPI/heap WAL with backup blocks (+ hash/gin/gist/spgist/brin AMs
      for the server tier). Design `0110-0002-*`.
- [ ] **M0110-0003 — pg_amcheck TAP** — `001_basic` (AC-001) + `002_nonesuch`
      (AC-002) ported; CREATE SCHEMA + user-schema table restart-durability enablers
      landed. **Remaining (AC-003, deferred):** `003_check`, `004_verify_heapam`,
      `005_opclass_damage` — need `verify_heapam()` SRF + opclass catalog parity +
      index AMs. (One 002 sub-section deferred: `datconnlimit=-2` invalid-DB filter —
      runtime shared-catalog write.) Design `0110-0003-*`.

## M0119 — Deferral-Ledger Backlog Consumption (filed 2026-06-29)

Milestone: `docs/milestones/0119-deferral-ledger-backlog-consumption.md`
(**living milestone** — tasks are appended over time). Source of truth:
`.ralph/deferral_ledger.md`. Goal: drive every open (`status = -`) ledger row to
closure — implement the deferred scope, or verify it already landed and mark the
row `resolved`.

**Per-task rule (applies to every M0119 implementation task):** before
implementation begins, the picking agent MUST (1) create a design doc at
`docs/design/<source-id>-NNNN-*.md` and index it in `docs/design/README.md`, and
(2) have that design doc pass an agent review. Implementation starts only after
the reviewed design doc exists. (The triage task M0119-0001 was doc-only, exempt.)

**Already landed (see git history / deferral ledger):** M0119-0001 triage
(2026-06-29: 224 open rows → 178 resolved, 46 remain), M0119-0002 (CLOG tail),
M0119-0003 (initdb options — empty backlog), M0119-0008 (isolation residual —
only the infeasible `deadlock-parallel` spec remains), M0119-0009 (UPDATE/DELETE
conflict-wait), plus the landed sub-slices of -0004 (NULLS NOT DISTINCT
enforcement + upsert arbiter) and -0005 (pg_waldump WD-003/WD-004 canonical
prune-WAL round-trip). The four open items below carry the remaining unbuilt scope.

- [ ] **M0119-0004 — pg_dump 002–010 TAP** (source: M0110-0001). Schema dump,
      dump/restore round-trip, parallel, filter-file, connstr — advance the
      catalog-view parity battery slice-by-slice (guard
      `TestPort_PgDumpConnectionSetup`; resume = next catalog getter gap tracked in
      `.ralph/working_set.md` / ledger). Two general SQL-engine gaps surfaced here
      remain: deferred-constraint *checking at COMMIT* (goopg checks immediately)
      and any residual dump-fidelity items.
- [ ] **M0119-0005 — pg_waldump server tier** (source: M0110-0002). `002_save_fullpage`
      (WD-003) + live `pg_waldump --rmgr=Heap2` round-trip DONE. **Still open:** only
      `001_basic.pl`'s server-dependent tier (per-rmgr/relation/block filtering) —
      needs hash/gin/gist/spgist/brin index AMs.
- [ ] **M0119-0006 — pg_amcheck server tier** (source: M0110-0003). `002_nonesuch`
      … `005_opclass_damage`; `CREATE EXTENSION amcheck` + `verify_heapam()` SRF on
      top of `internal/amcheck` + opclass catalog parity. Largest open cluster
      (~29 ledger rows): index AMs, `box`/`int4range`/`int4[]` types, STORAGE
      EXTERNAL TOAST corruption, the heapallindexed heap-scan producer, and the
      `datconnlimit = -2` invalid-DB filter (runtime shared-catalog write).
- [ ] **M0119-0007 — pg_basebackup recvlogical** (source: M0095-0003). `030 recvlogical`
      — blocked on logical decoding (tracks the logical-replication milestone / D-004).

> This task list is **seeded, not exhaustive.** M0119-0001 triage plus every future
> deferral-ledger entry (any new `status = -` row) feed additional M0119 tasks over
> time; the milestone's living nature means it need not be complete at filing.

## M0122 — Unimplemented-Feature Backlog Consumption (filed 2026-07-04)

Milestone: `docs/milestones/0122-unimplemented-feature-backlog-consumption.md`
(**living milestone** — tasks are appended over time). Source of truth:
`unimplemented_feat.json` (repo root; 181 entries generated 2026-07-02 from the
commit log). Goal: drive every `open` feature entry to closure — implement the
deferred scope, or verify it already landed and mark the entry `resolved`.

**⚠️ Verify-before-implement (READ FIRST):** `unimplemented_feat.json` is a
2026-07-02 snapshot and **may list features that are already implemented** — 24
entries have an `unclear`/absent `code_audit` and 61 have an open matching ledger
row (7 overlap both). When you pick up ANY M0122 task, FIRST re-verify each
candidate against current HEAD (grep/read code, probe a live goopg, check
ledger/fix_plan/git log). If it already exists, set the entry's `status` to
`resolved` (cite the proof) and DO NOT re-implement. Only build genuinely-missing
scope.

**Per-task rule (applies to every M0122 implementation task):** before
implementation begins, the picking agent MUST (1) create a design doc at
`docs/design/<id>-NNNN-*.md` and index it in `docs/design/README.md`, and (2) have
that design doc pass an agent review. Implementation starts only after the
reviewed design doc exists. (The triage task M0122-0001 is doc-only, exempt.)
Tracking field = a per-entry `status` (`open`/`resolved`) added by M0122-0001,
mirroring M0119's ledger `status` column.

- [ ] **M0122-0001 — Backlog triage / re-verification pass** (doc-only, exempt).
      Re-audit all 181 entries vs current HEAD; add the `status` field (init
      `open`/`resolved`); resolve the already-done ones — start with the 24
      `unclear`/no-audit + 61 `resolution_check.ledger=open` entries (7 overlap).
      Dedupe against M0119 + `.ralph/deferral_ledger.md` so nothing is worked
      twice. This task discharges the "may already be implemented" risk.
- [ ] **M0122-0002 — Catalog system functions & pg_* view stubs** (~9). Quick wins:
      `pg_relation_size`/`pg_total_relation_size`, `regexp_matches`, `pg_get_expr`,
      `isfinite`, `justify_*`, `pg_get_serial_sequence`, `pg_get_indexdef` recon.
- [ ] **M0122-0003 — EXPLAIN output & pg_stat instrumentation** (~7). EXPLAIN
      FORMAT XML/YAML, SETTINGS/BUFFERS rendering, `pg_stat_io` virtual table,
      per-CTE stats, `track_io_timing` runtime SET.
- [ ] **M0122-0004 — SQL language / executor features** (~21). Window frame
      ROWS/RANGE/GROUPS, GROUPING SETS/ROLLUP/CUBE, ANY/SOME/ALL, DEFAULT-clause
      parsing, intervals, BETWEEN SYMMETRIC, CTE-without-alias, WITH CHECK OPTION.
- [ ] **M0122-0005 — Types / opclasses / casts / collation / domains** (~11).
      1-byte `char`(OID 18) disambiguation, `pg_collation_for`, function-based cast
      dumping, ALTER TYPE RENAME/OWNER, domain CHECK renderer, `pg_ts_config` OIDs.
- [ ] **M0122-0006 — On-disk catalog persistence & shared catalogs** (~8).
      Persistent `pg_index` heap, index column order (ASC/DESC/NULLS) across
      restart, `pg_tablespace` visibility, `pg_database.datconnlimit` write.
- [ ] **M0122-0007 — DDL / admin commands / ctl / GUC config** (~14). CREATE/DROP
      DATABASE full DDL, `goopg ctl restart`, REINDEX, SIGHUP config reload,
      tablespaces, ALTER FUNCTION/COLUMN, planner/jit GUC stubs.
- [ ] **M0122-0008 — Auth / roles / multi-DB isolation / encoding** (~6). SASLprep
      / channel binding / `scram_iterations`, RBAC + `SET SESSION AUTHORIZATION`,
      encoding constraints during bootstrap/runtime.
- [ ] **M0122-0009 — WAL / recovery / crash-consistency infra** (~16). WAL segment
      recycling, `WALInsertLock` array (parallel inserts), MultiXact WAL,
      `pg_subtrans` truncation. Gate: `-race` + recovery E2E (WAL practice card).
- [ ] **M0122-0010 — Concurrency: buffer pool & btree locking** (~17, LARGE).
      Lehman/Yao crab-walk, `splitMu` removal, storage-pool pin-count race,
      re-enable the `-race` gate. Gate: race detector mandatory.
- [ ] **M0122-0011 — Query optimizer & TPC-H/HammerDB correctness** (~17). Anti/
      semi-join unnesting (NOT IN), Q8/Q9/Q21 row-count fixes; several blocked on
      the slot/TupleSlot pipeline (see M0122-0012). Gate: TPC-H spot-check.
- [ ] **M0122-0012 — Perf infra: vectorization / slot-pipeline / harness** (~19,
      ARCHITECTURAL). Borrow-semantics allocation rewrite, plannode migration,
      vectorized FilterOp/SeqScanOp, plan cache, HammerDB SF1 validation.
- [ ] **M0122-0013 — Physical/streaming replication & standby** (~10, EPIC/blocked).
      Streaming-replication epic (~25 sub-items), cascading replication,
      `STANDBY_SNAPSHOT_READY` transition.
- [ ] **M0122-0014 — Logical replication / decoding / subscription** (~11, EPIC).
      pgoutput DELETE identity, subscriber apply worker, DDL replication. Blocked
      on logical decoding (tracks D-004; overlaps M0119-0007 — dedupe).
- [ ] **M0122-0015 — Test-suite porting: amcheck / verify_heapam / pg_dump** (~8).
      `verify_heapam()` SRF + opclass parity, AC-002..005, pg_dump 002-010.
      **Overlaps M0119-0004/0006 — the triage assigns each item to ONE milestone;
      do not double-work.**

> This task list is **seeded, not exhaustive.** The M0122-0001 triage plus every
> future feature deferral appended to `unimplemented_feat.json` (any new `open`
> entry) feed additional M0122 tasks over time; the milestone's living nature
> means it need not be complete at filing. Small/residual entries (TOAST
> compression, autovacuum, FDW/HANDLER stub, GIST, LANGUAGE C) fold into the
> nearest cluster by the triage.
