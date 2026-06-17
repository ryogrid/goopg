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

// TestParseAlterTableAddForeignKey pins HammerDB TPC-H's
// post-load FK pass: `ALTER TABLE … ADD CONSTRAINT name FOREIGN
// KEY (cols) REFERENCES other (cols) [NOT DEFERRABLE | DEFERRABLE]`.
// v0 records the shape but does not enforce.
func TestParseAlterTableAddForeignKey(t *testing.T) {
	cases := []struct {
		sql        string
		localCols  []string
		refTable   string
		refCols    []string
		deferrable bool
	}{
		{
			"ALTER TABLE LINEITEM ADD CONSTRAINT LINEITEM_PARTSUPP_FK FOREIGN KEY (L_PARTKEY, L_SUPPKEY) REFERENCES PARTSUPP(PS_PARTKEY, PS_SUPPKEY) NOT DEFERRABLE",
			[]string{"l_partkey", "l_suppkey"},
			"partsupp",
			[]string{"ps_partkey", "ps_suppkey"},
			false,
		},
		{
			"ALTER TABLE LINEITEM ADD CONSTRAINT LINEITEM_ORDER_FK FOREIGN KEY (L_ORDERKEY) REFERENCES ORDERS (O_ORDERKEY) DEFERRABLE",
			[]string{"l_orderkey"},
			"orders",
			[]string{"o_orderkey"},
			true,
		},
		{
			// Trailer omitted entirely.
			"ALTER TABLE x ADD FOREIGN KEY (a) REFERENCES y(b)",
			[]string{"a"},
			"y",
			[]string{"b"},
			false,
		},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.sql, err)
			continue
		}
		at := stmts[0].(*AlterTableStmt)
		act := at.Actions[0]
		if act.Kind != AlterTableAddForeignKey {
			t.Errorf("%q: kind=%v want AddForeignKey", tc.sql, act.Kind)
		}
		if act.RefTable.Name != tc.refTable {
			t.Errorf("%q: refTable=%q want %q", tc.sql, act.RefTable.Name, tc.refTable)
		}
		if len(act.Columns) != len(tc.localCols) {
			t.Errorf("%q: localCols=%+v want %+v", tc.sql, act.Columns, tc.localCols)
		}
		if len(act.RefColumns) != len(tc.refCols) {
			t.Errorf("%q: refCols=%+v want %+v", tc.sql, act.RefColumns, tc.refCols)
		}
		if act.Deferrable != tc.deferrable {
			t.Errorf("%q: deferrable=%v want %v", tc.sql, act.Deferrable, tc.deferrable)
		}
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

// TestParseAlterTableDropConstraint verifies that DROP CONSTRAINT name [RESTRICT|CASCADE]
// is parsed into an AlterTableDropConstraint action. M0097-0036 / functional_deps.
func TestParseAlterTableDropConstraint(t *testing.T) {
	cases := []struct {
		sql            string
		wantConstraint string
		wantRestrict   bool
	}{
		{"ALTER TABLE articles DROP CONSTRAINT articles_pkey RESTRICT", "articles_pkey", true},
		{"ALTER TABLE articles DROP CONSTRAINT articles_pkey CASCADE", "articles_pkey", false},
		{"ALTER TABLE articles DROP CONSTRAINT articles_pkey", "articles_pkey", true}, // default = RESTRICT
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", tc.sql, err)
			continue
		}
		if len(stmts) != 1 {
			t.Errorf("Parse(%q): got %d stmts, want 1", tc.sql, len(stmts))
			continue
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Errorf("Parse(%q) got %T, want *AlterTableStmt", tc.sql, stmts[0])
			continue
		}
		if len(at.Actions) != 1 {
			t.Errorf("Parse(%q): got %d actions, want 1", tc.sql, len(at.Actions))
			continue
		}
		act := at.Actions[0]
		if act.Kind != AlterTableDropConstraint {
			t.Errorf("Parse(%q): Kind=%v, want AlterTableDropConstraint", tc.sql, act.Kind)
		}
		if act.ConstraintName != tc.wantConstraint {
			t.Errorf("Parse(%q): ConstraintName=%q, want %q", tc.sql, act.ConstraintName, tc.wantConstraint)
		}
		if act.Restrict != tc.wantRestrict {
			t.Errorf("Parse(%q): Restrict=%v, want %v", tc.sql, act.Restrict, tc.wantRestrict)
		}
	}
}

