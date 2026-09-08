package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// --- shared fixtures -------------------------------------------------

func ijCol(name string) SchemaColumn {
	return SchemaColumn{Name: name, Type: catalog.Type{Name: "int4"}}
}

// ijCTEScan builds a two-column CTE reference [y, cnt] — the reduced
// shape of TPC-DS Q75's `all_sales` consumer.
func ijCTEScan(name string) *CTEScan {
	return &CTEScan{
		Name:   "s",
		Alias:  name,
		Child:  &SeqScan{Table: &catalog.Table{Name: "t"}, schema: Schema{ijCol("y"), ijCol("cnt")}},
		schema: Schema{ijCol("y"), ijCol("cnt")},
	}
}

// ijEq builds `<col at idx named name> = <lit>`.
func ijEq(idx int, name string, lit int64) Expr {
	return &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: idx, Name: name, Type: catalog.Type{Name: "int4"}},
		Right: &IntegerConst{Value: lit},
	}
}

// ijFilterOverInnerJoin wires Filter(preds) -> Join(INNER, left, right).
func ijFilterOverInnerJoin(left, right Node, typ JoinType, preds ...Expr) (*Filter, *Join) {
	j := &Join{
		Type:   typ,
		Algo:   JoinAlgoHash,
		Left:   left,
		Right:  right,
		schema: append(append(Schema{}, left.Output()...), right.Output()...),
	}
	return &Filter{Child: j, Predicate: combineAnd(preds)}, j
}

// --- the positive pin ------------------------------------------------

// TestInnerJoinSingleSideQualsPushedOntoCTEInputs pins M0125-0004's
// primary fix on the reduced TPC-DS Q75 shape: an INNER join of a CTE to
// itself where the residual Filter carries one restriction per side.
//
// Both restrictions must reach their own input, and — property 2 — must
// ALSO remain in the residual Filter. The duplication is what makes the
// transformation idempotent on the result set while changing only the
// error behaviour: Q75's `all_sales` legitimately contains a
// `sales_cnt = 0` group at d_year=2003, and goopg used to evaluate the
// side-mixed division residual on it before the d_year equalities above
// the join could exclude the pair, raising `division by zero` where PG —
// which attaches single-relation quals to baserestrictinfo — never sees
// the zero.
//
// The right-hand conjunct also pins the index SHIFT: it is written at
// index 2 (merged coordinate space) and must land at index 0 in the
// right input's own space.
func TestInnerJoinSingleSideQualsPushedOntoCTEInputs(t *testing.T) {
	curr, prev := ijCTEScan("curr_yr"), ijCTEScan("prev_yr")
	// Merged layout: [curr.y=0, curr.cnt=1, prev.y=2, prev.cnt=3].
	f, j := ijFilterOverInnerJoin(curr, prev, JoinTypeInner,
		ijEq(0, "y", 2002),
		ijEq(2, "y", 2001),
	)
	pushInnerJoinInputQuals(f)

	lf, ok := j.Left.(*Filter)
	if !ok {
		t.Fatalf("Join.Left is %T, want *Filter carrying the pushed curr-side qual", j.Left)
	}
	if got := columnRefIndexes(lf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("pushed left predicate refs = %v, want [0]", got)
	}
	rf, ok := j.Right.(*Filter)
	if !ok {
		t.Fatalf("Join.Right is %T, want *Filter carrying the pushed prev-side qual", j.Right)
	}
	// Shifted from the merged index 2 into the right input's own space.
	if got := columnRefIndexes(rf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("pushed right predicate refs = %v, want [0] (shifted by -leftWidth)", got)
	}
	// Property 2: the residual Filter still carries BOTH conjuncts.
	if got := len(splitAnd(f.Predicate)); got != 2 {
		t.Errorf("residual Filter has %d conjuncts, want 2 — the pass must DUPLICATE, not move", got)
	}
	// The pushed copies must be distinct nodes: mutating one must not be
	// visible through the residual, or a later remap would double-shift.
	if lf.Predicate == splitAnd(f.Predicate)[0] {
		t.Errorf("pushed left predicate aliases the residual conjunct; cloneExprRefs must deep-copy")
	}
}

// TestInnerJoinQualPushEndToEndCTESelfJoin runs the same shape through
// the real planner pipeline, so the pass's PLACEMENT (after
// remapWithBindings / the last applyJoinTreePosMap) is pinned too, not
// just its logic.
func TestInnerJoinQualPushEndToEndCTESelfJoin(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "sales"}, []catalog.Column{
		{Name: "y", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "g", Type: catalog.Type{Name: "int4"}, Ordinal: 1},
		{Name: "cnt", Type: catalog.Type{Name: "int4"}, Ordinal: 2},
	}); err != nil {
		t.Fatal(err)
	}
	sql := `WITH s AS (SELECT y, g, sum(cnt) AS cnt FROM sales GROUP BY y, g)
	        SELECT a.g FROM s a, s b
	        WHERE a.g = b.g AND a.y = 2002 AND b.y = 2001 AND a.cnt / b.cnt < 0.9`
	plan, err := Plan(parseOne(t, sql), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	j := findInnerJoinOverCTEScans(plan)
	if j == nil {
		t.Fatalf("expected an INNER Join over two CTE scans:\n%s", plan)
	}
	if _, ok := j.Left.(*Filter); !ok {
		t.Errorf("Join.Left is %T, want *Filter with the pushed a.y qual:\n%s", j.Left, plan)
	}
	if _, ok := j.Right.(*Filter); !ok {
		t.Errorf("Join.Right is %T, want *Filter with the pushed b.y qual:\n%s", j.Right, plan)
	}
}

