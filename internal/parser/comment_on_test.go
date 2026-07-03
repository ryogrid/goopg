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

// TestParseCommentOnRule pins the COMMENT ON RULE <name> ON [schema.]table
// branch (DU-002 slice 372) — the same `<name> ON <table>` shape as TRIGGER and
// POLICY. pg_dump's dumpRule re-emits this so a rule comment round-trips.
func TestParseCommentOnRule(t *testing.T) {
	cases := []struct {
		sql        string
		wantSchema string
		wantTable  string
		wantRule   string
	}{
		{"COMMENT ON RULE r_noop ON foo IS 'c'", "", "foo", "r_noop"},
		{"COMMENT ON RULE r_noop ON public.foo IS 'c'", "public", "foo", "r_noop"},
		{"COMMENT ON RULE my_rule ON s.widget IS 'c'", "s", "widget", "my_rule"},
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
		if cs.ObjKind != "rule" {
			t.Errorf("%q: ObjKind=%q, want rule", tc.sql, cs.ObjKind)
		}
		if cs.ObjName.Schema != tc.wantSchema {
			t.Errorf("%q: ObjName.Schema=%q, want %q", tc.sql, cs.ObjName.Schema, tc.wantSchema)
		}
		if cs.ObjName.Name != tc.wantTable {
			t.Errorf("%q: ObjName.Name=%q, want %q", tc.sql, cs.ObjName.Name, tc.wantTable)
		}
		if cs.SubName != tc.wantRule {
			t.Errorf("%q: SubName=%q, want %q", tc.sql, cs.SubName, tc.wantRule)
		}
	}
}

// TestParseCommentOnServer covers COMMENT ON SERVER <name>. Foreign servers are
// top-level, schema-less objects (pg_foreign_server, classoid 1417); pg_dump's
// dumpForeignServer re-emits `COMMENT ON SERVER <name> IS '...'`. Before slice
// 386 the parser had no SERVER branch and silently swallowed the statement.
func TestParseCommentOnServer(t *testing.T) {
	cases := []struct {
		sql      string
		wantName string
	}{
		{"COMMENT ON SERVER goopg_srv IS 'a server comment'", "goopg_srv"},
		{"COMMENT ON SERVER my_server IS 'c'", "my_server"},
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
		if cs.ObjKind != "server" {
			t.Errorf("%q: ObjKind=%q, want server", tc.sql, cs.ObjKind)
		}
		if cs.ObjName.Schema != "" {
			t.Errorf("%q: ObjName.Schema=%q, want empty (server is schema-less)", tc.sql, cs.ObjName.Schema)
		}
		if cs.ObjName.Name != tc.wantName {
			t.Errorf("%q: ObjName.Name=%q, want %q", tc.sql, cs.ObjName.Name, tc.wantName)
		}
	}
}

// TestParseCommentOnForeignDataWrapper covers COMMENT ON FOREIGN DATA WRAPPER,
// the sibling of COMMENT ON SERVER. FOREIGN is a reserved keyword and DATA/
// WRAPPER are unreserved ident-keywords; an FDW is a top-level (schema-less)
// object. DU-002 slice 387.
func TestParseCommentOnForeignDataWrapper(t *testing.T) {
	cases := []struct {
		sql      string
		wantName string
	}{
		{"COMMENT ON FOREIGN DATA WRAPPER goopg_fdw IS 'a fdw comment'", "goopg_fdw"},
		{"COMMENT ON FOREIGN DATA WRAPPER my_fdw IS 'c'", "my_fdw"},
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
		if cs.ObjKind != "foreign data wrapper" {
			t.Errorf("%q: ObjKind=%q, want foreign data wrapper", tc.sql, cs.ObjKind)
		}
		if cs.ObjName.Schema != "" {
			t.Errorf("%q: ObjName.Schema=%q, want empty (FDW is schema-less)", tc.sql, cs.ObjName.Schema)
		}
		if cs.ObjName.Name != tc.wantName {
			t.Errorf("%q: ObjName.Name=%q, want %q", tc.sql, cs.ObjName.Name, tc.wantName)
		}
	}
}

