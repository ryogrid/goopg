package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// --- fixtures --------------------------------------------------------

// cipScan builds a CTE reference wired the way planScanRangeVar wires
// one for the M0125-0035 CTE-body pass: the scan carries a back-pointer
// to its plannedCTE, whose refs/inlineEligible drive the gate. The body
// is a bare two-column SeqScan — the aggregate crossing is pinned by
// the end-to-end tests below, this shape pins the plumbing.
func cipScan(refs int, eligible bool) (*CTEScan, *SeqScan) {
	body := &SeqScan{Table: &catalog.Table{Name: "t"}, schema: Schema{ijCol("y"), ijCol("cnt")}}
	ce := &plannedCTE{
		name:           "s",
		body:           body,
		schema:         Schema{ijCol("y"), ijCol("cnt")},
		refs:           refs,
		inlineEligible: eligible,
	}
	return &CTEScan{Name: "s", Alias: "s", Child: body, schema: ce.schema, cte: ce}, body
}

// cipFindCTEScan returns the first CTE reference in the plan.
func cipFindCTEScan(n Node) *CTEScan {
	if s, ok := n.(*CTEScan); ok {
		return s
	}
	for _, c := range planChildren(n) {
		if s := cipFindCTEScan(c); s != nil {
			return s
		}
	}
	return nil
}

// cipBodyLeafFilters counts Filter-over-SeqScan pairs inside a CTE
// body — the terminal shape pushConjunctIntoSubtree leaves behind when
// a conjunct completes the descent.
func cipBodyLeafFilters(n Node) []*Filter {
	var out []*Filter
	if f, ok := n.(*Filter); ok {
		if _, leaf := f.Child.(*SeqScan); leaf {
			out = append(out, f)
		}
	}
	for _, c := range planChildren(n) {
		out = append(out, cipBodyLeafFilters(c)...)
	}
	return out
}

// --- the positive pin ------------------------------------------------

// TestCTEBodyPushSingleRef pins the M0125-0035 CTE-body arm on the
// constructed shape: a restriction on a single-reference CTE's output
// crosses the reference into the body and lands on the body's leaf,
// while — property 2 of the join pass — the residual Filter keeps its
// own copy, so a decline anywhere degrades to post-CTE filtering, never
// to a dropped qual.
func TestCTEBodyPushSingleRef(t *testing.T) {
	scan, body := cipScan(1, true)
	f := &Filter{Child: scan, Predicate: ijEq(0, "y", 1998)}
	pushQualsThroughSingleRefCTEs(f)

	bf, ok := scan.Child.(*Filter)
	if !ok {
		t.Fatalf("CTE body root is %T, want *Filter carrying the pushed qual", scan.Child)
	}
	if bf.Child != Node(body) {
		t.Errorf("pushed Filter sits above %T, want the body SeqScan", bf.Child)
	}
	if !bf.LeafLocal {
		t.Errorf("pushed Filter over a base-relation leaf must set LeafLocal (M0077-0001 convention)")
	}
	if got := columnRefIndexes(bf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("pushed predicate refs = %v, want [0]", got)
	}
	if got := len(splitAnd(f.Predicate)); got != 1 {
		t.Errorf("residual Filter has %d conjuncts, want 1 — the pass must DUPLICATE, not move", got)
	}
	if bf.Predicate == f.Predicate {
		t.Errorf("pushed predicate aliases the residual conjunct; the remap must produce a fresh tree")
	}

	// Idempotence: the plan is re-walked once per enclosing scope, and
	// the second walk must find the conjunct already present (exprEqual
	// guard) rather than stack a duplicate.
	pushQualsThroughSingleRefCTEs(f)
	if got := len(splitAnd(bf.Predicate)); got != 1 {
		t.Errorf("after a second walk the body Filter has %d conjuncts, want 1 (idempotence)", got)
	}
}

// --- the declines ----------------------------------------------------

// TestCTEBodyPushDeclines pins the gate's three legs. A multiply
// referenced CTE shares ONE body Node between references AND one
// executor-side CTERowCache entry, so pushing any per-reference qual in
// would filter every other reference (TPC-DS Q31's 6×-referenced `ws3`
// is the real-plan control); a body that is not a plain non-recursive
// SELECT (inlineEligible false: WITH RECURSIVE, DML bodies) never
// admits a qual; and a CTEScan built outside preplanWithClause carries
// no back-pointer at all.
func TestCTEBodyPushDeclines(t *testing.T) {
	cases := []struct {
		name string
		scan *CTEScan
	}{
		{"refs=2", func() *CTEScan { s, _ := cipScan(2, true); return s }()},
		{"not-inline-eligible", func() *CTEScan { s, _ := cipScan(1, false); return s }()},
		{"nil-backpointer", func() *CTEScan { s, _ := cipScan(1, true); s.cte = nil; return s }()},
	}
	for _, tc := range cases {
		f := &Filter{Child: tc.scan, Predicate: ijEq(0, "y", 1998)}
		pushQualsThroughSingleRefCTEs(f)
		if _, pushed := tc.scan.Child.(*Filter); pushed {
			t.Errorf("%s: qual crossed into the CTE body; the gate must decline", tc.name)
		}
	}
}

