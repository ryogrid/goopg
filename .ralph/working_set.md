Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 6 (pg_init_privs
virtual view) COMPLETE this loop. NOTHING in flight; next loop starts on slice 7.

=== DONE (loop #29) — DU-002 slice 6 ===
pg_dump's getFuncs (like getTables/getTypes/…) LEFT-JOINs
  pg_init_privs pip ON (p.oid=pip.objoid AND pip.classoid='pg_proc'::regclass
                        AND pip.objsubid=0)
to diff stored proacl vs. initial privileges; aborted with
`relation "pg_init_privs" does not exist`. Fix: added the pg_init_privs virtual
view in internal/catalog/catalog.go, immediately after the slice-4
pg_depend/pg_tablespace/pg_foreign_table block. PG's exact schema (pg_init_privs.h
CATALOG line, OID 3394): columns (objoid oid, classoid oid, objsubid int4,
privtype "char", initprivs aclitem[]) — and like the upstream catalog NO oid
system column. EMPTY by construction (VirtualRows returns nil): goopg installs no
extensions and snapshots no initdb-time ACLs, so the LEFT JOIN yields NULL
pip.initprivs and `proacl IS DISTINCT FROM pip.initprivs` degenerates to "dump
the full ACL" (correct).
Files: internal/catalog/catalog.go (view), docs/design/0110-0001-pg-dump-tap-port.md
(slice-6 block + refreshed next-blocker), .ralph/fix_plan.md (loop #29 progress),
internal/testport/pgdump_connsetup_test.go (stale next-blocker comment refreshed).
Gates run: go build ./... OK; gofmt clean; go vet ./internal/catalog OK; catalog
+ executor unit suites PASS; TestPort_PgDumpConnectionSetup PASS (getFuncs now
resolves pg_init_privs; pg_dump advances past the join). tpch-spotcheck N/A
(additive empty virtual view only; zero existing query-path/row-count risk).

=== NEXT STEP — DU-002 slice 7 (pg_proc pronargs/proacl/proowner + pg_cast/pg_transform) ===
TestPort_PgDumpConnectionSetup now fails in getFuncs with
`column p.pronargs does not exist`. The getFuncs SELECT projects
p.tableoid, p.oid, p.proname, p.prolang, p.pronargs, p.proargtypes, p.prorettype,
p.proacl, acldefault('f', p.proowner) AS acldefault, p.pronamespace, p.proowner
and its WHERE filters on EXISTS subqueries over pg_cast (castfunc) and
pg_transform (trffromsql/trftosql). goopg's pg_proc virtual view
(internal/initdb/pg_proc_view.go registerPgProcView) lacks pronargs, proacl,
proowner; pg_cast/pg_transform catalog views don't exist. Slice 7 = add the three
pg_proc columns (pronargs int2 = len(argtypes); proacl aclitem[] = NULL/empty;
proowner oid = 10 bootstrap superuser) to registerPgProcView's Columns + both
row-builders (builtinProcs loop AND user-routine loop — sibling paths!), and add
empty pg_cast (OID 2602: oid, castsource, casttarget, castfunc, castcontext,
castmethod) + pg_transform (OID 3576: oid, trftype, trflang, trffromsql, trftosql)
virtual views beside pg_init_privs in catalog.go. Then continue the getter battery
(getFuncs tail, getTypes, getIndexes, …) per pg_dump's getter order.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
