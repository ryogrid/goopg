package parser

import "testing"

// TestParseCreatePolicy pins the CREATE POLICY grammar: name, table, the
// PERMISSIVE/RESTRICTIVE default, the FOR command, TO PUBLIC, and the
// USING / WITH CHECK expressions. goopg does not enforce RLS; the statement is
// parsed and stored only so the policy round-trips through pg_dump. DU-002
// slice 323.
func TestParseCreatePolicy(t *testing.T) {
	cases := []struct {
		name       string
		sql        string
		wantName   string
		wantTable  string
		permissive bool
		command    string
		hasUsing   bool
		hasCheck   bool
	}{
		{
			name:       "simple using",
			sql:        "CREATE POLICY p_simple ON public.pol_t USING (a > 0)",
			wantName:   "p_simple",
			wantTable:  "pol_t",
			permissive: true,
			command:    "all",
			hasUsing:   true,
		},
		{
			name:       "restrictive for select",
			sql:        "CREATE POLICY p_restr ON public.pol_t AS RESTRICTIVE FOR SELECT USING (a > 5)",
			wantName:   "p_restr",
			wantTable:  "pol_t",
			permissive: false,
			command:    "select",
			hasUsing:   true,
		},
		{
			name:       "for insert with check",
			sql:        "CREATE POLICY p_check ON public.pol_t FOR INSERT WITH CHECK (a < 100)",
			wantName:   "p_check",
			wantTable:  "pol_t",
			permissive: true,
			command:    "insert",
			hasCheck:   true,
		},
		{
			name:       "for update using and with check, to public",
			sql:        "CREATE POLICY p_both ON public.pol_t FOR UPDATE TO PUBLIC USING (a = 1) WITH CHECK (a <> 0)",
			wantName:   "p_both",
			wantTable:  "pol_t",
			permissive: true,
			command:    "update",
			hasUsing:   true,
			hasCheck:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.sql, err)
			}
			if len(stmts) != 1 {
				t.Fatalf("got %d stmts, want 1", len(stmts))
			}
			p, ok := stmts[0].(*CreatePolicyStmt)
			if !ok {
				t.Fatalf("got %T, want *CreatePolicyStmt", stmts[0])
			}
			if p.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", p.Name, tc.wantName)
			}
			if p.Table.Name != tc.wantTable {
				t.Errorf("Table.Name = %q, want %q", p.Table.Name, tc.wantTable)
			}
			if p.Permissive != tc.permissive {
				t.Errorf("Permissive = %v, want %v", p.Permissive, tc.permissive)
			}
			if p.Command != tc.command {
				t.Errorf("Command = %q, want %q", p.Command, tc.command)
			}
			if (p.Using != nil) != tc.hasUsing {
				t.Errorf("hasUsing = %v, want %v", p.Using != nil, tc.hasUsing)
			}
			if (p.WithCheck != nil) != tc.hasCheck {
				t.Errorf("hasCheck = %v, want %v", p.WithCheck != nil, tc.hasCheck)
			}
		})
	}
}

// TestParseDropPolicy pins DROP POLICY [IF EXISTS] name ON table. DU-002 slice 323.
func TestParseDropPolicy(t *testing.T) {
	stmts, err := Parse("DROP POLICY IF EXISTS p_simple ON public.pol_t")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, ok := stmts[0].(*DropPolicyStmt)
	if !ok {
		t.Fatalf("got %T, want *DropPolicyStmt", stmts[0])
	}
	if p.Name != "p_simple" || p.Table.Name != "pol_t" || !p.IfExists {
		t.Errorf("got Name=%q Table=%q IfExists=%v", p.Name, p.Table.Name, p.IfExists)
	}
}
