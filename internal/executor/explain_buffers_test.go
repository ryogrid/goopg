package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// fakeIOTimingOp is a minimal Operator stub for
// TestInstrumentedOpAccountsIOTime: it produces zero rows, existing purely
// so instrumentedOp has something to wrap while exercising the real
// Open/Next/Close accountBuffers diffing path.
type fakeIOTimingOp struct{}

func (fakeIOTimingOp) Schema() planner.Schema  { return nil }
func (fakeIOTimingOp) Open(*Context) error     { return nil }
func (fakeIOTimingOp) Next() (TupleSlot, error) { return nil, EOF }
func (fakeIOTimingOp) Close() error            { return nil }

// TestExplainBuffersAnalyzeTextLine pins the M0122-0003 BUFFERS slice: under
// EXPLAIN (ANALYZE, BUFFERS) each scan node gets a "Buffers: shared hit=N
// read=N" detail line (explain.c's show_buffer_usage), diffed from
// storage.Pool.BufferCounters() around each node's Open/Next/Close calls
// (internal/executor/instrument.go). Scope: shared-only, hit/read-only —
// local/temp buffers remain a deferred follow-up (dirtied/written now land
// too — see TestFormatBuffersLineDirtiedWritten).
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
// zeroes"). Scope: shared-only — local/temp remain deferred.
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

// TestExplainBuffersJSONAlwaysIncludesDirtiedWrittenBlocks extends the
// shared-blocks JSON slice above: "Shared Dirtied Blocks"/"Shared Written
// Blocks" are also unconditional once BUFFERS is requested (same
// peek_buffer_usage semantics — printed even when zero), closing the
// dirtied/written gap the two rows above deferred.
func TestExplainBuffersJSONAlwaysIncludesDirtiedWrittenBlocks(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx, "CREATE TABLE ebuf7 (data int)")
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT data FROM ebuf7")
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, `"Shared Dirtied Blocks"`) {
		t.Errorf("expected \"Shared Dirtied Blocks\" key in JSON output:\n%s", out)
	}
	if !strings.Contains(out, `"Shared Written Blocks"`) {
		t.Errorf("expected \"Shared Written Blocks\" key in JSON output:\n%s", out)
	}
}

// TestFormatBuffersLineDirtiedWritten pins formatBuffersLine's TEXT rendering
// of the dirtied=/written= terms directly (explain.c's show_buffer_usage
// shared-block branch): each term is independently gated on its own counter
// being positive, and the whole line is gated on any of the four being
// nonzero.
func TestFormatBuffersLineDirtiedWritten(t *testing.T) {
	cases := []struct {
		name string
		s    nodeStats
		want string
	}{
		{"all zero", nodeStats{}, ""},
		{"dirtied only", nodeStats{bufDirtied: 3}, "Buffers: shared dirtied=3"},
		{"written only", nodeStats{bufWritten: 2}, "Buffers: shared written=2"},
		{"all four", nodeStats{bufHit: 1, bufRead: 2, bufDirtied: 3, bufWritten: 4},
			"Buffers: shared hit=1 read=2 dirtied=3 written=4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatBuffersLine(&c.s); got != c.want {
				t.Errorf("formatBuffersLine(%+v) = %q, want %q", c.s, got, c.want)
			}
		})
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

// TestExplainBuffersJSONWithoutAnalyzeIncludesPlanningGroup pins the
// remaining M0122-0003 BUFFERS gap: upstream's ExplainOnePlan always shows a
// "Planning" group (buffer usage incurred by the planner itself) whenever
// BUFFERS is requested for a non-text format, independent of ANALYZE
// (explain.c's peek_buffer_usage returns true for any non-text format as
// soon as BUFFERS was requested, even with every counter at zero). Without
// ANALYZE, goopg previously rendered no "Planning" key at all.
func TestExplainBuffersJSONWithoutAnalyzeIncludesPlanningGroup(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx, "CREATE TABLE ebuf8 (data int)")
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (BUFFERS, FORMAT JSON) SELECT data FROM ebuf8")
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, `"Planning"`) {
		t.Fatalf("EXPLAIN (BUFFERS, FORMAT JSON) without ANALYZE missing \"Planning\" group:\n%s", out)
	}
	if !strings.Contains(out, `"Shared Hit Blocks"`) || !strings.Contains(out, `"Shared Read Blocks"`) {
		t.Errorf("Planning group missing Shared Hit/Read Blocks:\n%s", out)
	}
}

// TestExplainBuffersJSONWithoutBuffersOmitsPlanningGroup confirms the
// "Planning" group added above stays opt-in: plain EXPLAIN (FORMAT JSON),
// no BUFFERS, never emits it.
func TestExplainBuffersJSONWithoutBuffersOmitsPlanningGroup(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx, "CREATE TABLE ebuf9 (data int)")
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT JSON) SELECT data FROM ebuf9")
	out := strings.Join(lines, "\n")
	if strings.Contains(out, `"Planning"`) {
		t.Errorf("EXPLAIN (FORMAT JSON) without BUFFERS unexpectedly emitted a \"Planning\" group:\n%s", out)
	}
}

