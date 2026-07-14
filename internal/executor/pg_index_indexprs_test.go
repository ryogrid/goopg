package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPgIndexIndexprsExpressionIndex covers the live pg_index renderer
// (catalog.InMemory.PGIndexRowsForDBOid) for the pg_node_tree column indexprs
// on expression indexes. Before this fix indexprs was hardcoded to VirtualNull
// for every index, so pg_get_expr(indexprs, indrelid) returned NULL even for an
// expression index such as `CREATE INDEX ... (lower(b))`. Real PostgreSQL
// stores the serialized expression list there and pg_get_expr decompiles it to
// the deparsed expression text of the expression key columns only (plain and
// INCLUDE columns are omitted), comma-joined.
//
// The expected byte-for-byte output was captured from PostgreSQL 18.3:
//
//	CREATE INDEX i ON zz (lower(b));        pg_get_expr(indexprs) => lower(b)
//	CREATE INDEX i ON zz ((a+c), upper(b)); pg_get_expr(indexprs) => (a + c), upper(b)
//	CREATE INDEX i ON zz (a, (a*c));        pg_get_expr(indexprs) => (a * c)
//	CREATE INDEX i ON zz (a);               pg_get_expr(indexprs) => NULL
func TestPgIndexIndexprsExpressionIndex(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, ddl := range []string{
		"CREATE TABLE idxexpr_t (a int4, b text, c int4)",
		"CREATE INDEX idxexpr_lower ON idxexpr_t (lower(b))",
		"CREATE INDEX idxexpr_multi ON idxexpr_t ((a+c), upper(b))",
		"CREATE INDEX idxexpr_mixed ON idxexpr_t (a, (a*c))",
		"CREATE INDEX idxexpr_plain ON idxexpr_t (a)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}

	im := ctx.Catalog.(*catalog.InMemory)
	// want maps index OID -> expected pg_get_expr(indexprs) text ("" means SQL NULL).
	want := map[uint32]string{}
	for _, idx := range im.AllIndexes(catalog.DefaultDBOid) {
		switch idx.Name {
		case "idxexpr_lower":
			want[idx.OID] = "lower(b)"
		case "idxexpr_multi":
			want[idx.OID] = "(a + c), upper(b)"
		case "idxexpr_mixed":
			want[idx.OID] = "(a * c)"
		case "idxexpr_plain":
			want[idx.OID] = "" // NULL
		}
	}
	if len(want) != 4 {
		t.Fatalf("expected 4 target indexes, found %d", len(want))
	}

	rows := runQueryUnderDBOid(t, ctx,
		"SELECT indexrelid, pg_get_expr(indexprs, indrelid) FROM pg_index")

	seen := map[uint32]bool{}
	for _, r := range rows {
		if len(r) < 2 {
			t.Fatalf("row has %d cols, want 2", len(r))
		}
		oid := uint32(r[0].Int)
		expected, ok := want[oid]
		if !ok {
			continue
		}
		seen[oid] = true
		if expected == "" {
			if !r[1].IsNull() {
				t.Errorf("plain index (oid=%d) pg_get_expr(indexprs) = %q, want NULL", oid, r[1].StringValue())
			}
			continue
		}
		if r[1].IsNull() {
			t.Errorf("expression index (oid=%d) pg_get_expr(indexprs) is NULL, want %q", oid, expected)
		} else if got := r[1].StringValue(); got != expected {
			t.Errorf("expression index (oid=%d) pg_get_expr(indexprs) = %q, want %q", oid, got, expected)
		}
	}
	for oid := range want {
		if !seen[oid] {
			t.Errorf("index oid=%d missing from pg_index", oid)
		}
	}
}

// TestIndexExprsTextParenAndNullRules is a direct unit test on the shared
// helper catalog.IndexExprsText so a refactor cannot silently reintroduce the
// double-paren bug (wrapping an already-deparsed (a + c) into ((a + c))) or
// change the NULL-vs-present contract that the two pg_index renderers depend on.
func TestIndexExprsTextParenAndNullRules(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, ddl := range []string{
		"CREATE TABLE idxexpr2_t (a int4, b text, c int4)",
		"CREATE INDEX idxexpr2_lower ON idxexpr2_t (lower(b))",
		"CREATE INDEX idxexpr2_multi ON idxexpr2_t ((a+c), upper(b))",
		"CREATE INDEX idxexpr2_plain ON idxexpr2_t (a)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	im := ctx.Catalog.(*catalog.InMemory)
	for _, idx := range im.AllIndexes(catalog.DefaultDBOid) {
		txt, ok := catalog.IndexExprsText(idx)
		switch idx.Name {
		case "idxexpr2_lower":
			if !ok || txt != "lower(b)" {
				t.Errorf("lower(b): IndexExprsText = (%q, %v), want (\"lower(b)\", true)", txt, ok)
			}
		case "idxexpr2_multi":
			// A binary expression keeps its single wrapping parens; a bare func
			// call has none — no extra wrapping is added on top.
			if !ok || txt != "(a + c), upper(b)" {
				t.Errorf("multi: IndexExprsText = (%q, %v), want (\"(a + c), upper(b)\", true)", txt, ok)
			}
		case "idxexpr2_plain":
			if ok || txt != "" {
				t.Errorf("plain: IndexExprsText = (%q, %v), want (\"\", false)", txt, ok)
			}
		}
	}
}
