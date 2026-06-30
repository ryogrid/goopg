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

// TestParseCommentOnTrigger guards COMMENT ON TRIGGER <name> ON [schema.]table
// parsing. pg_dump's dumpTrigger emits exactly this shape (`COMMENT ON TRIGGER
// <name> ON <schema>.<table> IS '...'`), so it must parse for a dump→restore
// round-trip. Before DU-002 slice 370 the TRIGGER object kind fell through to
// the unsupported-default branch and the comment was silently dropped from
// pg_description. The trigger name is captured in SubName and the ON-table in
// ObjName, mirroring COMMENT ON CONSTRAINT.
func TestParseCommentOnTrigger(t *testing.T) {
	cases := []struct {
		sql         string
		wantSchema  string
		wantTable   string
		wantTrigger string
	}{
		{"COMMENT ON TRIGGER trg_biu ON foo IS 'c'", "", "foo", "trg_biu"},
		{"COMMENT ON TRIGGER trg_biu ON public.foo IS 'c'", "public", "foo", "trg_biu"},
		{"COMMENT ON TRIGGER my_trg ON s.widget IS 'c'", "s", "widget", "my_trg"},
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
		if cs.ObjKind != "trigger" {
			t.Errorf("%q: ObjKind=%q, want trigger", tc.sql, cs.ObjKind)
		}
		if cs.ObjName.Schema != tc.wantSchema {
			t.Errorf("%q: ObjName.Schema=%q, want %q", tc.sql, cs.ObjName.Schema, tc.wantSchema)
		}
		if cs.ObjName.Name != tc.wantTable {
			t.Errorf("%q: ObjName.Name=%q, want %q", tc.sql, cs.ObjName.Name, tc.wantTable)
		}
		if cs.SubName != tc.wantTrigger {
			t.Errorf("%q: SubName=%q, want %q", tc.sql, cs.SubName, tc.wantTrigger)
		}
	}
}

// TestParseCommentOnPolicy covers `COMMENT ON POLICY <name> ON [schema.]table`,
// the shape pg_dump's dumpPolicy emits. SubName is the policy name; ObjName is
// the table the policy is attached to. DU-002 slice 371.
func TestParseCommentOnPolicy(t *testing.T) {
	cases := []struct {
		sql        string
		wantSchema string
		wantTable  string
		wantPolicy string
	}{
		{"COMMENT ON POLICY p_simple ON foo IS 'c'", "", "foo", "p_simple"},
		{"COMMENT ON POLICY p_simple ON public.foo IS 'c'", "public", "foo", "p_simple"},
		{"COMMENT ON POLICY my_pol ON s.widget IS 'c'", "s", "widget", "my_pol"},
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
		if cs.ObjKind != "policy" {
			t.Errorf("%q: ObjKind=%q, want policy", tc.sql, cs.ObjKind)
		}
		if cs.ObjName.Schema != tc.wantSchema {
			t.Errorf("%q: ObjName.Schema=%q, want %q", tc.sql, cs.ObjName.Schema, tc.wantSchema)
		}
		if cs.ObjName.Name != tc.wantTable {
			t.Errorf("%q: ObjName.Name=%q, want %q", tc.sql, cs.ObjName.Name, tc.wantTable)
		}
		if cs.SubName != tc.wantPolicy {
			t.Errorf("%q: SubName=%q, want %q", tc.sql, cs.SubName, tc.wantPolicy)
		}
	}
}
