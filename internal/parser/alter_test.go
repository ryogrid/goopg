package parser

import "testing"

// TestParseAlterTablePgbenchPrimaryKey: pgbench's exact strings for
// installing primary keys after the data load. This is the
// load-bearing shape that closes the pgbench -i DDL surface.
func TestParseAlterTablePgbenchPrimaryKey(t *testing.T) {
	cases := []string{
		"alter table pgbench_branches add primary key (bid)",
		"alter table pgbench_tellers add primary key (tid)",
		"alter table pgbench_accounts add primary key (aid)",
	}
	for _, in := range cases {
		stmts, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", in, stmts[0])
		}
		if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableAddPrimaryKey {
			t.Errorf("Parse(%q) actions=%+v", in, at.Actions)
		}
		if len(at.Actions[0].Columns) != 1 {
			t.Errorf("Parse(%q) columns=%+v", in, at.Actions[0].Columns)
		}
	}
}

// TestParseAlterTableNamedConstraint covers the explicit ADD
// CONSTRAINT name PRIMARY KEY (cols) form psql's \d output emits.
func TestParseAlterTableNamedConstraint(t *testing.T) {
	stmts, err := Parse("ALTER TABLE t ADD CONSTRAINT t_pkey PRIMARY KEY (a, b)")
	if err != nil {
		t.Fatal(err)
	}
	at := stmts[0].(*AlterTableStmt)
	if at.Actions[0].ConstraintName != "t_pkey" {
		t.Errorf("constraint name=%q", at.Actions[0].ConstraintName)
	}
	if len(at.Actions[0].Columns) != 2 {
		t.Errorf("cols=%+v", at.Actions[0].Columns)
	}
}

// TestParseAlterTableAddColumn covers the simpler `ADD [COLUMN] coldef`
// form so ALTER TABLE isn't a one-trick.
func TestParseAlterTableAddColumn(t *testing.T) {
	stmts, err := Parse("ALTER TABLE IF EXISTS t ADD COLUMN extra int NOT NULL, ADD c2 char(8)")
	if err != nil {
		t.Fatal(err)
	}
	at := stmts[0].(*AlterTableStmt)
	if !at.IfExists {
		t.Error("IfExists not set")
	}
	if len(at.Actions) != 2 {
		t.Fatalf("actions=%+v", at.Actions)
	}
	if at.Actions[0].Kind != AlterTableAddColumn || at.Actions[0].Column.Name != "extra" || !at.Actions[0].Column.NotNull {
		t.Errorf("actions[0]=%+v", at.Actions[0])
	}
	if at.Actions[1].Column.Type.Name != "char" || at.Actions[1].Column.Type.Args[0] != 8 {
		t.Errorf("actions[1]=%+v", at.Actions[1])
	}
}

// TestParseAlterTableSyntaxErrors pins SyntaxError for canonical
// missing-piece cases.
func TestParseAlterTableSyntaxErrors(t *testing.T) {
	cases := []string{
		"ALTER",                           // missing TABLE
		"ALTER TABLE",                     // missing name
		"ALTER TABLE t",                   // missing action
		"ALTER TABLE t ADD",               // missing action body
		"ALTER TABLE t ADD CONSTRAINT",    // missing name
		"ALTER TABLE t ADD CONSTRAINT c",  // missing PRIMARY KEY
		"ALTER TABLE t ADD PRIMARY",       // missing KEY
		"ALTER TABLE t ADD PRIMARY KEY",   // missing column list
		"ALTER TABLE t ADD PRIMARY KEY (", // unclosed
	}
	for _, in := range cases {
		_, err := Parse(in)
		if err == nil {
			t.Errorf("Parse(%q) expected error", in)
			continue
		}
		if _, ok := err.(*SyntaxError); !ok {
			t.Errorf("Parse(%q) err type=%T", in, err)
		}
	}
}
