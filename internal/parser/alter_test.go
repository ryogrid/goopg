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

// TestParseAlterTableSetDropDefault covers `ALTER TABLE ... ALTER COLUMN c SET
// DEFAULT <expr>` and `... DROP DEFAULT` (DU-002 slice 269). SET DEFAULT records
// the parsed expression on DefaultExpr; DROP DEFAULT records the column with a
// nil DefaultExpr. Both previously fell through to the no-op consume.
func TestParseAlterTableSetDropDefault(t *testing.T) {
	// SET DEFAULT — expression must be captured.
	stmts, err := Parse("ALTER TABLE t ALTER COLUMN c SET DEFAULT 7")
	if err != nil {
		t.Fatalf("Parse SET DEFAULT: %v", err)
	}
	at, ok := stmts[0].(*AlterTableStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableSetDefault {
		t.Fatalf("SET DEFAULT: actions=%+v", at.Actions)
	}
	if at.Actions[0].ColumnName != "c" {
		t.Errorf("SET DEFAULT: ColumnName=%q want %q", at.Actions[0].ColumnName, "c")
	}
	if at.Actions[0].DefaultExpr == nil {
		t.Errorf("SET DEFAULT: DefaultExpr is nil, want the parsed expression")
	}

	// DROP DEFAULT — no expression, action recorded with nil DefaultExpr.
	stmts, err = Parse("ALTER TABLE t ALTER COLUMN c DROP DEFAULT")
	if err != nil {
		t.Fatalf("Parse DROP DEFAULT: %v", err)
	}
	at, ok = stmts[0].(*AlterTableStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableDropDefault {
		t.Fatalf("DROP DEFAULT: actions=%+v", at.Actions)
	}
	if at.Actions[0].ColumnName != "c" {
		t.Errorf("DROP DEFAULT: ColumnName=%q want %q", at.Actions[0].ColumnName, "c")
	}
	if at.Actions[0].DefaultExpr != nil {
		t.Errorf("DROP DEFAULT: DefaultExpr=%v want nil", at.Actions[0].DefaultExpr)
	}
}

// TestParseAlterTableSetDropNotNull covers `ALTER TABLE ... ALTER COLUMN c SET
// NOT NULL` and `... DROP NOT NULL` (DU-002 slice 270). Both previously fell
// through to the no-op consume; now they record a dedicated action kind so the
// executor can mutate pg_attribute.attnotnull and the contype='n' constraint.
func TestParseAlterTableSetDropNotNull(t *testing.T) {
	stmts, err := Parse("ALTER TABLE t ALTER COLUMN c SET NOT NULL")
	if err != nil {
		t.Fatalf("Parse SET NOT NULL: %v", err)
	}
	at, ok := stmts[0].(*AlterTableStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableSetNotNull {
		t.Fatalf("SET NOT NULL: actions=%+v", at.Actions)
	}
	if at.Actions[0].ColumnName != "c" {
		t.Errorf("SET NOT NULL: ColumnName=%q want %q", at.Actions[0].ColumnName, "c")
	}

	stmts, err = Parse("ALTER TABLE t ALTER COLUMN c DROP NOT NULL")
	if err != nil {
		t.Fatalf("Parse DROP NOT NULL: %v", err)
	}
	at, ok = stmts[0].(*AlterTableStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableDropNotNull {
		t.Fatalf("DROP NOT NULL: actions=%+v", at.Actions)
	}
	if at.Actions[0].ColumnName != "c" {
		t.Errorf("DROP NOT NULL: ColumnName=%q want %q", at.Actions[0].ColumnName, "c")
	}
}

// TestParseAlterTableAddNotNull covers the PG18 named NOT NULL constraint form
// `ALTER TABLE ... ADD [CONSTRAINT name] NOT NULL col [NO INHERIT]` (DU-002
// slice 271). It records AlterTableAddNotNull with the column, optional explicit
// name, and the NO INHERIT flag so the executor records a contype='n' row whose
// conname round-trips through pg_dump as `CONSTRAINT <name> NOT NULL <col>`.
func TestParseAlterTableAddNotNull(t *testing.T) {
	for _, tc := range []struct {
		sql           string
		wantCol       string
		wantName      string
		wantNoInherit bool
	}{
		{"ALTER TABLE t ADD CONSTRAINT my_nn NOT NULL c", "c", "my_nn", false},
		{"ALTER TABLE t ADD NOT NULL c", "c", "", false},
		{"ALTER TABLE t ADD CONSTRAINT my_nn NOT NULL c NO INHERIT", "c", "my_nn", true},
	} {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", tc.sql, stmts[0])
		}
		if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableAddNotNull {
			t.Fatalf("Parse(%q): actions=%+v", tc.sql, at.Actions)
		}
		a := at.Actions[0]
		if a.ColumnName != tc.wantCol {
			t.Errorf("Parse(%q): ColumnName=%q want %q", tc.sql, a.ColumnName, tc.wantCol)
		}
		if a.ConstraintName != tc.wantName {
			t.Errorf("Parse(%q): ConstraintName=%q want %q", tc.sql, a.ConstraintName, tc.wantName)
		}
		if a.NoInherit != tc.wantNoInherit {
			t.Errorf("Parse(%q): NoInherit=%v want %v", tc.sql, a.NoInherit, tc.wantNoInherit)
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

// TestParseAlterTableSetColumnOptions verifies the per-column attribute-option
// list (`ALTER COLUMN c SET (opt=value, …)`) is captured and normalized to PG's
// stored `name=value` form for pg_attribute.attoptions round-trip. DU-002 slice 185.
func TestParseAlterTableSetColumnOptions(t *testing.T) {
	for _, tc := range []struct {
		sql     string
		wantCol string
		wantOpt []string
	}{
		{"ALTER TABLE t ALTER COLUMN c SET (n_distinct = 0.5)", "c", []string{"n_distinct=0.5"}},
		{"ALTER TABLE t ALTER COLUMN c SET (n_distinct=0.5)", "c", []string{"n_distinct=0.5"}},
		{"ALTER TABLE t ALTER COLUMN c SET (n_distinct = -0.5)", "c", []string{"n_distinct=-0.5"}},
		{"ALTER TABLE t ALTER COLUMN c SET (n_distinct = 100)", "c", []string{"n_distinct=100"}},
		{"ALTER TABLE t ALTER COLUMN c SET (n_distinct = 0.5, n_distinct_inherited = -0.1)", "c",
			[]string{"n_distinct=0.5", "n_distinct_inherited=-0.1"}},
	} {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", tc.sql, stmts[0])
		}
		if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableAlterColumnSet {
			t.Fatalf("Parse(%q): actions=%+v", tc.sql, at.Actions)
		}
		if at.Actions[0].ColumnName != tc.wantCol {
			t.Errorf("Parse(%q): ColumnName=%q want %q", tc.sql, at.Actions[0].ColumnName, tc.wantCol)
		}
		got := at.Actions[0].SetOptions
		if len(got) != len(tc.wantOpt) {
			t.Fatalf("Parse(%q): SetOptions=%v want %v", tc.sql, got, tc.wantOpt)
		}
		for i := range got {
			if got[i] != tc.wantOpt[i] {
				t.Errorf("Parse(%q): SetOptions[%d]=%q want %q", tc.sql, i, got[i], tc.wantOpt[i])
			}
		}
	}
}
