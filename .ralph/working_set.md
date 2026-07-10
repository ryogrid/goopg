Task: M0122-0007 4e (`CREATE DATABASE ... TEMPLATE` epic). This loop fixed and
committed the pg_foreign_table row-content gap (follow-up 32, mirrors
follow-up 31's pg_rewrite fix — identical shape). 2 sibling builders + the
real relation-copy mechanism remain.

Files (this loop, committed): internal/catalog/catalog.go (new
PGForeignTableRowsForDBOid(dbOid uint32), extracted from pg_foreign_table's
inline VirtualRows closure — parameterized its 2 c.ns(DefaultDBOid)
references), internal/executor/context.go (PgForeignTableRows field),
internal/executor/operators.go (valuesOp.Open branch for pg_foreign_table),
internal/server/dispatch.go (wireExtensionRows wiring + pgForeignTableRowLister
interface), internal/executor/fk_dbid_routing_test.go
(TestPgForeignTableRowsScopedToConnectionDBOid), design doc + README +
fix_plan.md + deferral_ledger.md (1 new resolved row: "pg_foreign_table
per-dbOid content").

Key symbols: `catalog.InMemory.PGForeignTableRowsForDBOid(dbOid)` — single
builder, same shape as PGRewriteRowsForDBOid. `executor.Context.
PgForeignTableRows`, wired in `server.wireExtensionRows` via new
`pgForeignTableRowLister` interface (mirrors `pgRewriteRowLister`).

Findings (confirmed, don't re-derive):
- 2 sibling virtual builders still share the same c.ns(DefaultDBOid)-hardcoded
  VirtualRows pattern (Bug-A name-resolution already fixed by follow-up 23's
  generic LookupTable fallback; Bug-B row-content still open): pg_statistic_ext,
  information_schema.routines/parameters/routine_*_usage.
  - pg_statistic_ext is NOT a simple closure-extraction target — its
    VirtualRows reads c.statisticsObjs, a process-global
    map[string]*StatisticsObject with NO dbOid concept anywhere, so fixing
    it needs the bigger "give the underlying registry a dbOid key" treatment
    (same shape as the still-open seqRegistry/SequenceParamsFunc gap below),
    not just parameterizing a c.ns(...) call.
  - information_schema.routines/parameters/routine_*_usage has not yet been
    inspected for which shape it is — next loop should inspect this first;
    if it's a simple table-level-slice-over-c.ns(dbOid) shape, it's the next
    pick; otherwise switch to the relation-copy mechanism or another item.
- NEW this loop (deferred, not fixed): pg_foreign_table's ftserver OID lookup
  (`c.foreignServers[t.ForeignServerName]`) resolves against a single
  process-global map[string]*ForeignServer with NO dbOid key — CREATE SERVER
  has no per-database namespacing, so identically-named servers in two
  databases would collide (same shape as statisticsObjs/seqRegistry gaps).
  Not exercised by the live verification below (each test DB used a
  distinctly-named server), so it's a latent gap, not a regression.
- pg_sequence/pg_sequences/information_schema.sequences remain separately
  flagged by the "sequence ownership follow-on" ledger row (needs the full
  "thread DBName->Context" per_connection_virtual_catalog_scoping treatment;
  concrete resume point: give catalog.SequenceParamsFunc a trailing
  dbOid ...uint32 param).
- pg_attribute/pg_type (heap-backed, not VirtualRows) remain the structurally
  deeper gap — untouched, needs a heap-write/heap-scan fix in
  syncTableToCatalogHeap, not a VirtualRows one.
- oid::regclass (OID->name cast direction) still resolves against
  DefaultDBOid's pg_class only — separate cast/output-function mechanism,
  not fixed, own deferral-ledger row from follow-up 26.
- Live end-to-end verified via a real cmd/goopg binary + psql
  (LD_LIBRARY_PATH must include postgres/local_install/lib; goopg CLI is
  subcommand-based: `goopg init -D <dir>` then `goopg start -D <dir> -listen
  host:port`, NOT `-datadir`/`-p`): CREATE DATABASE ftA -> CREATE DATABASE
  ftB -> ftA: CREATE SERVER srv_a FOREIGN DATA WRAPPER goopg_fdw; CREATE
  FOREIGN TABLE ta_ft (a int) SERVER srv_a -> ftB: same pattern with
  srv_b/tb_ft -> SELECT relname,ftserver FROM pg_foreign_table JOIN pg_class
  in ftA returns exactly ta_ft, same query in ftB returns exactly tb_ft — no
  cross-database leak either direction.

Next step: inspect information_schema.routines/parameters/routine_*_usage's
VirtualRows closure to determine if it's a simple table-level-slice-over-
c.ns(dbOid) shape like pg_trigger/pg_rewrite/pg_foreign_table were; if so it
becomes follow-up 33. If it turns out to need the bigger registry-dbOid-key
treatment (like pg_statistic_ext), consider switching to the actual CREATE
DATABASE ... TEMPLATE relation-copy mechanism (design doc's "Remaining 4e
work" section has the sketch) — the 10 prerequisite virtual-table fixes
(pg_class/pg_indexes/pg_tables/pg_constraint/pg_index/pg_attrdef/pg_depend/
pg_inherits/pg_policy/pg_trigger/pg_rewrite/pg_foreign_table) all work
end-to-end for verifying it now. OR pick up the oid::regclass cast gap, OR
give SequenceParamsFunc a dbOid parameter, OR give c.foreignServers a dbOid
key (this loop's new discovery).

Gates run: go build/vet ./... clean; go test ./internal/catalog/...
./internal/executor/... ./internal/server/... ./internal/planner/... PASS;
tpch-spotcheck PASS (Q12=2/Q13=33). pgbench smoke will run automatically via
the pre-commit hook at commit time (not yet re-run standalone this loop).
make ralph-state-guard: ran and PASSED (auto-repaired a stale
status/progress completed-vs-running mismatch left over from the previous
loop's clean exit marker).
Nightly triage: ci/logs/action-items.md's single AI-20260710-011513-001
(build regression) was already fixed + closed in fix_plan.md by a prior loop
(follow-up 14, commit abbf7de1); verified `make build` clean at this loop's
start too; no new M-NIGHTLY task needed.

In-flight: none. /tmp/goopg-ft-verify-data (scratch server used for the live
verification) was removed after use.
