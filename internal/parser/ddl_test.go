package parser

import (
	"fmt"
	"testing"
)

// TestParseCreateTablePgbench: the four CREATE TABLE statements
// pgbench -i issues round-trip through the parser. Pins type
// modifiers (`char(22)`), inline NOT NULL, and timestamp typing.
func TestParseCreateTablePgbench(t *testing.T) {
	stmts, err := Parse("create table pgbench_history (tid int, bid int, aid int, delta int, mtime timestamp, filler char(22))")
	if err != nil {
		t.Fatal(err)
	}
	ct, ok := stmts[0].(*CreateTableStmt)
	if !ok {
		t.Fatalf("got %T", stmts[0])
	}
	if ct.Name.Name != "pgbench_history" {
		t.Errorf("name=%+v", ct.Name)
	}
	if len(ct.Columns) != 6 {
		t.Fatalf("columns=%d", len(ct.Columns))
	}
	if ct.Columns[5].Name != "filler" || ct.Columns[5].Type.Name != "char" || len(ct.Columns[5].Type.Args) != 1 || ct.Columns[5].Type.Args[0] != 22 {
		t.Errorf("filler col=%+v", ct.Columns[5])
	}
	if ct.Columns[4].Type.Name != "timestamp" {
		t.Errorf("mtime col=%+v", ct.Columns[4])
	}
}

