package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestCompiledStringComparisonMatchesInterpreted pins M0122-0012 slice 64:
// evalFastBinaryApply decides a comparison of two plain strings
// (plainCompareString) — with the bpchar blank rule applied inline — before
// the character-string ambiguity test, which can only answer false for such a
// pair. Compiled (per-row and batch) and interpreted evaluation must agree for
// every operator, both operand orders and every value pair over a char(9)
// column, a text column and bare literals, with values that are plain,
// blank-padded, uuid-, pg_lsn-, row- and array-shaped (so the reordered
// shortcut is tested beside every path it now precedes).
func TestCompiledStringComparisonMatchesInterpreted(t *testing.T) {
	bp := &optimizer.ColumnRef{Index: 0, Name: "c", Type: catalog.Type{Name: "bpchar", Args: []int64{9}}}
	tx := &optimizer.ColumnRef{Index: 1, Name: "t", Type: catalog.Type{Name: "text"}}
	vals := []string{"Sunday   ", "Sunday", "Monday   ", "", "         ", "sun", "(1,2)", "{1,2}", "0/16B3748", "0/A",
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "A0EEBC999C0B4EF8BB6D6BB9BD380A11"}
	var rows []Row
	for _, a := range vals {
		for _, b := range vals {
			rows = append(rows, Row{NewStringDatum(a), NewStringDatum(b)})
		}
	}
	rows = append(rows, Row{NullDatum, NewStringDatum("x")})
	views := make([]SlotView, len(rows))
	sel := make([]int32, len(rows))
	for r := range rows {
		views[r] = SlotFromRow(nil, rows[r])
		sel[r] = int32(r)
	}
	ops := []parser.OpCode{parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe}
	operands := []optimizer.Expr{bp, tx, &optimizer.StringConst{Value: "Sunday"}, &optimizer.StringConst{Value: "Sunday  "},
		&optimizer.StringConst{Value: "(1,2)"}}
	var sc exprBatchScratch
	checked := 0
	for _, op := range ops {
		for li, l := range operands {
			for ri, r := range operands {
				if li >= 2 && ri >= 2 {
					continue // two literals: nothing per row
				}
				e := &optimizer.BinaryOp{Op: op, Left: l, Right: r}
				var slab exprTreeSlab
				idx := slab.buildExpr(e)
				batch := make([]Datum, len(rows))
				batchErr := evalFastExprBatch(slab, idx, rows, views, sel, nil, &sc, batch)
				for i := range rows {
					want, wantErr := evalExpr(e, rows[i], nil)
					got, gotErr := evalFastExpr(slab, idx, views[i], nil)
					name := fmt.Sprintf("%v op %v %v row %q/%q", li, op, ri, rows[i][0].Format(), rows[i][1].Format())
					if !sameResult(got, gotErr, want, wantErr) {
						t.Fatalf("%s: compiled %v (%v), interpreted %v (%v)", name, got.Format(), gotErr, want.Format(), wantErr)
					}
					if batchErr == nil && wantErr == nil && !sameResult(batch[i], nil, want, nil) {
						t.Fatalf("%s: batch %v, interpreted %v", name, batch[i].Format(), want.Format())
					}
					checked++
				}
			}
		}
	}
	if checked < 10000 {
		t.Fatalf("only %d comparisons checked", checked)
	}
}