// TestParseCommentOnForeignTable covers COMMENT ON FOREIGN TABLE <name>. Foreign
// tables are pg_class relations (relkind='f') sharing goopg's ordinary table
// registry; pg_dump's dumpTableSchema re-emits `COMMENT ON FOREIGN TABLE <name>
// IS '...'`. Before slice 435 the "FOREIGN" branch only recognized "FOREIGN DATA
// WRAPPER" and any other continuation (including TABLE) was a hard parse error
// (not merely a silent drop). DU-002 slice 435.
func TestParseCommentOnForeignTable(t *testing.T) {
	cases := []struct {
		sql        string
		wantSchema string
		wantName   string
	}{
		{"COMMENT ON FOREIGN TABLE ft1 IS 'a foreign table comment'", "", "ft1"},
		{"COMMENT ON FOREIGN TABLE public.ft2 IS 'c'", "public", "ft2"},
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
		if cs.ObjKind != "foreign table" {
			t.Errorf("%q: ObjKind=%q, want foreign table", tc.sql, cs.ObjKind)
		}
		if cs.ObjName.Schema != tc.wantSchema {
			t.Errorf("%q: ObjName.Schema=%q, want %q", tc.sql, cs.ObjName.Schema, tc.wantSchema)
		}
		if cs.ObjName.Name != tc.wantName {
			t.Errorf("%q: ObjName.Name=%q, want %q", tc.sql, cs.ObjName.Name, tc.wantName)
		}
	}
}

// TestParseCommentOnExtension covers COMMENT ON EXTENSION <name>. Extensions are
// top-level, schema-less objects (pg_extension, classoid 3079); pg_dump's
// dumpExtension re-emits `COMMENT ON EXTENSION <name> IS '...'`. EXTENSION is an
// unreserved ident-keyword. Before slice 388 the parser had no EXTENSION branch
// and silently swallowed the statement. DU-002 slice 388.
func TestParseCommentOnExtension(t *testing.T) {
	cases := []struct {
		sql      string
		wantName string
	}{
		{"COMMENT ON EXTENSION amcheck IS 'an extension comment'", "amcheck"},
		{"COMMENT ON EXTENSION my_ext IS 'c'", "my_ext"},
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
		if cs.ObjKind != "extension" {
			t.Errorf("%q: ObjKind=%q, want extension", tc.sql, cs.ObjKind)
		}
		if cs.ObjName.Schema != "" {
			t.Errorf("%q: ObjName.Schema=%q, want empty (extension is schema-less)", tc.sql, cs.ObjName.Schema)
		}
		if cs.ObjName.Name != tc.wantName {
			t.Errorf("%q: ObjName.Name=%q, want %q", tc.sql, cs.ObjName.Name, tc.wantName)
		}
	}
}

// TestParseCommentOnCollation covers COMMENT ON COLLATION [schema.]name.
// Collations live in pg_collation (classoid 3456); pg_dump's dumpCollation
// re-emits `COMMENT ON COLLATION <schema>.<name> IS '...'`. COLLATION is an
// unreserved ident-keyword and a user collation is schema-qualifiable. Before
// slice 390 the parser had no COLLATION branch and silently swallowed the
// statement. DU-002 slice 390.
func TestParseCommentOnCollation(t *testing.T) {
	cases := []struct {
		sql        string
		wantSchema string
		wantName   string
	}{
		{"COMMENT ON COLLATION mycoll IS 'a collation comment'", "", "mycoll"},
		{"COMMENT ON COLLATION public.mycoll IS 'c'", "public", "mycoll"},
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
		if cs.ObjKind != "collation" {
			t.Errorf("%q: ObjKind=%q, want collation", tc.sql, cs.ObjKind)
		}
		if cs.ObjName.Schema != tc.wantSchema {
			t.Errorf("%q: ObjName.Schema=%q, want %q", tc.sql, cs.ObjName.Schema, tc.wantSchema)
		}
		if cs.ObjName.Name != tc.wantName {
			t.Errorf("%q: ObjName.Name=%q, want %q", tc.sql, cs.ObjName.Name, tc.wantName)
		}
	}
}