// TestExplainBuffersAnalyzeJSONIncludesPlanningGroup extends the coverage
// to the ANALYZE case: the "Planning" group is independent of ANALYZE, so
// EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) must show it too, alongside the
// per-node execution buffer counters already covered by
// TestExplainBuffersJSONAlwaysIncludesSharedBlocks.
func TestExplainBuffersAnalyzeJSONIncludesPlanningGroup(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx, "CREATE TABLE ebuf10 (data int)")
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT data FROM ebuf10")
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, `"Planning"`) {
		t.Fatalf("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) missing \"Planning\" group:\n%s", out)
	}
}

// TestExplainBuffersXMLWithoutAnalyzeIncludesPlanningGroup is the XML sibling
// of TestExplainBuffersJSONWithoutAnalyzeIncludesPlanningGroup, pinning
// xmlTagName's sanitization of the nested group ("Planning" has no
// whitespace so it passes through unchanged, unlike "Shared Hit Blocks").
func TestExplainBuffersXMLWithoutAnalyzeIncludesPlanningGroup(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx, "CREATE TABLE ebuf11 (data int)")
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (BUFFERS, FORMAT XML) SELECT data FROM ebuf11")
	out := strings.Join(lines, "\n")
	if !strings.Contains(out, "<Planning>") || !strings.Contains(out, "<Shared-Hit-Blocks>0</Shared-Hit-Blocks>") {
		t.Errorf("expected <Planning> group with <Shared-Hit-Blocks>0</Shared-Hit-Blocks> in XML output:\n%s", out)
	}
}

// TestFormatIOTimingsLine pins formatIOTimingsLine's TEXT rendering
// (explain.c's show_buffer_usage has_shared_timing branch): each term is
// independently gated on its own counter being positive, and the whole line
// is gated on either being nonzero. There is no "extend=" term — extend time
// folds into "write=" (see nodeStats.bufWriteTimeNs's doc comment).
func TestFormatIOTimingsLine(t *testing.T) {
	cases := []struct {
		name string
		s    nodeStats
		want string
	}{
		{"all zero", nodeStats{}, ""},
		{"read only", nodeStats{bufReadTimeNs: 2_500_000}, "I/O Timings: shared read=2.500"},
		{"write only", nodeStats{bufWriteTimeNs: 1_250_000}, "I/O Timings: shared write=1.250"},
		{"both", nodeStats{bufReadTimeNs: 500_000, bufWriteTimeNs: 750_000},
			"I/O Timings: shared read=0.500 write=0.750"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatIOTimingsLine(&c.s); got != c.want {
				t.Errorf("formatIOTimingsLine(%+v) = %q, want %q", c.s, got, c.want)
			}
		})
	}
}

// TestExplainIOTimingsOffByDefault confirms the "I/O Timings:" line stays
// absent when track_io_timing never accumulated any time (the default —
// matches upstream, where the times stay zero and has_shared_timing is
// false).
func TestExplainIOTimingsOffByDefault(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE eiot1 (data int)",
		"INSERT INTO eiot1 VALUES (1)",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, BUFFERS) SELECT data FROM eiot1")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "I/O Timings:") {
		t.Errorf("EXPLAIN (ANALYZE, BUFFERS) unexpectedly emitted an I/O Timings line with no accumulated time:\n%s", joined)
	}
}

// TestInstrumentedOpAccountsIOTime exercises the real
// instrumentedOp.accountBuffers diffing added for I/O Timings: adding
// wall-clock time to a Pool between a node's Open and Close — mirroring what
// a real track_io_timing-gated wait-event hook (OnPinDone/OnFlushDone/
// OnExtendDone) would do mid-execution — must roll into that node's
// bufReadTimeNs/bufWriteTimeNs, and formatIOTimingsLine must render it. This
// runs below the SQL layer (unlike TestExplainBuffersAnalyzeTextLine)
// because pre-seeding time via Pool.AddReadTimeNanos before a full EXPLAIN
// call would land inside instrumentedOp.Open's baseline snapshot and diff to
// zero — there is no hook to inject time mid-query from outside a single SQL
// round trip in this test harness.
func TestInstrumentedOpAccountsIOTime(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	stats := &nodeStats{}
	op := &instrumentedOp{inner: fakeIOTimingOp{}, plan: &planner.Values{}, stats: stats}

	if err := op.Open(&Context{Pool: pool}); err != nil {
		t.Fatal(err)
	}

	pool.AddReadTimeNanos(3_000_000)
	pool.AddWriteTimeNanos(1_000_000)

	if _, err := op.Next(); err != EOF {
		t.Fatalf("Next() err = %v, want EOF", err)
	}
	if err := op.Close(); err != nil {
		t.Fatal(err)
	}

	if stats.bufReadTimeNs != 3_000_000 {
		t.Errorf("bufReadTimeNs = %d, want 3000000", stats.bufReadTimeNs)
	}
	if stats.bufWriteTimeNs != 1_000_000 {
		t.Errorf("bufWriteTimeNs = %d, want 1000000", stats.bufWriteTimeNs)
	}
	if line := formatIOTimingsLine(stats); line != "I/O Timings: shared read=3.000 write=1.000" {
		t.Errorf("formatIOTimingsLine = %q, want 'I/O Timings: shared read=3.000 write=1.000'", line)
	}
}

