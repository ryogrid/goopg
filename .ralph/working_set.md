(idle — nothing in flight)

Last completed: M0134-0036 (create_table_like.sql) PARKED and committed
(790157a9 impl + 23e15b87 bookkeeping) — shipped `LIKE INCLUDING/EXCLUDING
COMPRESSION` (parser `ddl.go` compression case mirroring `storage`; executor
`operators_ddl.go` LIKE-merge loop gained `includeCompression` gating) and
`pg_get_statisticsobjdef_columns` (catalog `catalog.go` factored
`BuildStatisticsObjDef`'s ON-list rendering into `statisticsObjColumnsList`,
reused by new `BuildStatisticsObjDefColumns`; `expr.go` gained the builtin-
function case, mirroring the existing `pg_get_statisticsobjdef` case). Sized
at HEAD (sorted/normalized comparator): 32 content lines / 7 `^+ERROR` / 2
`^-ERROR`; result 7 -> 2 `^+ERROR`. Two buckets left unshipped (REFACTOR/
needs-repro): LIKE-sequence/composite-type resolution (blocked on
`SearchPathCatalog` lacking `HasCompositeType` passthrough — dead-code-
behind-live-wrapper pattern) and multi-parent INHERITS storage-conflict
detection not firing (needs a live repro). Design doc not needed (mechanical,
same size class as 0031-0035's shipped buckets). Three deferral rows appended
in .ralph/deferral_ledger.md (2026-08-20, M0134-0036): LIKE-copied statistics
objects lose their column/expr list (RegisterStatistics vs
RegisterStatisticsFull), a `regnamespace` cast bug rendering literal OID
instead of schema name in `\d+`'s footer, and `INCLUDING ALL` on a FOREIGN
TABLE wrongly copying COMPRESSION (PG guards this with `!cxt->isforeign`).

Next loop: per fix_plan.md banner, select M0134-0037 (join_hash.sql, status
`failed`) — same sizing pattern as 0006..0036 (researcher sizes at HEAD
first, confirm not stale, bucket root causes CONTAINED vs REFACTOR-tier, ship
the smallest CONTAINED bucket or PARK with ledger rows).

Gates run this loop: go build ./... PASS; go test
./internal/parser/... ./internal/executor/... ./internal/catalog/... PASS;
RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh PASS (worker
round); pg-regress-runner create_table_like PASS-run (net improvement,
verified numbers match worker report); pgbench pre-commit smoke PASS on both
commits; make ralph-state-guard TBD (run before finishing this loop).

In-flight: none.
