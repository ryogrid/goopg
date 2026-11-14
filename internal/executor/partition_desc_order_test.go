package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestPartitionMembersInBoundOrder pins expand_partitioned_rtentry's member
// order (M0148-0001c3a9 slice 1): partitions in PartitionDesc (bound)
// order, sub-partitioned children expanded depth-first in place, DEFAULT
// last — not creation order. Plans are PG 18.3's.
func TestPartitionMembersInBoundOrder(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	for _, s := range []string{
		"create table prt1 (a int, b int) partition by range (a)",
		"create table prt1_p1 partition of prt1 for values from (0) to (250)",
		"create table prt1_p3 partition of prt1 for values from (500) to (600)",
		"create table prt1_d partition of prt1 default",
		"create table prt1_p2 partition of prt1 for values from (250) to (500)",
		"create table pm (a int, b int) partition by range (a)",
		"create table pm_2 partition of pm for values from (10) to (20) partition by list (b)",
		"create table pm_2b partition of pm_2 for values in (5)",
		"create table pm_2a partition of pm_2 for values in (1)",
		"create table pm_1 partition of pm for values from (0) to (10)",
	} {
		runSQL(t, ctx, s)
	}
	ps := optimizer.DefaultPlannerSettings()
	for _, c := range []struct{ q, plan string }{
		{"SELECT * FROM prt1",
			"Append\n  ->  Seq Scan on prt1_p1 prt1_1\n  ->  Seq Scan on prt1_p2 prt1_2\n  ->  Seq Scan on prt1_p3 prt1_3\n  ->  Seq Scan on prt1_d prt1_4"},
		{"SELECT * FROM pm",
			"Append\n  ->  Seq Scan on pm_1\n  ->  Seq Scan on pm_2a pm_2\n  ->  Seq Scan on pm_2b pm_3"},
	} {
		if got := strings.Join(renderRows(runSQLWith(t, ctx, "EXPLAIN (COSTS OFF) "+c.q, ps)), "\n"); got != c.plan {
			t.Errorf("%s:\ngot:\n%s\nwant (PG 18.3):\n%s", c.q, got, c.plan)
		}
	}
}
