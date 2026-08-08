package executor

// Stage 7 (S2a) — operator reset/rewind prerequisites for the S2 rescan
// engine (design bundle docs/design/correlated-subquery-planning ch.04
// §4.2). Pins:
//
//   1. limitOp.Open resets its per-execution window (emitted/skipped/
//      ties state) so a retained subplan's re-Open restarts the limit
//      instead of returning EOF forever.
//   2. seqScanOp.Open on an already-open scan is a rewind: same mctx
//      arena reused (no leak), any leftover page pin from a partial
//      drain released, position back at block 0, identical results.
//   3. Sort deliberately has NO rewind path in this stage — Close()+
//      Open() is the rescan contract for Sort-rooted plans and must
//      round-trip after a partial drain without duplicated rows or
//      leaked spill files.
//   4. A Filter{IndexScan}-shaped tree (rescan-eligible via
//      planIsIndexScanBased since S6) returns identical results on a
//      double Open.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// resetFixture: one plain table (heap order = insertion order for this
// single-page data set) and one indexed table for the Filter{IndexScan}
// shape.
func resetFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, ddl := range []string{
		"CREATE TABLE reset_t (a int, b int)",
		"INSERT INTO reset_t VALUES (1, 10), (2, 20), (3, 30), (4, 40), (5, 50)",
		"CREATE TABLE reset_idx (a int, b int)",
		"CREATE INDEX reset_idx_a ON reset_idx (a)",
		"INSERT INTO reset_idx VALUES (1, 10), (2, 20), (2, -5), (3, 30)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", ddl, err)
		}
	}
	return ctx, cleanup
}

func buildFor(t *testing.T, ctx *Context, sql string) Operator {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("build %q: %v", sql, err)
	}
	return op
}

