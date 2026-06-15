Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 7 (pg_proc
pronargs/proacl/proowner + pg_cast/pg_transform views) COMPLETE this loop.
NOTHING in flight; next loop starts on slice 8 (pg_language view).

=== DONE (loop #30) — DU-002 slice 7 ===
pg_dump's getFuncs projects p.pronargs, p.proacl, p.proowner and filters on
EXISTS over pg_cast.castfunc / pg_transform.trffromsql|trftosql; aborted with
`column p.pronargs does not exist`. Fixes:
- internal/initdb/pg_proc_view.go (registerPgProcView): added 3 columns —
  pronargs int2 = len(proargtypes); proacl aclitem[] = "" (NULL, no per-routine
  grants); proowner oid = "10" (bootstrap superuser). Updated BOTH row-builders
  (builtinProcs loop uses len(strings.Fields(b.argTypes)); user-routine loop uses
  len(r.ArgTypes)) — sibling paths. New column order: oid,proname,pronamespace,
  prolang,prorettype,proargtypes,pronargs,proacl,proowner,prosrc,provolatile,…
- internal/catalog/catalog.go (beside pg_init_privs): added empty pg_cast
  (OID 2605: oid,castsource,casttarget,castfunc,castcontext,castmethod) +
  pg_transform (OID 3576: oid,trftype,trflang,trffromsql,trftosql). Both
  VirtualRows return nil. castfunc/trffromsql/trftosql typed oid (PG regproc is
  oid-compatible) so p.oid = … comparisons resolve. NOTE: pg_cast OID is 2605
  (working_set previously said 2602 — WRONG, verified from pg_cast.h).
- pg_proc_view_test.go (TestPgProcViewRendersRoutine): prosrc moved index 6→9;
  added pronargs/proacl/proowner assertions.
- Test next-blocker comment + design doc slice-7 block + fix_plan loop #30.
Gates run: go build ./... OK; gofmt clean; go vet catalog+initdb OK; catalog +
initdb unit suites PASS; TestPort_PgDumpConnectionSetup PASS (getFuncs now
completes). tpch-spotcheck N/A (additive empty virtual views + 3 pg_proc cols;
zero existing query-path/row-count risk).

=== NEXT STEP — DU-002 slice 8 (pg_language virtual view) ===
TestPort_PgDumpConnectionSetup now fails in getProcLangs with
`relation "pg_language" does not exist`. Query:
  SELECT tableoid, oid, lanname, lanpltrusted, lanplcallfoid, laninline,
         lanvalidator, lanacl, acldefault('l', lanowner) AS acldefault, lanowner
  FROM pg_language WHERE lanispl ORDER BY oid
Slice 8 = add an empty pg_language virtual view (OID 2612) beside pg_cast in
catalog.go. Schema (pg_language.h): oid, lanname name, lanowner oid, lanispl
bool, lanpltrusted bool, lanplcallfoid oid, laninline oid, lanvalidator oid,
lanacl aclitem[]. EMPTY is correct: `WHERE lanispl` filters out the built-in
internal/c/sql langs, so only user-installed PLs (none in goopg) are dumped.
Then continue the getter battery (getTypes, getTables tail, getIndexes, …).

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
