package sqlparser

import "testing"

// TestMergeStatement pins MERGE (gram.y MergeStmt) — 106 unrouted regress
// fragments, plus the four `WITH ... (MERGE ...)` and `PREPARE ... AS MERGE`
// fragments that were the last yacc-side rejects inside already-routed
// classes.
//
// Both the target and the source are base_table_refs, the nonterminal
// UPDATE/DELETE already use, so a parenthesised sub-select source with an
// alias needs no rule of its own. Two details come from legacy rather than
// gram.y: INTO is optional (parser.go parseMerge accepts a bare `MERGE t`),
// and `WHEN NOT MATCHED BY <anything-else>` degrades to a plain NOT MATCHED
// instead of erroring, because legacy only probes for SOURCE and TARGET.
func TestMergeStatement(t *testing.T) {
	for _, q := range []string{

		"MERGE INTO t USING s ON t.a = s.a WHEN MATCHED THEN UPDATE SET b = 1",
		"MERGE t USING s ON true WHEN MATCHED THEN DELETE",
		"MERGE INTO t AS x USING s AS y ON x.a = y.a WHEN NOT MATCHED THEN INSERT (a, b) VALUES (1, 2)",
		"MERGE INTO t USING s ON true WHEN NOT MATCHED THEN INSERT DEFAULT VALUES",
		"MERGE INTO t USING s ON true WHEN NOT MATCHED BY SOURCE THEN DELETE",
		"MERGE INTO t USING s ON true WHEN NOT MATCHED BY TARGET THEN DO NOTHING",
		"MERGE INTO t USING s ON true WHEN MATCHED AND s.x > 1 THEN UPDATE SET b = s.b, c = s.c",
		"MERGE INTO t USING (SELECT 1 AS a) s ON t.a = s.a WHEN MATCHED THEN DELETE",
		"MERGE INTO t USING s ON true WHEN MATCHED THEN DELETE WHEN NOT MATCHED THEN INSERT VALUES (1)",
		"MERGE INTO t USING s ON true WHEN MATCHED THEN DELETE RETURNING t.a",
		"WITH m AS (MERGE INTO t USING s ON true WHEN MATCHED THEN DELETE RETURNING t.a) SELECT * FROM m",
		"EXPLAIN (COSTS OFF) MERGE INTO t USING s ON true WHEN MATCHED THEN DELETE",
		} {
		assertParity(t, q)
	}
}