// TestParseCommentOnCast covers COMMENT ON CAST (source AS target). Casts live
// in pg_cast (classoid 2605); pg_dump's dumpCast re-emits
// `COMMENT ON CAST (<source> AS <target>) IS '...'`. The identity is the
// (source, target) type pair, captured into CastSource/CastTarget — not a name.
// "cast" is an unreserved ident-keyword. Before slice 396 the parser had no CAST
// branch and silently swallowed the statement. DU-002 slice 396.
func TestParseCommentOnCast(t *testing.T) {
	cases := []struct {
		sql        string
		wantSource string
		wantTarget string
	}{
		{"COMMENT ON CAST (text AS bytea) IS 'a cast comment'", "text", "bytea"},
		{"COMMENT ON CAST (integer AS bigint) IS 'c'", "integer", "bigint"},
		{"COMMENT ON CAST (character varying AS text) IS 'cv'", "character varying", "text"},
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
		if cs.ObjKind != "cast" {
			t.Errorf("%q: ObjKind=%q, want cast", tc.sql, cs.ObjKind)
		}
		if cs.CastSource != tc.wantSource {
			t.Errorf("%q: CastSource=%q, want %q", tc.sql, cs.CastSource, tc.wantSource)
		}
		if cs.CastTarget != tc.wantTarget {
			t.Errorf("%q: CastTarget=%q, want %q", tc.sql, cs.CastTarget, tc.wantTarget)
		}
	}
}

// TestParseCommentOnAccessMethod covers COMMENT ON ACCESS METHOD, the sibling
// of COMMENT ON SERVER/FOREIGN DATA WRAPPER. Access methods live in pg_am
// (classoid 2601); pg_dump's dumpAccessMethod re-emits `COMMENT ON ACCESS
// METHOD <name> IS '...'`. "ACCESS" is unreserved and unregistered as a
// keyword (matched via the ident path, same as CREATE ACCESS METHOD); an
// access method is a top-level (schema-less) object. Before slice 434 the
// parser had no "access method" branch and silently swallowed the statement
// (it fell into parseCommentOnTail's unsupported-type default, so the
// comment never reached pg_description). DU-002 slice 434.
func TestParseCommentOnAccessMethod(t *testing.T) {
	cases := []struct {
		sql      string
		wantName string
	}{
		{"COMMENT ON ACCESS METHOD goopg_am IS 'an access method comment'", "goopg_am"},
		{"COMMENT ON ACCESS METHOD my_am IS 'c'", "my_am"},
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
		if cs.ObjKind != "access method" {
			t.Errorf("%q: ObjKind=%q, want \"access method\"", tc.sql, cs.ObjKind)
		}
		if cs.ObjName.Schema != "" {
			t.Errorf("%q: ObjName.Schema=%q, want empty (access method is schema-less)", tc.sql, cs.ObjName.Schema)
		}
		if cs.ObjName.Name != tc.wantName {
			t.Errorf("%q: ObjName.Name=%q, want %q", tc.sql, cs.ObjName.Name, tc.wantName)
		}
	}
}

// TestParseCommentOnAccessMethodMissingMethodKeyword covers the error path
// when METHOD is omitted after ACCESS.
func TestParseCommentOnAccessMethodMissingMethodKeyword(t *testing.T) {
	_, err := Parse("COMMENT ON ACCESS goopg_am IS 'x'")
	if err == nil {
		t.Fatalf("expected parse error for COMMENT ON ACCESS without METHOD, got nil")
	}
}
