Loop #37 COMPLETE: M0119-0004 DU-002 slice 397 — a WITH FUNCTION cast
(`CREATE CAST (text AS integer) WITH FUNCTION public.text_as_int(text)`) now
round-trips through real pg_dump 18.3. Closes the slice-395 WITH FUNCTION deferral.

Root cause: slice 395 parsed the WITH FUNCTION form but DISCARDED the function
reference, leaving pg_cast.castfunc=0; dumpCast's COERCION_METHOD_FUNCTION arm then
warned "bogus value in pg_cast.castfunc" and emitted no function clause.

Three-layer fix (committed): the only requirement is castfunc == the function's
pg_proc.oid (dumpCast renders the signature from the function's REAL proargtypes via
format_function_signature, not from the user's typed arg list).
- internal/parser/ddl.go parseCreateCastTail: method="f" branch now parses
  `WITH FUNCTION funcname[(argtypes)]` → new CompatNoopStmt.CastFuncName/CastFuncArgs
  (ast.go). parseObjectName for name + comma'd parseCastTypeName loop for bare args.
- internal/executor/operators_ddl.go execCompatNoop "cast": for Method=="f" resolves
  the routine via Routines().Lookup(CastFuncName, argTypes) (explicit args → exact
  overload, mirrors COMMENT ON FUNCTION slice 147; LookupByName sole-overload fallback
  when parens omitted), passes routine.OID as new funcOID param to RegisterCast.
  Routine.OID == pg_proc virtual-view OID, so func/cast cross-ref matches.
- internal/catalog/catalog.go: RegisterCast gains funcOID uint32 param stored on
  Cast.FuncOID. pg_cast virtual row already surfaced FuncOID(castfunc)/Method(castmethod).
- Tests: internal/parser/create_cast_test.go TestParseCreateCastWithFunction (NEW file);
  slice-397 fixture+assertion in internal/testport/pgdump_connsetup_test.go.
- docs/design/0110-0001-pg-dump-tap-port.md slice 397; fix_plan + ledger row.

Gates: TestPort_PgDumpConnectionSetup PASS (6.9s); parser+catalog units PASS; build
clean. Verified byte-identical against real pg_dump 18.3 live (/tmp/castfn_pg cluster:
`CREATE CAST (text AS integer) WITH FUNCTION public.text_as_int(text);`). pgbench smoke
runs via pre-commit hook. No query-exec/codec/planner path touched.

Next loop: fresh M0119-0004 pg_dump slice. Candidates: cast/collation/type registry
restart persistence (the recurring 389–397 deferral — WAL-log CREATE CAST + castfunc
like CREATE SCHEMA in operators_ddl.go schema arm, replay re-resolving func OID);
CREATE CONVERSION (needs pg_encoding_to_char builtin + conproc regproc resolution,
harder — conversion funcs are C-language); CreateCast argument/return-type validation.
