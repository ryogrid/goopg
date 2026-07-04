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

**Next up:** the `updateViaIndex` partition/inheritance-child fan-out discovery
(project-wide — start with a plain non-view two-table `INHERITS` regression test
to bound the gap before touching `updateViaIndex`), OR continue the M0119-0004
pg_dump catalog-view parity battery / next unresolved DU-002 slice from
`.ralph/deferral_ledger.md`.

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