// --- the declines ----------------------------------------------------

// TestInnerJoinQualPushDeclinesOnUnpreservedJoin pins property 4 as
// M0125-0035 arm (a) restated it. The pin formerly read
// "DeclinesOnOuterJoin" and covered LEFT and RIGHT as well, on the
// reasoning that "goopg has no nullingrels model … so safety can only be
// expressed by declining on every non-INNER join". Its own comment
// conceded the qual it used "sits on the PRESERVED side of a LEFT JOIN,
// where pushing would in fact be safe".
//
// That conservatism cost real work — see
// TestInnerJoinQualPushReachesPreservedOuterSide — and the nullingrels
// model it was waiting for is not needed for a preserved side. What
// still declines, and is pinned here:
//
//   - FULL, where NEITHER input is preserved;
//   - SEMI / ANTI, whose Output() is Left's layout alone, so the merged
//     coordinate arithmetic does not describe the Filter's ColumnRefs;
//   - the NULLABLE side of LEFT / RIGHT, which is the case that really
//     does need nullingrels.
func TestInnerJoinQualPushDeclinesOnUnpreservedJoin(t *testing.T) {
	for _, typ := range []JoinType{JoinTypeFull, JoinTypeSemi, JoinTypeAnti} {
		curr, prev := ijCTEScan("curr_yr"), ijCTEScan("prev_yr")
		f, j := ijFilterOverInnerJoin(curr, prev, typ, ijEq(0, "y", 2002))
		pushInnerJoinInputQuals(f)
		if _, ok := j.Left.(*Filter); ok {
			t.Errorf("join type %v: qual was pushed onto Join.Left; neither input of this join kind is preserved", typ)
		}
	}
	// The nullable side of an outer join: a restriction there would
	// delete rows the join would otherwise have null-extended and kept.
	// Merged layout is [curr.y=0, curr.cnt=1, prev.y=2, prev.cnt=3], so
	// index 2 is the right (nullable, under LEFT) input.
	curr, prev := ijCTEScan("curr_yr"), ijCTEScan("prev_yr")
	f, j := ijFilterOverInnerJoin(curr, prev, JoinTypeLeft, ijEq(2, "y", 2001))
	pushInnerJoinInputQuals(f)
	if _, ok := j.Right.(*Filter); ok {
		t.Errorf("LEFT JOIN: qual was pushed onto the NULLABLE Join.Right")
	}
	curr, prev = ijCTEScan("curr_yr"), ijCTEScan("prev_yr")
	f, j = ijFilterOverInnerJoin(curr, prev, JoinTypeRight, ijEq(0, "y", 2002))
	pushInnerJoinInputQuals(f)
	if _, ok := j.Left.(*Filter); ok {
		t.Errorf("RIGHT JOIN: qual was pushed onto the NULLABLE Join.Left")
	}
}

// TestInnerJoinQualPushReachesPreservedOuterSide is the inversion: the
// preserved input of an outer join DOES receive the restriction.
//
// Why it is sound without a nullingrels model: every preserved-side row
// reaches the join output at least once, matched or null-extended, and
// the conjunct does not mention the other side — so discarding a
// preserved row before the join discards exactly the output rows the
// residual Filter above would have discarded.
//
// TPC-DS Q78 is the witness for why it matters: `ss_sold_year = 1998`
// sits above two stacked `Hash Left Join` whose preserved spine leads
// to the `ss` channel, and the pass declined at the first one.
func TestInnerJoinQualPushReachesPreservedOuterSide(t *testing.T) {
	curr, prev := ijCTEScan("curr_yr"), ijCTEScan("prev_yr")
	f, j := ijFilterOverInnerJoin(curr, prev, JoinTypeLeft, ijEq(0, "y", 2002))
	pushInnerJoinInputQuals(f)
	lf, ok := j.Left.(*Filter)
	if !ok {
		t.Fatalf("LEFT JOIN: Join.Left is %T, want *Filter with the pushed preserved-side qual", j.Left)
	}
	if got := columnRefIndexes(lf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("pushed predicate refs = %v, want [0]", got)
	}
	// Property 2 still holds across the widening. NOTE: this fixture is
	// UNATTRIBUTED — ijCol leaves SourceTableIdx at 0 — so qualSrcRelSet
	// declines, no proof exists, and the pass copies. The attributed
	// version of this same LEFT-preserved shape MOVES; that is pinned by
	// TestMoveAcrossPreservedLeftLink. Do not "fix" the fixture here
	// without moving this assertion with it.
	if got := len(splitAnd(f.Predicate)); got != 1 {
		t.Errorf("residual Filter has %d conjuncts, want 1 — unattributed, so copy not move", got)
	}
	// Mirror image for RIGHT: index 2 is the preserved (right) input.
	curr, prev = ijCTEScan("curr_yr"), ijCTEScan("prev_yr")
	f, j = ijFilterOverInnerJoin(curr, prev, JoinTypeRight, ijEq(2, "y", 2001))
	pushInnerJoinInputQuals(f)
	rf, ok := j.Right.(*Filter)
	if !ok {
		t.Fatalf("RIGHT JOIN: Join.Right is %T, want *Filter with the pushed preserved-side qual", j.Right)
	}
	if got := columnRefIndexes(rf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("pushed predicate refs = %v, want [0] (shifted by -leftWidth)", got)
	}
}

