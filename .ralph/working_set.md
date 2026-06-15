Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 4 (getTables
catalog views) COMPLETE this loop. NOTHING in flight; next loop starts on slice 5.

=== DONE (loop #27) — DU-002 slice 4 ===
pg_dump's getTables (pg_dump.c:7080-7239) is one big SELECT FROM pg_class c
LEFT JOIN pg_depend / pg_tablespace / pg_am / pg_class(toast), plus a relkind='f'
subquery against pg_foreign_table. Three of those relations were not exposed to
the SQL query layer (pg_am already existed), so the query aborted with
`relation "…" does not exist`, one per probe iteration.
Fix: added 3 virtual catalog views in internal/catalog/catalog.go (right after the
pg_am block), schemas matching upstream exactly:
- pg_depend (OID 2608, 7 cols) — EMPTY (goopg has no dependency graph; LEFT JOIN
  → NULL owning_tab/owning_col, is_identity_sequence=false). Correct by construction.
- pg_tablespace (OID 1213, oid/spcname/spcowner/spcacl/spcoptions) — bootstrap
  pg_default(1663)/pg_global(1664) owner 10 + M0095-0003 runtime in-place
  tablespaces, OID-ordered. New method InMemory.tablespaceVirtualRows (read-locked
  over c.tablespaces). Runtime ts report spcowner=10 (no owner-name→OID resolution).
- pg_foreign_table (OID 3118, ftrelid/ftserver/ftoptions) — EMPTY (no FDW).
Files: internal/catalog/catalog.go (3 views + tablespaceVirtualRows),
internal/catalog/tablespace_test.go (TestPgTablespaceVirtualView,
TestPgDependAndForeignTableViews), docs/design/0110-0001-pg-dump-tap-port.md,
.ralph/fix_plan.md.
Gates run: go build ./... OK; gofmt/vet clean; catalog + executor + server +
planner unit suites PASS; new catalog tests PASS; TestPort_PgDumpConnectionSetup
PASS (verified pg_dump now resolves all getTables relations, advances to the
array_remove() blocker). tpch-spotcheck N/A (additive virtual-view registration
only; zero existing query-path or row-count risk).

=== NEXT STEP — DU-002 slice 5 (array_remove() builtin) ===
TestPort_PgDumpConnectionSetup now fails at getTables with
`function array_remove does not exist`. getTables uses
`array_remove(array_remove(c.reloptions,'check_option=local'),'check_option=cascaded')`
to strip view check_option markers from reloptions. Slice 5 = add the
array_remove(anyarray, anyelement) scalar builtin to the executor
(internal/executor/expr.go — search how array funcs like array_remove's siblings
e.g. array_to_string / array_append are dispatched; check pg_proc seeding for the
OID). Then continue the getter battery (getTypes, getTables tail, getIndexes, …)
per pg_dump's getter order.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
