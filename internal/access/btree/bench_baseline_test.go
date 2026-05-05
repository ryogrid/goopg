package btree

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestBenchBaseline_M0055 (M0055-0001) is the baseline harness for
// the staged B-tree enhancement program. It performs N random
// 8-byte-key inserts into a fresh tree and reports the
// wall-clock total, split count, p50/p95/p99 single-insert
// latencies, and post-run RSS delta to stdout in a parsable
// format. The output is consumed by
// `analysis/btree-baseline-2026-05-06.md` (the freeze report)
// and by future Phase A/B regression tests that compare
// before/after numbers against the same harness.
//
// Run with:
//   go test ./internal/access/btree/ -run TestBenchBaseline_M0055 -count=1 -v
//
// Skipped under -short: the bench is not a correctness test and
// takes seconds-to-minutes depending on N.
func TestBenchBaseline_M0055(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping baseline bench in short mode")
	}
	const N = 100_000 // tighter than 1M so unit-test runs stay <30s
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	bt.ResetStats()

	keys := make([][]byte, N)
	rng := rand.New(rand.NewSource(42))
	for i := range keys {
		k := make([]byte, 8)
		binary.BigEndian.PutUint64(k, rng.Uint64())
		keys[i] = k
	}

	var rssBefore runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&rssBefore)

	latencies := make([]time.Duration, 0, N)
	t0 := time.Now()
	for i, k := range keys {
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i / 1000), Offset: uint16(i%1000) + 1}
		ti := time.Now()
		if err := bt.Insert(k, ptr); err != nil {
			t.Fatalf("Insert[%d]: %v", i, err)
		}
		latencies = append(latencies, time.Since(ti))
	}
	totalElapsed := time.Since(t0)

	var rssAfter runtime.MemStats
	runtime.ReadMemStats(&rssAfter)

	stats := bt.Stats()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[len(latencies)/2]
	p95 := latencies[(len(latencies)*95)/100]
	p99 := latencies[(len(latencies)*99)/100]
	maxLat := latencies[len(latencies)-1]

	fmt.Fprintln(os.Stdout, "M0055-baseline-summary {")
	fmt.Fprintf(os.Stdout, "  inserts=%d\n", N)
	fmt.Fprintf(os.Stdout, "  total_ms=%.2f\n", float64(totalElapsed.Microseconds())/1000.0)
	fmt.Fprintf(os.Stdout, "  inserts_per_sec=%.0f\n", float64(N)/totalElapsed.Seconds())
	fmt.Fprintf(os.Stdout, "  splits=%d (%.2f %%)\n", stats.Splits, 100*float64(stats.Splits)/float64(N))
	fmt.Fprintf(os.Stdout, "  p50_us=%d\n", p50.Microseconds())
	fmt.Fprintf(os.Stdout, "  p95_us=%d\n", p95.Microseconds())
	fmt.Fprintf(os.Stdout, "  p99_us=%d\n", p99.Microseconds())
	fmt.Fprintf(os.Stdout, "  max_us=%d\n", maxLat.Microseconds())
	fmt.Fprintf(os.Stdout, "  rss_delta_mb=%.1f\n", float64(rssAfter.HeapAlloc-rssBefore.HeapAlloc)/(1<<20))
	fmt.Fprintln(os.Stdout, "}")
}
