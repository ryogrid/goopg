package tpch_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/optimizer"
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
	optimizer.SetNLIEnabled(true)
	defer optimizer.SetNLIEnabled(true)
	rowsNLI, err := c.Query(ctx, q)
	if err != nil {
		t.Fatalf("query (NLI on): %v", err)
	}

	// Then run with NLI off — Hash join path.
	optimizer.SetNLIEnabled(false)
	rowsHash, err := c.Query(ctx, q)
	if err != nil {
		t.Fatalf("query (NLI off): %v", err)
	}
	optimizer.SetNLIEnabled(true)

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

// TestNLIResultParityCompositeKey (M0054-0006-followup-Q9-
// composite) drives a Q9-shaped two-equi-conjunct join across a
// composite-key B-tree index, asserting NLI-on and NLI-off
// produce identical row sets. This is the cluster-backed
// regression test for the run-012 attempt #1 failure
// `column "ps_suppkey" is not numeric at runtime`.
func TestNLIResultParityCompositeKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster-backed NLI composite-key parity in short mode")
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := cluster.New("nli-parity-composite", cluster.Options{
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
		// Mirror TPC-H partsupp / lineitem column shape (numeric
		// keys) so the regression case — composite PK on
		// (ps_partkey, ps_suppkey) — is exercised.
		`CREATE TABLE partsupp (ps_partkey int4 NOT NULL, ps_suppkey int4 NOT NULL, ps_supplycost int4)`,
		`CREATE TABLE lineitem (l_partkey int4 NOT NULL, l_suppkey int4 NOT NULL, l_quantity int4)`,
		`INSERT INTO partsupp VALUES
		    (1,1,100),(1,2,150),(2,1,200),(2,2,250),
		    (3,1,300),(3,3,350),(4,2,400),(5,5,500)`,
		`INSERT INTO lineitem VALUES
		    (1,1,10),(1,2,11),(2,1,20),(2,3,21),
		    (3,1,30),(3,3,31),(4,2,40),(5,5,50),(6,6,60)`,
		`CREATE INDEX partsupp_pk ON partsupp (ps_partkey, ps_suppkey)`,
		`ANALYZE partsupp`,
		`ANALYZE lineitem`,
	} {
		if _, err := c.Query(ctx, sql); err != nil {
			t.Fatalf("setup %q: %v", sql, err)
		}
	}

	const q = `SELECT l_partkey, l_suppkey, l_quantity, ps_supplycost
		FROM lineitem, partsupp
		WHERE l_partkey = ps_partkey AND l_suppkey = ps_suppkey
		ORDER BY l_partkey, l_suppkey`

	optimizer.SetNLIEnabled(true)
	defer optimizer.SetNLIEnabled(true)
	rowsNLI, err := c.Query(ctx, q)
	if err != nil {
		t.Fatalf("query (NLI on, composite key): %v", err)
	}

	optimizer.SetNLIEnabled(false)
	rowsHash, err := c.Query(ctx, q)
	if err != nil {
		t.Fatalf("query (NLI off, composite key): %v", err)
	}
	optimizer.SetNLIEnabled(true)

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

	// Sanity: expected matched rows are the (l_partkey,
	// l_suppkey) pairs that appear in both tables — by hand:
	// (1,1), (1,2), (2,1), (3,1), (3,3), (4,2), (5,5) → 7 rows.
	if got := len(rowsNLI); got != 7 {
		t.Errorf("expected 7 matched composite-key rows, got %d", got)
	}
}