// TestInnerJoinQualPushDescendsJoinSpine pins the other half of arm (a):
// a restriction is carried to the DEEPEST node that can hold it, not
// just to the residual join's immediate input.
//
// PG's distribute_restrictinfo_to_rels files a single-relation
// restriction on that relation's baserestrictinfo regardless of how deep
// the join tree above it is; stopping at the immediate input meant a
// conjunct only ever reached a leaf that happened to be a direct child
// of the join carrying the residual Filter, which is not the shape
// TPC-DS builds. Q78's spine — `Filter -> Join(LEFT) -> Join(LEFT) ->
// CTE Scan on ss` — is reproduced here, including the index SHIFT
// applying once per level.
func TestInnerJoinQualPushDescendsJoinSpine(t *testing.T) {
	ss, ws, cs := ijCTEScan("ss"), ijCTEScan("ws"), ijCTEScan("cs")
	// Inner spine: ss (0,1) LEFT JOIN ws (2,3).
	inner := &Join{
		Type:   JoinTypeLeft,
		Left:   ss,
		Right:  ws,
		schema: append(append(Schema{}, ss.Output()...), ws.Output()...),
	}
	// Outer spine: inner (0..3) LEFT JOIN cs (4,5), residual on ss.y.
	f, j := ijFilterOverInnerJoin(inner, cs, JoinTypeLeft, ijEq(0, "y", 1998))
	pushInnerJoinInputQuals(f)

	if _, ok := j.Left.(*Filter); ok {
		t.Fatalf("the conjunct stopped at the outer join's immediate input; it must descend to the CTE reference")
	}
	sf, ok := inner.Left.(*Filter)
	if !ok {
		t.Fatalf("inner Join.Left is %T, want *Filter directly above the `ss` CTE reference", inner.Left)
	}
	if _, ok := sf.Child.(*CTEScan); !ok {
		t.Errorf("pushed Filter sits above %T, want *CTEScan", sf.Child)
	}
	if got := columnRefIndexes(sf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("descended predicate refs = %v, want [0]", got)
	}
	// Re-running must be a no-op: a planned subtree is re-walked once per
	// enclosing scope (the Q69 double-print), and descent multiplies the
	// number of places that could stack a duplicate.
	pushInnerJoinInputQuals(f)
	if got := len(splitAnd(sf.Predicate)); got != 1 {
		t.Errorf("after a second walk the pushed Filter has %d conjuncts, want 1 (idempotence)", got)
	}
}

// TestInnerJoinQualPushReachesCrossJoinInput pins that a CROSS join
// participates. A CROSS join is an inner join whose predicate is absent
// or was demoted to a residual Filter — exactly M0125-0034's C1 shape,
// where the demoted equi-predicate cannot be pushed (it spans both
// inputs) but a single-side restriction alongside it can, and is worth
// more there than anywhere else because it shrinks an input of a
// Cartesian product.
func TestInnerJoinQualPushReachesCrossJoinInput(t *testing.T) {
	curr, prev := ijCTEScan("cs1"), ijCTEScan("cs2")
	f, j := ijFilterOverInnerJoin(curr, prev, JoinTypeCross,
		ijEq(0, "y", 1999),
		// A side-spanning conjunct that must NOT move.
		&BinaryOp{
			Op:    parser.OpEq,
			Left:  &ColumnRef{Index: 1, Name: "cnt", Type: catalog.Type{Name: "int4"}},
			Right: &ColumnRef{Index: 3, Name: "cnt", Type: catalog.Type{Name: "int4"}},
		},
	)
	pushInnerJoinInputQuals(f)
	lf, ok := j.Left.(*Filter)
	if !ok {
		t.Fatalf("CROSS: Join.Left is %T, want *Filter with the pushed single-side qual", j.Left)
	}
	if got := len(splitAnd(lf.Predicate)); got != 1 {
		t.Errorf("pushed Filter has %d conjuncts, want 1 — the side-spanning conjunct must stay above", got)
	}
	if _, ok := j.Right.(*Filter); ok {
		t.Errorf("CROSS: a side-spanning conjunct was pushed onto Join.Right")
	}
}