// --- EC constant derivation (the other half of the Q78 fix) ----------

// cipLeftJoin wires Filter(preds) -> Join(LEFT, l, r) whose ON clause
// is `l[0] = r[0]` (merged index leftWidth), the reduced Q78 spine:
// ss LEFT JOIN ws ON ws_sold_year = ss_sold_year.
func cipLeftJoin(l, r Node, onType string, preds ...Expr) (*Filter, *Join) {
	lw := len(l.Output())
	j := &Join{
		Type: JoinTypeLeft,
		Left: l,
		Right: r,
		Predicate: &BinaryOp{
			Op:    parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "y", Type: catalog.Type{Name: "int4"}},
			Right: &ColumnRef{Index: lw, Name: "y", Type: catalog.Type{Name: onType}},
		},
		schema: append(append(Schema{}, l.Output()...), r.Output()...),
	}
	return &Filter{Child: j, Predicate: combineAnd(preds)}, j
}

// TestJoinEqualityConstDerivedOntoNullableSide pins the equivclass.c
// analogue: `y = 1998` on the PRESERVED side of a LEFT join whose ON
// clause equates it with the nullable side's `y` seeds a DERIVED
// `y = 1998` onto the nullable input — the one shape where touching a
// nullable side needs no nullingrels model, because a nullable row
// failing the derived constant can never match (matching requires
// y = y' = const) and the no-match null-extension it leaves behind is
// what the join produced for it anyway.
func TestJoinEqualityConstDerivedOntoNullableSide(t *testing.T) {
	ss, ws := ijCTEScan("ss"), ijCTEScan("ws")
	f, j := cipLeftJoin(ss, ws, "int4", ijEq(0, "y", 1998))
	pushInnerJoinInputQuals(f)

	lf, ok := j.Left.(*Filter)
	if !ok {
		t.Fatalf("Join.Left is %T, want *Filter with the original preserved-side qual", j.Left)
	}
	if got := columnRefIndexes(lf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("preserved-side predicate refs = %v, want [0]", got)
	}
	rf, ok := j.Right.(*Filter)
	if !ok {
		t.Fatalf("Join.Right is %T, want *Filter with the DERIVED nullable-side qual", j.Right)
	}
	// Derived copy re-based into the right input's own space.
	if got := columnRefIndexes(rf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("derived predicate refs = %v, want [0] (shifted by -leftWidth)", got)
	}
	// Idempotence across a re-walk: the derived conjunct must join the
	// existing Filter through the exprEqual guard, not stack.
	pushInnerJoinInputQuals(f)
	if got := len(splitAnd(rf.Predicate)); got != 1 {
		t.Errorf("after a second walk the derived Filter has %d conjuncts, want 1", got)
	}
}

// TestJoinEqualityConstDerivationDeclines pins the fail-closed bounds:
// a type-name mismatch between the equated columns declines (no
// opfamily model, so cross-type transitivity is unproven), and a
// non-constant comparison never derives.
func TestJoinEqualityConstDerivationDeclines(t *testing.T) {
	// int4 = int8 equality: original still pushes, derivation declines.
	ss, ws := ijCTEScan("ss"), ijCTEScan("ws")
	f, j := cipLeftJoin(ss, ws, "int8", ijEq(0, "y", 1998))
	pushInnerJoinInputQuals(f)
	if _, ok := j.Right.(*Filter); ok {
		t.Errorf("cross-type join equality derived a constant; type mismatch must decline")
	}
	// col = col comparison (not a constant): nothing to derive.
	ss, ws = ijCTEScan("ss"), ijCTEScan("ws")
	f, j = cipLeftJoin(ss, ws, "int4", &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: 0, Name: "y", Type: catalog.Type{Name: "int4"}},
		Right: &ColumnRef{Index: 1, Name: "cnt", Type: catalog.Type{Name: "int4"}},
	})
	pushInnerJoinInputQuals(f)
	if _, ok := j.Right.(*Filter); ok {
		t.Errorf("non-constant comparison derived onto the nullable side")
	}
}

