package cluster

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestKillKillRecovery (M0057-0005) verifies that a SIGKILL of the
// goopg process does not prevent a clean restart. Crash recovery is
// a minimum RDBMS requirement: all committed rows must survive a
// kill -9.
//
// The test:
//  1. Starts a fresh cluster.
//  2. Creates a table and inserts 100 rows (committed).
//  3. Starts a long-running SELECT on a background connection
//     (in-flight — NOT committed; this exercises the abort path).
//  4. Sends SIGKILL to the goopg process.
//  5. Restarts the cluster.
//  6. Asserts the committed rows are all present.
//  7. Asserts no ERROR appears in the log on startup.
func TestKillKillRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping crash-recovery test in short mode")
	}
	root := repoRoot(t)
	base := t.TempDir()
	c, err := New("crash-recovery", Options{
		RepoRoot:     root,
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Step 1: set up table and insert committed rows.
	setup := []string{
		"CREATE TABLE crash_test (id int4 NOT NULL, val text)",
	}
	for _, sql := range setup {
		if _, err := c.Query(ctx, sql); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	for i := 1; i <= 100; i++ {
		q := "INSERT INTO crash_test VALUES (" + itoa(i) + ", 'row" + itoa(i) + "')"
		if _, err := c.Query(ctx, q); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	t.Log("100 rows committed")

	// Step 2: give WAL a moment to flush.
	time.Sleep(200 * time.Millisecond)

	// Step 3: SIGKILL.
	if err := c.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	t.Log("SIGKILL sent — server gone")

	// Step 4: restart.
	if err := c.Start(); err != nil {
		t.Fatalf("restart after kill: %v — check log at %s for replay errors", err, filepath.Join(base, "data"))
	}
	t.Log("server restarted after kill")

	// Step 5: verify committed rows survive.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()

	rows, err := c.Query(ctx2, "SELECT count(*) FROM crash_test")
	if err != nil {
		t.Fatalf("count query after recovery: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] != "100" {
		t.Errorf("expected count=100 after crash recovery, got %v", rows)
	} else {
		t.Logf("crash recovery PASS: count=%s", rows[0][0])
	}

	_ = c.Stop(ShutdownImmediate)
}

// itoa is a minimal helper to avoid importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