// TestInnerJoinQualPushReachesBaseRelationLeaf pins M0125-0035's
// REVERSAL of D2's leaf scoping. This test formerly asserted the
// opposite ("DeclinesOnBaseRelationLeaf"), on the reasoning that
// "pushing filters toward base-relation leaves is exactly what
// shouldAttachLocalFiltersBeforeSearch withholds behind its SmallDimension guard,
// whose comment records that without it Slice A regresses Q8 / Q21 from
// PASS to CANCEL".
//
// That borrowed the wrong risk. Slice A MOVES a conjunct out of the DP's
// input before enumeration, so it changes the join ORDER; this pass runs
// last and DUPLICATES, so the order is already fixed. What the old
// scoping actually withheld was the fix for C2, measured at 91f530c9 on
// the SF0.5 cluster: `store_sales ⋈ date_dim WHERE d_year = 2002` hashed
// all 73,049 date_dim rows and emitted 1,374,770 join rows for a
// 275,107-row answer, because the qual was a post-join residual.
// Design: docs/design/0125-0035-c2-single-table-qual-placement.md.
func TestInnerJoinQualPushReachesBaseRelationLeaf(t *testing.T) {
	scan := func() *SeqScan {
		return &SeqScan{Table: &catalog.Table{Name: "t"}, schema: Schema{ijCol("y"), ijCol("cnt")}}
	}
	l, r := scan(), scan()
	f, j := ijFilterOverInnerJoin(l, r, JoinTypeInner, ijEq(0, "y", 2002), ijEq(2, "y", 2001))
	pushInnerJoinInputQuals(f)

	lf, ok := j.Left.(*Filter)
	if !ok {
		t.Fatalf("left-side conjunct must be pushed onto the base-relation leaf")
	}
	rf, ok := j.Right.(*Filter)
	if !ok {
		t.Fatalf("right-side conjunct must be pushed onto the base-relation leaf")
	}
	// A Filter above a leaf carries leaf-local ColumnRefs, so it MUST
	// declare LeafLocal — otherwise a posMap remap would corrupt the
	// indices (M0077-0001).
	if !lf.LeafLocal || !rf.LeafLocal {
		t.Errorf("leaf-targeted Filters must set LeafLocal=true, got left=%v right=%v",
			lf.LeafLocal, rf.LeafLocal)
	}
	// The right-side conjunct was written at merged-output index 2 and
	// must be shifted by -leftWidth (2) into the input's own space.
	bin, ok := rf.Predicate.(*BinaryOp)
	if !ok {
		t.Fatalf("right Filter predicate = %T; want *BinaryOp", rf.Predicate)
	}
	cr, ok := bin.Left.(*ColumnRef)
	if !ok {
		t.Fatalf("right Filter predicate LHS = %T; want *ColumnRef", bin.Left)
	}
	if cr.Index != 0 {
		t.Errorf("right-side conjunct ColumnRef.Index = %d; want 0 (shifted by -leftWidth)", cr.Index)
	}
	// Property 2: the conjunct is DUPLICATED, never moved — the residual
	// above the join keeps both conjuncts.
	if got := len(splitAnd(f.Predicate)); got != 2 {
		t.Errorf("residual Filter must keep both conjuncts (duplicate, not move); got %d", got)
	}
}

// TestInnerJoinQualPushIsIdempotent pins the guard added with the leaf
// arm. A planned subtree is walked again whenever an enclosing scope's
// planSelect reaches this pass with the subtree embedded, so a second
// visit must not AND an already-present conjunct in a second time.
// Without the guard TPC-DS Q69 printed `d_year = 2002 AND d_moy >= 1 AND
// d_moy <= 3` TWICE on each date_dim scan — same rows, but a wasted
// re-evaluation per row and a divergence from PG's plan text.
func TestInnerJoinQualPushIsIdempotent(t *testing.T) {
	scan := func() *SeqScan {
		return &SeqScan{Table: &catalog.Table{Name: "t"}, schema: Schema{ijCol("y"), ijCol("cnt")}}
	}
	l, r := scan(), scan()
	f, j := ijFilterOverInnerJoin(l, r, JoinTypeInner, ijEq(0, "y", 2002))
	pushInnerJoinInputQuals(f)
	pushInnerJoinInputQuals(f)

	lf, ok := j.Left.(*Filter)
	if !ok {
		t.Fatalf("conjunct must be pushed onto the base-relation leaf")
	}
	if got := len(splitAnd(lf.Predicate)); got != 1 {
		t.Errorf("pushed predicate has %d conjuncts after two visits; want 1 (idempotent)", got)
	}
}

// TestInnerJoinQualPushDeclinesOnNameMismatch pins property 1 — RC-1b's
// lesson. If the conjunct's ColumnRef indices are in any coordinate
// space other than the join's merged output (a path where the remap did
// not run), the index-derived position carries a different column name
// than the ref claims. The pass must then DECLINE, leaving the conjunct
// to post-join evaluation: slower, never wrong. Running the equivalent
// pass before the remap is what pushed a date_dim predicate onto `store`
// and silently zeroed TPC-DS Q47/Q50.
func TestInnerJoinQualPushDeclinesOnNameMismatch(t *testing.T) {
	curr, prev := ijCTEScan("curr_yr"), ijCTEScan("prev_yr")
	// Index 1 is `cnt`, but the ref claims to be `y`.
	f, j := ijFilterOverInnerJoin(curr, prev, JoinTypeInner, ijEq(1, "y", 2002))
	pushInnerJoinInputQuals(f)
	if _, ok := j.Left.(*Filter); ok {
		t.Errorf("qual with a mismatched positional name was pushed; the name check must veto it")
	}
}

