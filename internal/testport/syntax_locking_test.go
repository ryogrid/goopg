package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestSyntax_Locking_ForUpdate(t *testing.T) {
	c := newCluster(t, "syntax_lock_fu")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 10), (2, 20)")

	rows := runSQL(t, c, "SELECT id FROM t ORDER BY id FOR UPDATE")
	if len(rows) != 2 || rows[0][0] != "1" {
		t.Fatalf("FOR UPDATE = %v, want [[1] [2]]", rows)
	}
	runSQL(t, c, "DROP TABLE t")
}

func TestSyntax_Locking_ForShare(t *testing.T) {
	c := newCluster(t, "syntax_lock_fs")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int)")
	runSQL(t, c, "INSERT INTO t VALUES (1)")

	rows := runSQL(t, c, "SELECT id FROM t FOR SHARE")
	if len(rows) != 1 || rows[0][0] != "1" {
		t.Fatalf("FOR SHARE = %v, want [[1]]", rows)
	}
	runSQL(t, c, "DROP TABLE t")
}

func TestSyntax_Locking_Nowait(t *testing.T) {
	c := newCluster(t, "syntax_lock_nw")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (id int)")
	runSQL(t, c, "INSERT INTO t VALUES (1)")

	rows := runSQL(t, c, "SELECT id FROM t FOR UPDATE NOWAIT")
	if len(rows) != 1 || rows[0][0] != "1" {
		t.Fatalf("FOR UPDATE NOWAIT = %v, want [[1]]", rows)
	}
	runSQL(t, c, "DROP TABLE t")
}
