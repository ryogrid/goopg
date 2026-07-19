(idle — nothing in flight)

Last loop (#39): M0123-S4 sub-slice 25 — EXPLICIT bare-`numeric` cast RelabelType
(relabelformat 1). LANDED + committed + pushed. The explicit counterpart of sub-slice
24: `(5.5::numeric(8,1))::numeric` collapses to a RelabelType stamped COERCE_EXPLICIT_CAST
(relabelformat 1, vs the implicit relabelformat-2 form), and pg_get_expr renders the
VISIBLE `::numeric` syntax so Rebuild reconstructs a bare `::numeric` CastExpr. Live-probed
2 shapes byte-identical to PG18.3.

Files: internal/pgnodes/resolver_expr.go (resolveCastExpr bare-numeric arm: numeric operand
carrying a typmod → wrapNumericRelabelToBare(arg,1); wrapNumericRelabelToBare generalized to
take relabelformat); rebuild.go (rebuildRelabelType now switches on relabelformat — 2 unwrap,
1 → CastExpr{numeric}); numeric_relabel_explicit_test.go (NEW: 2 goldens + guards);
numeric_relabel_test.go (reject-guard flipped relabelformat 1→0); oracle_pgnodes_adbin_test.go
(+2, now 61). NO executor change. Design 0123-0005 §"Sub-slice 25" + README index + ledger.

Gates GREEN: full pgnodes pkg; adbin oracle 61/61 byte-identical vs LIVE PG18.3 (1.63s);
ev_action oracle 13/13; executor attrdef siblings; go vet (pgnodes/testport/executor), gofmt
clean; pgbench smoke via pre-commit.

Next (M0123-S4 REMAINING): (1) float4-common (no float8) CASE result mix — int/numeric→float4
arms + outer float8(float4) column cast (selectCaseCommonType/coerceCaseResult); (2)
date-time-family CASE coercion — needs a `date` OID-1082 datum (datum.go) + `::date`/
`::timestamptz` string-literal fold; (3) other length types (varchar(N)=CoerceViaIO,
timestamp(N), bit(N)); (4) operator-driven view-qual coercion. ALL numeric column/typmod
DEFAULT + explicit-cast RelabelType shapes are now canonical.
