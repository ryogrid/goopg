package executor

import (
	"strings"
	"testing"
)

// TestExplainDeclareCursorExplainsInnerQuery pins the M0118-0002
// enabler: `EXPLAIN ... DECLARE c CURSOR FOR <query>` must explain
// the cursor's underlying query, mirroring PG's ExplainOneUtility →
// ExplainOneQuery dispatch for a DeclareCursorStmt. The cursor is
// never created; only its query is planned and rendered.
//
// Before this enabler the planner rejected a DeclareCursorStmt inner
// with `0A000 unsupported statement type *parser.DeclareCursorStmt`,
// which was the first divergence for the index-only-bitmapscan spec's
// `s1_explain` step (EXPLAIN (COSTS OFF) DECLARE foo ... CURSOR FOR ...).
func TestExplainDeclareCursorExplainsInnerQuery(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, data int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) DECLARE foo NO SCROLL CURSOR FOR SELECT * FROM t WHERE data = 34")
	joined := strings.Join(lines, "\n")
	// The rendered plan must be the cursor query's plan, i.e. a scan of
	// `t` with the WHERE predicate — not a cursor/utility node.
	if !strings.Contains(joined, "Scan on t") {
		t.Errorf("EXPLAIN DECLARE CURSOR must render the inner query plan (Scan on t); got:\n%s", joined)
	}
	if !strings.Contains(joined, "(data = 34)") {
		t.Errorf("EXPLAIN DECLARE CURSOR must carry the inner query's predicate; got:\n%s", joined)
	}
	// No cursor/declare artefact should leak into the plan text.
	if strings.Contains(strings.ToLower(joined), "cursor") || strings.Contains(strings.ToLower(joined), "declare") {
		t.Errorf("EXPLAIN DECLARE CURSOR must not surface a cursor/declare node; got:\n%s", joined)
	}
}
