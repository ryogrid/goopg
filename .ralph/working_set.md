Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 5 (array_remove()
scalar builtin) COMPLETE this loop. NOTHING in flight; next loop starts on slice 6.

=== DONE (loop #28) — DU-002 slice 5 ===
getTables' reloptions projection
  array_remove(array_remove(c.reloptions,'check_option=local'),'check_option=cascaded')
aborted with `function array_remove does not exist`. array_remove was already
seeded in pg_proc (OID 3167, HandlerName "array_remove") — only the EXECUTOR
HANDLER was missing, so evalFuncCall fell through to evalStoredRoutineFuncCall →
42883. Fix: added a `case "array_remove":` to evalFuncCall in
internal/executor/expr.go (right after array_cat, before array_dims). Semantics:
removes every element == arg2 from goopg's text-array form via
parseTextArray/formatTextArray; formatted element-text equality (matches the
array_append/_cat siblings; NULL element → the "NULL" placeholder those siblings
emit); NULL array → NULL (PG array_remove is NotStrict on element, array-strict).
Files: internal/executor/expr.go (handler), internal/executor/array_remove_test.go
(TestEvalArrayRemove + TestEvalArrayRemoveNested), docs/design/0110-0001-pg-dump-
tap-port.md (slice 5 block + next-blocker), .ralph/fix_plan.md, pgdump_connsetup_test.go
(stale next-blocker comment refreshed).
Gates run: go build ./... OK; gofmt clean; go vet ./internal/executor OK; executor
unit suite PASS; new array_remove tests PASS; TestPort_PgDumpConnectionSetup PASS
(getTables now completes; pg_dump advances to getFuncs). tpch-spotcheck N/A
(additive switch case only; zero existing query-path/row-count risk).

=== NEXT STEP — DU-002 slice 6 (pg_init_privs virtual view) ===
TestPort_PgDumpConnectionSetup now fails in getFuncs with
`relation "pg_init_privs" does not exist`. pg_dump's getFuncs (and getTables,
getTypes, …) LEFT JOIN `pg_init_privs pip ON (oid=pip.objoid AND
pip.classoid='<catalog>'::regclass AND pip.objsubid=0)` to diff stored *acl
against the object's initial (extension-installed) privileges. Slice 6 = add the
`pg_init_privs` virtual catalog view (internal/catalog/catalog.go, beside the
slice-4 pg_depend/pg_tablespace/pg_foreign_table block) — EMPTY by construction
(goopg installs no extensions → no recorded initial privileges → LEFT JOIN yields
NULL initprivs, the `proacl IS DISTINCT FROM pip.initprivs` predicate degenerates
correctly). Schema (pg_init_privs.h): oid? NO — columns are
(objoid oid, classoid oid, objsubid int4, privtype "char", initprivs aclitem[]),
NO oid system column. Then continue the getter battery (getFuncs tail, getTypes,
getIndexes, …) per pg_dump's getter order.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
