package testport

import (
	"testing"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestSyntax_SciNotation(t *testing.T) {
	c := newCluster(t, "sci_notation")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE t (pop real)")
	runSQL(t, c, "INSERT INTO t VALUES (4664.E+5)")
	rows := runSQL(t, c, "SELECT pop FROM t")
	t.Logf("4664.E+5 = %v", rows)

	runSQL(t, c, "CREATE TABLE t2 (name text, pop real, PRIMARY KEY (name))")
	runSQL(t, c, "INSERT INTO t2 VALUES ('Sac', 3.694E+5)")
	runSQL(t, c, "INSERT INTO t2 VALUES ('Sac', 4664.E+5) ON CONFLICT (name) DO UPDATE SET pop = excluded.pop")
	rows2 := runSQL(t, c, "SELECT pop FROM t2")
	t.Logf("after ON CONFLICT DO UPDATE: pop = %v (expected ~466400000 = 4.664e+08)", rows2)
}
