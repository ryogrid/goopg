# Milestone 0053 — HammerDB TPC-H Complete Run Verification & Report

**Status:** in-progress
**Depends on:** Milestone 0052 (oversized-message fix), Milestone 0041 (TPC-H parity), Milestone 0051 (planner expression improvements)
**Drives:** Authoritative end-to-end evidence that goopg can complete the full HammerDB TPC-H workflow without human intervention.

## Context

Milestone 0052 resolved the `ORDERS/LINEITEM` COPY regression (backend
disconnect caused by `MaxRegularMessageLength = 1 MiB` being exceeded by
HammerDB's batched INSERT, landed 2026-05-05). Run-010 confirmed the load
phase completes, but the power-test section was written while Q9–Q22 were
still executing and was never completed as a finished report.

This milestone performs a **clean, unambiguous, fully-attended end-to-end
run** of HammerDB TPC-H SF=1 against the current `perf-analysis` HEAD,
using **only `hammerdbcli`** — no manual `psql` DDL for tables, indexes,
or statistics. Results are captured in a structured English report.

## Scope

The run covers the following phases, exactly as driven by HammerDB:

| Phase | HammerDB action | Success criterion |
|-------|----------------|-------------------|
| Schema build | `buildschema` Tcl script | 8 tables created, no error |
| Data load | `loaddata` COPY path | All 8 tables at SF=1 row counts |
| Index creation | HammerDB `createsqlindex` Tcl | All PRIMARY KEY + FK + supplementary indexes created |
| ANALYZE | HammerDB `runanalyze` Tcl | Completes without error |
| Power test (Q1–Q22) | HammerDB `runtimer` Tcl | All 22 queries return results; timing recorded |

**Out of scope:** multi-user throughput test, result-parity verification
against upstream PostgreSQL (that is M0041 territory), performance
regression investigation.

## Execution Constraints

- Use **only `hammerdbcli`** for DDL and DML. No `psql` table/index creation.
- The benchmark is long-running (60–120 min at SF=1); execute via `nohup`
  with a 2-hour wall-clock timeout and monitor by tailing logs.
- Abort and report `PARTIAL` if the 2-hour timeout is reached before Q22.
- The fresh cluster must be set up with `setup_goopg.sh --reset`.

## Required Design Docs

- `docs/design/0053-0001-hammerdb-tpch-run-verification-procedure.md` — run
  procedure, timeout strategy, log-collection, and report schema.

## Definition of Done

1. A fresh goopg cluster is started with `setup_goopg.sh --reset`.
2. `build_schema_goopg.sh` completes without error (all 8 TPC-H tables present
   at SF=1 row counts).
3. HammerDB's `createsqlindex` step creates all required indexes without
   persistent failures (a single transient retry is acceptable if the final
   state is correct).
4. HammerDB's ANALYZE step completes.
5. The power test runs all 22 queries (Q1–Q22) through `hammerdbcli` alone.
6. A report `analysis/tpch-hammerdb-run-NNN.md` is written covering:
   - Row counts per table after load
   - Index creation outcomes (success / failure / transient)
   - ANALYZE outcome
   - Per-query timing for Q1–Q22
   - Overall pass/fail summary
   - Comparison table with run-010
7. `fix_plan.md` task statuses are updated to reflect pass/fail outcomes.
8. All changes are committed and pushed.

## Reference

- Benchmark scripts: `bench/tpch/`
- Previous run analysis: `analysis/tpch-hammerdb-run-010.md`
- Regression fix: `docs/design/0052-0001-oversized-message-graceful-recovery.md`
- HammerDB docs: `HammerDB-5.0/doc/`
