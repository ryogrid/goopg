Task: M0122-0007 4e (`CREATE DATABASE ... TEMPLATE` epic). This loop fixed and
committed the pg_policy row-content gap (follow-up 29, mirrors follow-up 28's
pg_inherits fix) — the next highest-value sibling builder follow-up 28
flagged. 6 sibling builders + the real relation-copy mechanism remain.

Files (this loop, committed): internal/catalog/catalog.go (new
PGPolicyRowsForDBOid(dbOid uint32), extracted from pg_policy's inline
VirtualRows closure — parameterized its one c.ns(DefaultDBOid) reference, the
loop over every table's Policies), internal/executor/context.go
(PgPolicyRows field), internal/executor/operators.go (valuesOp.Open branch
for pg_policy), internal/server/dispatch.go (wireExtensionRows wiring +
pgPolicyRowLister interface), internal/executor/fk_dbid_routing_test.go
(TestPgPolicyRowsScopedToConnectionDBOid), design doc + README +
fix_plan.md + deferral_ledger.md (1 new resolved row: "pg_policy per-dbOid
content").

Key symbols: `catalog.InMemory.PGPolicyRowsForDBOid(dbOid)` — single builder,
no cross-builder oid-numbering coupling (unlike the pg_attrdef/pg_depend pair
follow-up 27 had to fix together). `executor.Context.PgPolicyRows`, wired in
`server.wireExtensionRows` via new `pgPolicyRowLister` interface (mirrors
`pgInheritsRowLister`).

Findings (confirmed, don't re-derive):
- 6 sibling virtual builders still share the same c.ns(DefaultDBOid)-hardcoded
  VirtualRows pattern (Bug-A name-resolution already fixed by follow-up 23's
  generic LookupTable fallback; Bug-B row-content still open): pg_statistic_ext,
  pg_trigger, pg_rewrite, information_schema.routines/parameters/
  routine_*_usage, pg_foreign_table. pg_statistic_ext is NOT a simple
  closure-extraction target like pg_inherits/pg_policy were — its
  VirtualRows reads `c.statisticsObjs`, a process-global
  `map[string]*StatisticsObject` with NO dbOid concept anywhere (checked this
  loop), so fixing it needs the bigger "give the underlying registry a dbOid
  key" treatment (same shape as the still-open `seqRegistry`/
  `SequenceParamsFunc` gap below), not just parameterizing a `c.ns(...)` call.
  pg_trigger/pg_rewrite have not yet been inspected for which shape they are.
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
  not fixed, own deferral-ledger row from follow-up 26. Not re-checked this
  loop (no live-server touch needed beyond the pg_policy verification below).
- Live end-to-end verified via a real cmd/goopg binary + psql
  (LD_LIBRARY_PATH must include postgres/local_install/lib): CREATE DATABASE
  polA -> CREATE DATABASE polB -> polA: CREATE TABLE ta (a int); CREATE
  POLICY pol_a ON ta USING (a > 0) -> polB: CREATE TABLE tb (a int); CREATE
  POLICY pol_b ON tb USING (a > 0) -> SELECT polname FROM pg_policy in polA
  returns exactly pol_a, same query in polB returns exactly pol_b — no
  cross-database leak either direction.

Next step: pick ONE of the 6 remaining deferred virtual builders. Easiest
single-builder picks first (same closure-extraction pattern as pg_inherits/
pg_policy, no known structural blocker): inspect pg_trigger and pg_rewrite's
VirtualRows closures to see which is table-Policies-shaped (simple) vs.
statisticsObjs-shaped (needs a registry-level dbOid key first) — pick
whichever is simple. pg_statistic_ext needs the bigger fix (dbOid-key the
`c.statisticsObjs` map itself) and is a worse next pick until then. OR start
the actual CREATE DATABASE ... TEMPLATE relation-copy mechanism (design
doc's "Remaining 4e work" section has the sketch) now that
pg_class/pg_indexes/pg_tables/pg_constraint/pg_index/pg_attrdef/pg_depend/
pg_inherits/pg_policy all work end-to-end for verifying it. OR pick up the
oid::regclass cast gap, OR give SequenceParamsFunc a dbOid parameter (closes
the residual gap noted above). Any of these is a reasonable next pick.

Gates run: go build/vet ./... clean; go test ./internal/catalog/...
./internal/executor/... ./internal/server/... ./internal/planner/... PASS;
tpch-spotcheck PASS (Q12=2/Q13=33); pgbench smoke runs automatically via the
pre-commit hook at commit time (not run as a separate manual step this loop —
same coverage, enforced by .githooks/pre-commit).
make ralph-state-guard: to be run before status block.
Nightly triage: ci/logs/action-items.md's single AI-20260710-011513-001
(build regression) was already fixed + closed in fix_plan.md by a prior loop
(follow-up 14, commit abbf7de1) before this loop started — verified `make
build` clean at loop start; no new M-NIGHTLY task needed.

In-flight: none. tmp/manual-pgpolicy-test/ (scratch server used for the live
verification) was removed after use.
