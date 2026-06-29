package parser

import "testing"

// TestParseCreateRuleNothing pins the unconditional DO-NOTHING CREATE RULE
// grammar that DU-002 slice 324 models as a first-class CreateRuleStmt (so the
// rule round-trips through pg_dump). It also asserts that every other CREATE
// RULE form (action command, conditional WHERE, ON SELECT) keeps the historical
// CompatNoopStmt behaviour.
func TestParseCreateRuleNothing(t *testing.T) {
	cases := []struct {
		name      string
		sql       string
		wantName  string
		wantEvent string
		wantTable string
		instead   bool
	}{
		{
			name:      "insert do instead nothing",
			sql:       "CREATE RULE r_noins AS ON INSERT TO public.t DO INSTEAD NOTHING",
			wantName:  "r_noins",
			wantEvent: "INSERT",
			wantTable: "t",
			instead:   true,
		},
		{
			name:      "update do also nothing",
			sql:       "CREATE RULE r_also AS ON UPDATE TO public.t DO ALSO NOTHING",
			wantName:  "r_also",
			wantEvent: "UPDATE",
			wantTable: "t",
			instead:   false,
		},
		{
			name:      "delete do nothing",
			sql:       "CREATE RULE r_del AS ON DELETE TO t DO NOTHING",
			wantName:  "r_del",
			wantEvent: "DELETE",
			wantTable: "t",
			instead:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if len(stmts) != 1 {
				t.Fatalf("expected 1 stmt, got %d", len(stmts))
			}
			rs, ok := stmts[0].(*CreateRuleStmt)
			if !ok {
				t.Fatalf("expected *CreateRuleStmt, got %T", stmts[0])
			}
			if rs.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", rs.Name, tc.wantName)
			}
			if rs.Event != tc.wantEvent {
				t.Errorf("Event = %q, want %q", rs.Event, tc.wantEvent)
			}
			if rs.Table.Name != tc.wantTable {
				t.Errorf("Table.Name = %q, want %q", rs.Table.Name, tc.wantTable)
			}
			if rs.Instead != tc.instead {
				t.Errorf("Instead = %v, want %v", rs.Instead, tc.instead)
			}
		})
	}
}

// TestParseCreateRuleNonNothingStaysNoop verifies the slice-324 modelling never
// captures rules it cannot faithfully round-trip: action commands, conditional
// WHERE quals, and ON SELECT view rules all stay CompatNoopStmt.
func TestParseCreateRuleNonNothingStaysNoop(t *testing.T) {
	sqls := []string{
		// Action command (not NOTHING).
		"CREATE RULE r1 AS ON INSERT TO t DO INSTEAD INSERT INTO log VALUES (1)",
		// Conditional WHERE.
		"CREATE RULE r2 AS ON UPDATE TO t WHERE (a > 0) DO INSTEAD NOTHING",
		// ON SELECT (view rule).
		"CREATE RULE r3 AS ON SELECT TO t DO INSTEAD NOTHING",
	}
	for _, sql := range sqls {
		stmts, err := Parse(sql)
		if err != nil {
			t.Fatalf("Parse(%q) error: %v", sql, err)
		}
		if _, ok := stmts[0].(*CompatNoopStmt); !ok {
			t.Errorf("Parse(%q): expected *CompatNoopStmt, got %T", sql, stmts[0])
		}
	}
}
