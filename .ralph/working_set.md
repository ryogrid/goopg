Task: M0125-0028 (warm-stats programme item 2/4) — DONE and committed this loop
(#8, 2026-07-30 evening). ANALYZE/VACUUM named targets + bare `ANALYZE;` now
resolve in the connection's database.

Files: `internal/executor/operators_analyze.go` (expandAnalyzeTargets via
ctxPlanCatalog; bare-ANALYZE current-DB form; `targets()` deleted),
`internal/executor/operators_vacuum.go` (named-target twins via ctxPlanCatalog;
`relationStillExists` → LookupTableByOIDAllDBs; PartitionChildren nsOid),
`internal/catalog/catalog.go` (NEW `UserTableHandles` — live handles, vs
AllTables' deep copies), `internal/executor/analyze_dbid_routing_test.go`
(NEW, 3 pins), design 0125-0028 §-0028a, README index, fix_plan, ledger
(1 resolved + 3 new rows).

## Facts the next loop should NOT re-derive

- **probe-analyze FLIPPED**: `ANALYZE lineitem` in db `tpch` OK,
  reltuples=5,997,241 (exact). **A SECOND session saw the stats too** — the
  2026-07-23 "per-connection stats" symptom did NOT reproduce; -0029's gap-3
  "per-connection Table copies" mechanism is doubtful (SetTableStats mutates
  the shared live pointer). Re-verify in -0029, don't assume.
- All 3 pins proven to fail pre-fix (42P01 / silent no-op / silent skip) via
  `git stash push -- <2 files>` + run + pop.
- Deferred (ledger 2026-07-30 ×3): db-wide VACUUM still DefaultDBOid via
  AllTables DEEP COPIES (UpdateRelStats/RelFrozenXID writes silently LOST —
  freeze-bookkeeping change, own loop); bare ANALYZE can't reach heap system
  catalogs (none registered in ns.tables); `VACUUM <missing>` should be 42P01.
- Bench server 65433 was restarted with a fresh HEAD build
  (tmp/goopg-bench-bin) after the probe; region count verified. Nightly next
  fires 2026-07-31 00:00 JST.
- -0029 resume points: `persistStatsToPGStatistic` hardcodes
  `DBOid: catalog.DefaultDBOid` (operators_analyze.go:~200);
  `loadStatisticsFromHeap` reads only `cat.DBOID()` (initdb/open.go:3479);
  RowCount/Pages persistence needs the goopg-private mechanism (waiver);
  restart probe must run on a durable-config cluster (no --no-sync/fsync=off).

## NEXT (banner order)

**`M0125-0029`** (stats survive restart, every DB, every connection) per the
2026-07-30(b) directive; then `-0030` (bench warm-up + CHECKPOINT, premise
flip commit), then `-0002` commit 3 / `-0003` four-arm study / `-0026`.

Gates run: units precommit PASS; go build/vet clean; 3 new pins PASS (and
fail-before proven); tpch-spotcheck PASS Q12=2/Q13=35 (33.0 s, unchanged);
probe-analyze acceptance flipped; ralph-state-guard OK (1 auto-repair:
progress marker reconcile); pgbench smoke via commit hook.

In-flight: none.