// TestParseAlterTableSetCompression covers `ALTER TABLE ... ALTER COLUMN c SET
// COMPRESSION <method>` (DU-002 slice 183). The method normalizes to pglz/lz4;
// `default` (and any unknown token) normalizes to "" so no SET COMPRESSION is
// dumped. The action records CompressionType + ColumnName for the executor.
func TestParseAlterTableSetCompression(t *testing.T) {
	for _, tc := range []struct {
		sql      string
		wantCol  string
		wantMeth string
	}{
		{"ALTER TABLE t ALTER COLUMN c SET COMPRESSION pglz", "c", "pglz"},
		{"ALTER TABLE t ALTER COLUMN c SET COMPRESSION lz4", "c", "lz4"},
		{"ALTER TABLE t ALTER COLUMN c SET COMPRESSION LZ4", "c", "lz4"},  // case-insensitive
		{"ALTER TABLE t ALTER COLUMN c SET COMPRESSION default", "c", ""}, // reset to default
	} {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", tc.sql, stmts[0])
		}
		if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableSetCompression {
			t.Fatalf("Parse(%q): actions=%+v", tc.sql, at.Actions)
		}
		if at.Actions[0].ColumnName != tc.wantCol {
			t.Errorf("Parse(%q): ColumnName=%q want %q", tc.sql, at.Actions[0].ColumnName, tc.wantCol)
		}
		if at.Actions[0].CompressionType != tc.wantMeth {
			t.Errorf("Parse(%q): CompressionType=%q want %q", tc.sql, at.Actions[0].CompressionType, tc.wantMeth)
		}
	}
}

// TestParseCreateTableColumnCompression covers the inline `COMPRESSION <method>`
// clause in a CREATE TABLE column definition (`a text COMPRESSION lz4`), which
// threads the method onto ColumnDef.Compression. DU-002 slice 183.
func TestParseCreateTableColumnCompression(t *testing.T) {
	stmts, err := Parse("CREATE TABLE t (a text COMPRESSION lz4, b text COMPRESSION pglz, d text)")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ct, ok := stmts[0].(*CreateTableStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(ct.Columns) != 3 {
		t.Fatalf("columns=%d want 3", len(ct.Columns))
	}
	if ct.Columns[0].Compression != "lz4" {
		t.Errorf("a.Compression=%q want lz4", ct.Columns[0].Compression)
	}
	if ct.Columns[1].Compression != "pglz" {
		t.Errorf("b.Compression=%q want pglz", ct.Columns[1].Compression)
	}
	if ct.Columns[2].Compression != "" {
		t.Errorf("d.Compression=%q want \"\"", ct.Columns[2].Compression)
	}
}

// TestParseAlterTableSetStatistics covers `ALTER TABLE ... ALTER COLUMN c SET
// STATISTICS <n>` (DU-002 slice 184). The integer value (including a negative
// reset value like -1) is recorded in CheckExpr + ColumnName for the executor,
// which threads it onto pg_attribute.attstattarget for pg_dump round-trip.
func TestParseAlterTableSetStatistics(t *testing.T) {
	for _, tc := range []struct {
		sql     string
		wantCol string
		wantVal string
	}{
		{"ALTER TABLE t ALTER COLUMN c SET STATISTICS 100", "c", "100"},
		{"ALTER TABLE t ALTER COLUMN c SET STATISTICS 0", "c", "0"},
		{"ALTER TABLE t ALTER COLUMN c SET STATISTICS -1", "c", "-1"}, // reset to default
		{"ALTER TABLE t ALTER COLUMN c SET STATISTICS 10000", "c", "10000"},
	} {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", tc.sql, stmts[0])
		}
		if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableSetStatistics {
			t.Fatalf("Parse(%q): actions=%+v", tc.sql, at.Actions)
		}
		if at.Actions[0].ColumnName != tc.wantCol {
			t.Errorf("Parse(%q): ColumnName=%q want %q", tc.sql, at.Actions[0].ColumnName, tc.wantCol)
		}
		if at.Actions[0].CheckExpr != tc.wantVal {
			t.Errorf("Parse(%q): CheckExpr=%q want %q", tc.sql, at.Actions[0].CheckExpr, tc.wantVal)
		}
	}
}
