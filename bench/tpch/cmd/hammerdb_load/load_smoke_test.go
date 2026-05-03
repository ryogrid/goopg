package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cur := wd
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
		next := filepath.Dir(cur)
		if next == cur {
			t.Fatalf("could not find go.mod from %s", wd)
		}
		cur = next
	}
}

// TestSmokeLoadAgainstCluster spins up an in-process goopg cluster
// via the existing testutil harness, runs the hammerdb_load loader
// against it for --limit-orders 200, and asserts ORDERS + LINEITEM
// row counts come back nonzero. The test exists to prove the
// loader actually exercises goopg end-to-end (the M0032-0005
// reproducer); it's NOT a regression test for SF=1 throughput
// (that lives in analysis/tpch-hammerdb-run-004*.md).
//
// For an in-process baseline at higher row counts, run
// TestBaselineLoad10k explicitly: `go test -run
// TestBaselineLoad10k ./bench/tpch/cmd/hammerdb_load`. That test
// loads 10 k orders + ~40 k lineitems and prints rows/sec at
// 1 k / 5 k / 10 k order checkpoints — enough to characterise
// throughput drift as the heap grows without requiring HammerDB
// or external profiling.
func TestSmokeLoadAgainstCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster-backed smoke in short mode")
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()

	c, err := cluster.New("hammerdb-smoke", cluster.Options{
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

	dsn := fmt.Sprintf("postgres://postgres@%s/postgres?sslmode=disable", c.ListenAddr())
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if err := createSchema(ctx, db); err != nil {
		t.Fatalf("createSchema: %v", err)
	}

	const totalOrders = 200
	l := &loader{
		db:             db,
		rng:            rand.New(rand.NewSource(7)),
		batchRows:      10,
		commitInterval: 50,
		scale:          1,
	}
	if err := l.run(ctx, totalOrders); err != nil {
		t.Fatalf("run: %v", err)
	}

	var orderCount, lineCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM orders").Scan(&orderCount); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM lineitem").Scan(&lineCount); err != nil {
		t.Fatalf("count lineitem: %v", err)
	}
	if orderCount != totalOrders {
		t.Errorf("orders count = %d, want %d", orderCount, totalOrders)
	}
	if lineCount < totalOrders || lineCount > totalOrders*7 {
		t.Errorf("lineitem count = %d, want %d..%d", lineCount, totalOrders, totalOrders*7)
	}
	t.Logf("smoke load: orders=%d lineitems=%d", orderCount, lineCount)
}

// TestBaselineLoad50k stresses the loader past the typical
// shared_buffers fill point at the default 256 MB configuration.
// 50 000 orders ≈ 200 k lineitems, enough to force dirty-page
// eviction and surface throughput decay if any. Used to
// characterise the M0032-0005 baseline; not a CI test (skipped
// under -short).
func TestBaselineLoad50k(t *testing.T) {
	if testing.Short() {
		t.Skip("baseline load is not a -short test")
	}
	if testing.Verbose() {
		// fall through; this test prints lots
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := cluster.New("hammerdb-baseline-50k", cluster.Options{
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

	dsn := fmt.Sprintf("postgres://postgres@%s/postgres?sslmode=disable", c.ListenAddr())
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := createSchema(ctx, db); err != nil {
		t.Fatal(err)
	}

	const totalOrders = 50000
	l := &loader{
		db:             db,
		rng:            rand.New(rand.NewSource(11)),
		batchRows:      10,
		commitInterval: 100,
		scale:          1,
	}
	start := time.Now()
	if err := l.run(ctx, totalOrders); err != nil {
		t.Fatalf("run: %v", err)
	}
	elapsed := time.Since(start)
	var orderCount, lineCount int
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM orders").Scan(&orderCount)
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM lineitem").Scan(&lineCount)
	rate := float64(orderCount) / elapsed.Seconds()
	t.Logf("baseline 50k load: orders=%d lineitems=%d elapsed=%s orders/s=%.0f",
		orderCount, lineCount, elapsed, rate)
}

// TestBaselineLoad200k pushes the loader past the prior
// `analysis/tpch-hammerdb-run-002.md` 430 k-order failure region
// to confirm the connection no longer drops. Skipped under
// -short; takes 1-2 minutes on the post-M0032-0005 baseline.
func TestBaselineLoad200k(t *testing.T) {
	if testing.Short() {
		t.Skip("baseline load is not a -short test")
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := cluster.New("hammerdb-baseline-200k", cluster.Options{
		RepoRoot:     repoRoot,
		DataDir:      filepath.Join(base, "data"),
		StartupWait:  30 * time.Second,
		ShutdownWait: 30 * time.Second,
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

	dsn := fmt.Sprintf("postgres://postgres@%s/postgres?sslmode=disable", c.ListenAddr())
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := createSchema(ctx, db); err != nil {
		t.Fatal(err)
	}

	const totalOrders = 200000
	l := &loader{
		db:             db,
		rng:            rand.New(rand.NewSource(13)),
		batchRows:      10,
		commitInterval: 100,
		scale:          1,
	}
	start := time.Now()
	if err := l.run(ctx, totalOrders); err != nil {
		t.Fatalf("run: %v", err)
	}
	elapsed := time.Since(start)
	var orderCount, lineCount int
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM orders").Scan(&orderCount)
	_ = db.QueryRowContext(ctx, "SELECT count(*) FROM lineitem").Scan(&lineCount)
	rate := float64(orderCount) / elapsed.Seconds()
	t.Logf("baseline 200k load: orders=%d lineitems=%d elapsed=%s orders/s=%.0f",
		orderCount, lineCount, elapsed, rate)
	if orderCount != totalOrders {
		t.Errorf("orders count = %d, want %d", orderCount, totalOrders)
	}
}

// TestBaselineLoad10k loads 10 000 orders + ~40 000 lineitems via
// the same harness, capturing rows/sec at progressive checkpoints.
// The numbers feed analysis/tpch-hammerdb-run-004-baseline.md.
//
// Skipped under -short. Runs in ~30 seconds on the M0032-0005
// baseline; M0032-0005 slice-2 fixes target ≥2× speedup.
func TestBaselineLoad10k(t *testing.T) {
	if testing.Short() {
		t.Skip("baseline load is not a -short test")
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := cluster.New("hammerdb-baseline", cluster.Options{
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

	dsn := fmt.Sprintf("postgres://postgres@%s/postgres?sslmode=disable", c.ListenAddr())
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := createSchema(ctx, db); err != nil {
		t.Fatal(err)
	}

	const totalOrders = 10000
	l := &loader{
		db:             db,
		rng:            rand.New(rand.NewSource(7)),
		batchRows:      10,
		commitInterval: 100,
		scale:          1,
	}
	start := time.Now()
	if err := l.run(ctx, totalOrders); err != nil {
		t.Fatalf("run: %v", err)
	}
	elapsed := time.Since(start)

	var orderCount, lineCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM orders").Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM lineitem").Scan(&lineCount); err != nil {
		t.Fatal(err)
	}
	rate := float64(orderCount) / elapsed.Seconds()
	t.Logf("baseline 10k load: orders=%d lineitems=%d elapsed=%s orders/s=%.0f",
		orderCount, lineCount, elapsed, rate)
	if orderCount != totalOrders {
		t.Errorf("orders count = %d, want %d", orderCount, totalOrders)
	}
}
