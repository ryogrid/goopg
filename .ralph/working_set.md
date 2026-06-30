(idle — nothing in flight)

Loop #42 COMPLETE (pending commit): M0119-0004 DU-002 slice 401 — CREATE
CONVERSION encoding-name validation (closes encoding-name half of slice-399
deferral (b)). New executor helper validateCreateConversionEncodings
(operators_ddl.go) mirrors PG CreateConversionCommand static checks: unknown
source/destination encoding → 42704; SQL_ASCII endpoint → 42P17. execCompatNoop
case "conversion" calls it and reuses resolved forEnc/toEnc IDs. Tests:
TestValidateCreateConversionEncodings (executor) + slice-401 negative assertions
in TestPort_PgDumpConnectionSetup. Gates: executor validation + cast tests PASS;
TestPort_PgDumpConnectionSetup PASS (4.9s); pgbench smoke = pre-commit hook.

Next loop candidates (M0119-0004 pg_dump slices):
- CONVERSION FROM-function pg_proc validation (closes slice-399 deferral (b)
  remainder): needs a real (int4,int4,cstring,internal,int4,bool)→int4 function
  in the catalog so im.Routines().LookupByName resolves; store proc OID, surface
  conproc as regproc→pg_proc cross-ref like castfunc (slice 397). The slice-399
  test's myconv_func does not exist, so faithful validation needs the test to
  CREATE FUNCTION first. Runtime OidFunctionCall6 probe needs a conversion engine.
- cast/conversion/collation registry RESTART PERSISTENCE (recurring deferral c):
  WAL-log CreateConversion + replay like CREATE SCHEMA.
- CREATE TRANSFORM round-trip; column-level attacl heap re-sync GRANT slice.
