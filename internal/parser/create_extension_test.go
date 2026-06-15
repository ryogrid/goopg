package parser

import "testing"

// TestParseCreateExtension pins the CREATE EXTENSION shapes pg_amcheck's
// 002_nonesuch TAP test relies on: the bare form, IF NOT EXISTS, and the
// option list (SCHEMA / VERSION / CASCADE, any order). M0110-0003.
func TestParseCreateExtension(t *testing.T) {
	cases := []struct {
		sql         string
		name        string
		ifNotExists bool
		schema      string
		version     string
		cascade     bool
	}{
		{sql: "CREATE EXTENSION amcheck", name: "amcheck"},
		{sql: "CREATE EXTENSION IF NOT EXISTS amcheck", name: "amcheck", ifNotExists: true},
		{sql: "CREATE EXTENSION amcheck WITH SCHEMA public", name: "amcheck", schema: "public"},
		{sql: "CREATE EXTENSION amcheck SCHEMA myschema VERSION '1.4'", name: "amcheck", schema: "myschema", version: "1.4"},
		{sql: "CREATE EXTENSION IF NOT EXISTS amcheck WITH VERSION '1.4' CASCADE", name: "amcheck", ifNotExists: true, version: "1.4", cascade: true},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			ce, ok := stmts[0].(*CreateExtensionStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *CreateExtensionStmt", stmts[0])
			}
			if ce.Name != tc.name {
				t.Errorf("Name = %q, want %q", ce.Name, tc.name)
			}
			if ce.IfNotExists != tc.ifNotExists {
				t.Errorf("IfNotExists = %v, want %v", ce.IfNotExists, tc.ifNotExists)
			}
			if ce.Schema != tc.schema {
				t.Errorf("Schema = %q, want %q", ce.Schema, tc.schema)
			}
			if ce.Version != tc.version {
				t.Errorf("Version = %q, want %q", ce.Version, tc.version)
			}
			if ce.Cascade != tc.cascade {
				t.Errorf("Cascade = %v, want %v", ce.Cascade, tc.cascade)
			}
		})
	}
}
