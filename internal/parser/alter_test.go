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

// TestParseAlterTableClusterOn covers `ALTER TABLE t CLUSTER ON idx` and
// `ALTER TABLE t SET WITHOUT CLUSTER` (DU-002 slice 321). The CLUSTER ON form is
// exactly what pg_dump emits for a clustered table, so goopg must accept it to
// restore its own dumps; SET WITHOUT CLUSTER clears the selection.
func TestParseAlterTableClusterOn(t *testing.T) {
	// CLUSTER ON idx — index name captured.
	stmts, err := Parse("ALTER TABLE t CLUSTER ON t_b_idx")
	if err != nil {
		t.Fatalf("Parse CLUSTER ON: %v", err)
	}
	at, ok := stmts[0].(*AlterTableStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableClusterOn {
		t.Fatalf("CLUSTER ON: actions=%+v", at.Actions)
	}
	if at.Actions[0].ClusterIndexName != "t_b_idx" {
		t.Errorf("CLUSTER ON: ClusterIndexName=%q want %q", at.Actions[0].ClusterIndexName, "t_b_idx")
	}

	// SET WITHOUT CLUSTER — no index, distinct kind.
	stmts, err = Parse("ALTER TABLE t SET WITHOUT CLUSTER")
	if err != nil {
		t.Fatalf("Parse SET WITHOUT CLUSTER: %v", err)
	}
	at, ok = stmts[0].(*AlterTableStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableSetWithoutCluster {
		t.Fatalf("SET WITHOUT CLUSTER: actions=%+v", at.Actions)
	}

	// SET (reloptions) must still parse as a reloptions action, not be shadowed
	// by the SET WITHOUT CLUSTER branch.
	stmts, err = Parse("ALTER TABLE t SET (fillfactor = 70)")
	if err != nil {
		t.Fatalf("Parse SET (reloptions): %v", err)
	}
	at, _ = stmts[0].(*AlterTableStmt)
	if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableSetReloptions {
		t.Fatalf("SET (reloptions): actions=%+v", at.Actions)
	}
}

// TestParseAlterTableRowSecurity covers `ALTER TABLE ... {ENABLE|DISABLE} ROW
// LEVEL SECURITY` and `... [NO] FORCE ROW LEVEL SECURITY` (DU-002 slice 322).
// Each must record a distinct action; ENABLE/DISABLE TRIGGER must still fall to
// the no-op trigger arm (EnableDisableTrigger), not be captured as an action.
func TestParseAlterTableRowSecurity(t *testing.T) {
	cases := []struct {
		sql  string
		kind AlterTableActionKind
	}{
		{"ALTER TABLE t ENABLE ROW LEVEL SECURITY", AlterTableEnableRowSecurity},
		{"ALTER TABLE t DISABLE ROW LEVEL SECURITY", AlterTableDisableRowSecurity},
		{"ALTER TABLE t FORCE ROW LEVEL SECURITY", AlterTableForceRowSecurity},
		{"ALTER TABLE t NO FORCE ROW LEVEL SECURITY", AlterTableNoForceRowSecurity},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse %q: %v", tc.sql, err)
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Fatalf("%q: got %T", tc.sql, stmts[0])
		}
		if len(at.Actions) != 1 || at.Actions[0].Kind != tc.kind {
			t.Fatalf("%q: actions=%+v want kind=%d", tc.sql, at.Actions, tc.kind)
		}
	}

	// ENABLE TRIGGER must still be the no-op trigger arm, not an RLS action.
	stmts, err := Parse("ALTER TABLE t ENABLE TRIGGER trg")
	if err != nil {
		t.Fatalf("Parse ENABLE TRIGGER: %v", err)
	}
	at, _ := stmts[0].(*AlterTableStmt)
	if len(at.Actions) != 0 {
		t.Fatalf("ENABLE TRIGGER: expected no actions, got %+v", at.Actions)
	}
	if !at.EnableDisableTrigger {
		t.Errorf("ENABLE TRIGGER: EnableDisableTrigger not set")
	}
}

// TestParseAlterTableEnableDisableRule covers `ALTER TABLE … {ENABLE|DISABLE}
// [REPLICA|ALWAYS] RULE name` (DU-002 slice 325). Each form records a single
// AlterTableEnableDisableRule action carrying the target rule name and the
// pg_rewrite.ev_enabled char. ENABLE/DISABLE TRIGGER must still fall to the
// no-op trigger arm, not be captured as a rule action.
func TestParseAlterTableEnableDisableRule(t *testing.T) {
	cases := []struct {
		sql   string
		name  string
		state byte
	}{
		{"ALTER TABLE t ENABLE RULE r", "r", 'O'},
		{"ALTER TABLE t DISABLE RULE r", "r", 'D'},
		{"ALTER TABLE t ENABLE REPLICA RULE r", "r", 'R'},
		{"ALTER TABLE t ENABLE ALWAYS RULE r", "r", 'A'},
		{`ALTER TABLE t DISABLE RULE "MyRule"`, "MyRule", 'D'},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse %q: %v", tc.sql, err)
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Fatalf("%q: got %T", tc.sql, stmts[0])
		}
		if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableEnableDisableRule {
			t.Fatalf("%q: actions=%+v want one AlterTableEnableDisableRule", tc.sql, at.Actions)
		}
		if at.Actions[0].RuleName != tc.name {
			t.Errorf("%q: RuleName=%q want %q", tc.sql, at.Actions[0].RuleName, tc.name)
		}
		if at.Actions[0].RuleEnabledState != tc.state {
			t.Errorf("%q: RuleEnabledState=%q want %q", tc.sql, at.Actions[0].RuleEnabledState, tc.state)
		}
	}

	// DISABLE TRIGGER must still be the no-op trigger arm, not a rule action.
	stmts, err := Parse("ALTER TABLE t DISABLE TRIGGER trg")
	if err != nil {
		t.Fatalf("Parse DISABLE TRIGGER: %v", err)
	}
	at, _ := stmts[0].(*AlterTableStmt)
	if len(at.Actions) != 0 {
		t.Fatalf("DISABLE TRIGGER: expected no actions, got %+v", at.Actions)
	}
	if !at.EnableDisableTrigger {
		t.Errorf("DISABLE TRIGGER: EnableDisableTrigger not set")
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

// TestParseCreateTableColumnNotNullNoInherit covers the inline column-level
// `NOT NULL NO INHERIT` form in CREATE TABLE (DU-002 slice 272). PG18 records the
// NOT NULL as a contype='n' pg_constraint row with connoinherit='t'; the parser
// must consume the optional ` NO INHERIT` trailer into ColumnDef.NotNullNoInherit
// so the executor can thread it onto the constraint. A plain `NOT NULL` (no
// trailer) must leave NotNullNoInherit false.
func TestParseCreateTableColumnNotNullNoInherit(t *testing.T) {
	stmts, err := Parse("CREATE TABLE t (c integer NOT NULL NO INHERIT, d integer NOT NULL, e integer)")
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
	if !ct.Columns[0].NotNull || !ct.Columns[0].NotNullNoInherit {
		t.Errorf("c: NotNull=%v NotNullNoInherit=%v want both true", ct.Columns[0].NotNull, ct.Columns[0].NotNullNoInherit)
	}
	if !ct.Columns[1].NotNull || ct.Columns[1].NotNullNoInherit {
		t.Errorf("d: NotNull=%v NotNullNoInherit=%v want true/false", ct.Columns[1].NotNull, ct.Columns[1].NotNullNoInherit)
	}
	if ct.Columns[2].NotNull || ct.Columns[2].NotNullNoInherit {
		t.Errorf("e: NotNull=%v NotNullNoInherit=%v want false/false", ct.Columns[2].NotNull, ct.Columns[2].NotNullNoInherit)
	}
}

// TestParseCreateTableColumnNamedNotNull covers the inline column-level
// `CONSTRAINT <name> NOT NULL [NO INHERIT]` form in CREATE TABLE (DU-002 slice
// 273). PG18 lets a column carry an explicitly named NOT NULL; the parser must
// capture the user-given name into ColumnDef.NotNullConstraintName (and the
// optional ` NO INHERIT` trailer into NotNullNoInherit) so the executor threads
// the name onto the constraint and pg_dump re-emits the inline `CONSTRAINT
// <name> NOT NULL` form. Before this slice the inline CONSTRAINT switch had no
// NOT NULL arm, so the constraint was silently dropped by the default skip.
func TestParseCreateTableColumnNamedNotNull(t *testing.T) {
	stmts, err := Parse("CREATE TABLE t (c integer CONSTRAINT c_nn NOT NULL NO INHERIT, d integer CONSTRAINT d_nn NOT NULL, e integer NOT NULL)")
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
	// c: named NOT NULL with NO INHERIT.
	if !ct.Columns[0].NotNull || ct.Columns[0].NotNullConstraintName != "c_nn" || !ct.Columns[0].NotNullNoInherit {
		t.Errorf("c: NotNull=%v name=%q NoInherit=%v want true/\"c_nn\"/true",
			ct.Columns[0].NotNull, ct.Columns[0].NotNullConstraintName, ct.Columns[0].NotNullNoInherit)
	}
	// d: named NOT NULL, no NO INHERIT.
	if !ct.Columns[1].NotNull || ct.Columns[1].NotNullConstraintName != "d_nn" || ct.Columns[1].NotNullNoInherit {
		t.Errorf("d: NotNull=%v name=%q NoInherit=%v want true/\"d_nn\"/false",
			ct.Columns[1].NotNull, ct.Columns[1].NotNullConstraintName, ct.Columns[1].NotNullNoInherit)
	}
	// e: unnamed NOT NULL — NotNullConstraintName must stay empty.
	if !ct.Columns[2].NotNull || ct.Columns[2].NotNullConstraintName != "" {
		t.Errorf("e: NotNull=%v name=%q want true/\"\"",
			ct.Columns[2].NotNull, ct.Columns[2].NotNullConstraintName)
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


// TestParseAlterStatisticsSetStatistics pins parsing of
// `ALTER STATISTICS name SET STATISTICS n` into an AlterStatisticsStmt carrying
// the target (DU-002 slice 317). -1 resets to the default.
func TestParseAlterStatisticsSetStatistics(t *testing.T) {
	for _, tc := range []struct {
		sql        string
		wantName   string
		wantSchema string
		wantTarget int
		wantHas    bool
	}{
		{"ALTER STATISTICS s SET STATISTICS 100", "s", "", 100, true},
		{"ALTER STATISTICS s SET STATISTICS 0", "s", "", 0, true},
		{"ALTER STATISTICS s SET STATISTICS -1", "s", "", -1, true}, // reset
		{"ALTER STATISTICS public.s SET STATISTICS 250", "s", "public", 250, true},
		{"ALTER STATISTICS IF EXISTS s SET STATISTICS 5", "s", "", 5, true},
		{"ALTER STATISTICS s RENAME TO s2", "s", "", 0, false}, // no-op form
	} {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		as, ok := stmts[0].(*AlterStatisticsStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", tc.sql, stmts[0])
		}
		if as.Name.Name != tc.wantName {
			t.Errorf("Parse(%q): Name=%q want %q", tc.sql, as.Name.Name, tc.wantName)
		}
		if as.Name.Schema != tc.wantSchema {
			t.Errorf("Parse(%q): Schema=%q want %q", tc.sql, as.Name.Schema, tc.wantSchema)
		}
		if as.HasTarget != tc.wantHas {
			t.Errorf("Parse(%q): HasTarget=%v want %v", tc.sql, as.HasTarget, tc.wantHas)
		}
		if tc.wantHas && as.Target != tc.wantTarget {
			t.Errorf("Parse(%q): Target=%d want %d", tc.sql, as.Target, tc.wantTarget)
		}
	}
}

// TestParseAlterStatisticsRenameOwnerSetSchema pins parsing of the
// RENAME TO / OWNER TO / SET SCHEMA forms of ALTER STATISTICS into an
// AlterStatisticsStmt's Action/NewName/NewOwner/NewSchema fields — previously
// these parsed to a fully unmodelled no-op (HasTarget=false, no Action),
// silently discarding the mutation. DU-002 slice 441.
func TestParseAlterStatisticsRenameOwnerSetSchema(t *testing.T) {
	for _, tc := range []struct {
		sql        string
		wantAction string
		wantValue  string
	}{
		{"ALTER STATISTICS s RENAME TO s2", "rename", "s2"},
		{"ALTER STATISTICS s OWNER TO alice", "owner", "alice"},
		{"ALTER STATISTICS s OWNER TO CURRENT_USER", "owner", "current_user"},
		{"ALTER STATISTICS s SET SCHEMA myschema", "setschema", "myschema"},
		{"ALTER STATISTICS IF EXISTS s SET SCHEMA myschema", "setschema", "myschema"},
	} {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		as, ok := stmts[0].(*AlterStatisticsStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", tc.sql, stmts[0])
		}
		if as.Action != tc.wantAction {
			t.Errorf("Parse(%q): Action=%q want %q", tc.sql, as.Action, tc.wantAction)
		}
		var got string
		switch tc.wantAction {
		case "rename":
			got = as.NewName
		case "owner":
			got = as.NewOwner
		case "setschema":
			got = as.NewSchema
		}
		if got != tc.wantValue {
			t.Errorf("Parse(%q): got %q want %q", tc.sql, got, tc.wantValue)
		}
		if as.HasTarget {
			t.Errorf("Parse(%q): HasTarget=true, want false for a RENAME/OWNER/SET SCHEMA form", tc.sql)
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

// TestParseAlterTableDetachPartition pins the partition-DETACH grammar,
// including the PG14+ `CONCURRENTLY` / `FINALIZE` trailer that follows the
// child name. A prior bug accepted the trailer BEFORE the child name, so the
// valid form `DETACH PARTITION child CONCURRENTLY` failed with a syntax error
// (the unconsumed CONCURRENTLY token) — the very first step of every
// detach-partition-concurrently isolation spec. M0118-0008.
func TestParseAlterTableDetachPartition(t *testing.T) {
	cases := []struct {
		sql         string
		wantChild   string
		wantConcurr bool
	}{
		{"ALTER TABLE d_listp DETACH PARTITION d_listp2", "d_listp2", false},
		{"ALTER TABLE d_listp DETACH PARTITION d_listp2 CONCURRENTLY", "d_listp2", true},
		{"ALTER TABLE d_listp DETACH PARTITION d_listp2 FINALIZE", "d_listp2", false},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", tc.sql, stmts[0])
		}
		if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableDetachPartition {
			t.Fatalf("Parse(%q): actions=%+v", tc.sql, at.Actions)
		}
		act := at.Actions[0]
		if got := act.DetachPartitionChild.String(); got != tc.wantChild {
			t.Errorf("Parse(%q): child=%q want %q", tc.sql, got, tc.wantChild)
		}
		if act.DetachConcurrently != tc.wantConcurr {
			t.Errorf("Parse(%q): concurrently=%v want %v", tc.sql, act.DetachConcurrently, tc.wantConcurr)
		}
	}
}

// TestParseAlterTableReplicaIdentity covers the REPLICA IDENTITY action in all
// four forms (DEFAULT / FULL / NOTHING / USING INDEX name). The mode is stored
// as the single-char relreplident code on ReplicaIdentityMode; the USING INDEX
// form additionally captures the index name on ReplicaIdentityIndex. The parsed
// action drives catalog.Table.ReplicaIdentity so pg_class.relreplident
// round-trips through pg_dump. DU-002 slice 305.
func TestParseAlterTableReplicaIdentity(t *testing.T) {
	cases := []struct {
		sql       string
		wantMode  string
		wantIndex string
	}{
		{"ALTER TABLE t REPLICA IDENTITY DEFAULT", "d", ""},
		{"ALTER TABLE t REPLICA IDENTITY FULL", "f", ""},
		{"ALTER TABLE t REPLICA IDENTITY NOTHING", "n", ""},
		{"ALTER TABLE t REPLICA IDENTITY USING INDEX t_pkey", "i", "t_pkey"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", tc.sql, stmts[0])
		}
		if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableReplicaIdentity {
			t.Fatalf("Parse(%q): actions=%+v", tc.sql, at.Actions)
		}
		act := at.Actions[0]
		if act.ReplicaIdentityMode != tc.wantMode {
			t.Errorf("Parse(%q): mode=%q want %q", tc.sql, act.ReplicaIdentityMode, tc.wantMode)
		}
		if act.ReplicaIdentityIndex != tc.wantIndex {
			t.Errorf("Parse(%q): index=%q want %q", tc.sql, act.ReplicaIdentityIndex, tc.wantIndex)
		}
	}
}

// TestParseAlterTableSetAccessMethod covers `ALTER TABLE ... SET ACCESS METHOD name`
// (PG15+). pg_dump emits this for partitioned tables whose relam differs from the
// default. goopg only supports `heap`; the executor rejects any other AM.
// DU-002: next blocker after NOT NULL colname in CREATE TABLE INHERITS.
func TestParseAlterTableSetAccessMethod(t *testing.T) {
	cases := []struct {
		sql      string
		wantAM   string
	}{
		{"ALTER TABLE t SET ACCESS METHOD heap", "heap"},
		{"ALTER TABLE t SET ACCESS METHOD HEAP", "heap"},
		{"ALTER TABLE public.part SET ACCESS METHOD heap", "heap"},
	}
	for _, tc := range cases {
		stmts, err := Parse(tc.sql)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.sql, err)
		}
		at, ok := stmts[0].(*AlterTableStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", tc.sql, stmts[0])
		}
		if len(at.Actions) != 1 || at.Actions[0].Kind != AlterTableSetAccessMethod {
			t.Fatalf("Parse(%q): actions=%+v", tc.sql, at.Actions)
		}
		act := at.Actions[0]
		if act.AccessMethodName != tc.wantAM {
			t.Errorf("Parse(%q): AccessMethodName=%q want %q", tc.sql, act.AccessMethodName, tc.wantAM)
		}
	}
}
