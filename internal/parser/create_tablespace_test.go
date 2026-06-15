package parser

import "testing"

func TestParseCreateTablespace(t *testing.T) {
	cases := []struct {
		sql      string
		name     string
		owner    string
		location string
		nopts    int
	}{
		{"CREATE TABLESPACE ts1 LOCATION ''", "ts1", "", "", 0},
		{"CREATE TABLESPACE ts2 LOCATION '/data/ts2'", "ts2", "", "/data/ts2", 0},
		{"CREATE TABLESPACE ts3 OWNER alice LOCATION ''", "ts3", "alice", "", 0},
		{"CREATE TABLESPACE ts4 OWNER = bob LOCATION '/x'", "ts4", "bob", "/x", 0},
		// Options are captured as a raw token list (random_page_cost, =, 1.0).
		{"CREATE TABLESPACE ts5 LOCATION '' WITH (random_page_cost = 1.0)", "ts5", "", "", 3},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		s, ok := stmts[0].(*CreateTablespaceStmt)
		if !ok {
			t.Fatalf("Parse(%q): want *CreateTablespaceStmt, got %T", tc.sql, stmts[0])
		}
		if s.Name != tc.name {
			t.Errorf("%q: name=%q want %q", tc.sql, s.Name, tc.name)
		}
		if s.Owner != tc.owner {
			t.Errorf("%q: owner=%q want %q", tc.sql, s.Owner, tc.owner)
		}
		if s.Location != tc.location {
			t.Errorf("%q: location=%q want %q", tc.sql, s.Location, tc.location)
		}
		if len(s.Options) != tc.nopts {
			t.Errorf("%q: %d options want %d (%v)", tc.sql, len(s.Options), tc.nopts, s.Options)
		}
	}
}

func TestParseCreateTablespaceMissingLocation(t *testing.T) {
	if _, err := Parse("CREATE TABLESPACE ts1"); err == nil {
		t.Fatal("CREATE TABLESPACE without LOCATION: want error, got nil")
	}
}

func TestParseDropTablespace(t *testing.T) {
	cases := []struct {
		sql      string
		name     string
		ifExists bool
	}{
		{"DROP TABLESPACE ts1", "ts1", false},
		{"DROP TABLESPACE IF EXISTS ts2", "ts2", true},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		s, ok := stmts[0].(*DropTablespaceStmt)
		if !ok {
			t.Fatalf("Parse(%q): want *DropTablespaceStmt, got %T", tc.sql, stmts[0])
		}
		if s.Name != tc.name {
			t.Errorf("%q: name=%q want %q", tc.sql, s.Name, tc.name)
		}
		if s.IfExists != tc.ifExists {
			t.Errorf("%q: ifExists=%v want %v", tc.sql, s.IfExists, tc.ifExists)
		}
	}
}
