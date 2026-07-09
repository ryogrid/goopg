Task: M0122-0007 follow-up 13 — slice 4 sub-slice 4d-ii-part-2a ("close the
14 im/cat-local-bound LookupTable/LookupIndex sites 4d-ii-part-1 deferred
in operators_ddl.go"), per
docs/design/0122-0018-per-database-catalog-namespace.md. COMPLETE and
committed this loop (commit df26640b).

Files: internal/executor/operators_ddl.go (14 sites fixed: 12 mechanical
trailing-arg — execCreateView OR-REPLACE, execAttrACLChange,
execCommentOn x7, execCreateStatistics, lockRelationTransitively — plus 2
genuine signature-cascades — collectAllViewTransitiveDeps gained a
`dbOid uint32` param [4 call sites updated], and the collectViewPKDeps/
walkSelectPKDeps/walkExprPKDeps/addGroupByPKDeps cluster gained a
`dbOid uint32` param [1 external call site updated]), internal/executor/
ddl_write_dbid_routing_test.go (3 new tests: TestExecCommentOnFinds...,
TestExecCreateStatisticsFinds..., TestExecAttrACLChangeFinds...),
docs/design/0122-0018-per-database-catalog-namespace.md (4d-ii-part-2
renamed/split into landed "4d-ii-part-2a" + renumbered planned
"4d-ii-part-2b"; new item 3 in 2b for the CreateView/AllUserViews/
AllUserMatViews/IndexesOnTable finding below), docs/design/README.md (row
updated), .ralph/fix_plan.md (follow-up 13 entry), .ralph/deferral_ledger.md
(new row for the 4d-ii-part-2b gap, including the new finding).

Key symbols: `catalog.NamespaceDBOid(uint32) uint32` (unchanged since 4c) —
same pattern applied to all 14 sites. `collectAllViewTransitiveDeps(im,
startName, dbOid)` and `collectViewPKDeps/walkSelectPKDeps/walkExprPKDeps/
addGroupByPKDeps(..., dbOid)` are the two newly-cascaded signatures.

Hypothesis/Findings (confirmed): Of the 14 deferred sites, only 2 were
genuine signature-cascades — the other 12 already had ctx/o.ctx in scope
(the design doc's original estimate over-stated how many needed a
signature change). **New finding (the loop's main discovery, NOT yet
fixed):** `catalog.InMemory.CreateView` (write) and `AllUserViews`/
`AllUserMatViews`/`IndexesOnTable` (read) are ALL hardcoded to
`c.ns(DefaultDBOid)` with NO dbOid parameter at all — bigger than the
LookupTable/LookupIndex sweep. Concretely: CREATE VIEW on a distinct-dbOid
connection always lands under DefaultDBOid regardless of
ctx.CurrentDatabaseOid (namespace collision, not isolation); a table's own
indexes are unreachable via IndexesOnTable once the table lives under a
distinct dbOid. Discovered because BOTH an end-to-end test (CREATE VIEW ...
FROM base on a distinct-dbOid connection — also independently blocked by
o.planCatalog()'s FROM-table resolution not being dbOid-aware) AND a
white-box test (calling addGroupByPKDeps directly, bypassing SQL, still
failed at IndexesOnTable) could not exercise the 2 signature-cascaded
functions' fix. That fix is therefore verified ONLY by code inspection
(matches the identical pattern proven correct at 70+ other sites) + the
full test suite/tpch-spotcheck/pgbench-smoke staying green (proven
non-regressing on the always-exercised DefaultDBOid/"postgres" path, NOT
proven newly-working on a distinct one). Full details + exact repro in the
deferral ledger's new row and the design doc's "4d-ii-part-2a" section.

Gates run (all PASS): go build ./... clean; go vet ./... clean; go test
-race ./internal/catalog/... ./internal/executor/...; go test -short
$(go list ./... | grep -v /internal/testport) (full repo, short mode);
scripts/tpch-spotcheck.sh (Q12=2/Q13=33, ran twice — standalone + via
pre-commit hook); RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh
(0 failed, all 3 pgbench workloads, ran twice — standalone + via the git
pre-commit hook itself); make ralph-state-guard OK (self-repaired stale
prior-loop marker, same recurring harmless pattern as prior loops).

In-flight: none. Note: analysis/tpch-explain-baseline.md still carries the
same unrelated auto-regenerated diff flagged by many prior loops (side
effect of the full `go test` run) — deliberately left OUT of this loop's
commit too. `postgres` shows as untracked content (submodule) —
pre-existing, not touched.

Next step for a future loop: **4d-ii-part-2b is the next resume point**
(design doc's "4d-ii-part-2b" section, fix_plan.md's follow-up 13 entry's
"Remaining M0122-0007 items"). Recommend splitting further, in priority
order:
  1. The newly-found gap (item 3 of 2b): make catalog.InMemory.CreateView/
     AllUserViews/AllUserMatViews/IndexesOnTable dbOid-aware (mirror the
     `dbOid ...uint32`-variadic pattern 4b-ii used for the other 17 entry
     points), and separately make o.planCatalog()'s FROM-table resolution
     dbOid-aware. This is arguably higher-value than the cross-file sweep
     below since it's what actually makes 4d-ii-part-2a's own fix
     (collectAllViewTransitiveDeps/PK-deps cluster) reachable/testable, and
     unblocks CREATE VIEW itself being per-database.
  2. Then the cross-file sweep (items 1-2 of 2b, originally named by 4d-i):
     grep-measure + fix operators_fk.go/operators_cluster.go/
     operators_reindex.go/operators_sequence.go/operators_storage.go/
     operators_pg_get_publication_tables.go/DML operators — likely large
     enough to need its own further split (e.g. one file per loop).
  3. Then RelFileNode.DBOid (needs the postgres/template1 dual-mirror audit
     flagged in the design doc's "Blast radius" section before changing what
     oid live relations are created under).
  4. Then 4e (cross-cutting fixups + the actual CREATE DATABASE ... TEMPLATE
     copy mechanism this whole epic exists to unblock).
Re-run the full catalog/executor/server + short-mode whole-repo suites plus
tpch-spotcheck/pgbench-smoke gates after each sub-piece, per this loop's
practice. If picking up item 1, write the end-to-end/white-box regression
tests for 4d-ii-part-2a's `collectAllViewTransitiveDeps`/PK-deps-cluster
fix once CreateView/IndexesOnTable are dbOid-aware — they were deliberately
deferred from this loop for exactly that reason.
