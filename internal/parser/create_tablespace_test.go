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

// TestParseCreateTableTablespace pins the M0122-0007 fix: CREATE TABLE's
// TABLESPACE clause used to be parsed and silently discarded at all three
// sites that accept it (the main column-list path, the empty `()` /
// consumeCreateTableSuffix path, and the PARTITION OF child path). It must now
// populate CreateTableStmt.Tablespace so the executor can validate and store
// it.
func TestParseCreateTableTablespace(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{"main column-list path", "CREATE TABLE t (a int) TABLESPACE ts1", "ts1"},
		{"empty column-list path", "CREATE TABLE t () TABLESPACE ts2", "ts2"},
		{"no clause", "CREATE TABLE t (a int)", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			s, ok := stmts[0].(*CreateTableStmt)
			if !ok {
				t.Fatalf("Parse(%q): want *CreateTableStmt, got %T", tc.sql, stmts[0])
			}
			if s.Tablespace != tc.want {
				t.Errorf("%q: Tablespace=%q want %q", tc.sql, s.Tablespace, tc.want)
			}
		})
	}

	// PARTITION OF child path.
	stmts, err := Parse("CREATE TABLE parent (a int) PARTITION BY RANGE (a); " +
		"CREATE TABLE child PARTITION OF parent FOR VALUES FROM (1) TO (10) TABLESPACE ts3")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, ok := stmts[1].(*CreateTableStmt)
	if !ok {
		t.Fatalf("want *CreateTableStmt, got %T", stmts[1])
	}
	if s.Tablespace != "ts3" {
		t.Errorf("partition child: Tablespace=%q want %q", s.Tablespace, "ts3")
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

// TestParseCreateIndexTablespace pins the M0122-0007 follow-up: CREATE
// INDEX's TABLESPACE clause (real PG grammar: after WITH (storage_parameter),
// before WHERE) must populate CreateIndexStmt.Tablespace instead of being
// rejected or silently discarded.
func TestParseCreateIndexTablespace(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{"plain", "CREATE INDEX idx1 ON t (a) TABLESPACE ts1", "ts1"},
		{"no clause", "CREATE INDEX idx1 ON t (a)", ""},
		{"after WITH options", "CREATE INDEX idx1 ON t (a) WITH (fillfactor=70) TABLESPACE ts1", "ts1"},
		{"before WHERE predicate", "CREATE INDEX idx1 ON t (a) TABLESPACE ts1 WHERE a > 0", "ts1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.sql, err)
			}
			s, ok := stmts[0].(*CreateIndexStmt)
			if !ok {
				t.Fatalf("Parse(%q): want *CreateIndexStmt, got %T", tc.sql, stmts[0])
			}
			if s.Tablespace != tc.want {
				t.Errorf("%q: Tablespace=%q want %q", tc.sql, s.Tablespace, tc.want)
			}
		})
	}
}

// TestParseAlterTableSetTablespace pins the M0122-0007 follow-up: `ALTER
// TABLE name SET TABLESPACE name` was previously fully unparsed (the bare-SET
// dispatch only recognized the parenthesized reloptions form and SET
// SCHEMA/LOGGED, so this fell through to "expected ADD or DROP"). It must now
// produce an AlterTableSetTablespace action carrying TablespaceName.
func TestParseAlterTableSetTablespace(t *testing.T) {
	stmts, err := Parse("ALTER TABLE t SET TABLESPACE ts1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, ok := stmts[0].(*AlterTableStmt)
	if !ok {
		t.Fatalf("want *AlterTableStmt, got %T", stmts[0])
	}
	if len(s.Actions) != 1 || s.Actions[0].Kind != AlterTableSetTablespace {
		t.Fatalf("want 1 AlterTableSetTablespace action, got %+v", s.Actions)
	}
	if got := s.Actions[0].TablespaceName; got != "ts1" {
		t.Errorf("TablespaceName=%q want %q", got, "ts1")
	}
}

// TestParseAlterIndexSetTablespace mirrors TestParseAlterTableSetTablespace
// for `ALTER INDEX name SET TABLESPACE name` — checked ahead of the existing
// `ALTER INDEX name SET (param = value, …)` reloptions branch, which
// otherwise unconditionally expects a `(` after SET.
func TestParseAlterIndexSetTablespace(t *testing.T) {
	stmts, err := Parse("ALTER INDEX idx1 SET TABLESPACE ts1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, ok := stmts[0].(*AlterTableStmt)
	if !ok {
		t.Fatalf("want *AlterTableStmt, got %T", stmts[0])
	}
	if len(s.Actions) != 1 || s.Actions[0].Kind != AlterTableSetTablespace {
		t.Fatalf("want 1 AlterTableSetTablespace action, got %+v", s.Actions)
	}
	if got := s.Actions[0].TablespaceName; got != "ts1" {
		t.Errorf("TablespaceName=%q want %q", got, "ts1")
	}
	// The existing reloptions SET must remain unaffected.
	stmts2, err := Parse("ALTER INDEX idx1 SET (fillfactor = 70)")
	if err != nil {
		t.Fatalf("Parse (reloptions): %v", err)
	}
	s2 := stmts2[0].(*AlterTableStmt)
	if len(s2.Actions) != 1 || s2.Actions[0].Kind != AlterIndexSetReloptions {
		t.Fatalf("reloptions SET regressed: %+v", s2.Actions)
	}
}
