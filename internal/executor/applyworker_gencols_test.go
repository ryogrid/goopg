package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
)

// TestApplyColumnMapRejectsGeneratedTarget pins logicalrep_rel_open's
// generated-column check: a replicated column that lands on a generated
// subscriber column is refused with PG's 55000 message, every such column
// listed in remote order.
func TestApplyColumnMapRejectsGeneratedTarget(t *testing.T) {
	local := &catalog.Table{Name: "t1", Columns: []catalog.Column{
		{Name: "c1", Ordinal: 0},
		{Name: "c2", Ordinal: 1, GeneratedExpr: "c1 + 2", GeneratedAlways: true},
		{Name: "c3", Ordinal: 2, GeneratedExpr: "c1 + 2", GeneratedAlways: true},
	}}
	r := &applyRel{remote: &xlog.DecodedRelation{Schema: "public", Name: "t1",
		Columns: []xlog.DecodedAttr{{Name: "c1"}, {Name: "c2"}, {Name: "c3"}}}, local: local}
	_, _, err := r.columnMap()
	want := `logical replication target relation "public.t1" has incompatible generated columns: "c2", "c3"`
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "55000" || ee.Message != want {
		t.Fatalf("columnMap error = %v, want 55000 %s", err, want)
	}
	r = &applyRel{remote: &xlog.DecodedRelation{Name: "t1", Columns: []xlog.DecodedAttr{{Name: "c1"}}}, local: local}
	if _, _, err := r.columnMap(); err != nil {
		t.Fatalf("a remote relation without the generated columns must map: %v", err)
	}
	if err := IncompatibleGeneratedColumnsError("s", "t", []string{"g"}); err == nil ||
		err.(*ExecError).Message != `logical replication target relation "s.t" has incompatible generated column: "g"` {
		t.Fatalf("singular form = %v", err)
	}
}

// TestComputeGeneratedColumnsOverwritesReplicatedRow: the apply worker
// recomputes the subscriber's own generated values from the replicated row.
func TestComputeGeneratedColumnsOverwritesReplicatedRow(t *testing.T) {
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "int4"}, Ordinal: 1, GeneratedExpr: "a * 22", GeneratedAlways: true},
	}
	row := Row{NewIntDatum(4), NullDatum}
	_ = computeGeneratedColumns(cols, row)
	if row[1].IsNull() || row[1].Int != 88 {
		t.Fatalf("b = %v, want 88", row[1])
	}
}
