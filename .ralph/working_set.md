Task: M0122-0007 4e (`CREATE DATABASE ... TEMPLATE` epic). This loop fixed and
committed the pg_constraint row-content gap (follow-up 25) — the next
highest-value sibling builder follow-up 24 flagged. 10 sibling builders + the
real relation-copy mechanism remain.

Files (this loop, committed): internal/catalog/catalog.go
(PGConstraintRowsForDBOid, extracted from pg_constraint's VirtualRows closure
— the largest of these builders, ~300 lines / 4 passes: CHECK, UNIQUE·PK·
EXCLUDE, NOT NULL, FOREIGN KEY), internal/executor/context.go
(PgConstraintRows field), internal/executor/operators.go (valuesOp.Open branch
for pg_constraint), internal/server/dispatch.go (wireExtensionRows wiring +
pgConstraintRowLister interface), internal/executor/fk_dbid_routing_test.go
(TestPgConstraintRowsScopedToConnectionDBOid), design doc + README +
fix_plan.md + deferral_ledger.md.

Key symbols: `catalog.InMemory.PGConstraintRowsForDBOid` (inserted just before
`PGClassRowsForDBOid` in catalog.go) — same closure-extraction pattern as
`PGIndexesRowsForDBOid`/`PGTablesRowsForDBOid`, parameterized on dbOid instead
of DefaultDBOid for all `c.ns(dbOid)` table/index references; `c.domains`
(domain CHECK constraints) deliberately left global — domains aren't
namespace-scoped at all yet, same precedent `PGClassRowsForDBOid` documents
for composite types. `executor.Context.PgConstraintRows`, wired in
`server.wireExtensionRows` via new `pgConstraintRowLister` interface
(mirroring `pgClassRowLister`/`pgIndexesRowLister`/`pgTablesRowLister`).

Findings (confirmed, don't re-derive):
- 10 sibling virtual builders still share the same c.ns(DefaultDBOid)-
  hardcoded VirtualRows pattern (Bug-A name-resolution already fixed by
  follow-up 23's generic LookupTable fallback; Bug-B row-content still open):
  pg_attrdef, pg_inherits, pg_index, pg_statistic_ext, pg_policy, pg_depend,
  pg_trigger, pg_rewrite, information_schema.routines/parameters/routine_*_
  usage, pg_foreign_table. pg_depend/pg_index are next highest-value
  (pg_dump/psql \d catalog joins); pg_inherits after that (partition-child
  enumeration). Recorded in deferral ledger row "pg_constraint per-dbOid
  content" (2026-07-10) with the exact resume pattern.
- pg_sequence/pg_sequences/information_schema.sequences remain separately
  flagged by an earlier "sequence ownership follow-on" ledger row (zero-arg
  VirtualRows closures, different mechanism — needs the full "thread
  DBName->Context" per_connection_virtual_catalog_scoping treatment, not the
  dbOid-arg closure-extraction pattern the other builders use).
- pg_attribute/pg_type (heap-backed, not VirtualRows) remain the structurally
  deeper gap — untouched, needs a heap-write/heap-scan fix in
  syncTableToCatalogHeap, not a VirtualRows one.
- Live end-to-end verified via a real cmd/goopg binary + psql
  (LD_LIBRARY_PATH must include postgres/local_install/lib): CREATE DATABASE
  freshdb3 -> connect -> CREATE TABLE only_in_freshdb3 (id int PRIMARY KEY,
  val int CHECK (val > 0)) -> pg_constraint in freshdb3 shows only that
  table's 3 constraints (pkey/check/not-null) at its own dbOid-local OID; the
  same query against postgres db shows only postgres's own constraints (no
  cross-database leak either direction). Noticed collaterally (pre-existing,
  NOT caused by this change, reproduces on postgres db too): psql `\d+`
  errors `operator does not exist: text[] || text[]` — unrelated to
  pg_constraint scoping, out of scope for this loop, not recorded as a new
  deferral since it isn't this loop's discovery to own.

Next step: pick ONE of the 10 remaining deferred virtual builders (pg_depend
or pg_index are the next highest-value targets — pg_dump/psql's \d catalog
joins touch them; pg_inherits after that for partition-child enumeration) and
apply the same closure-extraction pattern PGConstraintRowsForDBOid used.
OR start the actual CREATE DATABASE ... TEMPLATE relation-copy mechanism
(design doc's "Remaining 4e work" section has the sketch) now that
pg_class/pg_indexes/pg_tables/pg_constraint all work end-to-end for verifying
it. Either is a reasonable next pick.

Gates run: go build/vet ./... clean; go test ./internal/catalog/...
./internal/executor/... ./internal/server/... ./internal/planner/... PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke PASS (0 failed, all 3
workloads). make ralph-state-guard OK (auto-repaired a stale status/progress
mismatch from the prior loop's clean-exit marker, as expected — same pattern
as every recent loop).
Nightly triage: ci/logs/action-items.md's single AI-20260710-011513-001
(build regression) was already fixed + closed in fix_plan.md by a prior loop
(follow-up 14, commit abbf7de1) before this loop started — no new M-NIGHTLY
task needed.

In-flight: none. tmp/manual-pgconstraint-test/ (scratch server used for the
live verification) was removed after use.
