Task: M0123-S4 sub-slice 4a — implicit int→numeric cast FuncExpr.
COMPLETE this loop (committing). Closes the sub-slice-3 discovery.

Landed: a bare INTEGER literal in a numeric column context now resolves to an
implicit-cast FuncExpr (int4_numeric funcid 1740 / int8_numeric 1781, funcformat
2 = COERCE_IMPLICIT_CAST) byte-for-byte identical to PG18.3's adbin, instead of
degrading to SQL text. numeric DEFAULT 0/12345/-5/5000000000 now emit canonical
pg_attrdef.adbin via the already-wired ResolveForColumn→canonicalAttrdefText path.
- resolver_expr.go: resolveIntLiteral wraps the int4/int8 Const via new
  wrapIntToNumericCast when expected==OidNumeric (negative fold BEFORE cast).
- rebuild.go: isImplicitIntToNumericCast + rebuildFuncExprWith rebuild the cast
  to the INNER integer literal (fixed point), in the shared *With recursion.

Gates (GREEN): internal/pgnodes/numeric_cast_test.go (5 live PG18.3 adbin
goldens 12345/0/-5/5000000000/32767: forward byte-for-byte + ResolveForColumn
accepts + codec round-trip + resolve→Rebuild→re-resolve fixed point + rebuilt-
shape + int-context no-wrap guard). Sibling gates reconciled (numeric-int case
flipped SQL-text→canonical): resolver_expr_test.go TestResolveForColumn,
executor sys_pg_attrdef_test.go TestCanonicalAttrdefText, initdb
catalog_heap_reload_attrdef_test.go TestRebuildAttrdefExpr. go build ./... + go
vet (pgnodes/initdb) clean. TestE2E_FailoverGoopgToPG PASS (6.68s).
Design 0123-0005 §"Sub-slice 4a" + README index + ledger row.

Key symbols: resolveIntLiteral, wrapIntToNumericCast, isImplicitIntToNumericCast,
rebuildFuncExprWith.

Next step (next loop): M0123-S4 remaining — timestamptz Const datums (OID 1184,
by-value int64 microseconds-since-2000; PG-faithful timestamp parser; scalar+view
goldens), then CaseExpr/BooleanTest/DistinctExpr (each codec+resolver+rebuild+
scalar+view live goldens), then the byte-diff oracle gate. Lower priority:
operator-driven implicit coercion in view quals + int2→numeric.
Resume file: internal/pgnodes/datum.go + resolver_expr.go/resolver_query.go.

In-flight: none. (Nightly AI-20260719-094219-* all [x] stale-verified.)
