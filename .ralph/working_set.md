(idle — nothing in flight)

Task just completed: M0122-0007 follow-up 6 — `REINDEX ... CONCURRENTLY`
physical rebuild (all three object types: INDEX/TABLE/SCHEMA) via a
shadow-file build-then-swap (`buildIndexShadow`/`swapRelationPhysicalFile`/
`rebuildIndexConcurrently`/`rebuildTableIndexesConcurrently`,
internal/executor/operators_{reindex,ddl}.go). Committed and pushed to
origin/align-data-structure-with-pg. All gates passed (unit tests incl. 3 new
non-vacuous ones, isolation specs reindex-concurrently/-toast/-schema +
multiple-cic unchanged, tpch-spotcheck Q12=2/Q13=33, pgbench smoke 0 failed
x3), live-verified against a real cmd/goopg binary (corrupt index → REINDEX
INDEX CONCURRENTLY repairs it, same relfilenode, survives restart), design
docs (0122-0007, 0122-0017, README index) + fix_plan.md + deferral ledger
updated.

Next step for a future loop: M0122-0007's only remaining item is slice 4 —
per-database catalog namespace (catalog.InMemory.tables/indexes have no
per-database key yet; prerequisite for CREATE DATABASE ... TEMPLATE actually
copying relations, and for routing relation I/O through base/<dbOid> instead
of catalog.DefaultDBOid — see docs/design/0122-0017-database-ddl-drop-guards.md
"Still open" section and its linked deferral ledger row). This is a LARGE
multi-loop architectural refactor (map[dbOid]*perDatabaseCatalog wrapping the
current single map, threaded through every lookup site in
internal/executor/internal/planner) — read the deferral ledger's slice-2 row
resume point before starting, and expect to scope it down into sub-slices
rather than attempting it whole. Separately, this loop's own follow-up left
one documented, deliberately-deferred gap: REINDEX/CREATE INDEX CONCURRENTLY
both do a single heap scan with no second validation pass (unlike upstream's
validate_index incremental catch-up) — a write racing the shadow build's scan
window may not appear in the rebuilt index. See the 2026-07-09 deferral
ledger row (task-id "REINDEX ... CONCURRENTLY shadow-file build-then-swap")
for the resume point if a future loop wants to close both CONCURRENTLY forms
together.
