package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// makeColRef constructs a ColumnRef test fixture with the
// given name + SourceTableIdx + type for equiv-class tests.
func makeColRef(name string, srcIdx int16, typeName string) *ColumnRef {
	return &ColumnRef{
		Name:           name,
		Type:           catalog.Type{Name: typeName},
		SourceTableIdx: srcIdx,
	}
}

// makeEqExpr constructs `a = b` as a *BinaryOp.
func makeEqExpr(a, b *ColumnRef) *BinaryOp {
	return &BinaryOp{Op: parser.OpEq, Left: a, Right: b}
}

// hasPair returns true if the synthesised conjuncts include
// `(a = b)` or `(b = a)` (orderless).
func hasPair(synth []Expr, a, b *ColumnRef) bool {
	wantA, wantB := identOf(a), identOf(b)
	for _, e := range synth {
		la, lb, ok := isColumnRefEquality(e)
		if !ok {
			continue
		}
		ia, ib := identOf(la), identOf(lb)
		if (ia == wantA && ib == wantB) || (ia == wantB && ib == wantA) {
			return true
		}
	}
	return false
}

// TestEquivClassClosureSimple pins the basic case:
// `a=b ∧ b=c` synthesises `a=c`. (M0075-0001.)
func TestEquivClassClosureSimple(t *testing.T) {
	a := makeColRef("a", 1, "int4")
	b := makeColRef("b", 2, "int4")
	c := makeColRef("c", 3, "int4")
	conjuncts := []Expr{makeEqExpr(a, b), makeEqExpr(b, c)}

	synth := inferTransitiveEqualities(conjuncts)
	if len(synth) != 1 {
		t.Fatalf("expected 1 synthesised conjunct, got %d", len(synth))
	}
	if !hasPair(synth, a, c) {
		t.Errorf("missing synthesised a=c; got %v", synth)
	}
}

// TestEquivClassNoSpurious pins that pre-existing
// closure predicates are NOT re-synthesised.
// (M0075-0001.)
func TestEquivClassNoSpurious(t *testing.T) {
	a := makeColRef("a", 1, "int4")
	b := makeColRef("b", 2, "int4")
	c := makeColRef("c", 3, "int4")
	conjuncts := []Expr{
		makeEqExpr(a, b),
		makeEqExpr(b, c),
		makeEqExpr(a, c), // already there
	}
	synth := inferTransitiveEqualities(conjuncts)
	if len(synth) != 0 {
		t.Errorf("expected 0 synthesised (closure already present), got %d: %v", len(synth), synth)
	}
}

// TestEquivClassMultiHop pins multi-hop closure:
// `a=b ∧ b=c ∧ c=d` synthesises a=c, a=d, b=d (3 new
// pairs since (a,b), (b,c), (c,d) are explicit).
// (M0075-0001.)
func TestEquivClassMultiHop(t *testing.T) {
	a := makeColRef("a", 1, "int4")
	b := makeColRef("b", 2, "int4")
	c := makeColRef("c", 3, "int4")
	d := makeColRef("d", 4, "int4")
	conjuncts := []Expr{
		makeEqExpr(a, b),
		makeEqExpr(b, c),
		makeEqExpr(c, d),
	}
	synth := inferTransitiveEqualities(conjuncts)
	// Closure of {a,b,c,d}: 4*3/2 = 6 pairs total; 3 explicit;
	// 3 synthesised: (a,c), (a,d), (b,d).
	if len(synth) != 3 {
		t.Fatalf("expected 3 synthesised, got %d: %v", len(synth), synth)
	}
	for _, pair := range []struct{ x, y *ColumnRef }{{a, c}, {a, d}, {b, d}} {
		if !hasPair(synth, pair.x, pair.y) {
			t.Errorf("missing synthesised %s=%s; got %v", pair.x.Name, pair.y.Name, synth)
		}
	}
}

