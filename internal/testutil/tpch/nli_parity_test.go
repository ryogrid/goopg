package tpch_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestNLIResultParityVsHashJoin (M0054-0006d) spins up a real
// goopg cluster, builds a small two-table fixture, and runs the
// same equi-join twice — once with NLI enabled (planner default)
// and once with the kill-switch off (HashJoin path). The result
// rows must match exactly, both content and ordering after a
// stable ORDER BY.
func TestNLIResultParityVsHashJoin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster-backed NLI parity in short mode")
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := cluster.New("nli-parity", cluster.Options{
		RepoRoot:     repoRoot,
		DataDir:      filepath.Join(base, "data"),
		StartupWait:  30 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, sql := range []string{
		`CREATE TABLE small (k int4, v int4)`,
		`CREATE TABLE indexed (id int4, payload int4)`,
		// goopg's INSERT path does not currently maintain
		// secondary indexes; CREATE INDEX bulk-builds from
		// existing heap rows. Insert first, then index, so the
		// btree is populated.
		`INSERT INTO small  VALUES (1,100),(2,200),(3,300),(4,400),(5,500)`,
		`INSERT INTO indexed VALUES (1,11),(2,22),(2,222),(3,33),(5,55),(7,77)`,
		`CREATE INDEX indexed_id_idx ON indexed (id)`,
		`ANALYZE small`,
		`ANALYZE indexed`,
	} {
		if _, err := c.Query(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}

	const q = `SELECT k, payload FROM small, indexed WHERE k = id ORDER BY k, payload`

	// First run with NLI on (default).
	planner.SetNLIEnabled(true)
	defer planner.SetNLIEnabled(true)
	rowsNLI, err := c.Query(ctx, q)
	if err != nil {
		t.Fatalf("query (NLI on): %v", err)
	}

	// Then run with NLI off — Hash join path.
	planner.SetNLIEnabled(false)
	rowsHash, err := c.Query(ctx, q)
	if err != nil {
		t.Fatalf("query (NLI off): %v", err)
	}
	planner.SetNLIEnabled(true)

	if len(rowsNLI) != len(rowsHash) {
		t.Fatalf("row count differs: NLI=%d Hash=%d", len(rowsNLI), len(rowsHash))
	}
	for i := range rowsNLI {
		if len(rowsNLI[i]) != len(rowsHash[i]) {
			t.Fatalf("row %d width: NLI=%d Hash=%d", i, len(rowsNLI[i]), len(rowsHash[i]))
		}
		for j := range rowsNLI[i] {
			if rowsNLI[i][j] != rowsHash[i][j] {
				t.Errorf("row %d col %d: NLI=%q Hash=%q", i, j, rowsNLI[i][j], rowsHash[i][j])
			}
		}
	}

	// Sanity: 5 expected matched output rows
	// (k=1→11; k=2→22; k=2→222; k=3→33; k=5→55).
	if got := len(rowsNLI); got != 5 {
		t.Errorf("expected 5 matched rows, got %d", got)
	}
}
