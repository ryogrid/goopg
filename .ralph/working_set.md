Loop #38 COMPLETE: M0119-0004 DU-002 slice 398 — CREATE CAST argument/return-type
validation. Closes the slice-397 deferral (c). goopg now rejects malformed
`CREATE CAST` the way PG's CreateCast (functioncmds.c) does, with SQLSTATE 42P17.

Landed:
- internal/executor/operators_ddl.go: new free fns `validateCreateCast(s, routine)`
  + `castTypeOIDMatch(a,b)`; `execCompatNoop` "cast" case now captures the resolved
  routine (slice-397 Lookup/LookupByName) and calls validateCreateCast before
  RegisterCast. Rules: WITH FUNCTION → pronargs(OUT-excluded) 1–3, arg0==source,
  arg1==integer, arg2==boolean, rettype==target, normal-fn (not procedure/window),
  not setof; same-type allowed only for a 2+-arg length-coercion fn (all methods).
  Type identity = catalog.TypeNameToOID OID compare (integer/int4, boolean/bool) with
  case-insensitive name fallback for user/OID-0 types.
- internal/executor/create_cast_validate_test.go (NEW): TestValidateCreateCast, 18
  accept/reject cases asserting message substring + 42P17.
- docs/design/0110-0001-pg-dump-tap-port.md: slice 398 section.
- .ralph/deferral_ledger.md: slice-398 row (deferrals a–d below).

Gates: go build ./internal/executor PASS; TestValidateCreateCast PASS;
TestPort_PgDumpConnectionSetup PASS (5.6s, slices 395–397 still green); pgbench
smoke runs via pre-commit hook on commit. No codec/planner/query-exec path touched.

Deferred (carry-forward): (a) binary-coercibility modeled as identity only (PG's
IsBinaryCoercibleWithCast accepts coercible-but-not-identical); (b) unresolved WITH
FUNCTION ref not rejected (PG errors 42883 "function does not exist") — kept lenient;
(c) WITHOUT-FUNCTION physical-compat checks (typlen/byval/align, composite/array/
range/enum) + pseudo/domain WARNINGs not ported; (d) ownership/USAGE-ACL + superuser
checks not ported (DDL has no role-bearing Context).

Next loop: fresh M0119-0004 pg_dump slice. Candidates: DROP CAST validation/error
parity; cast/collation/type registry RESTART PERSISTENCE (the recurring 389–398
deferral — WAL-log CREATE CAST + castfunc like CREATE SCHEMA, replay re-resolving
func OID); column-level attacl heap re-sync GRANT slice; CREATE CONVERSION.
