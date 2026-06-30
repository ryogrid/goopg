(idle — nothing in flight)

Loop #43 COMPLETE (pending commit): M0119-0004 DU-002 slice 402 — CREATE
CONVERSION FROM-function existence/return-type validation (closes the
slice-399/400/401 deferral (b)-remainder). New executor helper
resolveConversionFunc (operators_ddl.go) mirrors PG's LookupFuncName(...,
{int4,int4,cstring,internal,int4,bool}, false) + get_func_rettype != INT4OID:
scans im.Routines().LookupByName overloads for the fixed 6-arg signature
(int4/int4/cstring/internal/int4/bool via catalog.TypeNameToOID for the
int4/bool slots, literal match for cstring/internal pseudotypes); no match ->
42883 "function <name>(integer, integer, cstring, internal, integer,
boolean) does not exist"; wrong return type -> 42P17 "encoding conversion
function <name> must return type integer". execCompatNoop case "conversion"
calls it after the encoding checks (PG's order), before registering.
TestPort_PgDumpConnectionSetup setup now CREATE FUNCTIONs myconv_func /
aliasconv_func (LANGUAGE c stub, real 6-arg signature) before the CREATE
[DEFAULT] CONVERSION statements that reference them, plus two new negative
cases (missing function -> 42883, wrong return type -> 42P17). Tests:
TestResolveConversionFunc (executor, 7 cases) + slice-402 assertions in
TestPort_PgDumpConnectionSetup. Gates: executor/catalog/parser unit suites
PASS; TestPort_PgDumpConnectionSetup PASS (5.05s); TPC-H spotcheck Q12=2/
Q13=33 PASS; make ralph-state-guard OK; pgbench smoke = pre-commit hook.
Not yet committed — about to `git add` the 3 source files + deferral ledger
and commit (NOT .ralph/progress.json or the postgres/ submodule artifacts,
those are unrelated driver/build noise).

Remaining open conproc fidelity gap (recorded in ledger, NOT this loop's
scope): conproc still renders from UserConversion.ProcSchema/ProcName text
captured at CREATE time, not a FuncOID->pg_proc cross-ref (unlike
pg_cast.castfunc, slice 397) — a later RENAME/DROP CASCADE on the function
wouldn't propagate. EXECUTE ACL check and runtime OidFunctionCall6 probe
also still unimplemented (probe needs an encoding-conversion engine, out of
scope for goopg).

Next loop candidates (M0119-0004 pg_dump slices):
- conproc OID cross-ref (UserConversion.FuncOID + pg_conversion view resolves
  via routine registry by OID like findFuncByOid) — cosmetic-only under
  current semantics, low priority.
- cast/conversion/collation registry RESTART PERSISTENCE (recurring deferral
  c): WAL-log CreateConversion + replay like CREATE SCHEMA.
- CREATE TRANSFORM round-trip.
- Re-scan deferral_ledger.md for other open "| - |" rows (M0119-0004 has many
  accumulated CHECK-predicate / domain / literal-cast type-blind deferrals
  from slices 360-363 era) for the next bounded slice.
