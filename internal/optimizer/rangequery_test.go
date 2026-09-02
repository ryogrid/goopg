package optimizer

import (
	"math"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// rqCol builds a resolved column reference on FROM-item 1, column 0, with a
// histogram spanning 0..100 so a bound's selectivity is its position.
func rqScan(t *testing.T) *SeqScan {
	t.Helper()
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"},
		[]catalog.Column{{Name: "d", Type: catalog.Type{Name: "int4"}}})
	if err != nil {
		t.Fatal(err)
	}
	hist := make([]string, 0, 101)
	for i := 0; i <= 100; i++ {
		hist = append(hist, strconv.Itoa(i))
	}
	tbl.Stats = &catalog.TableStats{
		RowCount: 10000,
		Analyzed: true,
		Columns:  []catalog.ColumnStats{{NDistinct: 101, Histogram: hist}},
	}
	return &SeqScan{Table: tbl, EstRelRows: 10000}
}

func rqBound(op parser.OpCode, v int64) Expr {
	return &BinaryOp{
		Op:    op,
		Left:  &ColumnRef{Name: "d", Index: 0, SourceTableIdx: 1, Type: catalog.Type{Name: "int4"}},
		Right: &IntegerConst{Value: v},
	}
}

// TestRangeQueryClausePairsBoundsOnOneVariable is the case the item exists for.
// A band covering a tenth of the column must estimate near 0.1, not near the
// product of the two tail fractions (~0.18 for this band), which is what the
// independence assumption gives.
func TestRangeQueryClausePairsBoundsOnOneVariable(t *testing.T) {
	scan := rqScan(t)
	// 20 <= d < 30 on a 0..100 column: one tenth.
	and := &BinaryOp{
		Op:    parser.OpAnd,
		Left:  rqBound(parser.OpGe, 20),
		Right: rqBound(parser.OpLt, 30),
	}
	got := clauseSelectivity(and, scan)

	lo := clauseSelectivity(rqBound(parser.OpGe, 20), scan)
	hi := clauseSelectivity(rqBound(parser.OpLt, 30), scan)
	independent := lo * hi

	if math.Abs(got-0.10) > 0.03 {
		t.Errorf("paired selectivity = %.4f, want ~0.10 (lo=%.4f hi=%.4f)", got, lo, hi)
	}
	if math.Abs(got-independent) < 0.02 {
		t.Errorf("paired selectivity %.4f is indistinguishable from the independent "+
			"product %.4f — the pairing did not fire", got, independent)
	}
}

// TestRangeQueryClauseKeepsIndependenceAcrossVariables guards the other side:
// bounds on DIFFERENT columns must not be paired, or an unrelated pair of
// inequalities would be collapsed into one band.
func TestRangeQueryClauseKeepsIndependenceAcrossVariables(t *testing.T) {
	scan := rqScan(t)
	other := &BinaryOp{
		Op:    parser.OpLt,
		Left:  &ColumnRef{Name: "e", Index: 1, SourceTableIdx: 2, Type: catalog.Type{Name: "int4"}},
		Right: &IntegerConst{Value: 30},
	}
	and := &BinaryOp{Op: parser.OpAnd, Left: rqBound(parser.OpGe, 20), Right: other}
	got := clauseSelectivity(and, scan)
	want := clauseSelectivity(rqBound(parser.OpGe, 20), scan) * clauseSelectivity(other, scan)
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("cross-variable AND = %.6f, want the independent product %.6f", got, want)
	}
}

// TestRangeQueryClauseKeepsMoreRestrictiveDuplicate mirrors clausesel.c:456-471:
// `x > y AND x >= z` keeps only the more restrictive bound.
func TestRangeQueryClauseKeepsMoreRestrictiveDuplicate(t *testing.T) {
	scan := rqScan(t)
	conj := []Expr{rqBound(parser.OpGe, 20), rqBound(parser.OpGe, 40), rqBound(parser.OpLt, 60)}
	got := conjunctionSelectivity(conj, scan)
	// The 40 bound is the restrictive one, so the band is 40..60 = 0.2, not
	// 20..60 = 0.4.
	if math.Abs(got-0.20) > 0.05 {
		t.Errorf("duplicate lower bounds: selectivity = %.4f, want ~0.20", got)
	}
}
