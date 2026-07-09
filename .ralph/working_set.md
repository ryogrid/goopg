Task: M-NIGHTLY (AI-20260710-011513-001) / M0122-0007 follow-up 14 — fixed
the nightly-CI build regression by finishing the dbOid-threading of
CreateView/DropView/AllUserViews/AllUserMatViews/IndexesOnTable
(4d-ii-part-2b item 3's own-signature half). COMPLETE and committed this
loop (commit abbf7de1).

Files: internal/catalog/catalog.go (already had the WIP variadic dbOid
params on the 5 functions from an interrupted prior loop — untouched by me
beyond what was already there), internal/executor/operators_ddl.go (fixed
6 build-breaking call sites — execDropOneView/execDropOneMatView/
execDropTable's RESTRICT+CASCADE dependency-scan blocks — plus 2 more
found by inspection that compiled but were still hardcoded to DefaultDBOid:
execDropOneMatView's own DropView call, DROP SCHEMA CASCADE's
AllUserViews() call), internal/executor/ddl_write_dbid_routing_test.go (2
new tests: TestExecCreateViewRoutesToConnectionRealDBOid,
TestIndexesOnTableFindsOwnDistinctDBOidTable), docs/design/
0122-0018-per-database-catalog-namespace.md (4d-ii-part-2b item 3 updated:
own-signature half landed, planCatalog() half still open), docs/design/
README.md (row extended), .ralph/fix_plan.md (follow-up 14 entry),
.ralph/deferral_ledger.md (new row for AI-20260710-011513-001).

Key symbols: catalog.InMemory.CreateView/DropView/AllUserViews/
AllUserMatViews/IndexesOnTable all now take `dbOid ...uint32` (variadic,
same pattern as 70+ other sites). viewsDependingOnView/viewsDependingOnTable/
matViewsDependingOnRelation (operators_ddl.go) now take a required
`dbOid uint32` param and forward it into AllUserViews/AllUserMatViews.

Hypothesis/Findings (confirmed): the tree at this loop's start already had
uncommitted WIP (from an interrupted prior loop iteration, never recorded
in working_set.md before being cut off) implementing the "item 1" next-step
from the prior working_set.md, but 8 call sites were left un-threaded —
6 of them broke `go build` (nightly CI caught this as AI-20260710-011513-001),
2 more compiled fine (variadic-optional) but were silently still
DefaultDBOid-only. All 8 are now fixed. The 2 new tests use a no-FROM view
body (`CREATE VIEW v AS SELECT 1 AS id`) specifically to avoid the still-open
o.planCatalog() gap described below — do NOT add a FROM-clause view test
without first fixing that gap, it will fail for an unrelated reason.

Gates run (all PASS): go build ./... clean; go vet ./... clean; go test
-race ./internal/catalog/... ./internal/executor/...; go test
./internal/planner/...; go test -short $(go list ./... | grep -v
/internal/testport) (full repo, short mode); scripts/tpch-spotcheck.sh
(Q12=2/Q13=33, ran twice — before and after the 2 additional non-build-
breaking fixes); RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh
(0 failed, all 3 pgbench workloads, ran twice + once more via the git
pre-commit hook on actual commit); make ralph-state-guard OK.

In-flight: none. Note: analysis/tpch-explain-baseline.md still carries the
same unrelated auto-regenerated diff flagged by many prior loops (side
effect of the full `go test` run) — deliberately left OUT of this loop's
commit too, as are ci/logs/* (already staged by an external/cron process
before this loop started, unrelated to Ralph). `postgres` shows as
untracked content (submodule) — pre-existing, not touched.

Next step for a future loop: **4d-ii-part-2b's remaining two pieces are the
next resume point** (design doc's "4d-ii-part-2b" section item 3's
`planCatalog()` half, and items 1-2). Recommend, in priority order:
  1. Make `o.planCatalog()`'s FROM-table resolution dbOid-aware. This needs
     the planner package to learn a per-connection dbOid — currently
     `planner.Plan` receives a bare `catalog.Catalog` interface with no
     `Context`. Investigate whether wrapping in `SearchPathCatalog` (which
     already carries a `DBOid` field per slice 4c) is sufficient, or
     whether `LookupTable`'s internal call sites inside the planner need
     explicit dbOid threading too. Once this lands, write the end-to-end
     `CREATE VIEW ... FROM <table>` regression test that was deliberately
     deferred from this loop (and re-verify the collectAllViewTransitiveDeps/
     PK-deps-cluster fix from 4d-ii-part-2a with a FROM-clause-based
     white-box test, since that fix is currently verified only by code
     inspection + non-regression, not by a positive test).
  2. Then the cross-file `IndexesOnTable`/`AllUserViews`/`AllUserMatViews`
     call-site sweep (item 1-2 of 2b, ~50 sites found via `grep -rn
     '\.IndexesOnTable(\|\.AllUserViews(\|\.AllUserMatViews(' internal/`
     spanning operators_fk.go/operators_cluster.go/operators_reindex.go/
     operators_sequence.go/operators_storage.go/ssi.go/expr.go/
     applyworker.go/internal/planner/*) — likely large enough to need its
     own further split (e.g. one file per loop), per the existing
     recommendation in fix_plan.md/the design doc.
  3. Then RelFileNode.DBOid (needs the postgres/template1 dual-mirror audit
     flagged in the design doc's "Blast radius" section).
  4. Then 4e (cross-cutting fixups + the actual CREATE DATABASE ... TEMPLATE
     copy mechanism this whole epic exists to unblock).
Re-run the full catalog/executor/server + short-mode whole-repo suites plus
tpch-spotcheck/pgbench-smoke gates after each sub-piece. **Also**: before
picking any milestone work, re-check `ci/logs/action-items.md` for new
`## AI-` items per the M-NIGHTLY standing-priority rule — this loop's item
(AI-20260710-011513-001) is now resolved but a fresh nightly run may have
surfaced something new.
