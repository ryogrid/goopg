package executor

// M0146-0005w: SubqueryScan executor transparency + EXPLAIN label.
//
// The optimizer's SubqueryScan node is a labelling wrapper — PG's
// SubqueryScan plan node forwards tuples unchanged, so goopg builds the
// child op directly and renders `Subquery Scan on <alias>` with the
// subplan beneath it (internal/optimizer/plan.go). These tests pin the
// two halves of that contract: the label appears on non-simple derived
// tables, and execution returns the subquery's rows unchanged.

import (
	"strings"
	"testing"
)

func TestExplainSubqueryScanLabelOnSetOpArm(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE sqs1 (a int, b int)",
		"CREATE TABLE sqs2 (a int, b int)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	// u publishes two columns and the query reads only the first — a
	// subset consumption PG's setrefs judges non-trivial, so the label
	// survives (a full in-order read would strip it; see the optimizer's
	// triviality pins).
	rows := runExplainRows(t, ctx,
		"EXPLAIN (COSTS OFF) SELECT u.a FROM sqs1, "+
			"(SELECT a, b FROM sqs1 INTERSECT SELECT a, b FROM sqs2) u WHERE sqs1.a = u.a")
	joined := strings.Join(rows, "\n")
	labelIdx := strings.Index(joined, "Subquery Scan on u")
	if labelIdx < 0 {
		t.Fatalf("EXPLAIN lacks `Subquery Scan on u`:\n%s", joined)
	}
	// PG nests the arm's plan beneath the label; the inner scans must
	// appear UNDER it (deeper indent), not as siblings of the join's
	// other leaf — that nesting is what makes the plan-census leaf set
	// pair with PG's.
	var sawInnerBelow bool
	for _, ln := range strings.Split(joined[labelIdx:], "\n")[1:] {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(ln, "  ") && !strings.HasPrefix(ln, "\t") {
			break
		}
		if strings.HasPrefix(trimmed, "->") || strings.Contains(trimmed, "Scan on sqs") || strings.Contains(trimmed, "Intersect") || strings.Contains(trimmed, "SetOp") || strings.Contains(trimmed, "HashSetOp") {
			sawInnerBelow = true
		}
	}
	if !sawInnerBelow {
		t.Errorf("no inner subplan lines beneath the `Subquery Scan on u` label:\n%s", joined)
	}
}

func TestSubqueryScanExecutesTransparently(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE sqv1 (g int, x int)",
		"INSERT INTO sqv1 VALUES (1, 10)",
		"INSERT INTO sqv1 VALUES (1, 20)",
		"INSERT INTO sqv1 VALUES (2, 30)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	// A grouped derived table is non-simple (is_simple_subquery
	// declines), and reading the columns out of order makes the
	// wrapper non-trivial, so PG keeps the Subquery Scan; the wrapper
	// must not change the rows the subquery produces. The counter
	// advance makes the final INSERT's rows visible to the query
	// (M0129-S8.3).
	advanceStmtCounter(ctx)
	rows, err := runQueryFast(t, ctx,
		"SELECT u.c, u.g FROM (SELECT g, count(x) c FROM sqv1 GROUP BY g) u ORDER BY u.g")
	if err != nil {
		t.Fatalf("query through SubqueryScan: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("grouped derived table returned %d rows, want 2", len(rows))
	}
	got := [2]int64{rows[0][0].Int, rows[1][0].Int}
	if got[0] != 2 || got[1] != 1 {
		t.Errorf("aggregate values = %v, want [2 1] — the wrapper changed the subquery's output", got)
	}
}
