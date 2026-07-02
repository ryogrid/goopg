package parser

import (
	"testing"
)

// TestParseGrantAttrACL verifies that a column-level GRANT/REVOKE —
// `GRANT <priv>(<cols>) ON [TABLE] <name> …` — is captured into
// CompatNoopStmt.AttrACL with its per-privilege column lists, table names,
// grantees, and grant-option flag, while clearing TableACL (a column grant
// targets pg_attribute.attacl, not the whole-relation pg_class.relacl).
// M0119-0004-ACLHEAP (attacl half).
func TestParseGrantAttrACL(t *testing.T) {
	cases := []struct {
		sql        string
		wantRevoke bool
		wantPrivs  []ColumnPrivilege
		wantTables []ObjectName
		wantRoles  []string
		wantWGO    bool
	}{
		{
			sql:        "GRANT SELECT (a) ON TABLE t TO r",
			wantPrivs:  []ColumnPrivilege{{Privilege: "SELECT", Columns: []string{"a"}}},
			wantTables: []ObjectName{{Name: "t"}},
			wantRoles:  []string{"r"},
		},
		{
			// optional TABLE keyword omitted; multiple columns
			sql:        "GRANT SELECT (a, b) ON t TO r",
			wantPrivs:  []ColumnPrivilege{{Privilege: "SELECT", Columns: []string{"a", "b"}}},
			wantTables: []ObjectName{{Name: "t"}},
			wantRoles:  []string{"r"},
		},
		{
			// multiple privileges each with their own column list
			sql: "GRANT SELECT (a), UPDATE (b) ON TABLE public.t TO r",
			wantPrivs: []ColumnPrivilege{
				{Privilege: "SELECT", Columns: []string{"a"}},
				{Privilege: "UPDATE", Columns: []string{"b"}},
			},
			wantTables: []ObjectName{{Schema: "public", Name: "t"}},
			wantRoles:  []string{"r"},
		},
		{
			sql:        "GRANT INSERT (a) ON TABLE t TO r WITH GRANT OPTION",
			wantPrivs:  []ColumnPrivilege{{Privilege: "INSERT", Columns: []string{"a"}}},
			wantTables: []ObjectName{{Name: "t"}},
			wantRoles:  []string{"r"},
			wantWGO:    true,
		},
		{
			sql:        "GRANT ALL (a) ON TABLE t TO r",
			wantPrivs:  []ColumnPrivilege{{Privilege: "ALL", Columns: []string{"a"}}},
			wantTables: []ObjectName{{Name: "t"}},
			wantRoles:  []string{"r"},
		},
		{
			sql:        "REVOKE SELECT (a) ON TABLE t FROM r",
			wantRevoke: true,
			wantPrivs:  []ColumnPrivilege{{Privilege: "SELECT", Columns: []string{"a"}}},
			wantTables: []ObjectName{{Name: "t"}},
			wantRoles:  []string{"r"},
		},
		{
			sql:        "REVOKE SELECT (a) ON TABLE t FROM PUBLIC CASCADE",
			wantRevoke: true,
			wantPrivs:  []ColumnPrivilege{{Privilege: "SELECT", Columns: []string{"a"}}},
			wantTables: []ObjectName{{Name: "t"}},
			wantRoles:  []string{"public"},
		},
		{
			sql:        "GRANT SELECT (a) ON TABLE t TO r1, r2",
			wantPrivs:  []ColumnPrivilege{{Privilege: "SELECT", Columns: []string{"a"}}},
			wantTables: []ObjectName{{Name: "t"}},
			wantRoles:  []string{"r1", "r2"},
		},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", tc.sql, err)
		}
		ns, ok := stmts[0].(*CompatNoopStmt)
		if !ok {
			t.Fatalf("%q: expected *CompatNoopStmt, got %T", tc.sql, stmts[0])
		}
		if ns.AttrACL == nil {
			t.Fatalf("%q: AttrACL is nil, want populated", tc.sql)
		}
		// A column grant must clear TableACL so the relacl-xmax machinery is dormant.
		if ns.TableACL != "" {
			t.Errorf("%q: TableACL = %q, want empty", tc.sql, ns.TableACL)
		}
		got := ns.AttrACL
		if got.Revoke != tc.wantRevoke {
			t.Errorf("%q: Revoke = %v, want %v", tc.sql, got.Revoke, tc.wantRevoke)
		}
		if got.WithGrantOption != tc.wantWGO {
			t.Errorf("%q: WithGrantOption = %v, want %v", tc.sql, got.WithGrantOption, tc.wantWGO)
		}
		if !eqStrs(got.Grantees, tc.wantRoles) {
			t.Errorf("%q: Grantees = %v, want %v", tc.sql, got.Grantees, tc.wantRoles)
		}
		if len(got.TableNames) != len(tc.wantTables) {
			t.Fatalf("%q: TableNames = %v, want %v", tc.sql, got.TableNames, tc.wantTables)
		}
		for i := range tc.wantTables {
			if got.TableNames[i].Schema != tc.wantTables[i].Schema || got.TableNames[i].Name != tc.wantTables[i].Name {
				t.Errorf("%q: TableNames[%d] = %+v, want %+v", tc.sql, i, got.TableNames[i], tc.wantTables[i])
			}
		}
		if len(got.Privileges) != len(tc.wantPrivs) {
			t.Fatalf("%q: Privileges = %+v, want %+v", tc.sql, got.Privileges, tc.wantPrivs)
		}
		for i := range tc.wantPrivs {
			if got.Privileges[i].Privilege != tc.wantPrivs[i].Privilege {
				t.Errorf("%q: Privileges[%d].Privilege = %q, want %q", tc.sql, i, got.Privileges[i].Privilege, tc.wantPrivs[i].Privilege)
			}
			if !eqStrs(got.Privileges[i].Columns, tc.wantPrivs[i].Columns) {
				t.Errorf("%q: Privileges[%d].Columns = %v, want %v", tc.sql, i, got.Privileges[i].Columns, tc.wantPrivs[i].Columns)
			}
		}
	}
}

// TestParseGrantNonColumnLeavesAttrACLNil verifies the capture is scoped strictly
// to a parenthesised column list before ON — a whole-table grant, a function
// grant (parens follow ON), and a type grant all leave AttrACL nil so the
// existing table/type paths are unaffected. M0119-0004-ACLHEAP.
func TestParseGrantNonColumnLeavesAttrACLNil(t *testing.T) {
	for _, sql := range []string{
		"GRANT SELECT ON TABLE foo TO bar",
		"REVOKE SELECT ON public.foo FROM bar",
		"GRANT EXECUTE ON FUNCTION public.f(integer) TO bar",
		"GRANT USAGE ON TYPE t TO r",
		"GRANT TEMP ON DATABASE postgres TO PUBLIC",
	} {
		stmts, err := Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", sql, err)
		}
		ns, ok := stmts[0].(*CompatNoopStmt)
		if !ok {
			t.Fatalf("%q: expected *CompatNoopStmt, got %T", sql, stmts[0])
		}
		if ns.AttrACL != nil {
			t.Errorf("%q: AttrACL = %+v, want nil", sql, ns.AttrACL)
		}
	}
}
