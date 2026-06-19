package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPlanSelectFromVirtualPgClass: a SELECT against pg_catalog.pg_class
// should plan as Project(Filter(Values)) — the virtual table emits a
// materialised Values node populated from the in-memory catalog
// state at plan time, with the WHERE predicate and the projection
// applied on top.
//
// pgbench probes `SELECT relkind FROM pg_catalog.pg_class WHERE
// oid=$1::pg_catalog.regclass` to learn whether a relation is
// partitioned; the v0 virtual view stores the relname in the oid
// column so the bound text parameter compares equal.
func TestPlanSelectFromVirtualPgClass(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "pgbench_accounts"}, []catalog.Column{
		{Name: "aid", Type: catalog.Type{Name: "int4"}, NotNull: true},
	}); err != nil {
		t.Fatal(err)
	}

	stmt := parseOne(t, `SELECT relkind FROM pg_catalog.pg_class WHERE oid = $1::pg_catalog.regclass`)
	node, err := Plan(stmt, cat)
	if err != nil {
		t.Fatal(err)
	}
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("root=%T want *Project", node)
	}
	filt, ok := proj.Child.(*Filter)
	if !ok {
		t.Fatalf("Project.Child=%T want *Filter", proj.Child)
	}
	v, ok := filt.Child.(*Values)
	if !ok {
		t.Fatalf("Filter.Child=%T want *Values", filt.Child)
	}
	if len(v.Rows) < 1 {
		t.Errorf("rows=%d want ≥1 (at least the user table seeded)", len(v.Rows))
	}
	// Schema must list the four pg_class columns we expose so the
	// WHERE/projection above can resolve them by name.
	names := make([]string, 0, len(v.schema))
	for _, sc := range v.schema {
		names = append(names, sc.Name)
	}
	wantNames := map[string]bool{"oid": false, "relname": false, "relkind": false, "relnamespace": false}
	for _, n := range names {
		wantNames[n] = true
	}
	for k, seen := range wantNames {
		if !seen {
			t.Errorf("schema missing %q (got %v)", k, names)
		}
	}
}

// TestTypedVirtualCell verifies that virtual catalog-table cells are typed
// per column kind so integer-family and boolean columns compare/aggregate by
// value rather than lexicographically. Regression for the sysviews
// pg_backend_memory_contexts `total_bytes >= free_bytes` case, where text
// comparison of "1048576" >= "524288" wrongly yielded false. Both
// planner.buildVirtualValues and executor.rematerialiseVirtualRows route
// through this helper and must agree.
func TestTypedVirtualCell(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		colType string
		want    Expr
	}{
		{"int8 numeric", "1048576", "int8", &IntegerConst{Value: 1048576}},
		{"int4 numeric", "-5", "int4", &IntegerConst{Value: -5}},
		{"integer alias", "42", "integer", &IntegerConst{Value: 42}},
		{"bigint alias", "9000000000", "bigint", &IntegerConst{Value: 9000000000}},
		{"bool true", "t", "bool", &BooleanConst{Value: true}},
		{"bool false", "f", "boolean", &BooleanConst{Value: false}},
		{"text stays string", "TopMemoryContext", "text", &StringConst{Value: "TopMemoryContext"}},
		{"non-numeric int falls back", "", "int8", &StringConst{Value: ""}},
		{"non-bool bool falls back", "maybe", "bool", &StringConst{Value: "maybe"}},
		// Array-typed empty cells denote SQL NULL, not a 1-element empty-string
		// array — see DU-002 slice 47 (the spurious pg_dump `WITH (""='')`).
		{"empty text[] is NULL", "", "text[]", &NullConst{}},
		{"empty aclitem[] is NULL", "", "aclitem[]", &NullConst{}},
		{"empty oid[] is NULL", "", "oid[]", &NullConst{}},
		{"empty _text is NULL", "", "_text", &NullConst{}},
		// A non-empty array cell passes through as the array text literal.
		{"non-empty text[] passes through", "{a,b}", "text[]", &StringConst{Value: "{a,b}"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TypedVirtualCell(0, c.value, c.colType)
			switch want := c.want.(type) {
			case *IntegerConst:
				g, ok := got.(*IntegerConst)
				if !ok || g.Value != want.Value {
					t.Fatalf("got %#v, want IntegerConst{%d}", got, want.Value)
				}
			case *BooleanConst:
				g, ok := got.(*BooleanConst)
				if !ok || g.Value != want.Value {
					t.Fatalf("got %#v, want BooleanConst{%v}", got, want.Value)
				}
			case *NullConst:
				if _, ok := got.(*NullConst); !ok {
					t.Fatalf("got %#v, want NullConst", got)
				}
			case *StringConst:
				g, ok := got.(*StringConst)
				if !ok || g.Value != want.Value {
					t.Fatalf("got %#v, want StringConst{%q}", got, want.Value)
				}
			}
		})
	}
}
