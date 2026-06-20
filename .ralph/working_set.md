(idle — nothing in flight)

DU-002 slice 301 LANDED (fixture-only). Function-argument DEFAULT with a nested-arithmetic
binary op `(1 + 2) * 3` round-trips byte-identical vs real pg_dump 18.3 as
`add_calcdef(a integer DEFAULT ((1 + 2) * 3))`. This is the FOURTH/last deparse context
`executor.defaultExprToSQL` feeds (after slice 298 index-predicate / 299 index-column /
300 partition-key). Verified PG's print_function_arguments (ruleutils.c:3428) uses
`deparse_expression(expr,NIL,false,false)` with NO extra `(%s)` wrap (unlike partkeydef
slice 300) — full parens come from get_oper_expr non-pretty mode, which goopg already
mirrors via defaultExprToSQL's slice-298 BinaryOp arm + ArgDefaults storage. No production
change; fixture only. Files: testport/pgdump_connsetup_test.go (add_calcdef fixture +
4-paren assertion + one-paren-short negative guard), docs/design/0110-0001 (slice 301
section). Oracle-verified vs live PG 18.3.

All four defaultExprToSQL binary-op deparse contexts are now byte-verified.

Next loop (slice 302+): no remaining defaultExprToSQL context. Candidate surfaces —
multi-column / NULL-typed DEFAULT on the partition-leaf ALTER path, OR the keyword-vs-literal
MINVALUE partition-bound ambiguity (slice 169 deferral). Pick from fix_plan.

NOTE: a concurrent session commits on this branch (committed fe03ce91 in loop #68). Do NOT
git add -A — stage only your own files. Do NOT Edit .ralph/fix_plan.md (driver churns it).
