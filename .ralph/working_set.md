Task: M0123-S4 SUB-SLICE 36 LANDED — bit(N)/varbit(N) length coercion

Files:
  - internal/pgnodes/datum.go: added OidBit(1560), OidVarBit(1562), bitVarlena, parseBitFromString, formatBit, bitLenFromVarlena, bitDataFromVarlena, NewBitConst, NewVarBitConst
  - internal/pgnodes/resolver_expr.go: foldStringLiteralConst OidBit/OidVarBit cases, wrapBitLengthCoercion(1685), wrapVarBitLengthCoercion(1687), ResolveForColumnTypmod OidBit/OidVarBit cases, ColumnTypmod bit/varbit types
  - internal/pgnodes/rebuild.go: isImplicitBitLengthCoercion, isImplicitVarBitLengthCoercion, OidBit/OidVarBit Const rebuild cases, implicit cast check in rebuildFuncExprWith
  - internal/pgnodes/bit_lencoerce_test.go: NEW — 5 live PG18.3 goldens + structure + rebuild round-trip + varbit no-typmod + parse/format round-trip + ColumnTypmod
  - internal/testport/oracle_pgnodes_adbin_test.go: 6 new bit/varbit cases (102 total)

Key symbols: OidBit(1560), OidVarBit(1562), NewBitConst, NewVarBitConst,
  parseBitFromString, formatBit, bitVarlena, wrapBitLengthCoercion(1685),
  wrapVarBitLengthCoercion(1687), isImplicitBitLengthCoercion

Hypothesis/Findings:
  - SUB-SLICE 36 LANDED: bit(N)/varbit(N) length coercion.
  - Uses same COERCION_PATH_FUNC pattern as varchar/bpchar/timestamp — pg_cast.dat
    has bit→bit (funcid 1685) and varbit→varbit (funcid 1687) self-casts.
  - PG uses funcformat 2 (COERCE_IMPLICIT_CAST) for bare literals (no ::bit cast).
  - Unlike timestamp which has 2 args, bit/varbit coercion has 3 args (including
    bool isExplicit as third arg — false for implicit).
  - Varbit WITHOUT length qualifier stores bare Const (no coercion needed).
  - Broken $1Serana replace_content from previous loop's WIP was discarded and
    rebuild.go changes were re-applied cleanly with built-in Edit tool.

Next step: Add time(N) or timestamptz(N) length coercion. Or continue with any
  remaining datum types from the REMAINING list.

Gates run: UNITS PASS (0.031s), ORACLE PASS (102/102 including 6 new bit/varbit
  vs live PG18.3, 2.09s), initdb build PASS

In-flight: none
