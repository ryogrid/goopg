Task: M0122-0007 follow-up 12 — slice 4 sub-slice 4d-ii-part-1 ("thread the
connection's real dbOid through operators_ddl.go's direct-ctx
LookupTable/LookupIndex calls"), per
docs/design/0122-0018-per-database-catalog-namespace.md. COMPLETE and
committed this loop (commit 1f203e8c).

Files: internal/executor/operators_ddl.go (all 60 direct
o.ctx.Catalog.LookupTable(...)/o.ctx.Catalog.LookupIndex(...) — and the one
bare-ctx-param ctx.Catalog.LookupTable(...) in catalogHeapSyncAvailable —
call sites now append catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid) /
catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)), internal/executor/
ddl_write_dbid_routing_test.go (2 new tests:
TestExecDropTableFindsOwnDistinctDBOidTable,
TestExecCreateIndexFindsOwnDistinctDBOidTable), docs/design/
0122-0018-per-database-catalog-namespace.md (4d-ii split into landed
"4d-ii-part-1" full writeup + planned "4d-ii-part-2"; Recommended-order
section updated), docs/design/README.md (row updated),
.ralph/fix_plan.md (follow-up 12 entry), .ralph/deferral_ledger.md (new row
for the 4d-ii-part-2 gap).

Key symbols: `catalog.NamespaceDBOid(uint32) uint32` (unchanged since 4c) —
now threaded through operators_ddl.go's LOOKUP side too, matching the write
side 4d-i already did. Applied via a throwaway (uncommitted) Python script
that walks each `.LookupTable(`/`.LookupIndex(` call preceded by
`o.ctx.Catalog.`/`ctx.Catalog.` to its balanced closing paren (handles
multi-line calls, e.g. catalogHeapSyncAvailable, validateSeqOwnedBy) and
inserts the dbOid arg — diff hand-reviewed before running gates.

Hypothesis/Findings (confirmed): This loop closes the exact gap 4d-i's own
writeup proved empirically — CREATE TABLE then DROP TABLE / CREATE INDEX on
the SAME distinct-dbOid connection now round-trips (new tests fail with
"does not exist" if you temporarily revert operators_ddl.go's diff, proving
non-vacuousness). Explicitly NOT closed by this loop (documented in the
ledger + design doc as 4d-ii-part-2, itemized in full):
  (a) 15 `im.LookupTable`/`im.LookupIndex`/`cat.LookupTable` call sites
      STILL inside operators_ddl.go itself, bound from locals/params with
      no ctx/dbOid in scope — e.g. `collectAllViewTransitiveDeps(im
      *catalog.InMemory, startName parser.ObjectName)`,
      `walkSelectPKDeps(sel, cat, out, seen)`, the ACL-grant table-name
      loop (~line 17026), the DROP-CASCADE helpers (~17179-17472). These
      need a SIGNATURE-CASCADING fix (thread dbOid through the helper's own
      signature + every one of its callers), not a trailing-arg one — a
      materially different shape of change from this loop's 60 sites.
  (b) The entire cross-file sweep the original 4d-i finding named:
      operators_fk.go, operators_cluster.go, operators_reindex.go,
      operators_sequence.go, operators_storage.go,
      operators_pg_get_publication_tables.go, every DML operator
      (expr.go and friends) — UNTOUCHED. Re-measure via `grep -n
      '\.LookupTable(\|\.LookupIndex('` across those files before starting;
      not yet grep-counted (this loop only measured operators_ddl.go: 74
      total, 60 direct/fixed, 15 im/cat-local/deferred — the 74th, an
      im.LookupTable/im.LookupIndex overlap, see the exact 15-line list in
      the deferral ledger row).
  (c) RelFileNode.DBOid at creation time (4d-ii's second named piece) —
      still hardcoded to DefaultDBOid, unstarted.
  (d) The o.ctx.Catalog.(*catalog.InMemory) type-assertion count across
      internal/executor is 262 (measured via grep this loop) — relevant if
      a future loop reconsiders the "wrap ectx.Catalog in a
      SearchPathCatalog instead" alternative the design doc flags as
      possibly not less work.

Gates run (all PASS): go build ./... clean; go vet ./... clean; go test
-race ./internal/catalog/... ./internal/executor/...; go test -short
$(go list ./... | grep -v /internal/testport) (full repo, short mode);
scripts/tpch-spotcheck.sh (Q12=2/Q13=33, ran twice — once standalone, once
again via the pre-commit hook); RALPH_PRECOMMIT_SCOPE=smoke
scripts/ralph-precommit-test.sh (0 failed, all 3 pgbench workloads, ran
twice — once standalone, once via the git pre-commit hook itself); make
ralph-state-guard OK (self-repaired stale prior-loop marker, same recurring
pattern as before — not new, harmless).

In-flight: none. Note: analysis/tpch-explain-baseline.md still carries the
same unrelated auto-regenerated diff flagged by prior loops (side effect of
the full `go test` run) — deliberately left OUT of this loop's commit too,
still sitting unstaged in the working tree. `postgres` shows as untracked
content (submodule) — pre-existing, not touched.

Next step for a future loop: **4d-ii-part-2 is the next resume point** (see
the design doc's "4d-ii-part-2 — Remaining executor-operator-level lookups +
RelFileNode.DBOid" section, and fix_plan.md's `M0122-0007` follow-up 12
entry's "Remaining M0122-0007 items"). Recommend splitting further:
  - First sub-piece: the 15 im/cat-local sites in operators_ddl.go itself
    (small, contained, but needs real signature-threading design — read
    each of the ~4 enclosing helper functions' call graphs before editing).
  - Second sub-piece: grep-measure + fix the cross-file sweep (6+ files),
    likely large enough to need its own further split (e.g. one file per
    loop, or grouped by call-site count).
  - Then RelFileNode.DBOid (needs the postgres/template1 dual-mirror audit
    flagged in the design doc's "Blast radius" section before changing what
    oid live relations are created under).
  - Then 4e (cross-cutting fixups + the actual CREATE DATABASE ... TEMPLATE
    copy mechanism this whole epic exists to unblock).
Re-run the full catalog/executor/server + short-mode whole-repo suites plus
tpch-spotcheck/pgbench-smoke gates after each sub-piece, per this loop's
practice.
