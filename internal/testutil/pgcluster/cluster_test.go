package pgcluster

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestClusterRoundtrip is a smoke test: init → start → SELECT → stop.
// Skips when the upstream PG bin tree isn't present.
func TestClusterRoundtrip(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	bin := upstreamBinDir(t)
	Available(t, bin)

	c, err := New("roundtrip", Options{
		BinDir:      bin,
		StartupWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		_ = c.Stop()
		_ = os.RemoveAll(filepath.Dir(c.DataDir()))
	}()

	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.WaitReady(ctx, 15*time.Second); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

	c.Exec(t, "CREATE TABLE smoke (id int)")
	c.Exec(t, "INSERT INTO smoke VALUES (1), (2), (3)")
	got := c.QueryScalar(t, "SELECT count(*) FROM smoke")
	if got != "3" {
		t.Fatalf("count=%q want 3", got)
	}
}

func upstreamBinDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cur := wd
	for {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return filepath.Join(cur, "postgres", "local_install", "bin")
		}
		next := filepath.Dir(cur)
		if next == cur {
			t.Fatalf("could not locate go.mod from %s", wd)
		}
		cur = next
	}
}
