Task: M0122-0007 4e (`CREATE DATABASE ... TEMPLATE` epic). This loop fixed and
committed the pg_attrdef + pg_depend row-content gap TOGETHER (follow-up 27,
commit f8e62908) — the next highest-value sibling builder follow-up 26
flagged. 8 sibling builders + the real relation-copy mechanism remain.

Files (this loop, committed): internal/catalog/catalog.go
(attrDefRowsLocked -> attrDefRowsLockedForDBOid(dbOid); new
PGAttrdefRowsForDBOid/PGDependRowsForDBOid, extracted from pg_attrdef's/
dependVirtualRows's VirtualRows closures — dependVirtualRows itself renamed
to PGDependRowsForDBOid), internal/executor/context.go (PgAttrdefRows/
PgDependRows fields), internal/executor/operators.go (valuesOp.Open branches
for pg_attrdef/pg_depend), internal/server/dispatch.go (wireExtensionRows
wiring + pgAttrdefRowLister/pgDependRowLister interfaces),
internal/executor/fk_dbid_routing_test.go
(TestPgAttrdefRowsScopedToConnectionDBOid,
TestPgDependRowsScopedToConnectionDBOid), design doc + README + fix_plan.md
+ deferral_ledger.md (1 new resolved row: "pg_attrdef/pg_depend per-dbOid
content").

Key symbols: `catalog.InMemory.attrDefRowsLockedForDBOid(dbOid)` — shared by
BOTH PGAttrdefRowsForDBOid and PGDependRowsForDBOid, which MUST be called
with the same dbOid (their oid numbering must agree, per
dependVirtualRows's own pre-existing doc comment) — this is why this follow-up
did 2 builders in one loop instead of 1, unlike every prior follow-up in this
chain. `executor.Context.PgAttrdefRows`/`PgDependRows`, wired in
`server.wireExtensionRows` via new `pgAttrdefRowLister`/`pgDependRowLister`
interfaces (mirrors `pgIndexRowLister`).

Findings (confirmed, don't re-derive):
- 8 sibling virtual builders still share the same c.ns(DefaultDBOid)-hardcoded
  VirtualRows pattern (Bug-A name-resolution already fixed by follow-up 23's
  generic LookupTable fallback; Bug-B row-content still open): pg_inherits,
  pg_statistic_ext, pg_policy, pg_trigger, pg_rewrite,
  information_schema.routines/parameters/routine_*_usage, pg_foreign_table.
  pg_inherits is the next highest-value target (partition-child enumeration,
  pg_dump's inheritance queries).
- pg_sequence/pg_sequences/information_schema.sequences remain separately
  flagged by the "sequence ownership follow-on" ledger row (needs the full
  "thread DBName->Context" per_connection_virtual_catalog_scoping treatment).
- pg_attribute/pg_type (heap-backed, not VirtualRows) remain the structurally
  deeper gap — untouched, needs a heap-write/heap-scan fix in
  syncTableToCatalogHeap, not a VirtualRows one.
- oid::regclass (OID->name cast direction) still resolves against
  DefaultDBOid's pg_class only — separate cast/output-function mechanism,
  not fixed, own deferral-ledger row from follow-up 26 (not yet located which
  file implements it; internal/executor/expr.go and internal/catalog/codec.go
  are the two most likely candidates per a prior grep).
- NEW this loop, confirmed via live psql + recorded in its own ledger
  paragraph (not a new ledger row, folded into the existing "sequence
  ownership follow-on" entry): pg_depend's deptype='a' sequence-OWNED-BY row
  is STILL MISSING (not leaked — correctly absent, just also correctly not
  present) for a non-default database, because it resolves through
  catalog.SequenceParamsFunc(qualifiedName string) (SeqParams, bool) — a
  package-level func var with NO dbOid parameter in its signature at all
  (keyed only by qualified name via the process-global seqRegistry/
  LookupSequence). Concrete resume point for whoever next touches "sequence
  ownership follow-on": give SequenceParamsFunc a trailing dbOid ...uint32
  param (mirroring every other per-connection accessor) so
  PGDependRowsForDBOid's sequence-ownership loop can thread the connecting
  dbOid through it instead of hitting the global registry.
- Live end-to-end verified via a real cmd/goopg binary + psql
  (LD_LIBRARY_PATH must include postgres/local_install/lib): CREATE DATABASE
  freshdep1 -> connect -> CREATE TABLE only_in_freshdep1 (id serial PRIMARY
  KEY) -> raw pg_attrdef and pg_depend WHERE classid=2604 in freshdep1 each
  show exactly that table's own default/sequence OIDs (adrelid=16404,
  refobjid=16405 matching its own pg_class oids), not postgres's
  (16408/16409); the same queries against postgres db show only postgres's
  own rows. Cross-checked: pg_depend WHERE classid=1259 (the 'a' OWNED-BY
  row) shows 0 rows in freshdep1 (correct-by-omission, no leak) vs 1 row in
  postgres (correct) — confirming the residual gap above is a MISSING row,
  not a LEAKED one.

Next step: pick ONE of the 8 remaining deferred virtual builders (pg_inherits
is the next highest-value target — partition-child enumeration, pg_dump's
inheritance queries) and apply the same closure-extraction pattern
PGAttrdefRowsForDBOid/PGDependRowsForDBOid used (check first whether the
target builder shares a helper with another sibling the way pg_attrdef/
pg_depend did — if so, fix both together in one loop, don't split them). OR
start the actual CREATE DATABASE ... TEMPLATE relation-copy mechanism
(design doc's "Remaining 4e work" section has the sketch) now that
pg_class/pg_indexes/pg_tables/pg_constraint/pg_index/pg_attrdef/pg_depend
all work end-to-end for verifying it. OR pick up the oid::regclass cast gap,
OR give SequenceParamsFunc a dbOid parameter (closes the residual gap noted
above). Any of these is a reasonable next pick.

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

In-flight: none. tmp/manual-pgdepend-test/ (scratch server used for the live
verification) was removed after use.
