Task: M0123-S4 REMAINING #3 — other length types (`timestamp(N)`, `bit(N)`/`varbit(N)`, `time(N)`)

Files:
  - internal/pgnodes/datum.go: added OidVarchar(1043), OidBpchar(1042), NewVarcharConst, NewBpcharConst
  - internal/pgnodes/resolver_expr.go: foldStringLiteralConst varchar/bpchar, wrapVarchar/BpcharLengthCoercion, ColumnTypmod, ResolveForColumnTypmod generalized
  - internal/pgnodes/rebuild.go: isImplicitVarchar/BpcharLengthCoercion, rebuildConst OidText+OidVarchar+OidBpchar
  - internal/executor/sys_pg_attrdef.go: ColumnTypmod replaces NumericColumnTypmod
  - internal/pgnodes/varchar_lencoerce_test.go: NEW — 6 live PG18.3 goldens + rebuild round-trip + bare-column + ColumnTypmod
  - internal/testport/oracle_pgnodes_adbin_test.go: colSQLTypmod replaces numericColSQLTypmod + 6 new varchar/bpchar cases
  - docs/design/0123-0005-pgnodes-bool-null-scalar.md: Deferred section updated
  - .ralph/fix_plan.md: SUB-SLICE 34 landed entry

Key symbols: wrapVarcharLengthCoercion(669), wrapBpcharLengthCoercion(668), ColumnTypmod, ResolveForColumnTypmod, NewVarcharConst, NewBpcharConst

Hypothesis/Findings:
  - SUB-SLICE 34 LANDED: varchar(N) / bpchar(N) length coercion.
  - The CoerceViaIO hypothesis was REFUTED — PG uses COERCION_PATH_FUNC (same FuncExpr
    pattern as numeric). Every typmod-capable type has a pg_cast self-cast entry
    (varchar→varchar funcid 669, bpchar→bpchar funcid 668, both 3-arg with isExplicit).
  - varchar(N) typmod = N + VARHDRSZ(4); stored in the FuncExpr's int4 arg.
  - REMAINING: timestamp(N), bit(N)/varbit(N), time(N) — each needs its own datum
    support (timestamp without tz, bit literal parsing) before typmod coercion can land.

Next step: Add timestamp(N) datum support (timestamp without timezone, OID 1114),
  then add its self-cast `timestamp(timestamp,int4)` FuncExpr (funcid from pg_cast.dat).

Gates run: UNITS PASS (cached), ORACLE PASS (91/91 including 6 new varchar/bpchar vs live PG18.3, 1.97s),
  initdb PASS

In-flight: none
