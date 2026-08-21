package parser

import "testing"

// TestParseAlterRuleRename pins the `ALTER RULE name ON table RENAME TO
// newname` shape — the only ALTER RULE form in PG's grammar. M0134-0065.
func TestParseAlterRuleRename(t *testing.T) {
	cases := []struct {
		sql       string
		name      string
		tblSchema string
		tblName   string
		newName   string
	}{
		{sql: "ALTER RULE somerule ON sometable RENAME TO newname", name: "somerule", tblName: "sometable", newName: "newname"},
		{sql: "ALTER RULE somerule ON public.sometable RENAME TO newname", name: "somerule", tblSchema: "public", tblName: "sometable", newName: "newname"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			ar, ok := stmts[0].(*AlterRuleRenameStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterRuleRenameStmt", stmts[0])
			}
			if ar.Name != tc.name {
				t.Errorf("Name = %q, want %q", ar.Name, tc.name)
			}
			if ar.Table.Schema != tc.tblSchema || ar.Table.Name != tc.tblName {
				t.Errorf("Table = %+v, want schema=%q name=%q", ar.Table, tc.tblSchema, tc.tblName)
			}
			if ar.NewName != tc.newName {
				t.Errorf("NewName = %q, want %q", ar.NewName, tc.newName)
			}
		})
	}
}
