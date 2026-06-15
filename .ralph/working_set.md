Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 3 (tableoid
column label) COMPLETE this loop. NOTHING in flight; next loop starts on slice 4.

=== DONE (loop #26) — DU-002 slice 3 ===
Bug: pg_dump SEGFAULTed (exit 139) in "reading schemas" because getNamespaces'
first projected column `n.tableoid` came back labelled `?column?` instead of
`tableoid` → PQfnumber(res,"tableoid")=-1 → out-of-bounds read.
Root cause: resolveColumnRefAt lowers bare `tableoid` on a non-partitioned base
relation to a constant *TableOidExpr, but planner targetMeta
(internal/planner/planner.go ~L7697) had NO case for that node (only the
cast-wrapped tableoid::regclass form), so it fell through to ?column?.
Fix: added a *TableOidExpr arm to targetMeta returning ("tableoid", oid),
mirroring the existing *CTIDExpr → "ctid" case. Analyzer/executor naming twins
(deriveAnalyzerTargetName, deriveTargetName) operate on parser AST where
tableoid is still *parser.ColumnRef — already correct, no change.
Files: internal/planner/planner.go (targetMeta), internal/server/tableoid_test.go
(new TestTableoidColumnName), docs/design/0110-0001-pg-dump-tap-port.md,
.ralph/fix_plan.md, .ralph/deferral_ledger.md.
Gates run: go build ./... OK; planner/analyzer/executor/server unit tests PASS;
TestTableoidColumnName PASS; TestPort_PgDumpConnectionSetup PASS (verified
pg_dump now passes "reading schemas", advances to getTables). tpch-spotcheck
SKIPPED (no TPC-H data dir in env) — change is tableoid-naming-only, zero
row-count risk for TPC-H queries.

=== NEXT STEP — DU-002 slice 4 (pg_depend catalog view) ===
TestPort_PgDumpConnectionSetup now fails at getTables:
`relation "pg_depend" does not exist`. getTables runs
`pg_class c LEFT JOIN pg_depend d ON (...) LEFT JOIN pg_tablespace tsp ...
LEFT JOIN pg_am am ... LEFT JOIN pg_class tc (toast) WHERE c.relkind IN
('r','S','v','c','m','f','p') ORDER BY c.oid`.
Slice 4 = add a pg_depend virtual catalog view (search existing virtual-catalog
view registration; see per_connection_virtual_catalog_scoping memory + how
pg_class/pg_namespace views are wired). Verify pg_tablespace/pg_am/pg_class(toast)
LEFT JOINs also resolve. Then continue the getter battery (getTypes, getTables,
getIndexes, …) per pg_dump's getter order.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
