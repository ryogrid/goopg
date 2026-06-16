package parser

import "testing"

// TestParseCommentOnColumn guards COMMENT ON COLUMN name parsing for both the
// bare 2-part form (table.col) and the schema-qualified 3-part form
// (schema.table.col). The 3-part form is the one pg_dump itself emits, so it
// must parse for a dump→restore round-trip. Before DU-002 slice 55 the 3-part
// form failed with "expected IS after object name" because parseObjectName
// consumes only two dotted parts and the column case never read the trailing
// ".col"; the resulting parse error was silently swallowed by the server's
// COMMENT fallback, so the column comment was dropped from pg_description.
func TestParseCommentOnColumn(t *testing.T) {
	cases := []struct {
		sql        string
		wantSchema string
		wantTable  string
		wantColumn string
	}{
		{"COMMENT ON COLUMN foo.name IS 'c'", "", "foo", "name"},
		{"COMMENT ON COLUMN public.foo.name IS 'c'", "public", "foo", "name"},
		{"COMMENT ON COLUMN s.widget.label IS 'c'", "s", "widget", "label"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("%q: unexpected parse error: %v", tc.sql, err)
			continue
		}
		if len(stmts) != 1 {
			t.Errorf("%q: got %d stmts, want 1", tc.sql, len(stmts))
			continue
		}
		cs, ok := stmts[0].(*CommentOnStmt)
		if !ok {
			t.Errorf("%q: got %T, want *CommentOnStmt", tc.sql, stmts[0])
			continue
		}
		if cs.ObjKind != "column" {
			t.Errorf("%q: ObjKind=%q, want column", tc.sql, cs.ObjKind)
		}
		if cs.ObjName.Schema != tc.wantSchema {
			t.Errorf("%q: ObjName.Schema=%q, want %q", tc.sql, cs.ObjName.Schema, tc.wantSchema)
		}
		if cs.ObjName.Name != tc.wantTable {
			t.Errorf("%q: ObjName.Name=%q, want %q", tc.sql, cs.ObjName.Name, tc.wantTable)
		}
		if cs.SubName != tc.wantColumn {
			t.Errorf("%q: SubName=%q, want %q", tc.sql, cs.SubName, tc.wantColumn)
		}
	}
}
