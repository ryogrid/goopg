package executor

import (
	"strings"
	"testing"
)

// TestExplainBuffersAnalyzeTextLine pins the M0122-0003 BUFFERS slice: under
// EXPLAIN (ANALYZE, BUFFERS) each scan node gets a "Buffers: shared hit=N
// read=N" detail line (explain.c's show_buffer_usage), diffed from
// storage.Pool.BufferCounters() around each node's Open/Next/Close calls
// (internal/executor/instrument.go). Scope: shared-only, hit/read-only —
// local/temp buffers and dirtied/written are a deferred follow-up.
func TestExplainBuffersAnalyzeTextLine(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE ebuf (data int)",
		"INSERT INTO ebuf VALUES (1)",
		"INSERT INTO ebuf VALUES (2)",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)

	const q = "SELECT data FROM ebuf"

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, BUFFERS) "+q)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Buffers: shared ") {
		t.Fatalf("EXPLAIN (ANALYZE, BUFFERS) missing 'Buffers: shared ' line:\n%s", joined)
	}
	if !strings.Contains(joined, "hit=") && !strings.Contains(joined, "read=") {
		t.Errorf("Buffers line has neither hit= nor read=:\n%s", joined)
	}
}

// TestExplainBuffersOffByDefault confirms BUFFERS is opt-in: plain EXPLAIN
// ANALYZE (no BUFFERS) never emits the line, matching upstream's default.
func TestExplainBuffersOffByDefault(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE ebuf2 (data int)",
		"INSERT INTO ebuf2 VALUES (1)",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE SELECT data FROM ebuf2")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Buffers:") {
		t.Errorf("EXPLAIN ANALYZE (no BUFFERS) unexpectedly emitted a Buffers line:\n%s", joined)
	}
}

// TestExplainBuffersRepeatScanAccumulatesHits runs the same query twice so
// the second pass's pages are guaranteed resident, exercising the hit-only
// branch of formatBuffersLine (no read= term once nothing needs a disk
// fetch).
func TestExplainBuffersRepeatScanAccumulatesHits(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE ebuf3 (data int)",
		"INSERT INTO ebuf3 VALUES (1)",
		"INSERT INTO ebuf3 VALUES (2)",
		"INSERT INTO ebuf3 VALUES (3)",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)

	const q = "EXPLAIN (ANALYZE, BUFFERS) SELECT data FROM ebuf3"
	// Warm the cache.
	_ = runExplainRows(t, ctx, q)

	lines := runExplainRows(t, ctx, q)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Buffers: shared hit=") {
		t.Errorf("second (warm-cache) pass should report a shared hit=, got:\n%s", joined)
	}
	if strings.Contains(joined, "read=") {
		t.Errorf("second (warm-cache) pass should not report a read= (all pages already resident):\n%s", joined)
	}
}
