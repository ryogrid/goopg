Task: M0122-0007 4e (`CREATE DATABASE ... TEMPLATE` epic). This loop fixed and
committed the pg_class-under-fresh-database gap (follow-up 23, commit
1edd6ede). Epic's only remaining item is the real relation-copy mechanism.

Files (this loop, committed): internal/catalog/catalog.go (LookupTable
fallback + PGClassRowsForDBOid), internal/executor/context.go (PgClassRows
field), internal/executor/operators.go (valuesOp.Open pg_class branch),
internal/server/dispatch.go (wireExtensionRows wiring + pgClassRowLister),
internal/executor/fk_dbid_routing_test.go (2 new tests), design doc +
README + fix_plan.md + deferral_ledger.md.

Key symbols: `catalog.InMemory.LookupTable` (10751ish) — new
`lookupSystemCatalogTableLocked` fallback, generic for ALL pg_catalog/
information_schema names. `catalog.InMemory.PGClassRowsForDBOid` (~5823) —
extracted pg_class's former inline VirtualRows closure, now parameterized on
dbOid; `registerSystemTables`'s VirtualRows still calls it with DefaultDBOid
(byte-identical default). `executor.Context.PgClassRows`, wired in
`server.wireExtensionRows` via new `pgClassRowLister` interface.

Findings (confirmed, don't re-derive):
- Two distinct bugs existed: (A) NAME resolution — pg_catalog/
  information_schema tables are registered ONLY under DefaultDBOid's
  namespace, so ANY distinct dbOid's connection got 42P01 on system-catalog
  names. Fixed generically for all ~70 such tables. (B) ROW CONTENT — even
  once resolvable, pg_class's VirtualRows always enumerated DefaultDBOid's
  tables regardless of connection. Fixed for pg_class only this loop.
- ~13 sibling virtual builders (pg_indexes, pg_tables, pg_attrdef,
  pg_constraint, pg_inherits, pg_index, pg_statistic_ext, pg_policy,
  pg_depend, pg_trigger, pg_rewrite, information_schema.routines/parameters/
  routine_*_usage, pg_foreign_table) share the same c.ns(DefaultDBOid)-
  hardcoded VirtualRows pattern — now Bug-A-fixed (no more 42P01) but still
  Bug-B-open (list DefaultDBOid's objects regardless of connection).
  pg_sequence/pg_sequences already separately flagged by an earlier ledger
  row. Recorded in deferral ledger row "pg_class-under-fresh-database"
  (2026-07-10) with the exact per-table resume pattern (mirror pg_class's
  closure-extraction: PGXxxRowsForDBOid + Context field + wireExtensionRows
  site + valuesOp.Open branch), one table per future loop.
- pg_attribute/pg_type (heap-backed, not VirtualRows) are a DIFFERENT,
  deeper gap: their physical heap rows are hardcoded DBOid=DefaultDBOid in
  operators_ddl.go's syncTableToCatalogHeap — a heap-write/heap-scan fix,
  not a VirtualRows one. Not touched this loop.
- Live end-to-end verified via a real cmd/goopg binary + psql (LD_LIBRARY_PATH
  must include postgres/local_install/lib or psql fails with a symbol lookup
  error — undocumented gotcha, worth a memory if it recurs): CREATE DATABASE
  freshdb -> connect -> pg_class query succeeds -> CREATE TABLE -> \dt shows
  only that table, postgres db's own \dt unaffected.

Next step: pick ONE of the ~13 deferred virtual builders (pg_tables and
pg_indexes are the next highest-value targets, since psql's \d/\dt-family
commands and many compat-tool catalog joins touch them) and apply the same
closure-extraction pattern PGClassRowsForDBOid used. OR start the actual
CREATE DATABASE ... TEMPLATE relation-copy mechanism (design doc's
"Remaining 4e work" section has the sketch) now that pg_class works
end-to-end for verifying it. Either is a reasonable next pick.

Gates run: go build/vet ./... clean; go test ./internal/catalog/...
./internal/executor/... ./internal/server/... ./internal/planner/... PASS;
go test -short (full repo, non-testport) PASS; tpch-spotcheck PASS
(Q12=2/Q13=33); pgbench smoke PASS (0 failed, all 3 workloads, twice —
standalone + pre-commit hook). make ralph-state-guard OK (no repair needed).
Nightly triage: ci/logs/action-items.md's single AI-20260710-011513-001
(build regression) was already fixed + closed in fix_plan.md by a prior
loop (follow-up 14, commit abbf7de1) before this loop started — no new
M-NIGHTLY task needed; `make build` verified clean at loop start.

In-flight: none. tmp/manual-pgclass-test/ (scratch server used for the live
verification) was removed after use.