// TestParseCreateTableConstraints covers NOT NULL, inline PRIMARY KEY,
// table-level PRIMARY KEY, and the WITH (fillfactor=N) tail pgbench
// emits with --fillfactor.
func TestParseCreateTableConstraints(t *testing.T) {
	stmts, err := Parse("CREATE UNLOGGED TABLE IF NOT EXISTS pgbench_branches (bid int NOT NULL PRIMARY KEY, bbalance int, filler char(88)) WITH (fillfactor = 90)")
	if err != nil {
		t.Fatal(err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if !ct.Unlogged || !ct.IfNotExists {
		t.Errorf("flags: unlogged=%v ifne=%v", ct.Unlogged, ct.IfNotExists)
	}
	if len(ct.PrimaryKey) != 1 || ct.PrimaryKey[0] != "bid" {
		t.Errorf("PrimaryKey=%+v", ct.PrimaryKey)
	}
	if !ct.Columns[0].NotNull || !ct.Columns[0].Primary {
		t.Errorf("col[0]=%+v", ct.Columns[0])
	}
	if ct.With["fillfactor"] != "90" {
		t.Errorf("with=%+v", ct.With)
	}
}

// TestParseCreateTableTableLevelPrimaryKey: PK is declared as a
// constraint at the end of the column list rather than inline.
func TestParseCreateTableTableLevelPrimaryKey(t *testing.T) {
	stmts, err := Parse("CREATE TABLE t (a int, b int, primary key (a, b))")
	if err != nil {
		t.Fatal(err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if len(ct.PrimaryKey) != 2 || ct.PrimaryKey[0] != "a" || ct.PrimaryKey[1] != "b" {
		t.Errorf("pk=%+v", ct.PrimaryKey)
	}
}

// TestParseCreateIndex covers named, unnamed, unique, and USING-method
// variants. pgbench's primary keys come via ALTER TABLE; CREATE INDEX
// is what plain DDL paths use.
func TestParseCreateIndex(t *testing.T) {
	cases := []struct {
		in     string
		name   string
		uniq   bool
		method string
		cols   []string
	}{
		{"CREATE INDEX idx_aid ON pgbench_accounts (aid)", "idx_aid", false, "", []string{"aid"}},
		{"create unique index on t (x, y)", "", true, "", []string{"x", "y"}},
		{"create index if not exists ix on s.t using btree (a)", "ix", false, "btree", []string{"a"}},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ci, ok := stmts[0].(*CreateIndexStmt)
		if !ok {
			t.Fatalf("Parse(%q): got %T", c.in, stmts[0])
		}
		if ci.Name != c.name || ci.Unique != c.uniq || ci.Method != c.method {
			t.Errorf("Parse(%q): %+v", c.in, ci)
		}
		if len(ci.Columns) != len(c.cols) {
			t.Errorf("Parse(%q): cols=%v want %v", c.in, ci.Columns, c.cols)
		}
	}
}

// TestParseDropTablePgbench: pgbench's exact "drop table if exists
// a, b, c, d" string.
func TestParseDropTablePgbench(t *testing.T) {
	stmts, err := Parse("drop table if exists pgbench_accounts, pgbench_branches, pgbench_history, pgbench_tellers")
	if err != nil {
		t.Fatal(err)
	}
	dt := stmts[0].(*DropTableStmt)
	if !dt.IfExists || len(dt.Names) != 4 || dt.Names[0].Name != "pgbench_accounts" {
		t.Errorf("dt=%+v", dt)
	}
}

// TestParseDropIndexCascade pins the CASCADE behaviour discriminator.
func TestParseDropIndexCascade(t *testing.T) {
	stmts, err := Parse("DROP INDEX my_ix CASCADE")
	if err != nil {
		t.Fatal(err)
	}
	di := stmts[0].(*DropIndexStmt)
	if di.Behavior != DropCascade {
		t.Errorf("behavior=%v", di.Behavior)
	}
}

// TestParseTruncate: optional TABLE keyword and comma-separated names.
func TestParseTruncate(t *testing.T) {
	stmts, err := Parse("truncate table pgbench_accounts, pgbench_branches, pgbench_history, pgbench_tellers")
	if err != nil {
		t.Fatal(err)
	}
	tr := stmts[0].(*TruncateStmt)
	if len(tr.Names) != 4 {
		t.Errorf("names=%+v", tr.Names)
	}
	stmts2, err := Parse("TRUNCATE pgbench_history") // no TABLE keyword
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stmts2[0].(*TruncateStmt); !ok {
		t.Errorf("got %T", stmts2[0])
	}
}

// TestParseDDLSyntaxErrors pins SyntaxError type for canonical
// missing-piece cases.
func TestParseDDLSyntaxErrors(t *testing.T) {
	cases := []string{
		"CREATE",                 // nothing after CREATE
		"CREATE TABLE",           // missing name
		"CREATE TABLE t",         // missing column list
		"CREATE TABLE t (a)",     // missing type
		"CREATE TABLE t (a int,", // dangling comma
		"CREATE INDEX ON",        // missing target after ON
		"CREATE INDEX",           // nothing after INDEX
		"DROP",                   // missing object kind
		"DROP TABLE",             // missing names
		"DROP TABLE IF",          // missing EXISTS
		"DROP TABLE IF EXISTS",   // missing names
		"TRUNCATE",               // missing names
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

// TestParseCreateTableDefaultExpr pins M0103-0007 rung 13's parser surface:
// CREATE TABLE column DEFAULT clauses must capture an AST so the apply
// worker can evaluate them when filling subscriber-extra columns at INSERT
// time. Prior to rung 13 the DEFAULT clause was tokenized-and-dropped.
func TestParseCreateTableDefaultExpr(t *testing.T) {
	cases := []struct {
		in       string
		colName  string
		wantKind string // type-of-AST tag for the captured expression
	}{
		{"CREATE TABLE t (id int, note text DEFAULT 'unknown')", "note", "*parser.StringConst"},
		{"CREATE TABLE t (id int, counter int DEFAULT 0)", "counter", "*parser.IntegerConst"},
		{"CREATE TABLE t (id int, flag boolean DEFAULT TRUE)", "flag", "*parser.BooleanConst"},
		{"CREATE TABLE t (id int, n int DEFAULT NULL)", "n", "*parser.NullConst"},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ct, ok := stmts[0].(*CreateTableStmt)
		if !ok {
			t.Fatalf("Parse(%q): not a CreateTableStmt", c.in)
		}
		var col *ColumnDef
		for i := range ct.Columns {
			if ct.Columns[i].Name == c.colName {
				col = &ct.Columns[i]
				break
			}
		}
		if col == nil {
			t.Fatalf("Parse(%q): column %q not found", c.in, c.colName)
		}
		if col.DefaultExpr == nil {
			t.Fatalf("Parse(%q): DefaultExpr nil, want %s", c.in, c.wantKind)
		}
		got := typeOf(col.DefaultExpr)
		if got != c.wantKind {
			t.Errorf("Parse(%q): DefaultExpr type=%s want %s", c.in, got, c.wantKind)
		}
	}
}

func typeOf(v interface{}) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprintf("%T", v)
}
