package executor

// M0127-P4.4 — the streaming LATERAL join (design leftdeep-joins/07 §4).
//
// The eager `openLateral` drained the outer side, re-ran the right subtree per
// outer row, drained THAT, and accumulated every surviving concatenation into
// `joinOp.rows` before `Next` returned its first tuple. Four properties have to
// hold for the streaming replacement, and each is a separate test below:
//
//   - NOTHING HAPPENS AT OPEN. The eager form did all the work there. Open must
//     now not advance the outer side and must not open the right subtree even
//     once.
//   - THE OUTER SIDE STREAMS ON DEMAND. After k output rows the outer side must
//     have been advanced only as far as those k rows required — this is what
//     makes `LIMIT 1` over a LATERAL join stop paying for the whole product.
//   - PER-OUTER RE-EXECUTION IS UNCHANGED. LATERAL's defining semantics: the
//     right subtree is re-opened once per outer tuple with that tuple in scope.
//     Streaming may not merge, cache or skip those executions.
//   - THE CORRELATION CONTEXT DOES NOT LEAK. This one is NEW with streaming and
//     is the reason the binding is installed per right-side call rather than
//     per iteration. The eager loop popped `ctx.OuterRows` before returning to
//     its caller; a streaming inner side yields to the PARENT between tuples,
//     so if the push were held across a Next the parent's own OuterColumnRef
//     (level=1) would resolve against THIS join's outer tuple. Same for
//     `ctx.CTERowCache`, which must be per-outer-tuple inside the lateral and
//     untouched outside it.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// lateralOuterOp is the outer side: a row source that counts how many tuples
// have actually been pulled out of it, which is how "streams on demand" is
// observed.
type lateralOuterOp struct {
	rows     []Row
	idx      int
	advanced int
	opens    int
}

func (o *lateralOuterOp) Open(*Context) error { o.opens++; o.idx = 0; return nil }
func (o *lateralOuterOp) Schema() optimizer.Schema {
	return optimizer.Schema{{Name: "k", Type: catalog.Type{Name: "int4"}}}
}
func (o *lateralOuterOp) Close() error { return nil }
func (o *lateralOuterOp) Next() (TupleSlot, error) { //nolint:ireturn
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	o.advanced++
	return asSlot(o.Schema(), r), nil
}

// lateralProbeOp is the right side: a correlated subtree stand-in. Every Open
// re-reads the outer tuple from ctx.OuterRows and derives its rows from it, so
// a missing or stale binding shows up as wrong output rather than as a silent
// pass. It also records the OuterRows depth it observed on every call, which is
// what the leak test inspects.
type lateralProbeOp struct {
	ctx *Context
	// fanout maps the outer key to the payloads this execution yields.
	fanout func(key int64) []int64

	rows []Row
	idx  int

	opens    int
	closes   int
	sawOuter []int64 // outer key observed at each Open
	sawDepth []int   // len(ctx.OuterRows) observed at each Open
}

func (o *lateralProbeOp) Schema() optimizer.Schema {
	return optimizer.Schema{{Name: "v", Type: catalog.Type{Name: "int4"}}}
}

func (o *lateralProbeOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.opens++
	o.sawDepth = append(o.sawDepth, len(ctx.OuterRows))
	var key int64 = -1
	if n := len(ctx.OuterRows); n > 0 {
		outer := ctx.OuterRows[n-1]
		if len(outer) > 0 && !outer[0].IsNull() {
			key = outer[0].Int
		}
	}
	o.sawOuter = append(o.sawOuter, key)
	o.rows = nil
	for _, v := range o.fanout(key) {
		o.rows = append(o.rows, Row{NewIntDatum(v)})
	}
	o.idx = 0
	return nil
}

func (o *lateralProbeOp) Next() (TupleSlot, error) { //nolint:ireturn
	// The binding must be live for every right-side call, not only Open.
	if n := len(o.ctx.OuterRows); n == 0 {
		return nil, &ExecError{Code: "XX000", Message: "lateral outer row not bound during Next"}
	}
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	r := o.rows[o.idx]
	o.idx++
	return asSlot(o.Schema(), r), nil
}

