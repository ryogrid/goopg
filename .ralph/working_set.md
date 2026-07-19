(idle — nothing in flight)

Last loop (#29): M0123-S4 sub-slice 17 — simple-form CASE WHEN-value implicit
coercion (numeric operand + int4 value). LANDED + committed. NO resolver change
(resolveCaseWhenCond already resolves the WHEN value with the operand type as its
expected type → resolveIntLiteral applies int4_numeric, buildOpExpr picks
numeric_eq). Added 2 live PG18.3 scalar adbin goldens (case_test.go
simple_numeric_operand_int_when_coerce{,_multi}) + 2 live-oracle cases
(oracle_pgnodes_adbin_test.go, now 27) + doc comment on resolveCaseWhenCond +
design 0123-0005 §"Sub-slice 17" + ledger row (int8/explicit-cast-operand
deferral).

Gates GREEN: pgnodes pkg, adbin oracle 27/27 vs PG18.3 (1.29s), build/vet/gofmt
clean, ralph-state-guard consistent (auto-repaired), pgbench smoke via pre-commit.

Next (M0123-S4 REMAINING): float4-common (no float8) CASE mix (needs
int/numeric→float4 arms + outer float8(float4) column cast); date-time-family CASE
coercion; simple-form WHEN with int8/explicit-cast operand (native cross-type op
416 + explicit-cast FuncExpr operand modeling); operator-driven view-qual coercion
(int2/timestamptz literals). Resume: internal/pgnodes/resolver_expr.go
selectCaseCommonType/coerceCaseResult/operandTypmodCollid.