// drainAll materializes every remaining row as its String()-ed Row.
func drainAll(t *testing.T, ctx *Context, op Operator) []string {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	var out []string
	for {
		slot, err := op.Next()
		if err == EOF {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		row := slot.Materialize().Row()
		s := ""
		for i, d := range row {
			if i > 0 {
				s += "|"
			}
			s += datumKey(d)
		}
		out = append(out, s)
	}
}

func drainN(t *testing.T, ctx *Context, op Operator, n int) {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	for i := 0; i < n; i++ {
		if _, err := op.Next(); err != nil {
			t.Fatalf("Next %d: %v", i, err)
		}
	}
}

// findOp walks the built operator tree through the known single-child
// wrappers and returns the first operator for which match returns true.
func findOp(op Operator, match func(Operator) bool) Operator {
	for op != nil {
		if match(op) {
			return op
		}
		switch x := op.(type) {
		case *projectOp:
			op = x.child
		case *filterOp:
			op = x.child
		case *limitOp:
			op = x.child
		case *sortOp:
			op = x.child
		default:
			return nil
		}
	}
	return nil
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- 1. limitOp -----------------------------------------------------------

// TestLimitOpDoubleOpenResets: LIMIT/OFFSET over the raw heap (no Sort in
// the tree — Sort has no re-Open contract in this stage) must yield the
// same window on a second Open without an intervening Close.
func TestLimitOpDoubleOpenResets(t *testing.T) {
	ctx, cleanup := resetFixture(t)
	defer cleanup()

	op := buildFor(t, ctx, "SELECT a FROM reset_t LIMIT 2 OFFSET 1")
	if err := op.Open(ctx); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	first := drainAll(t, ctx, op)
	if len(first) != 2 {
		t.Fatalf("first pass: got %v, want 2 rows", first)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	second := drainAll(t, ctx, op)
	if !eqStrings(first, second) {
		t.Fatalf("re-Open changed the limit window: first %v, second %v", first, second)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// stubRowsOp is a minimal re-openable child for isolating limitOp: Open
// rewinds, Next yields the fixed rows.
type stubRowsOp struct {
	schema planner.Schema
	rows   []Row
	i      int
}

func (s *stubRowsOp) Open(*Context) error { s.i = 0; return nil }
func (s *stubRowsOp) Next() (TupleSlot, error) { //nolint:ireturn
	if s.i >= len(s.rows) {
		return nil, EOF
	}
	r := s.rows[s.i]
	s.i++
	return SlotFromRow(s.schema, r), nil
}
func (s *stubRowsOp) Close() error           { return nil }
func (s *stubRowsOp) Schema() planner.Schema { return s.schema }

// TestLimitOpWithTiesDoubleOpenResets pins the ties-state reset
// (inTiesPhase/tieKeyVals) in isolation, over a stub child so the test
// does not depend on Sort's (deliberately absent) re-Open contract.
// Rows are pre-ordered by k; FETCH FIRST 1 ROWS WITH TIES over k must
// emit the two k=1 rows — on both passes.
func TestLimitOpWithTiesDoubleOpenResets(t *testing.T) {
	ctx, cleanup := resetFixture(t)
	defer cleanup()

	schema := planner.Schema{{Name: "k", Type: catalog.Type{Name: "int4"}}}
	child := &stubRowsOp{schema: schema, rows: []Row{
		{NewIntDatum(1)}, {NewIntDatum(1)}, {NewIntDatum(2)},
	}}
	lim := newLimitOp(&planner.Limit{
		Limit:    &planner.IntegerConst{Value: 1},
		WithTies: true,
		TiesKeys: []planner.Expr{&planner.ColumnRef{Name: "k", Index: 0}},
	}, child)

	for pass := 1; pass <= 2; pass++ {
		if err := lim.Open(ctx); err != nil {
			t.Fatalf("pass %d Open: %v", pass, err)
		}
		got := drainAll(t, ctx, lim)
		if len(got) != 2 {
			t.Fatalf("pass %d: got %v, want the 2 tied k=1 rows", pass, got)
		}
	}
	if err := lim.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// --- 2. seqScanOp ---------------------------------------------------------

// TestSeqScanDoubleOpenRewinds: a second Open on the same Context must
// reuse the held mctx arena (no re-Acquire — the honest leak observable,
// since mctx exposes no global counters), rewind to block 0, and produce
// identical results.
func TestSeqScanDoubleOpenRewinds(t *testing.T) {
	ctx, cleanup := resetFixture(t)
	defer cleanup()

	op := buildFor(t, ctx, "SELECT a, b FROM reset_t")
	scan, _ := findOp(op, func(o Operator) bool { _, ok := o.(*seqScanOp); return ok }).(*seqScanOp)
	if scan == nil {
		t.Fatalf("no seqScanOp found in the built tree")
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	sctx1 := scan.sctx
	if sctx1 == nil {
		t.Fatalf("first Open did not acquire an mctx arena")
	}
	first := drainAll(t, ctx, op)
	if len(first) != 5 {
		t.Fatalf("first pass: got %d rows, want 5", len(first))
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if scan.sctx != sctx1 {
		t.Fatalf("re-Open re-acquired the mctx arena instead of reusing it (leak)")
	}
	if scan.curBlock != 0 {
		t.Fatalf("re-Open did not rewind: curBlock=%d", scan.curBlock)
	}
	second := drainAll(t, ctx, op)
	if !eqStrings(first, second) {
		t.Fatalf("rewound scan diverged: first %v, second %v", first, second)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if scan.sctx != nil {
		t.Fatalf("Close did not release the mctx arena")
	}
}

// TestSeqScanPartialDrainReopen: a re-Open after a partial drain must
// release the leftover page pin (the state Close would have dropped)
// and still deliver the full row set.
func TestSeqScanPartialDrainReopen(t *testing.T) {
	ctx, cleanup := resetFixture(t)
	defer cleanup()

	op := buildFor(t, ctx, "SELECT a, b FROM reset_t")
	scan, _ := findOp(op, func(o Operator) bool { _, ok := o.(*seqScanOp); return ok }).(*seqScanOp)
	if scan == nil {
		t.Fatalf("no seqScanOp found in the built tree")
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	drainN(t, ctx, op, 2) // leaves the scan mid-page, pin held
	if err := op.Open(ctx); err != nil {
		t.Fatalf("re-Open after partial drain: %v", err)
	}
	if scan.pinned != nil || scan.activePage != nil {
		t.Fatalf("re-Open left a stale page pin from the partial drain")
	}
	got := drainAll(t, ctx, op)
	if len(got) != 5 {
		t.Fatalf("post-rewind drain: got %d rows, want 5", len(got))
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// --- 3. Sort: Close()+Open() round-trip (the Stage-9 rescan contract) -----

func TestSortCloseOpenRoundTripAfterPartialDrain(t *testing.T) {
	ctx, cleanup := resetFixture(t)
	defer cleanup()

	op := buildFor(t, ctx, "SELECT a FROM reset_t ORDER BY a DESC")
	srt, _ := findOp(op, func(o Operator) bool { _, ok := o.(*sortOp); return ok }).(*sortOp)
	if srt == nil {
		t.Fatalf("no sortOp found in the built tree")
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	drainN(t, ctx, op, 1) // the EXISTS maxDrain=1 shape
	if err := op.Close(); err != nil {
		t.Fatalf("Close after partial drain: %v", err)
	}
	if len(srt.spillFiles) != 0 {
		t.Fatalf("Close left %d spill files behind", len(srt.spillFiles))
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	got := drainAll(t, ctx, op)
	want := make([]string, 0, 5)
	for _, n := range []int64{5, 4, 3, 2, 1} {
		want = append(want, datumKey(NewIntDatum(n)))
	}
	if !eqStrings(got, want) {
		t.Fatalf("Close+Open round-trip broke Sort: got %v, want %v", got, want)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("final Close: %v", err)
	}
	if len(srt.spillFiles) != 0 {
		t.Fatalf("final Close left %d spill files behind", len(srt.spillFiles))
	}
}

// --- 4. Filter{IndexScan} double Open (the S6 rescan-eligible shape) ------

func TestFilterIndexScanDoubleOpen(t *testing.T) {
	ctx, cleanup := resetFixture(t)
	defer cleanup()

	op := buildFor(t, ctx, "SELECT a, b FROM reset_idx WHERE a = 2 AND b > 0")
	if idx := findOp(op, func(o Operator) bool { _, ok := o.(*indexScanOp); return ok }); idx == nil {
		t.Skipf("planner did not choose an IndexScan for the probe shape — nothing to pin here")
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	first := drainAll(t, ctx, op)
	if len(first) != 1 {
		t.Fatalf("first pass: got %v, want exactly the (2,20) row", first)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	second := drainAll(t, ctx, op)
	if !eqStrings(first, second) {
		t.Fatalf("double Open diverged: first %v, second %v", first, second)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
