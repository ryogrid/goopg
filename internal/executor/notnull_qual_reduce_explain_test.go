package executor

import (
	"strings"
	"testing"
)

// TestExplainOneTimeFilterLiteralNotParenthesised is the M0134-0010c round 2
// regression guard for DEFECT 1 (dangling child) and DEFECT 2 (spurious
// parens): a NOT NULL-driven always-false reduction
// (`internal/optimizer/notnull_qual_reduce.go`) renders `Result` /
// `One-Time Filter: false` with NO `->` child line and NO parens around the
// bare literal — byte-identical to
// `postgres/src/test/regress/expected/predicate.out` lines 34-40.
func TestExplainOneTimeFilterLiteralNotParenthesised(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE pred_tab (a int NOT NULL, b int, c int NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	rows := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM pred_tab t WHERE t.a IS NULL")
	got := strings.Join(rows, "\n")
	want := "Result\n  One-Time Filter: false"
	if got != want {
		t.Fatalf("EXPLAIN output = %q, want %q", got, want)
	}
}

// TestExplainOneTimeFilterCompoundStillParenthesised is DEFECT 2's blast
// radius guard: the pre-existing const-arg min/max rewrite's One-Time
// Filter (`100 IS NOT NULL`, a compound NullTest expression, NOT a bare
// literal) must still render WITH its parens — the literal-only exception
// in `isLiteralOneTimeFilterConst` must not widen to swallow this case.
// Matches `postgres/src/test/regress/expected/aggregates.out:1199`.
func TestExplainOneTimeFilterCompoundStillParenthesised(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (x int4, y int4)"); err != nil {
		t.Fatal(err)
	}
	rows := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT max(100) FROM t")
	got := strings.Join(rows, "\n")
	if !strings.Contains(got, "One-Time Filter: (100 IS NOT NULL)") {
		t.Fatalf("EXPLAIN output missing parenthesised compound One-Time Filter; got:\n%s", got)
	}
}