// TestReselectDegenerateHashKey pins the follow-through: once qual
// placement pins `y = 1998` on both inputs of a hash join keyed on y,
// the whole build side shares one bucket and every probe walks it end
// to end (Q78's top spine, 245k × 30k). The pass must move the key to
// the remaining non-pinned pair; with no constant filters in place it
// must leave the planner's original choice alone.
func TestReselectDegenerateHashKey(t *testing.T) {
	mk := func(pinned bool) *Join {
		ss, ws := ijCTEScan("ss"), ijCTEScan("ws")
		var l, r Node = ss, ws
		if pinned {
			l = &Filter{Child: ss, Predicate: ijEq(0, "y", 1998)}
			r = &Filter{Child: ws, Predicate: ijEq(0, "y", 1998)}
		}
		col := func(idx int, name string) *ColumnRef {
			return &ColumnRef{Index: idx, Name: name, Type: catalog.Type{Name: "int4"}}
		}
		return &Join{
			Type: JoinTypeLeft,
			Algo: JoinAlgoHash,
			Left: l,
			Right: r,
			Predicate: combineAnd([]Expr{
				&BinaryOp{Op: parser.OpEq, Left: col(0, "y"), Right: col(2, "y")},
				&BinaryOp{Op: parser.OpEq, Left: col(1, "cnt"), Right: col(3, "cnt")},
			}),
			LeftKey:  col(0, "y"),
			RightKey: col(2, "y"),
			schema:   append(append(Schema{}, ss.Output()...), ws.Output()...),
		}
	}

	j := mk(true)
	reselectDegenerateHashKeys(j)
	lk, _ := j.LeftKey.(*ColumnRef)
	rk, _ := j.RightKey.(*ColumnRef)
	if lk == nil || rk == nil || lk.Index != 1 || rk.Index != 3 {
		t.Errorf("degenerate y-key not moved to the cnt pair: LeftKey=%v RightKey=%v", j.LeftKey, j.RightKey)
	}

	j = mk(false)
	reselectDegenerateHashKeys(j)
	lk, _ = j.LeftKey.(*ColumnRef)
	if lk == nil || lk.Index != 0 {
		t.Errorf("non-degenerate key was moved: LeftKey=%v", j.LeftKey)
	}
}

// --- end-to-end through Plan() ---------------------------------------

// cipCatalog builds the reduced Q78 relation.
func cipCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "sales"}, []catalog.Column{
		{Name: "y", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "cnt", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCTEBodyPushEndToEndGroupKey runs the reduced TPC-DS Q78 shape
// through the real pipeline: the restriction names a GROUP BY key of a
// single-reference aggregating CTE, so it must cross the reference,
// cross the aggregate (restricting a group key removes whole groups
// and leaves every surviving group's membership untouched — PG
// qual_is_pushdown_safe), and reach the body's base scan. This is the
// placement pin too: plannedCTE.refs is final only at Plan()'s tail.
func TestCTEBodyPushEndToEndGroupKey(t *testing.T) {
	sql := `WITH s AS (SELECT y, sum(cnt) AS total FROM sales GROUP BY y)
	        SELECT total FROM s WHERE y = 1998`
	plan, err := Plan(parseOne(t, sql), cipCatalog(t))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	scan := cipFindCTEScan(plan)
	if scan == nil {
		t.Fatalf("no CTE reference in plan:\n%s", plan)
	}
	lf := cipBodyLeafFilters(scan.Child)
	if len(lf) != 1 {
		t.Fatalf("body has %d leaf Filters, want 1 (the pushed group-key qual):\n%s", len(lf), plan)
	}
	if got := columnRefIndexes(lf[0].Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("pushed body predicate refs = %v, want [0] (sales.y)", got)
	}
}

// TestCTEBodyPushEndToEndDeclinesMultiRef is the Q31 control: the same
// CTE referenced twice must keep both per-reference restrictions on the
// reference side, and the shared body must stay clean.
func TestCTEBodyPushEndToEndDeclinesMultiRef(t *testing.T) {
	sql := `WITH s AS (SELECT y, sum(cnt) AS total FROM sales GROUP BY y)
	        SELECT a.total FROM s a, s b
	        WHERE a.y = b.y AND a.y = 1998 AND b.y = 1998`
	plan, err := Plan(parseOne(t, sql), cipCatalog(t))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	scan := cipFindCTEScan(plan)
	if scan == nil {
		t.Fatalf("no CTE reference in plan:\n%s", plan)
	}
	if lf := cipBodyLeafFilters(scan.Child); len(lf) != 0 {
		t.Errorf("shared body of a 2-reference CTE carries %d pushed Filter(s); refs>1 must decline:\n%s", len(lf), plan)
	}
}

// TestCTEBodyPushEndToEndDeclinesAggOutput pins the safety boundary
// inside the body: a restriction on the AGGREGATE RESULT column cannot
// cross below the aggregate (it would remove member rows and change
// surviving groups' values, not just remove groups).
func TestCTEBodyPushEndToEndDeclinesAggOutput(t *testing.T) {
	sql := `WITH s AS (SELECT y, sum(cnt) AS total FROM sales GROUP BY y)
	        SELECT y FROM s WHERE total = 5`
	plan, err := Plan(parseOne(t, sql), cipCatalog(t))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	scan := cipFindCTEScan(plan)
	if scan == nil {
		t.Fatalf("no CTE reference in plan:\n%s", plan)
	}
	if lf := cipBodyLeafFilters(scan.Child); len(lf) != 0 {
		t.Errorf("aggregate-output qual crossed below the aggregate (%d body Filter(s)):\n%s", len(lf), plan)
	}
}
