package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPublicationRowFilterDDL pins M0149-0059c against PG 18.3's
// publication_add_relation / pg_get_publication_tables:
//   - `FOR TABLE t WHERE (expr)` stores the filter, and pg_get_expr over
//     pg_get_publication_tables' qual returns it;
//   - `FOR TABLE parent` without ONLY publishes the inheritance children
//     with the same filter (OpenTableList's recursion); ONLY does not;
//   - a filter naming an unknown column fails the statement;
//   - ALTER ... SET TABLE replaces the filter, and a table listed without
//     one loses it;
//   - DROP TABLE with a WHERE clause is PG's 42601.
func TestPublicationRowFilterDDL(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()
	for _, q := range []string{
		"CREATE TABLE rf_t (a int, b text)",
		"CREATE TABLE rf_parent (a int)",
		"CREATE TABLE rf_child (b text) INHERITS (rf_parent)",
		"CREATE TABLE rf_only (a int)",
		"CREATE TABLE rf_only_child (b text) INHERITS (rf_only)",
		"CREATE PUBLICATION rf_p FOR TABLE rf_t WHERE (a > 10 AND b <> 'x'), rf_parent WHERE (a > 15), ONLY rf_only",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	quals := func() map[string]string {
		out := map[string]string{}
		for _, r := range runQueryRows(t, ctx, "SELECT c.relname, pg_get_expr(gpt.qual, gpt.relid) FROM pg_get_publication_tables('rf_p') gpt JOIN pg_class c ON c.oid = gpt.relid") {
			q := "<null>"
			if !r[1].IsNull() {
				q = r[1].StringValue()
			}
			out[r[0].StringValue()] = q
		}
		return out
	}
	got := quals()
	want := map[string]string{
		"rf_t":      "((a > 10) AND (b <> 'x'))",
		"rf_parent": "(a > 15)",
		"rf_child":  "(a > 15)",
		"rf_only":   "<null>",
	}
	if len(got) != len(want) {
		t.Errorf("published tables = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("qual of %s = %q, want %q", k, got[k], v)
		}
	}

	if err := runDDL(t, ctx, "CREATE PUBLICATION rf_bad FOR TABLE rf_t WHERE (nosuch > 1)"); err == nil {
		t.Error("a filter over an unknown column was accepted")
	}
	if _, ok := ctx.PubSub.LookupPublication("rf_bad"); ok {
		t.Error("the failed CREATE left the publication behind")
	}

	if err := runDDL(t, ctx, "ALTER PUBLICATION rf_p SET TABLE rf_t WHERE (a < 5), ONLY rf_only"); err != nil {
		t.Fatal(err)
	}
	got = quals()
	if got["rf_t"] != "(a < 5)" || got["rf_only"] != "<null>" || len(got) != 2 {
		t.Errorf("after SET: %v", got)
	}
	if err := runDDL(t, ctx, "ALTER PUBLICATION rf_p SET TABLE rf_t"); err != nil {
		t.Fatal(err)
	}
	if got = quals(); got["rf_t"] != "<null>" {
		t.Errorf("SET without WHERE kept the filter: %v", got)
	}
	err := runDDL(t, ctx, "ALTER PUBLICATION rf_p DROP TABLE rf_t WHERE (a > 1)")
	if ee, ok := err.(*ExecError); !ok || ee.Code != "42601" {
		t.Errorf("DROP ... WHERE: err = %v, want 42601", err)
	}
	_ = cat
}

// TestEvalPublicationRowFilter pins pgoutput_row_filter_exec_expr's result
// rules: the filters are OR'ed, a NULL result does not publish, values are
// bound through their type's input, and a virtual generated column is
// computed from its expression (PG expands it before evaluating).
func TestEvalPublicationRowFilter(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE rf_v (id int PRIMARY KEY, x int, y int GENERATED ALWAYS AS (x * 2) VIRTUAL)"); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "rf_v"})
	if !ok {
		t.Fatal("rf_v not found")
	}
	cols := []RowFilterColumn{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "x", Type: catalog.Type{Name: "int4"}},
		{Name: "y", Type: catalog.Type{Name: "int4"}},
	}
	s := func(v string) *string { return &v }
	for _, tc := range []struct {
		quals []string
		vals  []*string
		want  bool
	}{
		{[]string{"(x > 10)"}, []*string{s("1"), s("11"), nil}, true},
		{[]string{"(x > 10)"}, []*string{s("1"), s("10"), nil}, false},
		{[]string{"(x > 10)"}, []*string{s("1"), nil, nil}, false}, // NULL result
		{[]string{"(x > 10)", "(id = 1)"}, []*string{s("1"), s("3"), nil}, true},
		{[]string{"(y > 10)"}, []*string{s("1"), s("6"), nil}, true}, // y = 12
		{[]string{"(y > 10)"}, []*string{s("1"), s("5"), nil}, false},
	} {
		got, err := EvalPublicationRowFilter(cat, 0, tbl.OID, cols, tc.quals, tc.vals)
		if err != nil {
			t.Fatalf("%v: %v", tc.quals, err)
		}
		if got != tc.want {
			t.Errorf("%v over %v = %v, want %v", tc.quals, tc.vals, got, tc.want)
		}
	}
}
