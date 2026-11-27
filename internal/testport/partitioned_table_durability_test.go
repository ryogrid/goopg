package testport

// End-to-end coverage of partitioned-table restart persistence (M0149-0061k).
//
// Before this a restart lost every partitioned table: the startup loader
// kept only relkind r/m/v/S/f pg_class rows, the partition key was never
// written to disk, and a partition's parent link was not restored, so a
// partition came back as a standalone table. ATTACH PARTITION also never
// re-journaled the child (relispartition / relpartbound stayed unset).
// See docs/design/0100-0149/0149-0061-subscription-partition.md.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestPort_PartitionedTableSurvivesRestart(t *testing.T) {
	c, err := cluster.New("partitioned-durability", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	for _, stmt := range []string{
		"CREATE TABLE pt (a int PRIMARY KEY, c text DEFAULT 'dflt', b text) PARTITION BY RANGE (a)",
		"CREATE TABLE pt_1 (c text, b text, a int NOT NULL)",
		"ALTER TABLE pt ATTACH PARTITION pt_1 FOR VALUES FROM (0) TO (10)",
		"CREATE TABLE pt_2 PARTITION OF pt FOR VALUES FROM (10) TO (20) PARTITION BY LIST (b)",
		"CREATE TABLE pt_2_x PARTITION OF pt_2 FOR VALUES IN ('x', 'y')",
		"CREATE TABLE pt_2_d PARTITION OF pt_2 DEFAULT",
		"CREATE TABLE ph (k int) PARTITION BY HASH (k)",
		"CREATE TABLE ph_0 PARTITION OF ph FOR VALUES WITH (MODULUS 2, REMAINDER 0)",
		"CREATE TABLE ph_1 PARTITION OF ph FOR VALUES WITH (MODULUS 2, REMAINDER 1)",
		"CREATE TABLE pd (a int) PARTITION BY LIST (a)",
		"CREATE TABLE pd_1 PARTITION OF pd FOR VALUES IN (1)",
		"ALTER TABLE pd DETACH PARTITION pd_1",
		"INSERT INTO pt (a, b) VALUES (1, 'one'), (11, 'x'), (12, 'z')",
		"INSERT INTO ph VALUES (1), (2), (3)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	check := func(when string) {
		t.Helper()
		for _, tc := range []struct{ q, want string }{
			{"SELECT pg_get_partkeydef('pt'::regclass)", "RANGE (a)"},
			{"SELECT pg_get_partkeydef('pt_2'::regclass)", "LIST (b)"},
			{"SELECT pg_get_partkeydef('ph'::regclass)", "HASH (k)"},
			{"SELECT pg_get_expr(relpartbound, oid) FROM pg_class WHERE relname = 'pt_1'", "FOR VALUES FROM (0) TO (10)"},
			{"SELECT pg_get_expr(relpartbound, oid) FROM pg_class WHERE relname = 'pt_2_d'", "DEFAULT"},
			{"SELECT relispartition FROM pg_class WHERE relname = 'pd_1'", "false"},
			{"SELECT count(*) FROM pt", "3"},
			{"SELECT tableoid::regclass FROM pt WHERE a = 12", "pt_2_d"},
			{"SELECT count(*) FROM ph", "3"},
		} {
			if got := queryScalar(t, c, tc.q); got != tc.want {
				t.Errorf("%s: %s = %q, want %q", when, tc.q, got, tc.want)
			}
		}
	}
	check("before restart")
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	check("after restart")
	// Routing works on the reloaded partitions.
	if err := runSQLSimple(t, c, "INSERT INTO pt (a, b) VALUES (2, 'two'), (13, 'y')"); err != nil {
		t.Fatalf("insert after restart: %v", err)
	}
	if got := queryScalar(t, c, "SELECT tableoid::regclass FROM pt WHERE a = 13"); got != "pt_2_x" {
		t.Errorf("after restart: a = 13 routed to %q, want pt_2_x", got)
	}
}
