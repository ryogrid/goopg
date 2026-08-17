package nbtree

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

// TestBenchDedupRetention_M0055_Phase_B (M0055-0003) is the
// duplicate-heavy variant of the baseline harness. Inserts 100K
// records with only 100 distinct keys (so each key has ~1000
// duplicates) and asserts post-insert page count is bounded
// relative to a hypothetical post-bulk-build size. Without
// steady-state dedup (Phase B) every duplicate would consume a
// fresh line pointer and pages would grow without bound.
func TestBenchDedupRetention_M0055_Phase_B(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping duplicate-heavy bench in short mode")
	}
	const N = 100_000
	const distinctKeys = 100
	bt, _, cleanup := newTestTree(t)
	defer cleanup()
	bt.ResetStats()
	for i := 0; i < N; i++ {
		k := make([]byte, 8)
		binary.BigEndian.PutUint64(k, uint64(i%distinctKeys))
		ptr := storage.ItemPointer{Block: storage.BlockNumber(i / 1000), Offset: uint16(i%1000) + 1}
		if err := bt.Insert(k, ptr); err != nil {
			t.Fatalf("Insert[%d]: %v", i, err)
		}
	}
	stats := bt.Stats()
	// Acceptance: with steady-state dedup landed (Phase B), the
	// 100K inserts of 100 distinct keys should produce far fewer
	// splits than 100K inserts would without dedup. Each key's
	// posting absorbs duplicates in place; only when a posting
	// outgrows the page size threshold does a split happen.
	// Empirical bound: < 2× the post-bulk-build split count
	// (which would be ~1 split per overflow). We assert
	// `splits < 5000` as a generous cap that still fails if
	// dedup is wholly absent (which would produce ~100K splits).
	if stats.Splits > 5000 {
		t.Errorf("Phase B dedup: too many splits (%d) — duplicate-heavy retention not bounded", stats.Splits)
	}
	t.Logf("M0055-Phase-B-summary { inserts=%d distinct_keys=%d splits=%d }", N, distinctKeys, stats.Splits)
}

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