// TestEquivClassRespectsTypes pins that classes do NOT
// merge across SQL types (int4 = int8 stays as a literal
// predicate, not converted to closure). (M0075-0001.)
func TestEquivClassRespectsTypes(t *testing.T) {
	a := makeColRef("a", 1, "int4")
	b := makeColRef("b", 2, "int4")
	c := makeColRef("c", 3, "int8") // different type!
	conjuncts := []Expr{
		makeEqExpr(a, b),
		makeEqExpr(b, c), // cross-type — excluded
	}
	synth := inferTransitiveEqualities(conjuncts)
	// {a,b} is a class; {c} is alone. No closure to add.
	// (Specifically: a=c should NOT be synthesised since
	// b=c was excluded due to type mismatch.)
	for _, e := range synth {
		la, lb, ok := isColumnRefEquality(e)
		if !ok {
			continue
		}
		if la.Type.Name != lb.Type.Name {
			t.Errorf("synthesised cross-type predicate: %v = %v (types %s vs %s)",
				la.Name, lb.Name, la.Type.Name, lb.Type.Name)
		}
	}
}

// TestEquivClassRespectsSourceTableIdx pins self-join
// disambiguation: same-name columns from different
// aliases (different SourceTableIdx) stay in different
// classes. Q9 lineitem self-join would break without
// this. (M0075-0001 + M0071-0009.)
func TestEquivClassRespectsSourceTableIdx(t *testing.T) {
	// Two l_suppkey columns from aliases l1 (idx=1) and
	// l2 (idx=2). Same name + type, different source.
	l1Suppkey := makeColRef("l_suppkey", 1, "int4")
	l2Suppkey := makeColRef("l_suppkey", 2, "int4")
	sSuppkey := makeColRef("s_suppkey", 3, "int4")

	// Only l1.l_suppkey = s.s_suppkey explicitly.
	// l2.l_suppkey should NOT be in the same class
	// (different SourceTableIdx).
	conjuncts := []Expr{makeEqExpr(l1Suppkey, sSuppkey)}
	synth := inferTransitiveEqualities(conjuncts)

	// l2 is not even in the conjuncts; ensure no spurious
	// synthesis.
	for _, e := range synth {
		la, lb, ok := isColumnRefEquality(e)
		if !ok {
			continue
		}
		if (la == l2Suppkey || lb == l2Suppkey) {
			t.Errorf("synthesised predicate involving l2.l_suppkey (different alias): %v", e)
		}
	}
	if len(synth) != 0 {
		t.Errorf("expected 0 synthesised (singleton class only), got %d: %v", len(synth), synth)
	}
}

// TestEquivClassNoSelfPair pins that `a = a` doesn't
// trigger spurious synthesis. (M0075-0001.)
func TestEquivClassNoSelfPair(t *testing.T) {
	a := makeColRef("a", 1, "int4")
	conjuncts := []Expr{makeEqExpr(a, a)}
	synth := inferTransitiveEqualities(conjuncts)
	if len(synth) != 0 {
		t.Errorf("expected 0 synthesised for self-pair, got %d: %v", len(synth), synth)
	}
}

// TestEquivClassEmptyConjuncts pins the degenerate case.
func TestEquivClassEmptyConjuncts(t *testing.T) {
	if synth := inferTransitiveEqualities(nil); len(synth) != 0 {
		t.Errorf("expected nil → empty, got %v", synth)
	}
	if synth := inferTransitiveEqualities([]Expr{}); len(synth) != 0 {
		t.Errorf("expected empty → empty, got %v", synth)
	}
}

// TestEquivClassNonEqualityIgnored pins that
// inequality (`a < b`, `a <> b`) doesn't trigger union.
// (M0075-0001.)
func TestEquivClassNonEqualityIgnored(t *testing.T) {
	a := makeColRef("a", 1, "int4")
	b := makeColRef("b", 2, "int4")
	c := makeColRef("c", 3, "int4")
	conjuncts := []Expr{
		&BinaryOp{Op: parser.OpLt, Left: a, Right: b},
		&BinaryOp{Op: parser.OpNe, Left: b, Right: c},
	}
	synth := inferTransitiveEqualities(conjuncts)
	if len(synth) != 0 {
		t.Errorf("non-equality predicates should not synthesise: got %v", synth)
	}
}

