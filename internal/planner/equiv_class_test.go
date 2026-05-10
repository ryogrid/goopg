package planner

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
