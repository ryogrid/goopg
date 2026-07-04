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

// TestExplainBuffersJSONAlwaysIncludesSharedBlocks pins the FORMAT JSON slice
// of M0122-0003 BUFFERS: unlike TEXT's "Buffers:" line (only printed when
// non-zero), upstream's non-text show_buffer_usage() prints "Shared Hit
// Blocks"/"Shared Read Blocks" unconditionally once BUFFERS is requested,
// even when a counter is zero (explain.c's peek_buffer_usage comment: "when
// format is anything other than text, we print even if the counters are all
// zeroes"). Scope: shared-only — dirtied/written/local/temp remain deferred.
func TestExplainBuffersJSONAlwaysIncludesSharedBlocks(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx, "CREATE TABLE ebuf4 (data int)")
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT data FROM ebuf4")
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, `"Shared Hit Blocks"`) {
		t.Errorf("expected \"Shared Hit Blocks\" key in JSON output:\n%s", out)
	}
	if !strings.Contains(out, `"Shared Read Blocks"`) {
		t.Errorf("expected \"Shared Read Blocks\" key in JSON output:\n%s", out)
	}
}

// TestExplainBuffersJSONOmittedWithoutBuffersOption confirms the JSON/XML/YAML
// "Shared Hit/Read Blocks" properties are opt-in, matching TEXT's BUFFERS gate.
func TestExplainBuffersJSONOmittedWithoutBuffersOption(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx, "CREATE TABLE ebuf5 (data int)")
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, FORMAT JSON) SELECT data FROM ebuf5")
	out := strings.Join(lines, "\n")
	if strings.Contains(out, "Shared Hit Blocks") || strings.Contains(out, "Shared Read Blocks") {
		t.Errorf("EXPLAIN (ANALYZE, FORMAT JSON) without BUFFERS unexpectedly reported shared blocks:\n%s", out)
	}
}

// TestExplainBuffersXMLTagSanitized pins the XML rendering of the same
// property: xmlTagName sanitizes the space to '-' (ExplainXMLTag upstream).
func TestExplainBuffersXMLTagSanitized(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx, "CREATE TABLE ebuf6 (data int)")
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT XML) SELECT data FROM ebuf6")
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "<Shared-Hit-Blocks>") || !strings.Contains(out, "<Shared-Read-Blocks>") {
		t.Errorf("expected <Shared-Hit-Blocks>/<Shared-Read-Blocks> tags in XML output:\n%s", out)
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
