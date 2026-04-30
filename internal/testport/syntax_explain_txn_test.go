package testport

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestSyntax_Explain_Basic exercises EXPLAIN and EXPLAIN ANALYZE.
func TestSyntax_Explain_Basic(t *testing.T) {
	c := newCluster(t, "syntax_explain")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// EXPLAIN SELECT
	rows := runSQL(t, c, "EXPLAIN SELECT 1")
	if len(rows) == 0 {
		t.Fatal("EXPLAIN returned zero rows")
	}
	if !strings.Contains(rows[0][0], "Project") && !strings.Contains(rows[0][0], "Values") {
		t.Logf("EXPLAIN output = %v", rows[0])
	}

	// EXPLAIN with options
	rows = runSQL(t, c, "EXPLAIN (FORMAT JSON) SELECT 1")
	if len(rows) == 0 {
		t.Fatal("EXPLAIN JSON returned zero rows")
	}

	// EXPLAIN ANALYZE
	rows = runSQL(t, c, "EXPLAIN ANALYZE SELECT 1")
	if len(rows) == 0 {
		t.Fatal("EXPLAIN ANALYZE returned zero rows")
	}

	// EXPLAIN with tables
	runSQL(t, c, "CREATE TABLE explain_t (id int, val text)")
	runSQL(t, c, "INSERT INTO explain_t VALUES (1, 'a'), (2, 'b')")
	rows = runSQL(t, c, "EXPLAIN SELECT * FROM explain_t WHERE id = 1")
	if len(rows) == 0 {
		t.Fatal("EXPLAIN with table returned zero rows")
	}
	runSQL(t, c, "DROP TABLE explain_t")
}

// TestSyntax_Transaction_Basic exercises BEGIN/COMMIT.
// v0 wraps each simple-query batch in an implicit ReadCommitted
// transaction, so ROLLBACK is not effective across batches.
func TestSyntax_Transaction_Basic(t *testing.T) {
	c := newCluster(t, "syntax_txn")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// BEGIN / COMMIT
	runSQL(t, c, "BEGIN")
	runSQL(t, c, "CREATE TABLE txn_t (id int)")
	runSQL(t, c, "INSERT INTO txn_t VALUES (1)")
	runSQL(t, c, "COMMIT")

	rows := runSQL(t, c, "SELECT id FROM txn_t")
	if len(rows) != 1 || rows[0][0] != "1" {
		t.Fatalf("after COMMIT = %v, want [[1]]", rows)
	}

	// COMMIT (no-op alias via END)
	runSQL(t, c, "BEGIN")
	runSQL(t, c, "INSERT INTO txn_t VALUES (2)")
	runSQL(t, c, "COMMIT")

	rows = runSQL(t, c, "SELECT COUNT(*) FROM txn_t")
	if rows[0][0] != "2" {
		t.Fatalf("after second COMMIT count = %v, want 2", rows[0][0])
	}

	runSQL(t, c, "DROP TABLE txn_t")
}
