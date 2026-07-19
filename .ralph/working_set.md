(idle — nothing in flight)

Last loop (#30): M0123-S4 sub-slice 18 — simple-form CASE WHEN-value NATIVE
cross-type operator. LANDED + committed. resolveCaseWhenCond
(internal/pgnodes/resolver_expr.go) now models PG make_op's two phases: resolve
the WHEN value at its NATURAL type (rec(when,0)), then (1) use a native
(operand,value) `=` operator un-coerced when one exists — incl. cross-type
int8=int4 (416) / int4=int8 (15) — else (2) coerce the value up via
coerceCaseResult (sub-slice 17's numeric path, byte-identical, no golden churn).

Gates GREEN: pgnodes pkg; case_test.go 2 new goldens
(simple_int8_operand_int4_when_native opno 416 / simple_int4_operand_int8_when_native
opno 15) via golden/codec/rebuild loops; adbin oracle 29/29 vs PG18.3 (1.32s);
ev_action oracle green; build/vet/gofmt clean; state-guard consistent (auto-repaired);
pgbench smoke via pre-commit.

Next (M0123-S4 REMAINING): explicit-cast operand simple CASE (CASE 5::int8 WHEN 1…
— model int8(int4) funcformat-1 FuncExpr operand in operandTypmodCollid);
float4-common (no float8) CASE mix (int/numeric→float4 arms + outer float8(float4)
column cast); date-time-family CASE coercion; operator-driven view-qual coercion
(int2/timestamptz literals). Resume: internal/pgnodes/resolver_expr.go
operandTypmodCollid / selectCaseCommonType / coerceCaseResult.
