package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestSyntax_Window_RowNumber exercises ROW_NUMBER() window function.
func TestSyntax_Window_RowNumber(t *testing.T) {
	c := newCluster(t, "syntax_win_rn")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, cat text)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 'a'), (2, 'a'), (3, 'b')")

	rows := runSQL(t, c, "SELECT id, ROW_NUMBER() OVER (ORDER BY id) AS rn FROM t ORDER BY id")
	if len(rows) != 3 {
		t.Fatalf("ROW_NUMBER rows = %d, want 3", len(rows))
	}
	if rows[0][0] != "1" || rows[0][1] != "1" {
		t.Fatalf("ROW_NUMBER first = %v, want [1 1]", rows[0])
	}

	rows = runSQL(t, c, "SELECT id, ROW_NUMBER() OVER (PARTITION BY cat ORDER BY id) AS rn FROM t ORDER BY id")
	if rows[0][1] != "1" || rows[1][1] != "2" || rows[2][1] != "1" {
		t.Fatalf("PARTITION BY rns = %v, want [[1 1] [2 2] [3 1]]", rows)
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_Window_Rank exercises RANK() window function.
func TestSyntax_Window_Rank(t *testing.T) {
	c := newCluster(t, "syntax_win_rank")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 10), (2, 20), (3, 20), (4, 30)")

	rows := runSQL(t, c, "SELECT id, RANK() OVER (ORDER BY val) AS r FROM t ORDER BY id")
	if len(rows) != 4 {
		t.Fatalf("RANK rows = %d, want 4", len(rows))
	}
	if rows[0][1] != "1" || rows[1][1] != "2" || rows[2][1] != "2" || rows[3][1] != "4" {
		t.Fatalf("RANK values = %v, want [[1 1] [2 2] [3 2] [4 4]]", rows)
	}

	runSQL(t, c, "DROP TABLE t")
}
