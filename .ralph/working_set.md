Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 8 (pg_language
virtual view) COMPLETE this loop. NOTHING in flight; next loop starts on slice 9
(pg_operator view).

=== DONE (loop #31) — DU-002 slice 8 ===
pg_dump's getProcLangs runs `SELECT tableoid, oid, lanname, lanpltrusted,
lanplcallfoid, laninline, lanvalidator, lanacl, acldefault('l', lanowner) AS
acldefault, lanowner FROM pg_language WHERE lanispl ORDER BY oid`; aborted with
`relation "pg_language" does not exist`. Fix:
- internal/catalog/catalog.go (beside pg_transform): added empty pg_language
  virtual view OID 2612, schema from pg_language.h: oid, lanname name, lanowner
  oid, lanispl bool, lanpltrusted bool, lanplcallfoid oid, laninline oid,
  lanvalidator oid, lanacl aclitem[]. VirtualRows returns nil. EMPTY is correct:
  WHERE lanispl filters out built-in internal/c/sql langs (lanispl=false), only
  user PLs (none in goopg) dumped. lanowner typed oid so acldefault resolves.
- pgdump_connsetup_test.go: updated slice-7→8 next-blocker comment to pg_operator.
- design doc 0110-0001 slice-8 block + fix_plan loop #31 entry.
Gates run: go build ./... OK; gofmt clean; go vet catalog OK; catalog + initdb
unit suites PASS; TestPort_PgDumpConnectionSetup PASS (getProcLangs completes).
tpch-spotcheck N/A (additive empty virtual view; zero query-path/row-count risk).

=== NEXT STEP — DU-002 slice 9 (pg_operator virtual view) ===
TestPort_PgDumpConnectionSetup now fails in getOperators with
`relation "pg_operator" does not exist`. Query:
  SELECT tableoid, oid, oprname, oprnamespace, oprowner, oprkind, oprleft,
         oprright, oprcode::oid AS oprcode FROM pg_operator
Slice 9 = add an empty pg_operator virtual view (OID 2617) beside pg_language in
catalog.go. Schema (pg_operator.h): oid, oprname name, oprnamespace oid, oprowner
oid, oprkind char, oprcanmerge bool, oprcanhash bool, oprleft oid, oprright oid,
oprresult oid, oprcom oid, oprnegate oid, oprcode regproc/oid, oprrest oid,
oprjoin oid. (Query only needs oprname,oprnamespace,oprowner,oprkind,oprleft,
oprright,oprcode.) EMPTY correct: built-in operators are in pg_catalog and
filtered out by namespace dumpability; only user operators dumped (none here).
Note oprcode is regproc in PG (oid-compatible) — type oid; `oprcode::oid` cast
must resolve. Then continue getter battery (getTypes, getTables tail, getIndexes…).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
