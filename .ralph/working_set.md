Task: M0123-S4 sub-slice 5 — canonical BOOLEANTEST (x IS [NOT] TRUE/FALSE/UNKNOWN).
COMPLETE this loop (committing).

Resumed prior cut-off iteration's uncommitted WIP: it had landed the full
codec+resolver+rebuild chain for BooleanTest in 5 pgnodes files but never
committed or added a golden test. This loop verified it against live PG18.3,
added the golden test, updated docs+ledger, and committed.

Landed: `x IS TRUE` is a dedicated BooleanTest node (primnodes.h), not an
operator/NullTest; result always bool; stored unfolded in pg_attrdef.adbin.
- ir.go: BooleanTest{Arg,BoolTestType,Location} + 6-value ordinal enum
  (IsTrue=0..IsNotUnknown=5).
- outfuncs.go: outBooleanTest → `{BOOLEANTEST :arg … :booltesttype N :location -1}`
  (booltesttype is a PLAIN INT — WRITE_ENUM_FIELD, unlike BoolExpr's :boolop token).
- readfuncs.go: readBooleanTest (accepts any int; range-check → Rebuild).
- resolver_expr.go: resolveBooleanTest + booleanTestType(flags→ordinal) +
  resolveBooleanTestWith(rec) injectable variant (ready for view quals).
- rebuild.go: rebuildBooleanTest (exact inverse) + rebuildBooleanTestWith(rec);
  out-of-range ordinal → clean error.
- booleantest_test.go (NEW): 6 live PG18.3 adbin goldens (one per ordinal;
  last two over an OPEXPR arg) — forward byte-for-byte + ResolveForColumn accepts
  + codec round-trip + resolve→Rebuild→re-resolve fixed point + bad-ordinal reject.

Gates (GREEN): pgnodes package, executor TestCanonicalAttrdef*, initdb attrdef
reload, go build ./... + go vet ./internal/pgnodes/. pgbench smoke via pre-commit.
Design 0123-0005 §"Sub-slice 5" + README index row + ledger row appended.

Key symbols: BooleanTest, booleanTestType, resolveBooleanTestWith,
rebuildBooleanTestWith, outBooleanTest, readBooleanTest.

Next step (next loop): route the VIEW-QUERY dispatch — resolver_query.go
queryScope.resolveExpr `*parser.IsBoolExpr`→resolveBooleanTestWith(_, s.resolveExpr)
and rebuild_query.go viewRebuildScope.rebuildExpr `*BooleanTest`→rebuildBooleanTestWith
(mirrors sub-slice 2's bool/null view wiring); add a view WHERE-qual BOOLEANTEST
golden to view_bool_null_test.go + standby pg_get_viewdef parse assertion. Then
CaseExpr/DistinctExpr (each codec+resolver+rebuild+scalar+view, live goldens),
then the byte-diff oracle harness. Resume file: internal/pgnodes/resolver_query.go
+ rebuild_query.go dispatch.

In-flight: none.
