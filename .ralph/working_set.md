Task: M0123-S4 SUB-SLICE 35 LANDED — timestamp(N) length coercion

Files:
  - internal/pgnodes/datum.go: added OidTimestamp(1114), NewTimestampConst, parseTimestampMicros, formatTimestamp
  - internal/pgnodes/resolver_expr.go: foldStringLiteralConst OidTimestamp case, wrapTimestampLengthCoercion(1961), ResolveForColumnTypmod OidTimestamp case, ColumnTypmod timestamp type
  - internal/pgnodes/rebuild.go: isImplicitTimestampLengthCoercion, unwrap list, OidTimestamp Const rebuild case
  - internal/pgnodes/timestamp_lencoerce_test.go: NEW — 5 live PG18.3 goldens + structure + round-trip + ColumnTypmod
  - internal/testport/oracle_pgnodes_adbin_test.go: 5 new timestamp(N) cases (96 total)
  - .ralph/fix_plan.md: SUB-SLICE 35 landed entry

Key symbols: OidTimestamp(1114), NewTimestampConst, parseTimestampMicros, formatTimestamp,
  wrapTimestampLengthCoercion(1961), isImplicitTimestampLengthCoercion

Hypothesis/Findings:
  - SUB-SLICE 35 LANDED: timestamp(N) length coercion.
  - Uses same COERCION_PATH_FUNC pattern as varchar/bpchar — pg_cast.dat has a
    timestamp→timestamp self-cast via `timestamp(timestamp,int4)` (funcid 1961).
  - Typmod for timestamp is just the precision (0-6), no VARHDRSZ offset.
  - Unlike varchar/bpchar, the coercion FuncExpr has 2 args (no bool isExplicit third arg).
  - The formatTimestamp rebuild produces a canonical literal WITHOUT timezone (+00),
    matching PG's timestamp_in output.

Next step: Add bit(N)/varbit(N) datum support, then its self-cast FuncExpr.
  Or continue with time(N) if bit types are more complex.

Gates run: UNITS PASS (cached, 0.029s), ORACLE PASS (96/96 including 5 new timestamp vs live PG18.3, 2.01s),
  initdb build PASS

In-flight: none