// TestInnerJoinQualPushDeclinesOnSideMixedAndFuncCall pins the two
// remaining vetoes: a conjunct spanning BOTH inputs is a join qual, not
// a restriction (PG's distribute_restrictinfo_to_rels makes exactly this
// distinction); and any *FuncCall is declined because this package has
// no volatility model while property 2 duplicates the conjunct — a
// volatile call evaluated once per input row AND once per matched pair
// does not select the same rows.
func TestInnerJoinQualPushDeclinesOnSideMixedAndFuncCall(t *testing.T) {
	t.Run("side-mixed", func(t *testing.T) {
		curr, prev := ijCTEScan("curr_yr"), ijCTEScan("prev_yr")
		mixed := &BinaryOp{
			Op:    parser.OpLt,
			Left:  &ColumnRef{Index: 1, Name: "cnt", Type: catalog.Type{Name: "int4"}},
			Right: &ColumnRef{Index: 3, Name: "cnt", Type: catalog.Type{Name: "int4"}},
		}
		f, j := ijFilterOverInnerJoin(curr, prev, JoinTypeInner, mixed)
		pushInnerJoinInputQuals(f)
		if _, ok := j.Left.(*Filter); ok {
			t.Errorf("a side-mixed conjunct was pushed onto Join.Left")
		}
		if _, ok := j.Right.(*Filter); ok {
			t.Errorf("a side-mixed conjunct was pushed onto Join.Right")
		}
	})
	t.Run("func-call", func(t *testing.T) {
		curr, prev := ijCTEScan("curr_yr"), ijCTEScan("prev_yr")
		fc := &BinaryOp{
			Op: parser.OpEq,
			Left: &FuncCall{Name: "abs", Args: []Expr{
				&ColumnRef{Index: 0, Name: "y", Type: catalog.Type{Name: "int4"}},
			}},
			Right: &IntegerConst{Value: 2002},
		}
		f, j := ijFilterOverInnerJoin(curr, prev, JoinTypeInner, fc)
		pushInnerJoinInputQuals(f)
		if _, ok := j.Left.(*Filter); ok {
			t.Errorf("a conjunct containing a FuncCall was pushed; no volatility model exists to justify it")
		}
	})
}

// --- helpers ---------------------------------------------------------

// columnRefIndexes returns every ColumnRef.Index in e, in walk order.
func columnRefIndexes(e Expr) []int {
	var out []int
	walkExprRefs(e, scopeIgnore, exprVisitor{Visit: func(x Expr) bool {
		if cr, ok := x.(*ColumnRef); ok {
			out = append(out, cr.Index)
		}
		return true
	}})
	return out
}

// findInnerJoinOverCTEScans finds the first INNER Join whose two inputs
// are CTE references (optionally wrapped in the Filter this pass adds).
func findInnerJoinOverCTEScans(n Node) *Join {
	isCTE := func(x Node) bool {
		if f, ok := x.(*Filter); ok {
			x = f.Child
		}
		_, ok := x.(*CTEScan)
		return ok
	}
	var walk func(Node) *Join
	walk = func(x Node) *Join {
		switch p := x.(type) {
		case nil:
			return nil
		case *Join:
			if p.Type == JoinTypeInner && isCTE(p.Left) && isCTE(p.Right) {
				return p
			}
			if j := walk(p.Left); j != nil {
				return j
			}
			return walk(p.Right)
		case *Filter:
			return walk(p.Child)
		case *Project:
			return walk(p.Child)
		case *Sort:
			return walk(p.Child)
		case *Limit:
			return walk(p.Child)
		case *Aggregate:
			return walk(p.Child)
		case *CTEScan:
			return walk(p.Child)
		}
		return nil
	}
	return walk(n)
}

// --- C-02c moves (drop the residual on full-path proof) ----------------

// srcGt builds `<col at idx named name from src> > <lit>` — a non-equi
// conjunct, so deriveConstAcrossJoinEquality never seeds (EC arm needs
// bare `col = const`).
func srcGt(idx int, name string, src int16, lit int64) Expr {
	return &BinaryOp{
		Op:    parser.OpGt,
		Left:  &ColumnRef{Index: idx, Name: name, Type: catalog.Type{Name: "int4"}, SourceTableIdx: src},
		Right: &IntegerConst{Value: lit},
	}
}

