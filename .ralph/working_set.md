Task: M0123-S4 — byte-diff oracle gate (ev_action / view path). COMPLETE this
loop; committing + pushing.

Landed: new internal/testport/oracle_pgnodes_ev_action_test.go
(TestOraclePgnodesEvActionBytesMatchPG) — the query-tree analogue of the adbin
oracle. Seeds one shared bench_log(client int, src text) on a LIVE PG18, then for
each of 13 canonical view cases CREATE VIEWs the SELECT, reads back
pg_rewrite.ev_action::text, normalizes `:location N`→-1, and asserts
pgnodes.ResolveViewQuery→OutRuleAction is byte-identical (ErrUnsupported on a
PG-canonical case = hard failure). The piece the adbin path lacked: a LIVE
RelationResolver (liveRelationResolver) reading the base relation's real
relid/relkind (pg_class) + full column list (attname/attnum/atttypid/atttypmod/
attcollation via string_agg+QueryScalar, oids/relkind ::text-cast to dodge
`text||"char"` ambiguity) from the SAME cluster → goopg Var/RTE OIDs match PG's
ev_action, no baked 16384. Cases mirror pgnodes view goldens v/v2 + v3–v13.

Key symbols: TestOraclePgnodesEvActionBytesMatchPG, liveRelationResolver
(pgnodes.RelationResolver impl), parseSelectForOracle, normalizeOracleLocations
(shared w/ adbin oracle); drives pgnodes.ResolveViewQuery/OutRuleAction +
parser.Parse + pgcluster.New/Start/Exec/QueryScalar.

Gates (GREEN): all 13 subtests PASS vs live PG18.3 (1.25s); -short SKIP verified;
go build ./..., go vet ./internal/testport/, gofmt clean; ralph-state-guard
(self-repaired to consistent). pgbench smoke runs in pre-commit hook on commit.

Next step: pick next M0123-S4 REMAINING (fix_plan ~L1013). Recommend float4-common
(no float8) CASE mix (int/numeric→float4 cast arms + outer float8(float4) column
cast in resolver_expr.go selectCaseCommonType/coerceCaseResult), OR operator-driven
implicit coercion in view quals (resolver_query.go queryScope.resolveExpr).

Gates run: ev_action oracle (13 green vs PG18), build/vet (green), gofmt (clean),
ralph-state-guard (consistent after repair), pgbench smoke (pre-commit).
In-flight: none.
