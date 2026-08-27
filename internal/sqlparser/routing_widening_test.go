package sqlparser

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestRoutingModifiersAndRecursive pins three dispatcher decisions that had
// kept whole classes on legacy although the grammar already parsed them:
// `CREATE TEMP|TEMPORARY|UNLOGGED TABLE` (368 of 387 regress fragments
// parsed identically before the flip; opt_create_modifier is on every CREATE
// TABLE alternative), `CREATE UNIQUE INDEX` (opt_unique), and every `WITH
// RECURSIVE` / `AS [NOT] MATERIALIZED` CTE — withFollowerRouted had been
// returning routedStmts["recursive"], i.e. false, for all of them.
func TestRoutingModifiersAndRecursive(t *testing.T) {
	routed := func(q string) bool {
		toks, err := parser.Lex(q)
		if err != nil {
			t.Fatalf("lex %q: %v", q, err)
		}
		return fragmentRouted(toks)
	}
	for _, q := range []string{
		"CREATE TEMP TABLE t (a int)",
		"CREATE TEMPORARY TABLE t (a int)",
		"CREATE UNLOGGED TABLE t (a int)",
		"CREATE TEMP TABLE t AS SELECT 1",
		"CREATE UNIQUE INDEX i ON t (a)",
		"WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM t) SELECT n FROM t",
		"WITH c AS MATERIALIZED (SELECT 1) SELECT * FROM c",
		"WITH c AS NOT MATERIALIZED (SELECT 1) INSERT INTO x SELECT * FROM c",
	} {
		if !routed(q) {
			t.Errorf("not routed: %q", q)
		}
		assertParity(t, q)
	}
	// The grammar has no CREATE TEMP VIEW / SEQUENCE: a modifier on any other
	// kind must still fall to legacy rather than surface a 42601.
	for _, q := range []string{
		"CREATE TEMP VIEW v AS SELECT 1",
		"CREATE TEMP SEQUENCE s",
	} {
		if routed(q) {
			t.Errorf("unexpectedly routed: %q", q)
		}
	}
	// MergeStmt carries no WITH clause (neither in the AST nor in gram.y's
	// MergeStmt), and legacy says so outright: "WITH clause must be followed
	// by ...". Routing MERGE must not quietly make the combination legal.
	assertBothReject(t, "WITH c AS (SELECT 1) MERGE INTO t USING c ON true WHEN MATCHED THEN DELETE")
}

// TestRoutingWideningGaps pins the four grammar gaps the wider routing
// exposed: NULLS [NOT] DISTINCT on CREATE INDEX, a three-argument typmod,
// the aliases-CTAS taking the composable tail (ON COMMIT), and
// `<field> TO SECOND(p)` interval qualifiers.
func TestRoutingWideningGaps(t *testing.T) {
	for _, q := range []string{
		"create unique index t2_z_uidx on t2(z) nulls not distinct",
		"create unique index i on t(z) nulls distinct",
		"CREATE TEMP TABLE mytab (foo widget(42,13,7))",
		"CREATE TEMP TABLE temptest(col) ON COMMIT DELETE ROWS AS SELECT 1",
		"SELECT interval '12:34.5678' minute to second(2)",
		"SELECT interval '1 2:03:04.5' day to second(1)",
		"SELECT interval '1' hour to minute",
	} {
		assertParity(t, q)
	}
}
