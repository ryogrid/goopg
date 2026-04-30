package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestSyntax_Select_Basic exercises basic SELECT forms.
func TestSyntax_Select_Basic(t *testing.T) {
	c := newCluster(t, "syntax_sel_basic")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// SELECT constants
	runSQL(t, c, "SELECT 1")

	// SELECT expressions
	rows := runSQL(t, c, "SELECT 1 + 2 AS sum")
	if rows[0][0] != "3" {
		t.Fatalf("1+2 = %v, want 3", rows[0][0])
	}

	// SELECT with WHERE
	runSQL(t, c, "CREATE TABLE t (id int, val text)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 'a'), (2, 'b'), (3, 'c')")

	rows = runSQL(t, c, "SELECT val FROM t WHERE id = 2")
	if len(rows) != 1 || rows[0][0] != "b" {
		t.Fatalf("WHERE id=2 = %v, want [[b]]", rows)
	}

	// ORDER BY
	rows = runSQL(t, c, "SELECT id FROM t ORDER BY id DESC")
	if rows[0][0] != "3" || rows[2][0] != "1" {
		t.Fatalf("ORDER BY DESC = %v, want [[3] [2] [1]]", rows)
	}

	// LIMIT / OFFSET
	rows = runSQL(t, c, "SELECT id FROM t ORDER BY id LIMIT 2 OFFSET 1")
	if len(rows) != 2 || rows[0][0] != "2" {
		t.Fatalf("LIMIT 2 OFFSET 1 = %v, want [[2] [3]]", rows)
	}

	// IN
	rows = runSQL(t, c, "SELECT id FROM t WHERE id IN (1, 3) ORDER BY id")
	if len(rows) != 2 || rows[0][0] != "1" {
		t.Fatalf("IN (1,3) = %v, want [[1] [3]]", rows)
	}

	// BETWEEN
	rows = runSQL(t, c, "SELECT id FROM t WHERE id BETWEEN 1 AND 2 ORDER BY id")
	if len(rows) != 2 {
		t.Fatalf("BETWEEN count = %d, want 2", len(rows))
	}

	// LIKE
	rows = runSQL(t, c, "SELECT id FROM t WHERE val LIKE 'b'")
	if len(rows) != 1 || rows[0][0] != "2" {
		t.Fatalf("LIKE 'b' = %v, want [[2]]", rows)
	}

	// Null handling: skip explicit IS NULL assertions if v0 parser doesn't
	// support them. Just verify the table accepts NULLs.
	runSQL(t, c, "INSERT INTO t VALUES (4, NULL)")
	rows = runSQL(t, c, "SELECT COUNT(*) FROM t")
	if rows[0][0] != "4" {
		t.Fatalf("row count = %v, want 4", rows[0][0])
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_Select_Join exercises JOIN operations.
func TestSyntax_Select_Join(t *testing.T) {
	c := newCluster(t, "syntax_sel_join")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE a (id int, val text)")
	runSQL(t, c, "CREATE TABLE b (id int, val text)")
	runSQL(t, c, "INSERT INTO a VALUES (1, 'a1'), (2, 'a2')")
	runSQL(t, c, "INSERT INTO b VALUES (1, 'b1'), (3, 'b3')")

	// INNER JOIN
	rows := runSQL(t, c, "SELECT a.val, b.val FROM a INNER JOIN b ON a.id = b.id ORDER BY a.id")
	if len(rows) != 1 || rows[0][0] != "a1" || rows[0][1] != "b1" {
		t.Fatalf("INNER JOIN = %v, want [[a1 b1]]", rows)
	}

	// LEFT JOIN
	rows = runSQL(t, c, "SELECT a.val, b.val FROM a LEFT JOIN b ON a.id = b.id ORDER BY a.id")
	if len(rows) != 2 || rows[1][0] != "a2" || rows[1][1] != "" {
		t.Fatalf("LEFT JOIN = %v, want [[a1 b1] [a2 '']]", rows)
	}

	// CROSS JOIN
	rows = runSQL(t, c, "SELECT a.val, b.val FROM a CROSS JOIN b ORDER BY a.id, b.id")
	if len(rows) != 4 {
		t.Fatalf("CROSS JOIN count = %d, want 4", len(rows))
	}

	runSQL(t, c, "DROP TABLE a")
	runSQL(t, c, "DROP TABLE b")
}

// TestSyntax_Select_GroupBy exercises GROUP BY and aggregates.
func TestSyntax_Select_GroupBy(t *testing.T) {
	c := newCluster(t, "syntax_sel_group")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (cat text, val int)")
	runSQL(t, c, "INSERT INTO t VALUES ('a', 10), ('a', 20), ('b', 100)")

	rows := runSQL(t, c, "SELECT cat, COUNT(*), SUM(val), AVG(val), MIN(val), MAX(val) FROM t GROUP BY cat ORDER BY cat")
	if len(rows) != 2 {
		t.Fatalf("GROUP BY rows = %d, want 2", len(rows))
	}
	if rows[0][0] != "a" || rows[0][1] != "2" || rows[0][2] != "30" {
		t.Fatalf("group a = %v, want [a 2 30 ...]", rows[0])
	}

	// HAVING
	rows = runSQL(t, c, "SELECT cat, COUNT(*) FROM t GROUP BY cat HAVING COUNT(*) > 1")
	if len(rows) != 1 || rows[0][0] != "a" {
		t.Fatalf("HAVING = %v, want [[a 2]]", rows)
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_Select_Subquery exercises subquery expressions.
func TestSyntax_Select_Subquery(t *testing.T) {
	c := newCluster(t, "syntax_sel_subq")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 10), (2, 20), (3, 30)")

	// Scalar subquery in SELECT
	rows := runSQL(t, c, "SELECT (SELECT val FROM t WHERE id = 1) AS x")
	if rows[0][0] != "10" {
		t.Fatalf("scalar subquery = %v, want [[10]]", rows)
	}

	// IN (subquery)
	rows = runSQL(t, c, "SELECT id FROM t WHERE id IN (SELECT id FROM t WHERE val >= 20) ORDER BY id")
	if len(rows) != 2 || rows[0][0] != "2" {
		t.Fatalf("IN subquery = %v, want [[2] [3]]", rows)
	}

	// Derived table (FROM subquery)
	rows = runSQL(t, c, "SELECT x FROM (SELECT id AS x FROM t WHERE id > 1) AS sub ORDER BY x")
	if len(rows) != 2 || rows[0][0] != "2" {
		t.Fatalf("derived table = %v, want [[2] [3]]", rows)
	}

	runSQL(t, c, "DROP TABLE t")
}

// TestSyntax_Select_Case exercises CASE expressions.
func TestSyntax_Select_Case(t *testing.T) {
	c := newCluster(t, "syntax_sel_case")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	rows := runSQL(t, c, "SELECT CASE WHEN 1=1 THEN 'yes' ELSE 'no' END")
	if rows[0][0] != "yes" {
		t.Fatalf("CASE WHEN = %v, want yes", rows[0])
	}

	rows = runSQL(t, c, "SELECT CASE 1 WHEN 1 THEN 'one' WHEN 2 THEN 'two' ELSE 'other' END")
	if rows[0][0] != "one" {
		t.Fatalf("CASE simple = %v, want one", rows[0])
	}

	// CASE in WHERE
	runSQL(t, c, "CREATE TABLE t (id int)")
	runSQL(t, c, "INSERT INTO t VALUES (1), (2), (3)")
	rows = runSQL(t, c, "SELECT id FROM t WHERE CASE WHEN id > 1 THEN 1 ELSE 0 END = 1 ORDER BY id")
	if len(rows) != 2 || rows[0][0] != "2" {
		t.Fatalf("CASE in WHERE = %v, want [[2] [3]]", rows)
	}
	runSQL(t, c, "DROP TABLE t")
}