func (o *lateralProbeOp) Close() error { o.closes++; return nil }

// lateralPlan is a LATERAL join over a width-1 outer and a width-1 inner.
func lateralPlan(jt optimizer.JoinType, residual optimizer.Expr) *optimizer.Join {
	return &optimizer.Join{Type: jt, Lateral: true, Predicate: residual}
}

func lateralOuterRows(keys ...int64) []Row {
	out := make([]Row, 0, len(keys))
	for _, k := range keys {
		out = append(out, Row{NewIntDatum(k)})
	}
	return out
}

// drainLateral runs the join to completion and returns the output as
// "outer/inner" strings, with a NULL inner rendered as "outer/NULL".
func drainLateral(t *testing.T, o *joinOp) []string {
	t.Helper()
	var got []string
	for {
		slot, err := o.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		row := slotRow(slot)
		if len(row) != 2 {
			t.Fatalf("output row width %d, want 2: %v", len(row), row)
		}
		if row[1].IsNull() {
			got = append(got, formatLateralPair(row[0].Int, "NULL"))
			continue
		}
		got = append(got, formatLateralPair(row[0].Int, formatLateralInt(row[1].Int)))
	}
	return got
}

func formatLateralInt(v int64) string {
	if v < 0 {
		return "-" + formatLateralInt(-v)
	}
	if v < 10 {
		return string(rune('0' + v))
	}
	return formatLateralInt(v/10) + string(rune('0'+v%10))
}

func formatLateralPair(outer int64, inner string) string {
	return formatLateralInt(outer) + "/" + inner
}