// TestMoveOnProvenInnerPath pins the C-02c move: fully-attributed INNER
// path, single-side non-const conjunct, no derivation — the copy lands
// and the residual is dropped (vacuous Filter spliced by the walker).
func TestMoveOnProvenInnerPath(t *testing.T) {
	left := srcScan("a", srcCol("x", 1))
	right := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeInner, left, right)
	f := &Filter{Child: j, Predicate: srcGt(0, "x", 1, 7)}

	newChild, dropSelf := pushInnerJoinInputQuals(f)
	if !dropSelf {
		t.Fatal("proven INNER move must drop the vacuous residual Filter")
	}
	nj, ok := newChild.(*Join)
	if !ok {
		t.Fatalf("spliced child is %T, want *Join", newChild)
	}
	lf, ok := nj.Left.(*Filter)
	if !ok {
		t.Fatalf("Join.Left is %T, want *Filter carrying the placed copy", nj.Left)
	}
	if got := columnRefIndexes(lf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("placed predicate refs = %v, want [0]", got)
	}
	// Spliced by the walker too: no Filter above the Join.
	if got := pushSingleSideQualsIntoInnerJoinInputs(
		&Filter{Child: srcJoin(JoinTypeInner,
			srcScan("a", srcCol("x", 1)),
			srcScan("b", srcCol("y", 2)),
		), Predicate: srcGt(0, "x", 1, 7)},
	); true {
		if _, isFilter := got.(*Filter); isFilter {
			t.Error("walker must splice a fully-moved residual Filter")
		}
	}
}

// TestPlantedDerivationVetoesMove pins the C-02c EC gate: `x = const`
// with a spanning join equality seeds the other side, so the residual
// is KEPT (copy) even on a fully-attributed INNER path.
func TestPlantedDerivationVetoesMove(t *testing.T) {
	left := srcScan("a", srcCol("x", 1))
	right := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeInner, left, right)
	j.Predicate = &BinaryOp{Op: parser.OpEq,
		Left:  &ColumnRef{Index: 0, Name: "x", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1},
		Right: &ColumnRef{Index: 1, Name: "y", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2},
	}
	f := &Filter{Child: j, Predicate: srcEq(0, "x", 1, 7)}

	newChild, dropSelf := pushInnerJoinInputQuals(f)
	if dropSelf {
		t.Fatal("planted derivation must veto the move (residual kept)")
	}
	if got := len(splitAnd(f.Predicate)); got != 1 {
		t.Fatalf("residual has %d conjuncts, want 1 (kept)", got)
	}
	nj := newChild.(*Join)
	rf, ok := nj.Right.(*Filter)
	if !ok {
		t.Fatalf("Join.Right is %T, want *Filter carrying the derived seed", nj.Right)
	}
	if got := columnRefIndexes(rf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("derived seed refs = %v, want [0]", got)
	}
}

// TestMoveAcrossPreservedLeftLink pins the C-02d move: a qual reading only
// the PRESERVED side of a LEFT link moves — a preserved row rejected below
// produces no join row at all, and every join row it would have produced
// (matched or NULL-extended) is one the residual above would have rejected
// on the same, un-extended values.
func TestMoveAcrossPreservedLeftLink(t *testing.T) {
	left := srcScan("a", srcCol("x", 1))
	right := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeLeft, left, right)
	f := &Filter{Child: j, Predicate: srcGt(0, "x", 1, 7)}

	newChild, dropSelf := pushInnerJoinInputQuals(f)
	if !dropSelf {
		t.Fatal("C-02d: a preserved-side qual on a LEFT link must MOVE")
	}
	nj := newChild.(*Join)
	if _, ok := nj.Left.(*Filter); !ok {
		t.Fatalf("Join.Left is %T, want *Filter (the placed conjunct)", nj.Left)
	}
	if len(f.PushedBelow) != 0 {
		t.Fatalf("PushedBelow has %d entries; a MOVE duplicates nothing, so "+
			"the descendant prices the conjunct once by construction",
			len(f.PushedBelow))
	}
}

// TestMoveAcrossPreservedRightLink is the RIGHT-link mirror: the preserved
// side is the right one, so the same argument holds with the sides swapped.
// Both directions are pinned because joinRestrictionSides encodes them
// separately, and a one-sided fix would look correct in every LEFT test.
func TestMoveAcrossPreservedRightLink(t *testing.T) {
	left := srcScan("a", srcCol("x", 1))
	right := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeRight, left, right)
	f := &Filter{Child: j, Predicate: srcGt(1, "y", 2, 7)}

	newChild, dropSelf := pushInnerJoinInputQuals(f)
	if !dropSelf {
		t.Fatal("C-02d: a preserved-side qual on a RIGHT link must MOVE")
	}
	nj := newChild.(*Join)
	rf, ok := nj.Right.(*Filter)
	if !ok {
		t.Fatalf("Join.Right is %T, want *Filter (the placed conjunct)", nj.Right)
	}
	// RIGHT is the direction where the rebase is non-zero (delta =
	// -leftWidth), so the placed index is pinned here specifically.
	if got := columnRefIndexes(rf.Predicate); len(got) != 1 || got[0] != 0 {
		t.Errorf("placed predicate refs = %v, want [0] (rebased into the right input)", got)
	}
	if len(f.PushedBelow) != 0 {
		t.Fatalf("PushedBelow has %d entries; a MOVE duplicates nothing", len(f.PushedBelow))
	}
}

