package planner

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

// TestInnerJoinQualPushDeclinesOnOuterJoin pins property 4. The qual
// here sits on the PRESERVED side of a LEFT JOIN, where pushing would in
// fact be safe — the pin is on the deliberate conservatism, not on a
// semantic hazard. goopg has no nullingrels model, and PG 18.3 removed
// check_outerjoin_delay in the nullingrels rework, so safety can only be
// expressed by declining on every non-INNER join.
func TestInnerJoinQualPushDeclinesOnOuterJoin(t *testing.T) {
	for _, typ := range []JoinType{JoinTypeLeft, JoinTypeRight, JoinTypeFull, JoinTypeSemi, JoinTypeAnti} {
		curr, prev := ijCTEScan("curr_yr"), ijCTEScan("prev_yr")
		f, j := ijFilterOverInnerJoin(curr, prev, typ, ijEq(0, "y", 2002))
		pushInnerJoinInputQuals(f)
		if _, ok := j.Left.(*Filter); ok {
			t.Errorf("join type %v: qual was pushed onto Join.Left; the pass must decline on non-INNER joins", typ)
		}
	}
}

// TestInnerJoinQualPushReachesBaseRelationLeaf pins M0125-0035's
// REVERSAL of D2's leaf scoping. This test formerly asserted the
// opposite ("DeclinesOnBaseRelationLeaf"), on the reasoning that
// "pushing filters toward base-relation leaves is exactly what
// shouldAttachBeforeMHJ withholds behind its SmallDimension guard,
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
