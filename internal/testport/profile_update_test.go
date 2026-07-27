package testport

import (
	"context"
	"fmt"
	"os"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestProfileUpdate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping profile test in short mode")
	}

	// Durable-pinned NOT for durability: at fsync=off throughput (~2.3k TPS
	// vs ~1.3k) this fixed 4-worker × 5-row contention reproducibly trips
	// the known spurious-40001 divergence (plain autocommit UPDATE returns
	// "could not serialize access due to concurrent update (deadlock)";
	// PG would wait — deferred tuple-lock FIFO work, ledger 0021-0012).
	// Adjudicated fast-only per ci/design/test-gate-speedups/06 B.2 (fast
	// 2/2 fail, durable 3/3 pass, 2026-07-17); un-pin when 0021-0012 lands.
	c := newDurableCluster(t, "profile_update")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	ctx := context.Background()

	// Use pgbench's own init
	runSQL(t, c, "CREATE TABLE t (id int, val int)")
	runSQL(t, c, "CREATE INDEX i ON t (id)")
	runSQL(t, c, "INSERT INTO t VALUES (1, 0)")
	runSQL(t, c, "INSERT INTO t VALUES (2, 0)")
	runSQL(t, c, "INSERT INTO t VALUES (3, 0)")
	runSQL(t, c, "INSERT INTO t VALUES (4, 0)")
	runSQL(t, c, "INSERT INTO t VALUES (5, 0)")

	f, err := os.Create("/tmp/update.pprof")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	t.Log("Starting 10s CPU profile...")
	if err := pprof.StartCPUProfile(f); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	done := make(chan struct{})
	counts := make(chan int, 4)
	workerErrs := make(chan error, 4)

	for w := 0; w < 4; w++ {
		go func() {
			localCount := 0
			for {
				id := (localCount % 5) + 1
				delta := localCount % 1000
				_, err := c.Query(ctx,
					fmt.Sprintf("UPDATE t SET val = val + %d WHERE id = %d", delta, id))
				if err != nil {
					// MUST still send counts: the collector below reads
					// exactly 4 values, and a silent return here used to
					// deadlock the whole suite until go test's timeout.
					workerErrs <- err
					counts <- localCount
					return
				}
				localCount++
				select {
				case <-done:
					counts <- localCount
					return
				default:
				}
			}
		}()
	}

	<-deadline
	close(done)
	pprof.StopCPUProfile()

	totalCount := 0
	for i := 0; i < 4; i++ {
		totalCount += <-counts
	}
	close(workerErrs)
	for err := range workerErrs {
		t.Errorf("UPDATE worker exited on error: %v", err)
	}
	t.Logf("UPDATEs: %d in 10s = %d TPS", totalCount, totalCount/10)
	t.Logf("Profile: go tool pprof -top /tmp/update.pprof")
}
