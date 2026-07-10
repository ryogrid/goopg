Task: M0122-0007 4e (`CREATE DATABASE ... TEMPLATE` epic). This loop fixed and
committed the pg_index row-content gap (follow-up 26, commit ac59630d) — the
next highest-value sibling builder follow-up 25 flagged. 9 sibling builders +
the real relation-copy mechanism remain.

Files (this loop, committed): internal/catalog/catalog.go
(PGIndexRowsForDBOid, extracted from pg_index's VirtualRows closure;
toastBearingTables gained a required dbOid uint32 param — its only caller),
internal/executor/context.go (PgIndexRows field), internal/executor/operators.go
(valuesOp.Open branch for pg_index), internal/server/dispatch.go
(wireExtensionRows wiring + pgIndexRowLister interface),
internal/executor/fk_dbid_routing_test.go (TestPgIndexRowsScopedToConnectionDBOid,
gained an "fmt" import), design doc + README + fix_plan.md + deferral_ledger.md
(2 new rows: "pg_index per-dbOid content" resolved row + a separate "pg_index
live verification — collateral discovery" open row for the regclass gap below).

Key symbols: `catalog.InMemory.PGIndexRowsForDBOid` (inserted just before
`PGClassRowsForDBOid` in catalog.go) — same closure-extraction pattern as
`PGConstraintRowsForDBOid`, calling `c.AllIndexes(dbOid)` (was already
dbOid-variadic, just unused by this closure) and `c.toastBearingTables(dbOid)`
(signature changed from zero-arg to required dbOid uint32 — safe since it had
exactly one caller). `executor.Context.PgIndexRows`, wired in
`server.wireExtensionRows` via new `pgIndexRowLister` interface (mirrors
`pgConstraintRowLister`).

Findings (confirmed, don't re-derive):
- 9 sibling virtual builders still share the same c.ns(DefaultDBOid)-hardcoded
  VirtualRows pattern (Bug-A name-resolution already fixed by follow-up 23's
  generic LookupTable fallback; Bug-B row-content still open): pg_attrdef,
  pg_inherits, pg_statistic_ext, pg_policy, pg_depend, pg_trigger, pg_rewrite,
  information_schema.routines/parameters/routine_*_usage, pg_foreign_table.
  pg_depend is the next highest-value target (pg_dump/psql \d catalog joins);
  pg_inherits after that (partition-child enumeration).
- pg_sequence/pg_sequences/information_schema.sequences remain separately
  flagged by an earlier "sequence ownership follow-on" ledger row (zero-arg
  VirtualRows closures, different mechanism — needs the full "thread
  DBName->Context" per_connection_virtual_catalog_scoping treatment, not the
  dbOid-arg closure-extraction pattern the other builders use).
- pg_attribute/pg_type (heap-backed, not VirtualRows) remain the structurally
  deeper gap — untouched, needs a heap-write/heap-scan fix in
  syncTableToCatalogHeap, not a VirtualRows one.
- NEW this loop, recorded in its own deferral-ledger row (not fixed):
  `oid::regclass` (the OID→name cast direction) still resolves against
  DefaultDBOid's pg_class only — in a fresh non-default database, casting a
  real index/table OID to ::regclass prints the bare numeric OID instead of
  the name, even though the underlying pg_index/pg_class row data is
  byte-correct (confirmed via a raw uncast query + a companion pg_class
  lookup). `name::regclass` (NAME→OID direction) works fine everywhere —
  only the reverse direction is broken. Not yet located which file implements
  the regclass output/cast function (grepped "regclass" across ~18 files,
  didn't narrow further this loop) — next loop on this thread should start
  there (internal/executor/expr.go and internal/catalog/codec.go are the two
  most likely candidates per the grep).
- Live end-to-end verified via a real cmd/goopg binary + psql
  (LD_LIBRARY_PATH must include postgres/local_install/lib): CREATE DATABASE
  freshidx1 -> connect -> CREATE TABLE only_in_freshidx1 (id int PRIMARY KEY,
  val int) + CREATE INDEX ... ON only_in_freshidx1(val) -> raw (uncast)
  pg_index in freshidx1 shows exactly those 2 rows with indrelid matching the
  table's own pg_class oid; the same query against postgres db shows only
  postgres's own index row (no cross-database leak either direction).

Next step: pick ONE of the 9 remaining deferred virtual builders (pg_depend
is the next highest-value target — pg_dump/psql's \d catalog joins touch it;
pg_inherits after that for partition-child enumeration) and apply the same
closure-extraction pattern PGIndexRowsForDBOid used. OR start the actual
CREATE DATABASE ... TEMPLATE relation-copy mechanism (design doc's "Remaining
4e work" section has the sketch) now that pg_class/pg_indexes/pg_tables/
pg_constraint/pg_index all work end-to-end for verifying it. OR pick up the
newly-discovered oid::regclass cast gap (separate mechanism, own bounded
loop). Any of the three is a reasonable next pick.

Gates run: go build/vet ./... clean; go test ./internal/catalog/...
./internal/executor/... ./internal/server/... ./internal/planner/... PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke PASS (0 failed, all 3
workloads, both the pre-commit-hook run and this loop's own manual run).
make ralph-state-guard OK (auto-repaired a stale status/progress mismatch
from the prior loop's clean-exit marker, as expected — same pattern as every
recent loop).
Nightly triage: ci/logs/action-items.md's single AI-20260710-011513-001
(build regression) was already fixed + closed in fix_plan.md by a prior loop
(follow-up 14, commit abbf7de1) before this loop started — verified `make
build` clean at loop start; no new M-NIGHTLY task needed.

In-flight: none. tmp/manual-pgindex-test/ (scratch server used for the live
verification) was removed after use.