// TestExplainIOTimingsJSONOmittedWithoutAccumulatedTime mirrors
// TestExplainBuffersJSONOmittedWithoutBuffersOption for the new I/O timing
// properties: with no accumulated time, "Shared I/O Read Time"/"Shared I/O
// Write Time" stay absent (goopg's nonzero gate — see formatIOTimingsLine's
// doc comment on the accepted deviation from upstream's GUC-only gate).
func TestExplainIOTimingsJSONOmittedWithoutAccumulatedTime(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx, "CREATE TABLE eiot3 (data int)")
	commitTx(t, ctx)
	beginTx(t, ctx)

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT data FROM eiot3")
	out := strings.Join(lines, "\n")
	if strings.Contains(out, "Shared I/O Read Time") || strings.Contains(out, "Shared I/O Write Time") {
		t.Errorf("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) unexpectedly reported I/O timing with no accumulated time:\n%s", out)
	}
}

// TestPlanToJSONWithStatsRendersIOTimingsWhenNonzero is the FORMAT
// JSON/XML/YAML sibling of TestInstrumentedOpAccountsIOTime: pins
// planToJSONWithStats's "Shared I/O Read Time"/"Shared I/O Write Time"
// properties directly against a synthetic stats table, avoiding the same
// pre-seeding-lands-in-the-baseline problem a full SQL round trip would hit.
func TestPlanToJSONWithStatsRendersIOTimingsWhenNonzero(t *testing.T) {
	n := &planner.Values{}
	stats := nodeStatsTable{n: {bufReadTimeNs: 2_000_000, bufWriteTimeNs: 500_000}}
	obj := planToJSONWithStats(n, parser.ExplainOptions{Buffers: true}, stats, true)
	if got, ok := obj["Shared I/O Read Time"].(float64); !ok || got != 2.0 {
		t.Errorf("Shared I/O Read Time = %v, want 2.0", obj["Shared I/O Read Time"])
	}
	if got, ok := obj["Shared I/O Write Time"].(float64); !ok || got != 0.5 {
		t.Errorf("Shared I/O Write Time = %v, want 0.5", obj["Shared I/O Write Time"])
	}
}

// TestPlanToJSONWithStatsRendersIOTimingsWhenTrackIOTimingOnEvenAtZero pins
// the fix for the GUC-vs-nonzero gating gap this loop closes (ledger row
// 2026-07-05, M0122-0003): upstream's non-text show_buffer_usage() gates
// "Shared I/O Read/Write Time" on the live track_io_timing GUC and prints
// it even when the accumulated value is exactly zero — unlike the
// nonzero-only gate the TEXT "I/O Timings:" line (formatIOTimingsLine)
// still uses.
func TestPlanToJSONWithStatsRendersIOTimingsWhenTrackIOTimingOnEvenAtZero(t *testing.T) {
	n := &planner.Values{}
	stats := nodeStatsTable{n: {}}
	obj := planToJSONWithStats(n, parser.ExplainOptions{Buffers: true}, stats, true)
	if got, ok := obj["Shared I/O Read Time"].(float64); !ok || got != 0 {
		t.Errorf("Shared I/O Read Time = %v, want 0 (present)", obj["Shared I/O Read Time"])
	}
	if got, ok := obj["Shared I/O Write Time"].(float64); !ok || got != 0 {
		t.Errorf("Shared I/O Write Time = %v, want 0 (present)", obj["Shared I/O Write Time"])
	}
}

// TestPlanToJSONWithStatsOmitsIOTimingsWhenTrackIOTimingOff is the mirror
// of the above: when the caller's track_io_timing snapshot is false, the
// properties stay absent even though the node accumulated a nonzero value
// (matches upstream: the accumulator itself never advances with the GUC
// off, so a nonzero reading with the GUC off shouldn't happen in practice,
// but the gate must key off the GUC, not the value, to match explain.c).
func TestPlanToJSONWithStatsOmitsIOTimingsWhenTrackIOTimingOff(t *testing.T) {
	n := &planner.Values{}
	stats := nodeStatsTable{n: {bufReadTimeNs: 2_000_000, bufWriteTimeNs: 500_000}}
	obj := planToJSONWithStats(n, parser.ExplainOptions{Buffers: true}, stats, false)
	if _, ok := obj["Shared I/O Read Time"]; ok {
		t.Errorf("Shared I/O Read Time present with trackIOTiming=false, want absent")
	}
	if _, ok := obj["Shared I/O Write Time"]; ok {
		t.Errorf("Shared I/O Write Time present with trackIOTiming=false, want absent")
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
