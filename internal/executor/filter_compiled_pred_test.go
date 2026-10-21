package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestFilterOpCompiledPredicateMatchesInterpreted pins M0122-0012 slice 40:
// the legacy filterOp now evaluates its predicate compiled (evalFastExpr over
// a slab built once in newFilterOp) instead of through evalExprSlot, and its
// batch path (evalFilterBatch) re-evaluates the string rows the per-row twins
// treat specially. All three are sibling paths and must keep exactly the rows
// evalExprSlot keeps — across int, text and bpchar(n) columns, padded and
// unpadded values, bare literals, the shapes the interpreter sniffs (row
// "(…)", array "{…}", pg_lsn "X/Y", uuid), and NULLs. Every case runs with
// the batch path on and off.
func TestFilterOpCompiledPredicateMatchesInterpreted(t *testing.T) {
	ctx, cleanup := resetFixture(t)
	defer cleanup()

	bpchar4 := catalog.Type{Name: "bpchar", Args: []int64{4}}
	text := catalog.Type{Name: "text"}
	int4 := catalog.Type{Name: "int4"}
	schema := optimizer.Schema{
		{Name: "i", Type: int4}, {Name: "t", Type: text}, {Name: "b", Type: bpchar4}, {Name: "u", Type: text},
	}
	col := func(i int, ty catalog.Type) optimizer.Expr {
		return &optimizer.ColumnRef{Index: i, Name: schema[i].Name, Type: ty}
	}
	str := func(s string) Datum { return NewStringDatum(s) }
	rows := []Row{
		{NewIntDatum(1), str("ab"), str("ab  "), str("ab")},
		{NewIntDatum(2), str("ab  "), str("ab"), str("zz")},
		{NewIntDatum(3), str("(1,2)"), str("(1,2"), str("(1,2)")},
		{NewIntDatum(4), str("{1,2}"), str("{1,2"), str("{1,02}")},
		{NewIntDatum(5), str("1/A"), str("1/A"), str("1/a")},
		{NewIntDatum(6), str("a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"), str("x"), str("A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11")},
		{NullDatum, NullDatum, str("q"), NullDatum},
		{NewIntDatum(8), str(""), str(""), str(" ")},
	}
	type tc struct {
		name string
		pred optimizer.Expr
	}
	var cases []tc
	for _, op := range []parser.OpCode{parser.OpEq, parser.OpLt, parser.OpGt, parser.OpNe} {
		for _, pair := range [][2]int{{1, 2}, {1, 3}, {2, 3}} {
			l, r := pair[0], pair[1]
			cases = append(cases, tc{
				name: fmt.Sprintf("%s %v %s", schema[l].Name, op, schema[r].Name),
				pred: &optimizer.BinaryOp{Op: op, Left: col(l, schema[l].Type), Right: col(r, schema[r].Type)},
			})
		}
		cases = append(cases, tc{
			name: fmt.Sprintf("i %v 3", op),
			pred: &optimizer.BinaryOp{Op: op, Left: col(0, int4), Right: &optimizer.IntegerConst{Value: 3}},
		})
		for _, lit := range []string{"ab", "1/a", "(1,2)"} {
			for _, c := range []int{1, 2} {
				cases = append(cases, tc{
					name: fmt.Sprintf("%s %v '%s'", schema[c].Name, op, lit),
					pred: &optimizer.BinaryOp{Op: op, Left: col(c, schema[c].Type), Right: &optimizer.StringConst{Value: lit}},
				})
			}
		}
	}
	for _, c := range cases {
		for _, batch := range []bool{true, false} {
			child := &stubRowsOp{schema: schema, rows: rows}
			f := newFilterOp(&optimizer.Filter{Predicate: c.pred}, child)
			f.noReadAhead = !batch
			if len(f.predExprs) == 0 || f.predIdx == noExpr {
				t.Fatalf("%s: the predicate was not compiled", c.name)
			}
			if err := f.Open(ctx); err != nil {
				t.Fatal(err)
			}
			var got []int64
			for {
				slot, err := f.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatalf("%s: %v", c.name, err)
				}
				got = append(got, slotRow(slot)[0].Int)
			}
			_ = f.Close()
			var want []int64
			for _, r := range rows {
				v, err := evalExprSlot(c.pred, SlotFromRow(schema, r), ctx)
				if err != nil {
					t.Fatalf("%s: interpreted: %v", c.name, err)
				}
				if !v.IsNull() && v.Kind == KindBool && v.BoolValue() {
					want = append(want, r[0].Int)
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("%s (batch=%v): filter kept %v, interpreted %v", c.name, batch, got, want)
			}
		}
	}
}
