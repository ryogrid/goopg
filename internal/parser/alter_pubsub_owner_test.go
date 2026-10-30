package parser

import "testing"

// TestParseAlterPublicationOwner pins the `ALTER PUBLICATION name OWNER TO
// {role | CURRENT_USER | SESSION_USER | CURRENT_ROLE}` shape. M0119-0004
// (DU-002, loop #65 ledger follow-up).
func TestParseAlterPublicationOwner(t *testing.T) {
	cases := []struct {
		sql      string
		name     string
		newOwner string
	}{
		{sql: "ALTER PUBLICATION mypub OWNER TO newrole", name: "mypub", newOwner: "newrole"},
		{sql: "ALTER PUBLICATION mypub OWNER TO CURRENT_USER", name: "mypub", newOwner: "current_user"},
		{sql: "ALTER PUBLICATION mypub OWNER TO SESSION_USER", name: "mypub", newOwner: "current_user"},
		{sql: "ALTER PUBLICATION mypub OWNER TO CURRENT_ROLE", name: "mypub", newOwner: "current_user"},
	}
	for _, tc := range cases {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			ac, ok := stmts[0].(*AlterPublicationOwnerStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterPublicationOwnerStmt", stmts[0])
			}
			if ac.Name != tc.name {
				t.Errorf("Name = %q, want %q", ac.Name, tc.name)
			}
			if ac.NewOwner != tc.newOwner {
				t.Errorf("NewOwner = %q, want %q", ac.NewOwner, tc.newOwner)
			}
		})
	}
}

// TestParseAlterSubscriptionOwner is TestParseAlterPublicationOwner's ALTER
// SUBSCRIPTION counterpart.
func TestParseAlterSubscriptionOwner(t *testing.T) {
	sql := "ALTER SUBSCRIPTION mysub OWNER TO newrole"
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q): %v", sql, err)
	}
	ac, ok := stmts[0].(*AlterSubscriptionOwnerStmt)
	if !ok {
		t.Fatalf("stmt type = %T, want *AlterSubscriptionOwnerStmt", stmts[0])
	}
	if ac.Name != "mysub" {
		t.Errorf("Name = %q, want %q", ac.Name, "mysub")
	}
	if ac.NewOwner != "newrole" {
		t.Errorf("NewOwner = %q, want %q", ac.NewOwner, "newrole")
	}
}

// TestParseAlterPublicationOtherFormsStillNoop confirms an unmodelled
// ALTER PUBLICATION/SUBSCRIPTION tail (RENAME TO, ADD TABLES IN SCHEMA, the
// subscription forms) still parses without error as the pre-existing
// compatibility no-op — ADD / SET / DROP TABLE and SET ( options ) are
// modelled (TestParseAlterPublicationTables, TestParsePublicationDDLForms) —
// rather than erroring or being misrouted into the OWNER TO path.
func TestParseAlterPublicationOtherFormsStillNoop(t *testing.T) {
	cases := []string{
		"ALTER PUBLICATION mypub RENAME TO otherpub",
		"ALTER PUBLICATION mypub ADD TABLES IN SCHEMA s",
		"ALTER SUBSCRIPTION mysub RENAME TO othersub",
		"ALTER SUBSCRIPTION mysub SET PUBLICATION p2",
		"ALTER SUBSCRIPTION mysub REFRESH PUBLICATION",
	}
	for _, sql := range cases {
		t.Run(sql, func(t *testing.T) {
			stmts, err := Parse(sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", sql, err)
			}
			// A bare *AlterTableStmt no-op stub (no Name) used to reach the
			// executor's generic ALTER TABLE path, which tried to resolve the
			// empty relation name and raised a spurious 42P01 instead of
			// succeeding silently — CompatNoopStmt short-circuits before any
			// lookup happens.
			if _, ok := stmts[0].(*CompatNoopStmt); !ok {
				t.Fatalf("stmt type = %T, want *CompatNoopStmt (no-op stub)", stmts[0])
			}
		})
	}
}

