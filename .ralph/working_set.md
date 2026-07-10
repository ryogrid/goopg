(idle — nothing in flight)

## Loop summary (2026-07-10, loop #7)

**Outcome: M0122-0007 4e follow-up 43 — extended `CREATE DATABASE ...
TEMPLATE`'s relation-copy mechanism (follow-ups 40/41/42) to also cover
materialized views — implemented, independently verified, committed.**

- Nightly triage: sole action item AI-20260710-011513-001 (build failure)
  already had a closed M-NIGHTLY task from loop #6; `go build ./...` clean
  at loop start — no new M-NIGHTLY task needed.
- Picked the next resume point per follow-up 42's own deferral row:
  matview TEMPLATE copying, reusing follow-up 42's AST-copy half plus
  follow-up 40's physical-relation-file-copy half.
- Unlike a plain view, a matview's pg_class row is NOT `Virtual`
  (`execCreateMatView`'s `CreateTable` call) — it already surfaces via the
  existing `tmpl.AllTables(oid)` loop, so no new `AllMatViews` registry
  method was needed (unlike `AllViews` for plain views). Split the
  `IsMatView || OfTypeOID != 0` unsupported branch so only typed tables
  stay unsupported; matviews collect into a new `matViews` 5th return value.
  New `copyTemplateMatViews` (`internal/server/database_ddl.go`) combines
  `copyTemplateTables`' physical-file-copy discipline with
  `copyTemplateViews`' AST/ViewDef/IsPopulated field-copy discipline.
  `syncCopiedTableCatalogHeap`/`rollbackTemplateCopy` needed zero changes
  (both already generic/already sweep non-Virtual rows).
- **Real, independently-discovered correctness bug found and fixed:**
  `execCreateMatView`/`execRefreshMatView` validated their query via the
  raw, dbOid-unaware `o.ctx.Catalog` instead of `o.planCatalog()` (the
  search-path/dbOid-aware wrapper every sibling `planner.Plan` call in the
  same functions already used) — `CREATE`/`REFRESH MATERIALIZED VIEW`
  referencing a same-database table falsely raised `42P01` on ANY
  non-default database. Same shape as the `ctxPlanCatalog` gap
  4d-ii-part-2b item 3 fixed for `CREATE VIEW`, but that fix never touched
  these two matview functions' own separate `analyzer.Analyze` calls.
  Caught only because this loop's own new E2E test was the first to
  exercise `CREATE`/`REFRESH MATERIALIZED VIEW` under a non-default
  database at all. Fixed both call sites to use `o.planCatalog()`
  (`internal/executor/operators_ddl.go`).
- Repurposed the now-stale `TestTryHandleDatabaseDDLCreateTemplateWithMatViewErrors`
  (previously pinned matviews as unsupported) into
  `...WithMatViewCopies`, mirroring `...WithViewCopies`'s shape. New E2E
  `TestCreateDatabaseTemplateMatViewCopiesDataAndSurvivesRestart`
  (`internal/server/database_template_copy_restart_test.go`): real wire
  protocol, table + populated matview, TEMPLATE copy, immediate data
  visibility, physical independence (insert+refresh on the copy leaves the
  source unchanged), and restart durability.
- Verified independently: `go build`/`go vet ./...` clean; `go test
  ./internal/server/... ./internal/catalog/... ./internal/executor/...
  ./internal/initdb/...` PASS; `-race` on server+catalog+executor PASS
  except the pre-existing `TestConnectExceedsPositiveDatconnlimitRejected`
  (re-confirmed via `git stash` against unmodified HEAD, reproduces
  identically — no other flaky tests surfaced this run); `go test -short`
  full repo (excl. testport) PASS; `tpch-spotcheck.sh` PASS (Q12=2/Q13=33);
  pgbench smoke PASS (0 failed, all 3 workloads).
- Updated fix_plan.md (follow-up-43 entry), deferral_ledger.md (new row),
  design doc `0122-0018-...md` (new subsection + status line + 2 other
  mentions), and `docs/design/README.md` index row.

**Next natural M0122-0007 work:** index/typed-table TEMPLATE copying
(index-file cloning + per-database sys-btree catalog bootstrap;
composite-type OID resolution for typed tables) — see follow-up 43's
deferral ledger row for the resume point; OR the independent per-database
index/type catalog-row + sys-btree bootstrap gap itself; OR move to a
different M0122-00xx milestone for variety.

Gates run: go build, go vet, go test (server/catalog/executor/initdb,
fresh), go test -race (server/catalog/executor, 1 pre-existing unrelated
failure re-confirmed via stash), go test -short full repo (excl.
testport), tpch-spotcheck, pgbench smoke, ralph-state-guard (self-repaired
a stale running/completed mismatch) — all PASS.
In-flight: none
