package executor

import (
	"strings"
	"testing"
)

// TestExplainNotNullQualDroppedUnderInnerJoin pins M0146-0038 against PG
// 18.3: add_base_clause_to_rel drops a restriction `restriction_is_always_true`
// proves — IS NOT NULL on a column declared NOT NULL that no outer join nulls —
// whether or not other relations share the query level (TPC-DS Q51's
// `ws_item_sk IS NOT NULL` beside a join to date_dim). Under a LEFT JOIN the
// nullable side's column keeps its test: goopg has no varnullingrels, so the
// reduction stays off for any scope with an outer join, and PG's own
// PG 18.3 plan keeps `Filter: (nn.a IS NULL)` on the Merge Left Join.
func TestExplainNotNullQualDroppedUnderInnerJoin(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, ddl := range []string{
		"CREATE TABLE nn (a int NOT NULL, b int)",
		"CREATE TABLE d (k int, y int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	inner := runExplain(t, ctx, `SELECT * FROM nn, d WHERE nn.b = d.k AND nn.a IS NOT NULL AND nn.b IS NOT NULL`)
	joined := strings.Join(inner, "\n")
	if countLinesContaining(inner, "a IS NOT NULL") != 0 {
		t.Errorf("IS NOT NULL on a NOT NULL column must be dropped:\n%s", joined)
	}
	if countLinesContaining(inner, "b IS NOT NULL") != 1 {
		t.Errorf("IS NOT NULL on a nullable column must stay:\n%s", joined)
	}
	// The nullable side of a LEFT JOIN: the WHERE test runs above the
	// join, where the null-extended rows it exists to find do carry NULL.
	outer := runExplain(t, ctx, `SELECT * FROM d LEFT JOIN nn ON nn.b = d.k WHERE nn.a IS NULL`)
	if countLinesContaining(outer, "a IS NULL") != 1 {
		t.Errorf("a LEFT JOIN nullable side's column must keep its test:\n%s", strings.Join(outer, "\n"))
	}
}