// TestNullableSideQualNeverDescends is the C-02d safety counter-pin: a qual
// reading the NULLABLE side of a LEFT link must not be placed below it at
// all — pushing `b.y > 7` under the link would judge NULL-extended rows on
// base-row values and drop rows the query keeps. Two independent gates
// refuse it (joinRestrictionSides answers left-only, and delayedAboveOJ
// reports delay); this pins the OUTCOME so neither can be relaxed silently.
func TestNullableSideQualNeverDescends(t *testing.T) {
	left := srcScan("a", srcCol("x", 1))
	right := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeLeft, left, right)
	pred := srcGt(1, "y", 2, 7)
	f := &Filter{Child: j, Predicate: pred}

	newChild, dropSelf := pushInnerJoinInputQuals(f)
	if dropSelf {
		t.Fatal("a nullable-side qual must NEVER move: the residual is what " +
			"masks match -> null-extension flips")
	}
	nj := newChild.(*Join)
	if _, ok := nj.Right.(*Filter); ok {
		t.Fatal("nullable-side qual was placed below the LEFT link")
	}
	if got := len(splitAnd(f.Predicate)); got != 1 {
		t.Fatalf("residual has %d conjuncts, want 1 (kept intact)", got)
	}
}

// TestFullJoinQualNeverDescends pins the FULL case: both sides are
// nullable, so no descent is legal in either direction.
func TestFullJoinQualNeverDescends(t *testing.T) {
	left := srcScan("a", srcCol("x", 1))
	right := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeFull, left, right)
	f := &Filter{Child: j, Predicate: srcGt(0, "x", 1, 7)}

	newChild, dropSelf := pushInnerJoinInputQuals(f)
	if dropSelf {
		t.Fatal("FULL: both sides null-extend, so nothing may move")
	}
	nj := newChild.(*Join)
	if _, ok := nj.Left.(*Filter); ok {
		t.Fatal("qual was placed below a FULL link")
	}
}

// TestIdempotenceHitKeepsResidual pins the unknown-prior-proof rule: a
// copy already present below (legacy-placed, proof unknown) must keep
// the residual on re-descent.
func TestIdempotenceHitKeepsResidual(t *testing.T) {
	left := &Filter{Child: srcScan("a", srcCol("x", 1)), Predicate: srcGt(0, "x", 1, 7)}
	right := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeInner, left, right)
	f := &Filter{Child: j, Predicate: srcGt(0, "x", 1, 7)}

	_, dropSelf := pushInnerJoinInputQuals(f)
	if dropSelf {
		t.Fatal("idempotence-hit descent must keep the residual (prior proof unknown)")
	}
	if got := len(splitAnd(f.Predicate)); got != 1 {
		t.Fatalf("residual has %d conjuncts, want 1 (kept)", got)
	}
	// Still exactly one copy below (no duplicate planted).
	lf := left
	if got := len(splitAnd(lf.Predicate)); got != 1 {
		t.Fatalf("lower Filter has %d conjuncts, want 1 (no duplicate)", got)
	}
}

// TestPartialMoveKeepsOneConjunct pins the mixed C-02c outcome: one
// conjunct moves (proven INNER, non-const), one stays (const with a
// spanning join equality → derivation plants → veto). The residual
// Filter survives with exactly the kept conjunct, and only it is noted
// in PushedBelow (exactly-once charging).
func TestPartialMoveKeepsOneConjunct(t *testing.T) {
	left := srcScan("a", srcCol("x", 1))
	right := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeInner, left, right)
	j.Predicate = &BinaryOp{Op: parser.OpEq,
		Left:  &ColumnRef{Index: 0, Name: "x", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1},
		Right: &ColumnRef{Index: 1, Name: "y", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2},
	}
	// Merged layout: [a.x=0, b.y=1]. Gt moves; Eq seeds (veto).
	f := &Filter{Child: j, Predicate: combineAnd([]Expr{
		srcGt(0, "x", 1, 7),
		srcEq(0, "x", 1, 5),
	})}

	newChild, dropSelf := pushInnerJoinInputQuals(f)
	if dropSelf {
		t.Fatal("partial move must keep the residual Filter")
	}
	rest := splitAnd(f.Predicate)
	if len(rest) != 1 {
		t.Fatalf("residual has %d conjuncts, want 1 (the Eq)", len(rest))
	}
	if _, ok := rest[0].(*BinaryOp); !ok || rest[0].(*BinaryOp).Op != parser.OpEq {
		t.Errorf("residual holds %v, want the x=5 Eq conjunct", rest[0])
	}
	if len(f.PushedBelow) != 1 {
		t.Errorf("PushedBelow has %d entries, want 1 (kept copy only)", len(f.PushedBelow))
	}
	nj := newChild.(*Join)
	lf, ok := nj.Left.(*Filter)
	if !ok {
		t.Fatalf("Join.Left is %T, want *Filter", nj.Left)
	}
	if got := len(splitAnd(lf.Predicate)); got != 2 {
		t.Errorf("placed Filter has %d conjuncts, want 2 (moved Gt + kept-copy Eq)", got)
	}
}

