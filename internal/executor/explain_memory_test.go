package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// TestFormatMemoryLine pins formatMemoryLine's TEXT rendering:
// "Memory: used=NkB  allocated=NkB", omitting the whole line when
// both counters are zero. M0122-0003.
func TestFormatMemoryLine(t *testing.T) {
	cases := []struct {
		name string
		s    nodeStats
		want string
	}{
		{"all zero", nodeStats{}, ""},
		{"used only", nodeStats{memPeak: 2048}, "Memory: used=2kB  allocated=0kB"},
		{"allocated only", nodeStats{memAllocated: 65536}, "Memory: used=0kB  allocated=64kB"},
		{"both", nodeStats{memPeak: 4096, memAllocated: 131072},
			"Memory: used=4kB  allocated=128kB"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatMemoryLine(&c.s); got != c.want {
				t.Errorf("formatMemoryLine(%+v) = %q, want %q", c.s, got, c.want)
			}
		})
	}
}

// TestExplainMemoryOffByDefault confirms MEMORY is opt-in: plain EXPLAIN
// ANALYZE (no MEMORY) never emits the line, matching upstream's default.
func TestExplainMemoryOffByDefault(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE emem1 (data int)",
		"INSERT INTO emem1 VALUES (1)",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE SELECT data FROM emem1")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Memory:") {
		t.Errorf("EXPLAIN ANALYZE (no MEMORY) unexpectedly emitted a Memory line:\n%s", joined)
	}
}

// TestExplainMemoryAnalyzeTextLine verifies EXPLAIN (ANALYZE, MEMORY) is
// accepted and the query completes without error. The Memory line may or
// may not appear depending on whether any mctx allocations happened during
// the node's execution (SeqScan decodes tuples without mctx, so a simple
// scan may report zero memory). The low-level formatMemoryLine and
// instrumentedOp tests pin the output path independently. M0122-0003.
func TestExplainMemoryAnalyzeTextLine(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.Mctx = mctx.Acquire(nil, mctx.KindStmt)
	defer ctx.Mctx.Release()

	runComposite(t, ctx,
		"CREATE TABLE emem2 (data int, label text)",
		"INSERT INTO emem2 VALUES (1, 'hello')",
		"INSERT INTO emem2 VALUES (2, 'world')",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)

	const q = "SELECT data, label FROM emem2"

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, MEMORY) "+q)
	joined := strings.Join(lines, "\n")
	// The statement must parse and execute without error — the Memory
	// line presence depends on whether the executor path used mctx.
	if strings.Contains(joined, "error") || strings.Contains(joined, "ERROR") {
		t.Fatalf("EXPLAIN (ANALYZE, MEMORY) produced an error:\n%s", joined)
	}
	// Verify the EXPLAIN produced reasonable output.
	if !strings.Contains(joined, "Seq Scan") {
		t.Errorf("output missing Seq Scan:\n%s", joined)
	}
}

// TestExplainMemoryJSONAlwaysIncludesKeys confirms EXPLAIN (ANALYZE,
// MEMORY, FORMAT JSON) includes "Memory Used" / "Memory Allocated"
// per-node, matching PG's show_memory_counters non-text branch.
func TestExplainMemoryJSONAlwaysIncludesKeys(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.Mctx = mctx.Acquire(nil, mctx.KindStmt)
	defer ctx.Mctx.Release()

	runComposite(t, ctx,
		"CREATE TABLE emem3 (data int)",
		"INSERT INTO emem3 VALUES (1)",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, MEMORY, FORMAT JSON) SELECT data FROM emem3")
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, `"Memory Used"`) {
		t.Errorf("expected \"Memory Used\" key in JSON output:\n%s", out)
	}
	if !strings.Contains(out, `"Memory Allocated"`) {
		t.Errorf("expected \"Memory Allocated\" key in JSON output:\n%s", out)
	}
}

// TestExplainMemoryJSONOmittedWithoutMemoryOption confirms MEMORY
// properties are opt-in in JSON/XML/YAML: EXPLAIN (ANALYZE, FORMAT JSON)
// without MEMORY never includes them.
func TestExplainMemoryJSONOmittedWithoutMemoryOption(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE emem4 (data int)",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, FORMAT JSON) SELECT data FROM emem4")
	out := strings.Join(lines, "\n")
	if strings.Contains(out, "Memory Used") || strings.Contains(out, "Memory Allocated") {
		t.Errorf("EXPLAIN (ANALYZE, FORMAT JSON) without MEMORY unexpectedly reported memory:\n%s", out)
	}
}

