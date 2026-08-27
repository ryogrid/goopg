package parser

import "testing"

// TestSQLValueFunctions pins the SQL "no-parens" niladic family
// (grammar/pg_grammar.y sql_value_func_name) against the legacy parser's
// IsNoParenFuncName classification (internal/parser/select.go:4753-4762).
//
// These tokens are RESERVED keywords, so ColId/ColLabel exclude them: until
// func_expr_common_subexpr landed, no production consumed them at all and any
// routed statement containing one was an unconditional syntax error. That is
// what broke pgbench's TPC-B history INSERT after the P3.1 INSERT routing flip
// — see TestPgbenchTPCBScriptParity below, which is the regression anchor.
func TestSQLValueFunctions(t *testing.T) {
	for _, q := range []string{
		"SELECT CURRENT_TIMESTAMP",
		"SELECT CURRENT_DATE",
		"SELECT CURRENT_TIME",
		"SELECT LOCALTIMESTAMP",
		"SELECT LOCALTIME",
		"SELECT CURRENT_USER",
		"SELECT SESSION_USER",
		"SELECT USER",
		"SELECT CURRENT_ROLE",
		"SELECT CURRENT_CATALOG",
		"SELECT CURRENT_SCHEMA",
		// Alias, argument and predicate positions.
		"SELECT CURRENT_TIMESTAMP AS now",
		"SELECT current_user, session_user FROM t",
		"SELECT * FROM t WHERE created_at < CURRENT_TIMESTAMP",
		"SELECT date_trunc('day', CURRENT_TIMESTAMP)",
		"INSERT INTO t (a, b) VALUES (1, CURRENT_TIMESTAMP)",
		"UPDATE t SET seen = CURRENT_TIMESTAMP WHERE id = 1",
		// Call forms: legacy routes these through parseFuncCallTail, so the
		// empty-parens case must keep Args/Variadic nil and the precision case
		// must carry Args=[IntegerConst] / Variadic=[false].
		"SELECT current_timestamp()",
		"SELECT current_timestamp(3)",
		"SELECT current_time(2)",
		"SELECT localtimestamp(0)",
		"SELECT localtime(6)",
	} {
		l, n, err := diffParse(q)
		if err != nil {
			t.Errorf("%q -> %v", q, err)
			continue
		}
		if l != n {
			t.Errorf("DIFF %q\n L=%s\n N=%s", q, l, n)
		}
	}
}

// TestPgbenchTPCBScriptParity runs the exact statements the pre-commit
// pgbench smoke issues (scripts/ralph-precommit-test.sh Part 2: `pgbench -i`
// followed by the builtin tpcb-like / -N / -S workloads) through both
// parsers.
//
// WHY THIS EXISTS: the pgbench smoke is the CI-parity gate the git hook runs
// on every commit, but it needs a built binary and ~2-3 min. A parser wave
// that breaks it is only discovered at commit time — and if the hook is
// bypassed, not at all. This test reproduces the same SQL surface in ~1 ms so
// the failure surfaces during `go test ./internal/sqlparser/` instead.
//
// Placeholders are substituted with literals because pgbench sends the
// builtin scripts over the SIMPLE query protocol (no -M flag in the smoke).
func TestPgbenchTPCBScriptParity(t *testing.T) {
	for _, q := range []string{
		// pgbench -i (schema load)
		"DROP TABLE IF EXISTS pgbench_accounts, pgbench_branches, pgbench_history, pgbench_tellers",
		"CREATE TABLE pgbench_history (tid int, bid int, aid int, delta int, mtime timestamp, filler char(22))",
		"CREATE TABLE pgbench_tellers (tid int not null, bid int, tbalance int, filler char(84))",
		"CREATE TABLE pgbench_accounts (aid int not null, bid int, abalance int, filler char(84))",
		"CREATE TABLE pgbench_branches (bid int not null, bbalance int, filler char(88))",
		"ALTER TABLE pgbench_branches ADD PRIMARY KEY (bid)",
		"ALTER TABLE pgbench_tellers ADD PRIMARY KEY (tid)",
		"ALTER TABLE pgbench_accounts ADD PRIMARY KEY (aid)",
		// builtin tpcb-like (the default workload); command 9 is the INSERT
		// that failed with `syntax error at or near "current_timestamp"`.
		"BEGIN",
		"UPDATE pgbench_accounts SET abalance = abalance + 21517 WHERE aid = 2112",
		"SELECT abalance FROM pgbench_accounts WHERE aid = 2112",
		"UPDATE pgbench_tellers SET tbalance = tbalance + 21517 WHERE tid = 10",
		"UPDATE pgbench_branches SET bbalance = bbalance + 21517 WHERE bid = 1",
		"INSERT INTO pgbench_history (tid, bid, aid, delta, mtime) VALUES (10, 1, 21517, 2112, CURRENT_TIMESTAMP)",
		"END",
	} {
		l, n, err := diffParse(q)
		if err != nil {
			t.Errorf("%q -> %v", q, err)
			continue
		}
		if l != n {
			t.Errorf("DIFF %q\n L=%s\n N=%s", q, l, n)
		}
	}
}
