package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestProjectColumnRefCopyMatchesEval pins M0122-0012 slice 58: projectOp
// copies a bare ColumnRef target straight from a *MaterializedSlot's row
// into its output cell instead of through evalExprSlot. Every output cell
// must equal evalExprSlot of its target on the same input — for bare column
// targets (reordered and repeated), a computed target beside them, NULL and
// string values — and an out-of-range column index must still raise
// evalExprSlot's error, not panic.
func TestProjectColumnRefCopyMatchesEval(t *testing.T) {
	int4 := catalog.Type{Name: "int4"}
	schema := optimizer.Schema{{Name: "a", Type: int4}, {Name: "b", Type: int4}, {Name: "s", Type: catalog.Type{Name: "text"}}}
	col := func(i int) *optimizer.ColumnRef {
		return &optimizer.ColumnRef{Index: i, Name: schema[i].Name, Type: schema[i].Type}
	}
	rows := []Row{
		{NewIntDatum(1), NewIntDatum(2), NewStringDatum("x")},
		{NullDatum, NewIntDatum(-7), NullDatum},
		{NewIntDatum(40), NullDatum, NewStringDatum("")},
	}
	targets := []optimizer.Expr{
		col(2), col(0), &optimizer.BinaryOp{Op: parser.OpAdd, Left: col(0), Right: col(1)}, col(0), col(1),
	}
	p := &projectOp{child: &stubRowsOp{schema: schema, rows: rows}, targets: targets, schema: schema}
	ctx := NewContext()
	if err := p.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for ri, r := range rows {
		slot, err := p.Next()
		if err != nil {
			t.Fatalf("row %d: Next: %v", ri, err)
		}
		in := SlotFromRow(schema, r)
		for ti, tgt := range targets {
			want, werr := evalExprSlot(tgt, in, ctx)
			if werr != nil {
				t.Fatalf("row %d target %d: evalExprSlot: %v", ri, ti, werr)
			}
			if got := slot.Get(ti); !sameResult(got, nil, want, nil) {
				t.Fatalf("row %d target %d: project %v, evalExprSlot %v", ri, ti, got.Format(), want.Format())
			}
		}
	}

	bad := &optimizer.ColumnRef{Index: 9, Name: "zz", Type: int4}
	q := &projectOp{child: &stubRowsOp{schema: schema, rows: rows[:1]}, targets: []optimizer.Expr{bad}, schema: schema[:1]}
	if err := q.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, gotErr := q.Next()
	_, wantErr := evalExprSlot(bad, SlotFromRow(schema, rows[0]), ctx)
	if gotErr == nil || wantErr == nil || gotErr.Error() != wantErr.Error() {
		t.Fatalf("out-of-range column: project error %v, evalExprSlot error %v", gotErr, wantErr)
	}
}
