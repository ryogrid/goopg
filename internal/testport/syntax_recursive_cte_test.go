package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestSyntax_RecursiveCTE_BasicFixpoint tests the standard count-up pattern.
func TestSyntax_RecursiveCTE_BasicFixpoint(t *testing.T) {
	c := newCluster(t, "syntax_rcte_basic")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	rows := runSQL(t, c, `WITH RECURSIVE r AS (
		SELECT 1 AS n
		UNION ALL
		SELECT n + 1 FROM r WHERE n < 3
	) SELECT * FROM r ORDER BY n`)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %v", len(rows), rows)
	}
	if rows[0][0] != "1" || rows[1][0] != "2" || rows[2][0] != "3" {
		t.Fatalf("rows = %v, want [[1] [2] [3]]", rows)
	}
}

// TestSyntax_RecursiveCTE_SingleIteration tests a recursive CTE where
// the recursive member produces no rows (single iteration).
func TestSyntax_RecursiveCTE_SingleIteration(t *testing.T) {
	c := newCluster(t, "syntax_rcte_single")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	rows := runSQL(t, c, `WITH RECURSIVE r AS (
		SELECT 1 AS n
		UNION ALL
		SELECT n + 1 FROM r WHERE n < 0
	) SELECT * FROM r`)
	if len(rows) != 1 || rows[0][0] != "1" {
		t.Fatalf("single iteration: got %v, want [[1]]", rows)
	}
}

// TestSyntax_RecursiveCTE_MultiColumn tests a recursive CTE with
// multiple columns.
func TestSyntax_RecursiveCTE_MultiColumn(t *testing.T) {
	c := newCluster(t, "syntax_rcte_multi")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	rows := runSQL(t, c, `WITH RECURSIVE r AS (
		SELECT 1 AS i, 'a' AS t
		UNION ALL
		SELECT i + 1, 'b' FROM r WHERE i < 2
	) SELECT * FROM r ORDER BY i`)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %v", len(rows), rows)
	}
	if rows[0][0] != "1" || rows[0][1] != "a" {
		t.Fatalf("row 1 = %v, want [[1 a]]", rows[0])
	}
}

// TestSyntax_RecursiveCTE_LargeIteration tests many iterations.
func TestSyntax_RecursiveCTE_LargeIteration(t *testing.T) {
	c := newCluster(t, "syntax_rcte_large")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	rows := runSQL(t, c, `WITH RECURSIVE r AS (
		SELECT 1 AS n
		UNION ALL
		SELECT n + 1 FROM r WHERE n < 10
	) SELECT COUNT(*) FROM r`)
	if rows[0][0] != "10" {
		t.Fatalf("10 iterations: count = %v, want 10", rows[0][0])
	}
}
