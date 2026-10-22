package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// TestInListHashMatchesLinear pins M0122-0012 slice 50: a literal IN list of
// nine or more items (PG's MIN_ARRAY_SIZE_FOR_HASHED_SAOP) is probed through a
// hash set (inListConst.hashed). With a Context the list is cached and hashed;
// without one evalInExprScalarRow re-evaluates the items and compares every
// one linearly. Both must agree on every row, for IN and NOT IN, over:
// text, bpchar(5) and int operands; string, int, padded, bpchar-cast,
// NULL-holding and mixed-kind lists; and operand values that are NULL,
// padded, of the other kind, or absent from the list.
func TestInListHashMatchesLinear(t *testing.T) {
	txt := &optimizer.ColumnRef{Index: 0, Name: "t", Type: catalog.Type{Name: "text"}}
	bp := &optimizer.ColumnRef{Index: 1, Name: "c", Type: catalog.Type{Name: "bpchar", Args: []int64{5}}}
	in := &optimizer.ColumnRef{Index: 2, Name: "i", Type: catalog.Type{Name: "int4"}}
	rows := []Row{
		{NewStringDatum("k3"), NewStringDatum("k3   "), NewIntDatum(3)},
		{NewStringDatum("k3 "), NewStringDatum("zz   "), NewIntDatum(42)},
		{NewStringDatum("nope"), NewStringDatum("k11  "), NewIntDatum(11)},
		{NullDatum, NullDatum, NullDatum},
		{NewStringDatum(""), NewStringDatum("     "), NewIntDatum(0)},
		{NewStringDatum("7"), NewStringDatum("7    "), NewIntDatum(7)},
	}
	strs := func(n int, extra ...optimizer.Expr) []optimizer.Expr {
		var out []optimizer.Expr
		for i := 0; i < n; i++ {
			out = append(out, &optimizer.StringConst{Value: fmt.Sprintf("k%d", i)})
		}
		return append(out, extra...)
	}
	ints := func(n int, extra ...optimizer.Expr) []optimizer.Expr {
		var out []optimizer.Expr
		for i := 0; i < n; i++ {
			out = append(out, &optimizer.IntegerConst{Value: int64(i)})
		}
		return append(out, extra...)
	}
	lists := map[string][]optimizer.Expr{
		"strings":       strs(12),
		"strings+null":  strs(12, &optimizer.NullConst{}),
		"padded":        strs(10, &optimizer.StringConst{Value: "k11  "}, &optimizer.StringConst{Value: "  "}),
		"bpchar-cast":   strs(10, &optimizer.CastExpr{Operand: &optimizer.StringConst{Value: "zz "}, TargetType: "bpchar", Typmod: 5}),
		"ints":          ints(12),
		"ints+null":     ints(12, &optimizer.NullConst{}),
		"mixed":         ints(10, &optimizer.StringConst{Value: "k3"}),
		"short-strings": strs(8),
	}
	hashedLists := 0
	for _, op := range []optimizer.Expr{txt, bp, in} {
		for name, list := range lists {
			for _, neg := range []bool{false, true} {
				x := &optimizer.InExpr{Operand: op, List: list, Negated: neg}
				ctx := NewContext()
				for ri, r := range rows {
					slot := SlotFromRow(nil, r)
					want, wantErr := evalInExprScalarRow(x, slot, nil)
					got, gotErr := evalInExprScalarRow(x, slot, ctx)
					if !sameResult(got, gotErr, want, wantErr) {
						t.Errorf("%s IN %s neg=%v row %d: hashed %v (%v), linear %v (%v)",
							op.(*optimizer.ColumnRef).Name, name, neg, ri, got.Format(), gotErr, want.Format(), wantErr)
					}
				}
				if c := ctx.inListConsts[x]; c != nil && c.hash.ok {
					hashedLists++
				}
			}
		}
	}
	// Strings, strings+null, padded and ints/ints+null hash under at least one
	// operand each: the fixture must reach the hash, not only the loop.
	if hashedLists < 10 {
		t.Fatalf("only %d lists were hashed; the fixture never reaches the hash set", hashedLists)
	}
}