// TestParseAlterPublicationTables: ALTER PUBLICATION name { ADD | SET | DROP }
// TABLE [ONLY] t [*] [, ...] parses into AlterPublicationTablesStmt (gram.y
// AlterPublicationStmt, pub_obj_list of TABLE objects). pg_dump writes
// membership as `ALTER PUBLICATION p ADD TABLE ONLY s.t;`; it used to be a
// silent compat no-op, so a restored dump lost every member table.
func TestParseAlterPublicationTables(t *testing.T) {
	cases := []struct {
		sql    string
		action string
		tables []ObjectName
	}{
		{"ALTER PUBLICATION p ADD TABLE ONLY public.t1", "add", []ObjectName{{Schema: "public", Name: "t1"}}},
		{"ALTER PUBLICATION p SET TABLE a, b *", "set", []ObjectName{{Name: "a"}, {Name: "b"}}},
		{"ALTER PUBLICATION p DROP TABLE s.c", "drop", []ObjectName{{Schema: "s", Name: "c"}}},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			stmts, err := Parse(c.sql)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			st, ok := stmts[0].(*AlterPublicationTablesStmt)
			if !ok {
				t.Fatalf("stmt type = %T, want *AlterPublicationTablesStmt", stmts[0])
			}
			if st.Name != "p" || st.Action != c.action || len(st.Tables) != len(c.tables) {
				t.Fatalf("got %+v, want action %s tables %v", st, c.action, c.tables)
			}
			for i, want := range c.tables {
				if st.Tables[i].Schema != want.Schema || st.Tables[i].Name != want.Name {
					t.Errorf("table %d = %+v, want %+v", i, st.Tables[i], want)
				}
			}
		})
	}
}

// TestParseAlterPublicationUnmodelledTableFormsStayNoop: a column list, a row
// filter or a TABLES IN SCHEMA item is a publication feature goopg does not
// model; such an ALTER PUBLICATION stays the compat no-op it was, instead of
// a syntax error on SQL PG accepts.
func TestParseAlterPublicationUnmodelledTableFormsStayNoop(t *testing.T) {
	for _, sql := range []string{
		"ALTER PUBLICATION p ADD TABLE t (a, b)",
		"ALTER PUBLICATION p SET TABLE t WHERE (a > 1)",
		"ALTER PUBLICATION p ADD TABLE t, TABLES IN SCHEMA s",
	} {
		stmts, err := Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", sql, err)
		}
		if _, ok := stmts[0].(*CompatNoopStmt); !ok {
			t.Errorf("%q: stmt type = %T, want *CompatNoopStmt", sql, stmts[0])
		}
	}
}

// TestParsePublicationDDLForms: ALTER PUBLICATION ... SET ( options )
// (AlterPublicationOptions), DROP PUBLICATION's name list (gram.y DropStmt
// name_list), and CREATE PUBLICATION's [ONLY] t [*] table items (pub_obj_list
// relation_expr; a later item may repeat TABLE).
func TestParsePublicationDDLForms(t *testing.T) {
	stmts, err := Parse("ALTER PUBLICATION p SET (publish = 'insert, update')")
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := stmts[0].(*AlterPublicationOptionsStmt); !ok || o.Name != "p" || o.With["publish"] != "insert, update" {
		t.Errorf("SET options = %#v", stmts[0])
	}
	stmts, err = Parse("DROP PUBLICATION IF EXISTS a, b CASCADE")
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := stmts[0].(*DropPublicationStmt); !ok || !d.IfExists || len(d.Names) != 2 || d.Names[0] != "a" || d.Names[1] != "b" || d.Name != "a" {
		t.Errorf("DROP list = %#v", stmts[0])
	}
	stmts, err = Parse("CREATE PUBLICATION p FOR TABLE ONLY s.t1, t2 *, TABLE t3")
	if err != nil {
		t.Fatal(err)
	}
	c, ok := stmts[0].(*CreatePublicationStmt)
	if !ok || len(c.Tables) != 3 || c.Tables[0].Schema != "s" || c.Tables[0].Name != "t1" || c.Tables[1].Name != "t2" || c.Tables[2].Name != "t3" {
		t.Errorf("CREATE tables = %#v", stmts[0])
	}
}