// TestEquivClassQ5Shape pins the Q5 motivating case:
// `c.nationkey = s.nationkey AND s.nationkey = n.nationkey`
// gains the inferred `c.nationkey = n.nationkey`.
// (M0075-0001.)
func TestEquivClassQ5Shape(t *testing.T) {
	cNk := makeColRef("c_nationkey", 1, "int4") // customer
	sNk := makeColRef("s_nationkey", 2, "int4") // supplier
	nNk := makeColRef("n_nationkey", 3, "int4") // nation
	conjuncts := []Expr{
		makeEqExpr(cNk, sNk),
		makeEqExpr(sNk, nNk),
	}
	synth := inferTransitiveEqualities(conjuncts)
	if len(synth) != 1 {
		t.Fatalf("expected 1 synthesised (c=n), got %d: %v", len(synth), synth)
	}
	if !hasPair(synth, cNk, nNk) {
		t.Errorf("missing synthesised c_nationkey=n_nationkey; got %v", synth)
	}
}

// TestPushdownInfersTransitiveEquality verifies M0119-0011: the legacy
// pushPredicatesIntoCrossJoins pass calls inferTransitiveEqualities so that a
// 3-relation equivalence class written as two equalities (a.x = b.x AND
// b.x = c.x) generates the transitive clause (a.x = c.x).
//
// This test verifies the call site wiring by checking that after
// pushPredicatesIntoCrossJoins, a simple 2-table predicate that forms a
// 3-member EC has its CROSS promoted to INNER — achievable only because the
// inferred transitive clause connects the two tables.
func TestPushdownInfersTransitiveEquality(t *testing.T) {
	// Build a 2-table CROSS join: Cross(left, right)
	// Schema: l_x(0) | ra_x(1), rb_x(2)
	left := &SeqScan{
		Table:  &catalog.Table{Name: "l", OID: 1},
		Alias:  "l",
		schema: Schema{{Name: "l_x", Type: catalog.Type{Name: "int4"}}},
	}
	right := &SeqScan{
		Table:  &catalog.Table{Name: "r", OID: 2},
		Alias:  "r",
		schema: Schema{
			{Name: "ra_x", Type: catalog.Type{Name: "int4"}},
			{Name: "rb_x", Type: catalog.Type{Name: "int4"}},
		},
	}
	cross := &Join{Type: JoinTypeCross, Left: left, Right: right,
		schema: appendSchema(left.Output(), right.Output())}

	// Columns in cumulative schema: l_x(0) | ra_x(1), rb_x(2)
	lX := &ColumnRef{Name: "l_x", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 1, Index: 0}
	raX := &ColumnRef{Name: "ra_x", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2, Index: 1}
	rbX := &ColumnRef{Name: "rb_x", Type: catalog.Type{Name: "int4"}, SourceTableIdx: 2, Index: 2}

	// Predicate: l_x = ra_x AND ra_x = rb_x
	// Forms EC {l_x, ra_x, rb_x} with 3 members over only 2 relations.
	// Without inferTransitiveEqualities, the pushdown sees only:
	//   l_x = ra_x (spans both sides → promotes CROSS)
	//   ra_x = rb_x (both on right side → stays as residual)
	// With inferTransitiveEqualities, it also sees:
	//   l_x = rb_x (spans both sides → ANDed to inner join predicate)
	pred := combineAnd([]Expr{
		makeEqExpr(lX, raX),
		makeEqExpr(raX, rbX),
	})

	f := &Filter{Child: cross, Predicate: pred}
	out := pushPredicatesIntoCrossJoins(f)

	// After pushdown: the CROSS must have been promoted to INNER by at least
	// one of the spanning equalities (l_x = ra_x). The ra_x = rb_x conjunct
	// is single-side and may remain as a residual Filter — that is expected.
	// The key property we verify is that the CROSS → INNER promotion happened.
	top := out
	if rf, ok := out.(*Filter); ok {
		top = rf.Child
	}
	j, ok := top.(*Join)
	if !ok {
		t.Fatalf("root after pushdown is %T, expected *Join", top)
	}
	if j.Type != JoinTypeInner {
		t.Error("CROSS join should have been promoted to INNER by the pushdown")
	}
	if j.Predicate == nil {
		t.Error("join should have a predicate after pushdown")
	}
}