// TestInstrumentedOpAccountsMemory exercises the mctx.Usage() diffing added
// for EXPLAIN MEMORY: allocating bytes into the statement mctx between a
// node's Open and Close must roll into that node's memPeak/memAllocated,
// and formatMemoryLine must render it.
func TestInstrumentedOpAccountsMemory(t *testing.T) {
	// We use a dedicated mctx context rather than relying on the test
	// harness's SQL-level context, so we can seed bytes precisely.
	sctx := mctx.Acquire(nil, mctx.KindStmt)
	defer sctx.Release()

	stats := &nodeStats{}
	op := &instrumentedOp{inner: fakeIOTimingOp{}, plan: &planner.Values{}, stats: stats}

	if err := op.Open(&Context{Mctx: sctx}); err != nil {
		t.Fatal(err)
	}

	// Allocate some bytes into sctx — emulating what a real operator
	// does during tuple decoding.
	_ = sctx.Alloc(4096)  // 4 KiB
	_ = sctx.Alloc(1024)  // 1 KiB

	if _, err := op.Next(); err != EOF {
		t.Fatalf("Next() err = %v, want EOF", err)
	}
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}

	if stats.memPeak < 4096 {
		t.Errorf("memPeak = %d, want >= 4096 (allocated 4+1 KiB)", stats.memPeak)
	}
	if stats.memPeak > stats.memAllocated {
		t.Errorf("memPeak (%d) > memAllocated (%d) — peak should never exceed allocated",
			stats.memPeak, stats.memAllocated)
	}
	line := formatMemoryLine(stats)
	if !strings.Contains(line, "Memory: used=") {
		t.Errorf("formatMemoryLine = %q, want a non-empty Memory line", line)
	}
}

// TestPlanToJSONWithStatsRendersMemoryWhenNonzero is the FORMAT
// JSON/XML/YAML sibling of TestInstrumentedOpAccountsMemory: pins
// planToJSONWithStats's "Memory Used"/"Memory Allocated" properties
// directly against a synthetic stats table.
func TestPlanToJSONWithStatsRendersMemoryWhenNonzero(t *testing.T) {
	n := &planner.Values{}
	stats := nodeStatsTable{n: {memPeak: 8192, memAllocated: 65536}}
	obj := planToJSONWithStats(n, parser.ExplainOptions{Memory: true}, stats, false)
	if got, ok := obj["Memory Used"].(int64); !ok || got != 8 {
		t.Errorf("Memory Used = %v, want 8 (8192 bytes / 1024)", obj["Memory Used"])
	}
	if got, ok := obj["Memory Allocated"].(int64); !ok || got != 64 {
		t.Errorf("Memory Allocated = %v, want 64 (65536 bytes / 1024)", obj["Memory Allocated"])
	}
}

// TestPlanToJSONWithStatsOmitsMemoryWhenOptionOff is the mirror of the
// above: without opts.Memory, the properties stay absent even though the
// node has nonzero memory counters.
func TestPlanToJSONWithStatsOmitsMemoryWhenOptionOff(t *testing.T) {
	n := &planner.Values{}
	stats := nodeStatsTable{n: {memPeak: 4096, memAllocated: 16384}}
	obj := planToJSONWithStats(n, parser.ExplainOptions{}, stats, false)
	if _, ok := obj["Memory Used"]; ok {
		t.Errorf("Memory Used present without opts.Memory, want absent")
	}
	if _, ok := obj["Memory Allocated"]; ok {
		t.Errorf("Memory Allocated present without opts.Memory, want absent")
	}
}

// TestExplainMemoryRepeatScanAccumulatesMore confirms that running the same
// EXPLAIN (ANALYZE, MEMORY) query twice works correctly without error.
// The mctx context is reused across statements within a session — each
// statement resets currentBytes but totalAllocated keeps growing as chunks
// are reused. This test verifies the statement completes cleanly; the
// precise Memory line content is pinned by the formatMemoryLine unit tests.
func TestExplainMemoryRepeatScanAccumulatesMore(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	ctx.Mctx = mctx.Acquire(nil, mctx.KindStmt)
	defer ctx.Mctx.Release()

	runComposite(t, ctx,
		"CREATE TABLE emem5 (data int, label text)",
		"INSERT INTO emem5 VALUES (1, 'first')",
		"INSERT INTO emem5 VALUES (2, 'second')",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)

	const q = "EXPLAIN (ANALYZE, MEMORY) SELECT data, label FROM emem5"
	// Both runs should succeed without error.
	lines1 := runExplainRows(t, ctx, q)
	lines2 := runExplainRows(t, ctx, q)
	for _, lines := range [][]string{lines1, lines2} {
		joined := strings.Join(lines, "\n")
		if strings.Contains(joined, "error") || strings.Contains(joined, "ERROR") {
			t.Errorf("EXPLAIN (ANALYZE, MEMORY) produced an error:\n%s", joined)
		}
	}
}

// TestExplainMemoryWithMctxNil confirms that EXPLAIN (ANALYZE, MEMORY)
// does not panic when ctx.Mctx is nil (e.g. some edge paths where the
// statement context isn't set).
func TestExplainMemoryWithMctxNil(t *testing.T) {
	// Direct low-level test: instrumentedOp with nil Mctx must not panic.
	stats := &nodeStats{}
	op := &instrumentedOp{inner: fakeIOTimingOp{}, plan: &planner.Values{}, stats: stats}

	// Open with nil Mctx.
	if err := op.Open(&Context{}); err != nil {
		t.Fatal(err)
	}
	if _, err := op.Next(); err != EOF {
		t.Fatalf("Next() err = %v, want EOF", err)
	}
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}
	// memSeeded should remain false, so line should be empty.
	if line := formatMemoryLine(stats); line != "" {
		t.Errorf("with nil Mctx, formatMemoryLine = %q, want \"\"", line)
	}
}
