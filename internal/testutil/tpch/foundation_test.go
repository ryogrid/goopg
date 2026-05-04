package tpch_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestFoundationSeqScanFilterJoin verifies that the core
// execution path — SeqScan, WHERE filter, ORDER BY, GROUP BY,
// and 2‑table hash‑join — returns correct results against real
// data loaded via INSERT.  This gates out bugs in storage,
// buffer‑pool, SI, comparison operators, and executor plumbing
// before attributing TPC‑H parity failures to planner column
// alignment.
func TestFoundationSeqScanFilterJoin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster-backed foundation test in short mode")
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := cluster.New("foundation", cluster.Options{
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --- Create tables ---
	tables := []string{
		`CREATE TABLE t1 (id INT8, name TEXT, val NUMERIC)`,
		`CREATE TABLE t2 (id INT8, desc TEXT)`,
		`CREATE TABLE t3 (id INT8, qty INT8, price NUMERIC)`,
	}
	for _, ddl := range tables {
		if _, err := c.Query(ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", firstWords(ddl, 4), err)
		}
	}

	// --- Insert data ---
	inserts := []string{
		`INSERT INTO t1 VALUES (1, 'alpha', 10.5), (2, 'beta', 20.0), (3, 'gamma', 30.0), (4, 'delta', 40.0)`,
		`INSERT INTO t2 VALUES (1, 'one'), (2, 'two'), (5, 'five')`,
		`INSERT INTO t3 VALUES (1, 100, 1.99), (2, 200, 2.99), (2, 300, 3.99), (3, 400, 4.99)`,
	}
	for _, ins := range inserts {
		if _, err := c.Query(ctx, ins); err != nil {
			t.Fatalf("INSERT %q: %v", firstWords(ins, 4), err)
		}
	}

	// --- Test 1: SELECT * ---
	rows, err := c.Query(ctx, `SELECT * FROM t1 ORDER BY id`)
	if err != nil {
		t.Fatalf("SELECT * FROM t1: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("SELECT * FROM t1: got %d rows, want 4", len(rows))
	}
	if rows[0][0] != "1" || rows[0][1] != "alpha" || rows[0][2] != "10.5" {
		t.Errorf("row 0: got %v, want [1 alpha 10.5]", rows[0])
	}

	// --- Test 2: WHERE filter (equality) ---
	rows, err = c.Query(ctx, `SELECT name FROM t1 WHERE id = 2`)
	if err != nil {
		t.Fatalf("WHERE id=2: %v", err)
	}
	if len(rows) != 1 || rows[0][0] != "beta" {
		t.Errorf("WHERE id=2: got %v, want [[beta]]", rows)
	}

	// --- Test 3: WHERE filter (range) ---
	rows, err = c.Query(ctx, `SELECT id FROM t1 WHERE val > 15.0 ORDER BY id`)
	if err != nil {
		t.Fatalf("WHERE val>15: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("WHERE val>15: got %d rows, want 3", len(rows))
	}

	// --- Test 4: GROUP BY + aggregate ---
	rows, err = c.Query(ctx, `SELECT id, sum(qty) FROM t3 GROUP BY id ORDER BY id`)
	if err != nil {
		t.Fatalf("GROUP BY: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("GROUP BY: got %d rows, want 3", len(rows))
	}
	// id=2 sum = 200+300 = 500
	if rows[0][0] != "1" || rows[0][1] != "100" {
		t.Errorf("GROUP BY id=1: %v", rows[0])
	}
	if rows[1][0] != "2" || rows[1][1] != "500" {
		t.Errorf("GROUP BY id=2: %v", rows[1])
	}

	// --- Test 5: 2-table INNER JOIN (hash join) ---
	rows, err = c.Query(ctx, `SELECT t1.name, t2.desc FROM t1 JOIN t2 ON t1.id = t2.id ORDER BY t1.id`)
	if err != nil {
		t.Fatalf("JOIN: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("JOIN: got %d rows, want 2 (inner join drops id=3,4 from t1 and id=5 from t2)", len(rows))
	}
	if rows[0][0] != "alpha" || rows[0][1] != "one" {
		t.Errorf("JOIN row 0: %v", rows[0])
	}
	if rows[1][0] != "beta" || rows[1][1] != "two" {
		t.Errorf("JOIN row 1: %v", rows[1])
	}

	// --- Test 6: LIKE operator ---
	rows, err = c.Query(ctx, `SELECT name FROM t1 WHERE name LIKE 'a%'`)
	if err != nil {
		t.Fatalf("LIKE: %v", err)
	}
	if len(rows) != 1 || rows[0][0] != "alpha" {
		t.Errorf("LIKE 'a%%': got %v, want [[alpha]]", rows)
	}

	// --- Test 7: 3-table JOIN ---
	// M0039/M0041 fixed ColumnRef misalignment for ≥3 tables.
	// alpha (id=1) has t3.qty=100, filtered by WHERE t3.qty>150; only beta rows remain.
	rows, err = c.Query(ctx, `SELECT t1.name, t3.qty FROM t1 JOIN t2 ON t1.id = t2.id JOIN t3 ON t1.id = t3.id WHERE t3.qty > 150 ORDER BY t3.qty`)
	if err != nil {
		t.Fatalf("3-table JOIN: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("3-table JOIN: got %d rows, want 2", len(rows))
	}
	if rows[0][0] != "beta" || rows[0][1] != "200" {
		t.Errorf("3-table JOIN row 0: got %v, want [beta 200]", rows[0])
	}
	if rows[1][0] != "beta" || rows[1][1] != "300" {
		t.Errorf("3-table JOIN row 1: got %v, want [beta 300]", rows[1])
	}
}