// TestLateralJoinDoesNothingAtOpen — the eager form computed the whole answer
// in Open. The streaming form must not advance the outer side or open the
// right subtree until Next asks.
func TestLateralJoinDoesNothingAtOpen(t *testing.T) {
	left := &lateralOuterOp{rows: lateralOuterRows(1, 2, 3)}
	right := &lateralProbeOp{fanout: func(k int64) []int64 { return []int64{k * 10} }}
	o := newJoinOp(lateralPlan(optimizer.JoinTypeInner, nil), left, right)
	ctx := NewContext()
	if err := o.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if left.advanced != 0 {
		t.Fatalf("Open pulled %d outer tuple(s); it must pull none", left.advanced)
	}
	if right.opens != 0 {
		t.Fatalf("Open re-executed the right subtree %d time(s); it must execute none", right.opens)
	}
	if err := o.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestLateralJoinOuterStreamsOnDemand — with one inner row per outer row, the
// k-th Next must have advanced the outer side exactly k times. The eager form
// would report 4 (the whole outer side) after the first Next; that difference
// is what a LIMIT over a LATERAL join now stops paying for.
func TestLateralJoinOuterStreamsOnDemand(t *testing.T) {
	left := &lateralOuterOp{rows: lateralOuterRows(1, 2, 3, 4)}
	right := &lateralProbeOp{fanout: func(k int64) []int64 { return []int64{k * 10} }}
	o := newJoinOp(lateralPlan(optimizer.JoinTypeInner, nil), left, right)
	ctx := NewContext()
	if err := o.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for k := 1; k <= 4; k++ {
		if _, err := o.Next(); err != nil {
			t.Fatalf("Next #%d: %v", k, err)
		}
		if left.advanced != k {
			t.Fatalf("after %d output row(s) the outer side had advanced %d time(s), want %d",
				k, left.advanced, k)
		}
		if right.opens != k {
			t.Fatalf("after %d output row(s) the right subtree had run %d time(s), want %d",
				k, right.opens, k)
		}
	}
	if err := o.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestLateralJoinPerOuterReExecution — LATERAL's defining semantics survive the
// rewrite: one right-subtree execution per outer tuple, each seeing that tuple,
// each closed before the next begins.
func TestLateralJoinPerOuterReExecution(t *testing.T) {
	keys := []int64{7, 8, 9}
	left := &lateralOuterOp{rows: lateralOuterRows(keys...)}
	// A varying fanout proves the executions are genuinely independent: 0 rows,
	// 1 row, 2 rows.
	right := &lateralProbeOp{fanout: func(k int64) []int64 {
		switch k {
		case 7:
			return nil
		case 8:
			return []int64{80}
		default:
			return []int64{90, 91}
		}
	}}
	o := newJoinOp(lateralPlan(optimizer.JoinTypeInner, nil), left, right)
	ctx := NewContext()
	if err := o.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := drainLateral(t, o)
	want := []string{"8/80", "9/90", "9/91"}
	assertLateralRows(t, got, want)
	if right.opens != len(keys) {
		t.Fatalf("right subtree ran %d time(s), want one per outer tuple (%d)", right.opens, len(keys))
	}
	if right.closes != len(keys) {
		t.Fatalf("right subtree closed %d time(s), want %d — a leaked execution holds its resources",
			right.closes, len(keys))
	}
	for i, saw := range right.sawOuter {
		if saw != keys[i] {
			t.Fatalf("execution #%d saw outer key %d, want %d", i, saw, keys[i])
		}
	}
	if err := o.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestLateralLeftJoinNullExtends — a LEFT lateral null-extends an outer tuple
// whose right side produced nothing AND one whose rows were all rejected by the
// join predicate. The eager form conflated the two conditions
// (`len(rightRows) == 0 || !matched`); streaming tracks only `matched`, so both
// arms need covering.
func TestLateralLeftJoinNullExtends(t *testing.T) {
	// Predicate: inner value != 20. Outer key 1 → no rows at all; key 2 → one
	// row that the predicate rejects; key 3 → one row that survives.
	residual := &optimizer.BinaryOp{
		Op:    parser.OpNe,
		Left:  &optimizer.ColumnRef{Index: 1, Type: catalog.Type{Name: "int4"}},
		Right: &optimizer.IntegerConst{Value: 20},
	}
	left := &lateralOuterOp{rows: lateralOuterRows(1, 2, 3)}
	right := &lateralProbeOp{fanout: func(k int64) []int64 {
		if k == 1 {
			return nil
		}
		return []int64{k * 10}
	}}
	o := newJoinOp(lateralPlan(optimizer.JoinTypeLeft, residual), left, right)
	ctx := NewContext()
	if err := o.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	assertLateralRows(t, drainLateral(t, o), []string{"1/NULL", "2/NULL", "3/30"})
	if err := o.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The same shape as an INNER lateral drops both unmatched outer tuples.
	left2 := &lateralOuterOp{rows: lateralOuterRows(1, 2, 3)}
	right2 := &lateralProbeOp{fanout: right.fanout}
	o2 := newJoinOp(lateralPlan(optimizer.JoinTypeInner, residual), left2, right2)
	if err := o2.Open(NewContext()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	assertLateralRows(t, drainLateral(t, o2), []string{"3/30"})
	if err := o2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestLateralJoinDoesNotLeakCorrelationContext is the streaming-specific
// hazard. Between Next calls the join hands control back to its PARENT, and the
// parent's own OuterColumnRefs must not resolve against this join's outer
// tuple. So ctx.OuterRows must be back to the enclosing depth — and
// ctx.CTERowCache back to the enclosing cache — every time Next returns.
func TestLateralJoinDoesNotLeakCorrelationContext(t *testing.T) {
	left := &lateralOuterOp{rows: lateralOuterRows(1, 2, 3)}
	right := &lateralProbeOp{fanout: func(k int64) []int64 { return []int64{k * 10, k*10 + 1} }}
	o := newJoinOp(lateralPlan(optimizer.JoinTypeInner, nil), left, right)

	ctx := NewContext()
	// Simulate an enclosing correlated scope: one outer row already pushed, and
	// a CTE cache that belongs to it.
	enclosing := Row{NewIntDatum(999)}
	ctx.OuterRows = append(ctx.OuterRows, enclosing)
	ctx.CTERowCache = map[string][]Row{"enclosing": {enclosing}}
	enclosingCache := ctx.CTERowCache

	if err := o.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 0; ; i++ {
		_, err := o.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if len(ctx.OuterRows) != 1 || ctx.OuterRows[0][0].Int != 999 {
			t.Fatalf("after output row %d the enclosing OuterRows was %v; the lateral's binding leaked past Next",
				i, ctx.OuterRows)
		}
		if _, ok := ctx.CTERowCache["enclosing"]; !ok {
			t.Fatalf("after output row %d the enclosing CTERowCache was replaced by the lateral's", i)
		}
	}
	if len(ctx.OuterRows) != 1 {
		t.Fatalf("at EOF the enclosing OuterRows depth was %d, want 1", len(ctx.OuterRows))
	}
	if len(ctx.CTERowCache) != len(enclosingCache) {
		t.Fatalf("at EOF the enclosing CTERowCache had %d entr(ies), want %d",
			len(ctx.CTERowCache), len(enclosingCache))
	}
	// The right side, in turn, must always have seen depth 2: the enclosing row
	// plus its own. A depth of 1 means the binding was never installed; 3+ means
	// a push was not popped.
	for i, d := range right.sawDepth {
		if d != 2 {
			t.Fatalf("right execution #%d saw OuterRows depth %d, want 2", i, d)
		}
	}
	if err := o.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// runQueryFast executes sql through BuildFastIterator (the OpNode fast-iterator
// path the server uses for simple-protocol queries) and returns the result rows
// as independent copies. Distinct from runQuery, which uses the legacy Build
// path — only the fast path wraps a Join's children in opNodeOperator, and it
// is that wrapper (which implements lateralBindable unconditionally) that
// reproduces the M0134-0001 lateral-aggregate bug.
func runQueryFast(t *testing.T, ctx *Context, sql string) ([]Row, error) {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		return nil, err
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		return nil, err
	}
	it, err := BuildFastIterator(plan)
	if err != nil {
		return nil, err
	}
	if err := it.Open(ctx); err != nil {
		return nil, err
	}
	defer it.Close()
	var rows []Row
	for {
		slot, err := it.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if slot == nil {
			break
		}
		// it.dst's Cells are reused across Next calls; snapshot independently.
		rows = append(rows, append(Row(nil), slotRow(slot)...))
	}
	return rows, nil
}

// TestLateralAggregateOuterRef — M0134-0001 result-correctness regression.
//
// A CROSS-joined LATERAL aggregate whose aggregate expression references the
// outer row must return one row per (s1, s2) pair with sm == s1+s2
// (aggregates.out:735-760). On the server's BuildFast path the Join's right
// child is wrapped in opNodeOperator, which implements lateralBindable
// unconditionally — so a right child that is a HashAggregate over
// generate_series (NOT a bare SRF) set m.bindable, and bindOuter early-returned
// without pushing ctx.OuterRows. The aggregate's sum(s1+s2) then resolved the
// OuterColumnRef s1/level=1 against an empty ctx.OuterRows, raising
// `outer column ref s1/level=1 out of range (depth=0)`.
//
// The legacy Build path builds raw operator children (no opNodeOperator
// wrapper), so `right.(lateralBindable)` is false there and the general
// ctx.OuterRows path already works — this test MUST drive the query through
// BuildFastIterator to reproduce the bug.
func TestLateralAggregateOuterRef(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows, err := runQueryFast(t, ctx, `
		SELECT s1, s2, sm
		FROM generate_series(1, 3) s1,
		  LATERAL (SELECT s2, sum(s1 + s2) sm
		  FROM generate_series(1, 3) s2 GROUP BY s2) ss
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(rows) != 9 {
		t.Fatalf("row count = %d, want 9 (rows=%v)", len(rows), rows)
	}
	for i, r := range rows {
		if len(r) != 3 {
			t.Fatalf("row %d width = %d, want 3 (row=%v)", i, len(r), r)
		}
		s1, s2, sm := r[0].Int, r[1].Int, r[2].Int
		if sm != s1+s2 {
			t.Fatalf("row %d: sm=%d, want s1+s2=%d (row=%v)", i, sm, s1+s2, r)
		}
	}
}

func assertLateralRows(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("emitted %d row(s) %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}
