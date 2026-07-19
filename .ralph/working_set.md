Task: M0123-S4 — byte-diff oracle gate (adbin). COMPLETE this loop; committing + pushing.

Landed: new internal/testport/oracle_pgnodes_adbin_test.go
(TestOraclePgnodesAdbinBytesMatchPG) — the S4 "byte-diff oracle" deliverable for
the column-DEFAULT (adbin) path. For each of 25 canonical (col-type, DEFAULT-expr)
cases it CREATE TABLEs the default on a LIVE PG18 (pgcluster.New+Start), reads
back pg_attrdef.adbin::text, normalizes `:location N`→`-1`, and asserts
pgnodes.ResolveForColumn→Out is byte-identical. SQL-text fallback on a
PG-canonical case = hard failure. Cases span every S4 family (int/text/numeric
Consts, int4→numeric cast, upper(), timestamptz lit, BoolExpr/NullTest/OpExpr,
BooleanTest, DistinctExpr, CaseExpr searched+simple + int→numeric/int4→int8
coercion), all drawn from existing pgnodes goldens → the value is a LIVE oracle
(catches hand-capture drift + auto-covers future types).

Key symbols: TestOraclePgnodesAdbinBytesMatchPG, normalizeOracleLocations,
adbinOracleCase; drives pgnodes.ResolveForColumn/Out + parser.ParseExpr +
pgcluster.New/Start/Exec/QueryScalar.

Gates (GREEN): all 25 subtests PASS vs live PG18.3 (1.3s); -short SKIP verified;
go build ./..., go vet ./internal/testport/, gofmt clean; ralph-state-guard
(self-repaired to consistent). pgbench smoke runs in pre-commit hook on commit.

Next step: pick next M0123-S4 REMAINING (fix_plan ~L1013). Recommend the VIEW
ev_action (pg_rewrite) byte-diff oracle: parameterize the harness on a
resolver-driver func, add a RelationResolver that runs
`SELECT attname,atttypid,attnum FROM pg_attribute WHERE attrelid='<tbl>'::regclass
AND attnum>0` on the pgcluster handle, then diff pgnodes.ResolveViewQuery→Out vs
normalized pg_rewrite.ev_action. OR: float4-common CASE mix (int/numeric→float4
arms + outer column cast).

Gates run: oracle (25 green vs PG18), build/vet (green), gofmt (clean),
ralph-state-guard (consistent after repair), pgbench smoke (pre-commit).
In-flight: none.