// --- C-02d multi-level pins (the proof is over the WHOLE path) ---------

// TestNullableSideQualBlockedTwoLevels pins the conjunctive part of the
// claim on a two-level spine: `(a LJ b) LJ c` with a qual on `b`, which is
// the NULLABLE side of the LOWER link. The upper link would admit a
// left-side descent on its own; the lower one must refuse, and the
// conjunct must survive whole in the residual. A single-level test cannot
// distinguish "proof is conjunctive over the path" from "proof consults
// only the top link".
func TestNullableSideQualBlockedTwoLevels(t *testing.T) {
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	c := srcScan("c", srcCol("z", 3))
	lower := srcJoin(JoinTypeLeft, a, b)
	upper := srcJoin(JoinTypeLeft, lower, c)
	f := &Filter{Child: upper, Predicate: srcGt(1, "y", 2, 7)}

	newChild, dropSelf := pushInnerJoinInputQuals(f)
	if dropSelf {
		t.Fatal("a qual on the LOWER link's nullable side must never move")
	}
	nj := newChild.(*Join)
	lj, ok := nj.Left.(*Join)
	if !ok {
		t.Fatalf("upper.Left is %T, want the lower *Join", nj.Left)
	}
	if _, ok := lj.Right.(*Filter); ok {
		t.Fatal("qual was placed on the nullable side of the lower LEFT link")
	}
	if got := len(splitAnd(f.Predicate)); got != 1 {
		t.Fatalf("residual has %d conjuncts, want 1 (kept whole)", got)
	}
}

// TestMoveInnerAboveOuterTwoLevels is the positive two-level case:
// `(a LJ b) JOIN c` with a qual on `a`. The descent crosses an INNER link
// and then the preserved side of a LEFT link, so both levels prove and the
// conjunct moves all the way onto `a`.
func TestMoveInnerAboveOuterTwoLevels(t *testing.T) {
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	c := srcScan("c", srcCol("z", 3))
	lower := srcJoin(JoinTypeLeft, a, b)
	upper := srcJoin(JoinTypeInner, lower, c)
	f := &Filter{Child: upper, Predicate: srcGt(0, "x", 1, 7)}

	newChild, dropSelf := pushInnerJoinInputQuals(f)
	if !dropSelf {
		t.Fatal("INNER above LEFT, qual on the preserved leaf: must MOVE")
	}
	nj := newChild.(*Join)
	lj, ok := nj.Left.(*Join)
	if !ok {
		t.Fatalf("upper.Left is %T, want the lower *Join", nj.Left)
	}
	if _, ok := lj.Left.(*Filter); !ok {
		t.Fatalf("lower.Left is %T, want *Filter (placed on `a`)", lj.Left)
	}
}

// TestIdentityDisagreementKeepsResidual pins the one case where the delay
// proof is the SOLE control: the conjunct's ColumnRef.Index says
// preserved-side (so innerJoinPushTarget selects the left input and the
// side gate admits it) while its SourceTableIdx says the qual really reads
// the NULLABLE side. Index drives execution, so the copy still lands —
// but the move must be refused, because on that disagreement the identity
// is what describes the true column. Without this the two attributions
// could silently diverge and a nullable-side qual would be dropped from
// the residual.
func TestIdentityDisagreementKeepsResidual(t *testing.T) {
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeLeft, a, b)
	// Index 0 = left/preserved column; SourceTableIdx 2 = table `b`,
	// which is this link's nullable side.
	pred := &BinaryOp{
		Op:    parser.OpGt,
		Left:  &ColumnRef{Index: 0, Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2},
		Right: &IntegerConst{Value: 7},
	}
	f := &Filter{Child: j, Predicate: pred}

	_, dropSelf := pushInnerJoinInputQuals(f)
	if dropSelf {
		t.Fatal("Index/SourceTableIdx disagreement must degrade to a COPY, " +
			"never a move: the identity describes the true column")
	}
	if got := len(splitAnd(f.Predicate)); got != 1 {
		t.Fatalf("residual has %d conjuncts, want 1 (kept)", got)
	}
}

// TestConstantConjunctIsUnprovable pins the fail-closed treatment of a
// conjunct that reads no relation. innerJoinPushTarget refuses it today,
// so this asserts the OUTCOME (no move) rather than the mechanism — the
// ledgered pseudoconstant work would make the delay site reachable with
// an empty relset, where a vacuous "proven" would be a silent move.
func TestConstantConjunctIsUnprovable(t *testing.T) {
	a := srcScan("a", srcCol("x", 1))
	b := srcScan("b", srcCol("y", 2))
	j := srcJoin(JoinTypeInner, a, b)
	f := &Filter{Child: j, Predicate: &BooleanConst{Value: false}}

	_, dropSelf := pushInnerJoinInputQuals(f)
	if dropSelf {
		t.Fatal("a column-free conjunct must not move (unprovable, fail-closed)")
	}
}
