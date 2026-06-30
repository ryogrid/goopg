package parser

import "testing"

// TestParseCreateCollation pins the CREATE COLLATION shapes goopg records in
// the runtime pg_collation registry so pg_dump round-trips them: the option-list
// form (LOCALE / LC_COLLATE+LC_CTYPE / PROVIDER / DETERMINISTIC), IF NOT EXISTS,
// a schema-qualified name, and the FROM-existing form. DU-002 (M0119-0004).
func TestParseCreateCollation(t *testing.T) {
	cases := []struct {
		sql           string
		schema        string
		name          string
		ifNotExists   bool
		provider      string
		locale        string
		lcCollate     string
		lcCtype       string
		deterministic string
		from          string
	}{
		{sql: "CREATE COLLATION mycoll (LOCALE = 'C')", name: "mycoll", locale: "C"},
		{sql: "CREATE COLLATION public.mycoll (LOCALE = 'C')", schema: "public", name: "mycoll", locale: "C"},
		{sql: "CREATE COLLATION IF NOT EXISTS mycoll (LOCALE = 'C')", name: "mycoll", ifNotExists: true, locale: "C"},
		{sql: "CREATE COLLATION german (LC_COLLATE = 'de_DE', LC_CTYPE = 'de_DE')", name: "german", lcCollate: "de_DE", lcCtype: "de_DE"},
		{sql: "CREATE COLLATION ci (provider = icu, locale = 'und', deterministic = false)", name: "ci", provider: "icu", locale: "und", deterministic: "false"},
		{sql: "CREATE COLLATION dup FROM \"C\"", name: "dup", from: "C"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			cc, ok := stmts[0].(*CreateCollationStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *CreateCollationStmt", stmts[0])
			}
			if cc.Name.Schema != tc.schema {
				t.Errorf("Schema = %q, want %q", cc.Name.Schema, tc.schema)
			}
			if cc.Name.Name != tc.name {
				t.Errorf("Name = %q, want %q", cc.Name.Name, tc.name)
			}
			if cc.IfNotExists != tc.ifNotExists {
				t.Errorf("IfNotExists = %v, want %v", cc.IfNotExists, tc.ifNotExists)
			}
			if cc.Provider != tc.provider {
				t.Errorf("Provider = %q, want %q", cc.Provider, tc.provider)
			}
			if cc.Locale != tc.locale {
				t.Errorf("Locale = %q, want %q", cc.Locale, tc.locale)
			}
			if cc.LcCollate != tc.lcCollate {
				t.Errorf("LcCollate = %q, want %q", cc.LcCollate, tc.lcCollate)
			}
			if cc.LcCtype != tc.lcCtype {
				t.Errorf("LcCtype = %q, want %q", cc.LcCtype, tc.lcCtype)
			}
			if cc.Deterministic != tc.deterministic {
				t.Errorf("Deterministic = %q, want %q", cc.Deterministic, tc.deterministic)
			}
			if cc.FromName.Name != tc.from {
				t.Errorf("FromName = %q, want %q", cc.FromName.Name, tc.from)
			}
		})
	}
}
