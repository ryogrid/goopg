Task: M0122-0007 follow-up 11 — slice 4 sub-slice 4d-i ("thread the
connection's real dbOid through catalog WRITE entry points"), per
docs/design/0122-0018-per-database-catalog-namespace.md. COMPLETE and
committed this loop (see commit after this file — not yet made when this was
written; check `git log -1` for the actual hash).

Files: internal/catalog/catalog.go (AddColumn/RegisterTable/RestoreIndex
gained their first `dbOid ...uint32` param), internal/executor/operators_ddl.go
(~24 call sites: CreateTable/CreateIndex/DropTable/DropIndex/RenameTable/
RenameIndex/AddColumn now pass catalog.NamespaceDBOid(ctx.CurrentDatabaseOid);
CreateSequenceCatalogRelation gained a *required* dbOid param),
internal/executor/operators_tx.go (rollbackDDLCreate, ProcessRollbackUndos,
execRollbackTo — RegisterTable/RestoreIndex/DropTable/DropIndex calls),
internal/initdb/sequence_ddl_recovery.go (CreateSequenceCatalogRelation caller
passes catalog.DefaultDBOid explicitly, preserving replay behavior),
internal/executor/ddl_write_dbid_routing_test.go (new: 3 tests),
docs/design/0122-0018-per-database-catalog-namespace.md (4d split into landed
4d-i + planned 4d-ii, full writeup + critical scope finding),
docs/design/README.md (row updated), .ralph/fix_plan.md (follow-up 11 entry),
.ralph/deferral_ledger.md (new row for the 4d-ii gap).

Key symbols: `catalog.NamespaceDBOid(uint32) uint32` (unchanged from 4c) is
now used on BOTH the read side (SearchPathCatalog/PlanCatalog, 4c) AND the
write side (this loop's ~24 executor call sites) — the postgres/DefaultDBOid
dual-mirror shim is bit-for-bit unchanged. `ectx.Catalog` (internal/server/
dispatch.go:295, assigned once, raw *catalog.InMemory) vs `ectx.PlanCatalog`
(wrapped in SearchPathCatalog by 4c, dbOid-aware) — these are TWO SEPARATE
fields. Every executor-operator-level LookupTable/LookupIndex call (there are
60+ in operators_ddl.go alone, more across operators_fk.go/
operators_cluster.go/operators_reindex.go/operators_sequence.go/
operators_storage.go/operators_pg_get_publication_tables.go/DML operators)
still goes through the UNWRAPPED ctx.Catalog and therefore still resolves
only DefaultDBOid, regardless of ctx.CurrentDatabaseOid.

Hypothesis/Findings (confirmed, not speculative): (1) This loop's write-side
dbOid threading is a pure no-op for all EXISTING traffic — every real
connection today resolves via NamespaceDBOid to DefaultDBOid — confirmed by
full non-testport suite + -race catalog/executor + tpch-spotcheck
(Q12=2/Q13=33) + pgbench smoke (0 failed, all 3 workloads) all staying green
with canonical counts. (2) The write-routing is NECESSARY BUT NOT SUFFICIENT
for a genuinely-second-database connection to actually work end-to-end: a
CREATE TABLE under a distinct dbOid now correctly lands in that dbOid's
namespace (independently tested), but a subsequent DROP TABLE/CREATE
INDEX/ALTER on the SAME connection would fail to find it, because
execDropTable/execCreateIndex/etc. locate the object via a bare
o.ctx.Catalog.LookupTable(name) call that still defaults to DefaultDBOid.
Proven empirically while writing this loop's own test
(TestExecCreateTableAsRoutesToConnectionRealDBOid): a `CREATE TABLE ... AS
SELECT ... FROM <table-in-distinct-namespace>` failed to plan
("relation does not exist") until rewritten to a FROM-less literal SELECT,
because o.planCatalog() falls back to the raw ctx.Catalog when
ctx.PlanCatalog is nil (true in every unit-test harness — only the live
server's dispatch.go wires PlanCatalog with dbOid routing).

Gates run (all PASS): go build ./... clean; go vet ./... clean; go test -race
./internal/catalog/... ./internal/executor/...; go test -short $(go list
./... | grep -v /internal/testport) (full repo, short mode);
scripts/tpch-spotcheck.sh (Q12=2/Q13=33); RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh (0 failed, all 3 pgbench workloads);
make ralph-state-guard OK (self-repaired stale prior-loop marker, same
recurring pattern as before — not new).

In-flight: none. Note: analysis/tpch-explain-baseline.md still carries the
same unrelated auto-regenerated diff flagged by prior loops (side effect of
the full `go test` run) — deliberately left OUT of this loop's commit too.
`postgres` shows as untracked content (submodule) — pre-existing, not touched.

Next step for a future loop: **4d-ii is the next resume point** (see the
design doc's "4d-ii — Thread dbOid through executor-operator-level lookups +
RelFileNode.DBOid" section, and fix_plan.md's `M0122-0007` follow-up 11
entry's "Remaining M0122-0007 items"). Two pieces, likely worth their own
further split:
  (a) thread catalog.NamespaceDBOid(ctx.CurrentDatabaseOid) through every
      executor-operator-level LookupTable/LookupIndex call (60+ in
      operators_ddl.go alone; grep `ctx\.Catalog\.LookupTable(\|ctx\.Catalog\.LookupIndex(`
      across internal/executor for the full list) so 4d-i's write-routing
      can actually find what it creates. Consider (design doc already flags
      this) whether wrapping ectx.Catalog itself in a SearchPathCatalog is
      viable instead of per-call-site edits — but note dozens of
      `im, ok := o.ctx.Catalog.(*catalog.InMemory)` type assertions would
      break if ctx.Catalog became a SearchPathCatalog wrapper, so it may not
      actually be less work.
  (b) wire RelFileNode.DBOid to the connection's real dbOid at creation time
      (still hardcoded to DefaultDBOid) — must re-audit the postgres/
      template1 dual-mirror (base/1 + base/5) before changing what oid live
      relations are created under, since that's genuinely new observable
      behavior (unlike 4d-i's read-preserving change).
Budget 4d-ii (or its first sub-piece) as its own bounded pass; re-run the
full catalog/executor/server + short-mode whole-repo suites plus
tpch-spotcheck/pgbench-smoke gates after.
