Task: M0125-0029 (warm-stats programme item 2/4) — DONE and committed this loop
(#9, 2026-07-30). Stats survive restart for every database, visible to every
connection.

Files:
- `internal/catalog/catalog.go` — NEW `GoopgRelStatsRelationId` (9410) sidecar constant
- `internal/executor/operators_analyze.go` — per-DB routing (`tableCatalogHeapDBOid`),
  per-column resilience (first error kept, remaining columns + size row written),
  `GoopgRelStatsColumns()`, sidecar size row write
- `internal/initdb/open.go` — `loadStatisticsFromHeap` iterates ALL databases via
  `loadStatisticsFromHeapForDB`; sidecar scanner + size restoration
- `internal/server/stats_dbid_restart_test.go` — NEW, 2 E2E restart-durability pins
- `scripts/tpch-relsize-arm.sh` — w-arm warm-up via durable ANALYZE, stale guard
  text retired
- `docs/design/0125-0028-warm-stats-programme.md` — §-0029a execution record
- `.ralph/deferral_ledger.md` — 1 new row (TOAST gap for pg_statistic histograms)
- `.ralph/fix_plan.md` — M0125-0029 ticked [x], NEXT → M0125-0030

## Facts the next loop should NOT re-derive

- Three gaps closed together: (1) per-DB routing via `tableCatalogHeapDBOid(ctx)`,
  (2) reltuples/relpages via sidecar heap 9410, (3) cross-connection visibility
  — re-verified on the SECOND session of the E2E test.
- Per-column resilience: a wide histogram that exceeds a page (partsupp.ps_comment
  etc.) no longer aborts the entire ANALYZE. The write fails silently for that
  column only; the remaining columns and the size row are still written. First
  error is kept for caller bookkeeping.
- TOAST gap (deferred): goopg's catalog heap writer has no TOAST, so wide
  histograms are silently absent — ledger row. Real PG toasts via pg_statistic.h.
- The 2026-07-23 "per-connection stats" symptom did NOT reproduce; cross-connection
  visibility is verified by the E2E test (second session reads same reltuples).
- `tpch-relsize-arm.sh` now does its own ANALYZE warm-up when `ARM_ANALYZE=1` —
  retired the need for `cmd/tpch-runner -analyze`.

## NEXT (banner order)

**`M0125-0030`** (bench clusters warm-up + CHECKPOINT) per the 2026-07-30(b)
directive; then `-0031` (warm-stats planning line), then
`M0125-0002` commit 3 / `M0125-0003` four-arm study / `M0125-0026`.

Gates run: units precommit PASS; 2 new E2E restart pins PASS; tpch-spotcheck
PASS Q12=2/Q13=35 (cached, unchanged); go build/vet clean; ralph-state-guard
(to be run); pgbench smoke via commit hook.

In-flight: none.
