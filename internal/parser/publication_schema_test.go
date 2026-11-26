package parser

import "testing"

// TestParsePublicationTablesInSchema covers gram.y's pub_obj_list TABLES IN
// SCHEMA items (M0149-0053a): mixed with TABLE items, a bare item after a
// comma continuing the previous kind, CURRENT_SCHEMA, and the ALTER
// PUBLICATION ADD / SET / DROP forms.
func TestParsePublicationTablesInSchema(t *testing.T) {
	stmts, err := Parse("CREATE PUBLICATION p FOR TABLES IN SCHEMA sch1, sch2, TABLE public.t1, t2, TABLES IN SCHEMA CURRENT_SCHEMA")
	if err != nil {
		t.Fatal(err)
	}
	cp, ok := stmts[0].(*CreatePublicationStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if want := []string{"sch1", "sch2", PubCurrentSchema}; len(cp.Schemas) != 3 || cp.Schemas[0] != want[0] || cp.Schemas[1] != want[1] || cp.Schemas[2] != want[2] {
		t.Fatalf("Schemas = %q, want %q", cp.Schemas, want)
	}
	if len(cp.Tables) != 2 || cp.Tables[0].Name != "t1" || cp.Tables[1].Name != "t2" {
		t.Fatalf("Tables = %+v", cp.Tables)
	}
	for _, c := range []struct {
		sql, action string
		schemas     int
		tables      int
	}{
		{"ALTER PUBLICATION p ADD TABLES IN SCHEMA sch1", "add", 1, 0},
		{"ALTER PUBLICATION p DROP TABLES IN SCHEMA sch1, sch2", "drop", 2, 0},
		{"ALTER PUBLICATION p SET TABLE t1, TABLES IN SCHEMA sch1", "set", 1, 1},
	} {
		stmts, err := Parse(c.sql)
		if err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		ap, ok := stmts[0].(*AlterPublicationTablesStmt)
		if !ok {
			t.Fatalf("%s: got %T", c.sql, stmts[0])
		}
		if ap.Action != c.action || len(ap.Schemas) != c.schemas || len(ap.Tables) != c.tables {
			t.Errorf("%s: action %q schemas %q tables %+v", c.sql, ap.Action, ap.Schemas, ap.Tables)
		}
	}
	if _, err := Parse("CREATE PUBLICATION p FOR TABLES sch1"); err == nil {
		t.Error("FOR TABLES without IN SCHEMA parsed")
	}
}
