package optimizer

// M0145-0003 ANY arm — `convert_ANY_sublink_to_join` at goopg's seam.
//
// The census picked this arm (34 of 45 unpulled sublink conjuncts on TPC-DS
// SF0.25). What the pins below protect is the pair of refusals that keep it
// PG-faithful, and the synthesised link predicate that is the whole mechanism:
// an ANY body need not be correlated at all, so the conjunct the seam needs
// does not exist in the body and this arm builds it.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAnyPullupConjunctRefusesNonPlainEquality pins the recognition gate.
// NOT IN is the important one: it is `<> ALL`, an ALL_SUBLink, and PG's
// pull_up_sublinks converts ANY and EXISTS only — the three-valued NULL
// semantics of `<> ALL` are not an anti-join's (one NULL inner row makes the
// predicate NULL where an anti-join would emit the outer row).
func TestAnyPullupConjunctRefusesNonPlainEquality(t *testing.T) {
	body := &parser.SelectStmt{}
	operand := &ColumnRef{Index: 0, Name: "k"}

	if _, ok := anyPullupConjunct(&InExpr{Operand: operand, Subquery: body}); !ok {
		t.Fatal("a plain IN over a retained body must be recognised")
	}
	cases := map[string]*InExpr{
		"NOT IN":           {Operand: operand, Subquery: body, Negated: true},
		"<> ANY":           {Operand: operand, Subquery: body, NotEqualAny: true},
		"op ANY":           {Operand: operand, Subquery: body, AnyOp: parser.OpLt},
		"ALL":              {Operand: operand, Subquery: body, AllOp: true},
		"no retained body": {Operand: operand},
		"no operand":       {Subquery: body},
	}
	for name, in := range cases {
		if _, ok := anyPullupConjunct(in); ok {
			t.Errorf("%s must not be recognised by the ANY arm", name)
		}
	}
}

// TestOuterOperandAsLevel1LiftsColumnsAndVetoesOuterRefs pins the space
// translation the synthesised link predicate depends on: the outer operand
// has to read as a correlated reference from INSIDE the body, because that is
// the space rebasePulledQual translates out of.
func TestOuterOperandAsLevel1LiftsColumnsAndVetoesOuterRefs(t *testing.T) {
	got, ok := outerOperandAsLevel1(&ColumnRef{Index: 3, Name: "o_orderkey", SourceTableIdx: 2})
	if !ok {
		t.Fatal("a plain outer column must lift")
	}
	ocr, isOCR := got.(*OuterColumnRef)
	if !isOCR {
		t.Fatalf("lifted operand is %T, want *OuterColumnRef", got)
	}
	if ocr.Level != 1 || ocr.Index != 3 || ocr.Name != "o_orderkey" || ocr.SourceTableIdx != 2 {
		t.Fatalf("lifted operand = %#v, want Level 1 at the same coordinate", ocr)
	}

	// An operand that ALREADY carries an outer reference names a scope above
	// this statement; shifting its level is a different rebase than the one
	// performed on the way out, so the arm refuses rather than guessing.
	if _, ok := outerOperandAsLevel1(&OuterColumnRef{Level: 1, Index: 0, Name: "x"}); ok {
		t.Fatal("an operand carrying an OuterColumnRef must be refused")
	}
	// A constant operand references no outer column, so there is nothing to
	// correlate on and the conversion would change the predicate.
	if _, ok := outerOperandAsLevel1(&IntegerConst{Value: 1}); ok {
		t.Fatal("a constant operand must be refused")
	}
}
