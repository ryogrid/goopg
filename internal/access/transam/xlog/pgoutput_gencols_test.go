package xlog

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// genColsFilter publishes everything; stored reports
// publish_generated_columns = stored.
type genColsFilter struct{ stored bool }

func (genColsFilter) Allows(*RelationDef, ChangeKind) bool         { return true }
func (f genColsFilter) PublishesStoredGenerated(*RelationDef) bool { return f.stored }

// TestPgOutputGeneratedColumns pins logicalrep_should_publish_column: a
// VIRTUAL generated column is never in the Relation message or the tuple;
// a STORED one only under publish_generated_columns = stored.
func TestPgOutputGeneratedColumns(t *testing.T) {
	c := catalog.NewInMemory()
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "int4"}, Ordinal: 1, GeneratedExpr: "a * 2", GeneratedAlways: true},
		{Name: "c", Type: catalog.Type{Name: "int4"}, Ordinal: 2, GeneratedExpr: "a * 3", GeneratedAlways: true, GeneratedVirtual: true},
	}
	tbl, err := c.CreateTable(parser.ObjectName{Name: "g"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	rel := c.RelFileNode(tbl)
	body := encodeBodyV0([]any{1, 2}, []string{"int4", "int4"})
	tuple := wrapAsHeapTuple(t, body, 2) // c (virtual) beyond natts, as stored NULL
	for _, tc := range []struct {
		stored        bool
		names, values string
	}{
		{false, "[a]", "[1]"},
		{true, "[a b]", "[1 2]"},
	} {
		out := &msgWriter{}
		po := NewPgOutput(BuildCatalogSnapshot(c, nil), out)
		po.SetFilter(genColsFilter{tc.stored})
		if err := po.Change(Change{Kind: ChangeInsert, Rel: rel, NewTuple: tuple}); err != nil {
			t.Fatal(err)
		}
		if len(out.msgs) != 2 {
			t.Fatalf("stored=%v: %d messages, want R + I", tc.stored, len(out.msgs))
		}
		r, err := DecodeMessage(out.msgs[0])
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, a := range r.Relation.Columns {
			names = append(names, a.Name)
		}
		ins, err := DecodeMessage(out.msgs[1])
		if err != nil {
			t.Fatal(err)
		}
		var values []string
		for _, v := range ins.NewTuple {
			values = append(values, string(v.Bytes))
		}
		if fmt.Sprint(names) != tc.names || fmt.Sprint(values) != tc.values {
			t.Errorf("stored=%v: relation columns %v values %v, want %s %s", tc.stored, names, values, tc.names, tc.values)
		}
	}
}

// colListFilter publishes everything with a fixed column list.
type colListFilter []string

func (colListFilter) Allows(*RelationDef, ChangeKind) bool       { return true }
func (f colListFilter) ColumnList(*RelationDef) ([]string, bool) { return f, true }
func (colListFilter) PublishesStoredGenerated(*RelationDef) bool { return false }

// TestPgOutputColumnList: a column list takes precedence over
// publish_generated_columns — the listed stored generated column is
// published even under none, and the unlisted ordinary column is not.
func TestPgOutputColumnList(t *testing.T) {
	c := catalog.NewInMemory()
	cols := []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}, Ordinal: 0},
		{Name: "b", Type: catalog.Type{Name: "int4"}, Ordinal: 1, GeneratedExpr: "a * 2", GeneratedAlways: true},
	}
	tbl, err := c.CreateTable(parser.ObjectName{Name: "g"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	out := &msgWriter{}
	po := NewPgOutput(BuildCatalogSnapshot(c, nil), out)
	po.SetFilter(colListFilter{"b"})
	tuple := wrapAsHeapTuple(t, encodeBodyV0([]any{1, 2}, []string{"int4", "int4"}), 2)
	if err := po.Change(Change{Kind: ChangeInsert, Rel: c.RelFileNode(tbl), NewTuple: tuple}); err != nil {
		t.Fatal(err)
	}
	r, _ := DecodeMessage(out.msgs[0])
	ins, _ := DecodeMessage(out.msgs[1])
	if len(r.Relation.Columns) != 1 || r.Relation.Columns[0].Name != "b" || len(ins.NewTuple) != 1 || string(ins.NewTuple[0].Bytes) != "2" {
		t.Fatalf("relation %+v tuple %+v; want only b = 2", r.Relation.Columns, ins.NewTuple)
	}
}
