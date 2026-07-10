Task: M0122-0007 4e (`CREATE DATABASE ... TEMPLATE` epic). This loop fixed and
committed the pg_rewrite row-content gap (follow-up 31, mirrors follow-up 30's
pg_trigger fix). 3 sibling builders + the real relation-copy mechanism remain.

Files (this loop, to be committed): internal/catalog/catalog.go (new
PGRewriteRowsForDBOid(dbOid uint32), extracted from pg_rewrite's inline
VirtualRows closure — parameterized its one c.ns(DefaultDBOid) reference, the
loop over every table's Rules), internal/executor/context.go
(PgRewriteRows field), internal/executor/operators.go (valuesOp.Open branch
for pg_rewrite), internal/server/dispatch.go (wireExtensionRows wiring +
pgRewriteRowLister interface), internal/executor/fk_dbid_routing_test.go
(TestPgRewriteRowsScopedToConnectionDBOid), design doc + README +
fix_plan.md + deferral_ledger.md (1 new resolved row: "pg_rewrite per-dbOid
content").

Key symbols: `catalog.InMemory.PGRewriteRowsForDBOid(dbOid)` — single
builder, no cross-builder oid-numbering coupling. `executor.Context.
PgRewriteRows`, wired in `server.wireExtensionRows` via new
`pgRewriteRowLister` interface (mirrors `pgTriggerRowLister`).

Findings (confirmed, don't re-derive):
- 3 sibling virtual builders still share the same c.ns(DefaultDBOid)-hardcoded
  VirtualRows pattern (Bug-A name-resolution already fixed by follow-up 23's
  generic LookupTable fallback; Bug-B row-content still open): pg_statistic_ext,
  information_schema.routines/parameters/routine_*_usage, pg_foreign_table.
  - pg_statistic_ext is NOT a simple closure-extraction target — its
    VirtualRows reads c.statisticsObjs, a process-global
    map[string]*StatisticsObject with NO dbOid concept anywhere, so fixing
    it needs the bigger "give the underlying registry a dbOid key" treatment
    (same shape as the still-open seqRegistry/SequenceParamsFunc gap below),
    not just parameterizing a c.ns(...) call.
  - information_schema.routines/parameters/routine_*_usage and
    pg_foreign_table have not yet been inspected for which shape they are —
    next loop should inspect these first; whichever is a simple
    table-level-slice-over-c.ns(dbOid) shape is the next pick.
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
  host:port`, NOT `-datadir`/`-p`): CREATE DATABASE rwA -> CREATE DATABASE
  rwB -> rwA: CREATE TABLE ta (a int); CREATE RULE rule_a AS ON INSERT TO ta
  DO INSTEAD NOTHING -> rwB: same pattern with tb/rule_b -> SELECT rulename
  FROM pg_rewrite in rwA returns exactly rule_a, same query in rwB returns
  exactly rule_b — no cross-database leak either direction.

Next step: inspect information_schema.routines/parameters/routine_*_usage
and pg_foreign_table's VirtualRows closures to determine which (if either)
is a simple table-level-slice-over-c.ns(dbOid) shape like pg_trigger/
pg_rewrite were; whichever is simplest becomes follow-up 32. If both turn
out to need the bigger registry-dbOid-key treatment (like pg_statistic_ext),
consider switching to the actual CREATE DATABASE ... TEMPLATE relation-copy
mechanism (design doc's "Remaining 4e work" section has the sketch) — the 9
prerequisite virtual-table fixes (pg_class/pg_indexes/pg_tables/
pg_constraint/pg_index/pg_attrdef/pg_depend/pg_inherits/pg_policy/pg_trigger/
pg_rewrite) all work end-to-end for verifying it now. OR pick up the
oid::regclass cast gap, OR give SequenceParamsFunc a dbOid parameter.

Gates run: go build/vet ./... clean; go test ./internal/catalog/...
./internal/executor/... ./internal/server/... ./internal/planner/... PASS;
tpch-spotcheck PASS (Q12=2/Q13=33). pgbench smoke will run automatically via
the pre-commit hook at commit time (not yet re-run standalone this loop).
make ralph-state-guard: to be run before the status block.
Nightly triage: ci/logs/action-items.md's single AI-20260710-011513-001
(build regression) was already fixed + closed in fix_plan.md by a prior loop
(follow-up 14, commit abbf7de1) before this loop started — verified `make
build` clean at loop start; no new M-NIGHTLY task needed.

In-flight: none. tmp/manual-pgrewrite-test/ (scratch server used for the live
verification) was removed after use.
