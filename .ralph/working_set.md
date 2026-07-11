(idle — nothing in flight)

## Loop summary (2026-07-12, loop #75)

**Resumed cut-off loop #74's in-flight WIP** (working_set was stale from #73):
`pg_stat_all_tables` / `pg_stat_user_tables` / `pg_stat_sys_tables` system views
(M0122-0003 sub-slice). Files were already written+building; I finished it:
- Verified build + targeted/full catalog & executor package tests PASS.
- Wrote the MISSING referenced design doc
  `docs/design/0122-0003-pg-stat-user-tables.md` + README index row.
- Appended deferral-ledger row (missing per-table counter subsystem;
  empty pg_stat_sys_tables since system catalogs are storage-less).
- Added the fix_plan M0122-0003 banner note.
- Gates: catalog+executor full PASS; tpch-spotcheck PASS (Q12=2/Q13=33);
  pgbench smoke via pre-commit hook. Committed.

**Nightly triage — batch `20260712-020530` (39 items):** NOT real regressions.
Evidence log (`ci/logs/20260712-020530/testport/go-test.log`) shows the failures
are all `dial tcp 127.0.0.1:39219: connect: connection refused` — a single
co-load server-unavailability cascade (TPC-H co-load starves initdb/server
start, known signature). No new M-NIGHTLY tasks added; do NOT treat these 39 as
independent product regressions. If they recur in an ISOLATED (non-co-load) run,
re-triage.

In-flight: none
