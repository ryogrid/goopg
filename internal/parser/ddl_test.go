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

func TestParseCreateTableBareCharDefaultsToCharacterOne(t *testing.T) {
	stmts, err := Parse(`CREATE TABLE t (a char, b "char")`)
	if err != nil {
		t.Fatal(err)
	}
	ct := stmts[0].(*CreateTableStmt)
	if got := ct.Columns[0].Type; got.Name != "char" || len(got.Args) != 1 || got.Args[0] != 1 {
		t.Fatalf("bare char type=%+v, want char(1)", got)
	}
	if got := ct.Columns[1].Type; got.Name != "char" || len(got.Args) != 0 {
		t.Fatalf("quoted char type=%+v, want internal char", got)
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

// TestParseCreateIndexColOrders pins the per-column ASC/DESC + NULLS FIRST/LAST
// capture (DU-002 slice 56). goopg previously parsed and discarded these
// modifiers, so a DESC index lost its ordering on dump. NullsFirst defaults to
// the descending flag (NULLS FIRST is the btree default for DESC) unless an
// explicit NULLS clause overrides it. Also guards that `NULLS` is not mis-read
// as an opclass name in `(col NULLS FIRST)`.
func TestParseCreateIndexColOrders(t *testing.T) {
	cases := []struct {
		in   string
		want []IndexColOrder
	}{
		{"CREATE INDEX i ON t (a)", []IndexColOrder{{false, false}}},
		{"CREATE INDEX i ON t (a DESC)", []IndexColOrder{{true, true}}},
		{"CREATE INDEX i ON t (a ASC)", []IndexColOrder{{false, false}}},
		{"CREATE INDEX i ON t (a DESC NULLS LAST)", []IndexColOrder{{true, false}}},
		{"CREATE INDEX i ON t (a NULLS FIRST)", []IndexColOrder{{false, true}}},
		{"CREATE INDEX i ON t (a DESC, b NULLS FIRST)", []IndexColOrder{{true, true}, {false, true}}},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ci := stmts[0].(*CreateIndexStmt)
		if len(ci.ColOrders) != len(c.want) {
			t.Fatalf("Parse(%q): ColOrders=%+v want %+v", c.in, ci.ColOrders, c.want)
		}
		for i, w := range c.want {
			if ci.ColOrders[i] != w {
				t.Errorf("Parse(%q): ColOrders[%d]=%+v want %+v", c.in, i, ci.ColOrders[i], w)
			}
		}
	}
}

// TestParseCreateIndexNullsNotDistinct pins the PostgreSQL 15+ NULLS [NOT]
// DISTINCT capture (DU-002 slice 134). goopg previously parsed and discarded the
// clause, so a NULLS NOT DISTINCT unique index dumped as a plain one — a silent
// loss of the NULL-deduplication semantics. The bare/default `NULLS DISTINCT`
// (and an omitted clause) must leave the flag false; only `NULLS NOT DISTINCT`
// sets it. The clause may appear before or after WHERE/WITH in practice; here we
// pin the canonical position (after the column list) plus the WHERE combination.
func TestParseCreateIndexNullsNotDistinct(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"CREATE UNIQUE INDEX i ON t (a)", false},
		{"CREATE UNIQUE INDEX i ON t (a) NULLS DISTINCT", false},
		{"CREATE UNIQUE INDEX i ON t (a) NULLS NOT DISTINCT", true},
		{"CREATE UNIQUE INDEX i ON t (a) NULLS NOT DISTINCT WHERE a > 0", true},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ci := stmts[0].(*CreateIndexStmt)
		if ci.NullsNotDistinct != c.want {
			t.Errorf("Parse(%q): NullsNotDistinct=%v want %v", c.in, ci.NullsNotDistinct, c.want)
		}
	}
}

// TestParseTableUniqueNullsNotDistinct pins the PostgreSQL 15+ NULLS [NOT]
// DISTINCT capture on a TABLE-level UNIQUE constraint (DU-002 slice 135). For a
// constraint the clause precedes the column list (`UNIQUE NULLS NOT DISTINCT
// (cols)`, ruleutils.c pg_get_constraintdef order), unlike CREATE INDEX where it
// trails. The flag rides parallel to TableUniques so the backing index records
// it and pg_get_constraintdef re-emits it. Only `NULLS NOT DISTINCT` sets it;
// the bare/default `NULLS DISTINCT` and an omitted clause leave it false.
func TestParseTableUniqueNullsNotDistinct(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"CREATE TABLE t (a int, b int, UNIQUE (a))", false},
		{"CREATE TABLE t (a int, b int, UNIQUE NULLS DISTINCT (a))", false},
		{"CREATE TABLE t (a int, b int, UNIQUE NULLS NOT DISTINCT (a))", true},
		{"CREATE TABLE t (a int, b int, UNIQUE NULLS NOT DISTINCT (a) INCLUDE (b))", true},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if len(ct.TableUniques) != 1 {
			t.Fatalf("Parse(%q): expected 1 table UNIQUE, got %d", c.in, len(ct.TableUniques))
		}
		if len(ct.TableUniqueNullsNotDistinct) != 1 {
			t.Fatalf("Parse(%q): TableUniqueNullsNotDistinct len=%d want 1", c.in, len(ct.TableUniqueNullsNotDistinct))
		}
		if ct.TableUniqueNullsNotDistinct[0] != c.want {
			t.Errorf("Parse(%q): NullsNotDistinct=%v want %v", c.in, ct.TableUniqueNullsNotDistinct[0], c.want)
		}
	}
}

// TestParseTableUniqueDeferrable pins the DEFERRABLE [INITIALLY DEFERRED |
// INITIALLY IMMEDIATE] capture on a TABLE-level UNIQUE constraint (DU-002
// slice 139). The clause trails the column list (and any INCLUDE list); it was
// previously a hard parse error for the anonymous UNIQUE form (no DEFERRABLE
// branch existed, unlike PRIMARY KEY which silently discarded it). The two flags
// ride parallel to TableUniques so the backing index records them and
// pg_get_constraintdef / pg_constraint re-emit the clause. INITIALLY DEFERRED
// implies DEFERRABLE; INITIALLY IMMEDIATE is the default and leaves both false.
func TestParseTableUniqueDeferrable(t *testing.T) {
	cases := []struct {
		in           string
		wantDefer    bool
		wantDeferred bool
	}{
		{"CREATE TABLE t (a int, UNIQUE (a))", false, false},
		{"CREATE TABLE t (a int, UNIQUE (a) NOT DEFERRABLE)", false, false},
		{"CREATE TABLE t (a int, UNIQUE (a) DEFERRABLE)", true, false},
		{"CREATE TABLE t (a int, UNIQUE (a) DEFERRABLE INITIALLY IMMEDIATE)", true, false},
		{"CREATE TABLE t (a int, UNIQUE (a) DEFERRABLE INITIALLY DEFERRED)", true, true},
		{"CREATE TABLE t (a int, UNIQUE (a) INITIALLY DEFERRED)", true, true},
		{"CREATE TABLE t (a int, b int, UNIQUE (a) INCLUDE (b) DEFERRABLE INITIALLY DEFERRED)", true, true},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if len(ct.TableUniques) != 1 {
			t.Fatalf("Parse(%q): expected 1 table UNIQUE, got %d", c.in, len(ct.TableUniques))
		}
		if len(ct.TableUniqueDeferrable) != 1 || len(ct.TableUniqueInitiallyDeferred) != 1 {
			t.Fatalf("Parse(%q): deferrable flag arrays len=%d/%d want 1/1", c.in,
				len(ct.TableUniqueDeferrable), len(ct.TableUniqueInitiallyDeferred))
		}
		if ct.TableUniqueDeferrable[0] != c.wantDefer {
			t.Errorf("Parse(%q): Deferrable=%v want %v", c.in, ct.TableUniqueDeferrable[0], c.wantDefer)
		}
		if ct.TableUniqueInitiallyDeferred[0] != c.wantDeferred {
			t.Errorf("Parse(%q): InitiallyDeferred=%v want %v", c.in, ct.TableUniqueInitiallyDeferred[0], c.wantDeferred)
		}
	}
}

// TestParseColumnUniqueNullsNotDistinct pins the PostgreSQL 15+ NULLS [NOT]
// DISTINCT capture on an INLINE column UNIQUE constraint (DU-002 slice 136).
// `a int UNIQUE NULLS NOT DISTINCT` rides on ColumnDef.UniqueNullsNotDistinct so
// the backing index records it and pg_get_constraintdef re-emits
// `UNIQUE NULLS NOT DISTINCT (a)`. Only `NULLS NOT DISTINCT` sets it; the
// bare/default `NULLS DISTINCT` and an omitted clause leave it false.
func TestParseColumnUniqueNullsNotDistinct(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"CREATE TABLE t (a int UNIQUE)", false},
		{"CREATE TABLE t (a int UNIQUE NULLS DISTINCT)", false},
		{"CREATE TABLE t (a int UNIQUE NULLS NOT DISTINCT)", true},
		{"CREATE TABLE t (a int UNIQUE NULLS NOT DISTINCT, b text)", true},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ct := stmts[0].(*CreateTableStmt)
		col := ct.Columns[0]
		if !col.Unique {
			t.Fatalf("Parse(%q): column a Unique=false, want true", c.in)
		}
		if col.UniqueNullsNotDistinct != c.want {
			t.Errorf("Parse(%q): UniqueNullsNotDistinct=%v want %v", c.in, col.UniqueNullsNotDistinct, c.want)
		}
	}
}

// TestParseColumnNamedUniqueNullsNotDistinct pins the inline NAMED column UNIQUE
// form (`a int CONSTRAINT myname UNIQUE [NULLS NOT DISTINCT]`) — DU-002 slice
// 137. Before this slice the `CONSTRAINT name UNIQUE` column case absorbed the
// keyword WITHOUT setting col.Unique, so no backing index was created (silent
// loss). Now col.Unique is set, the explicit name rides on
// ColumnDef.UniqueConstraintName, and the optional PG15+ NULLS NOT DISTINCT
// clause is captured.
func TestParseColumnNamedUniqueNullsNotDistinct(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantNND  bool
	}{
		{"CREATE TABLE t (a int CONSTRAINT myname UNIQUE)", "myname", false},
		{"CREATE TABLE t (a int CONSTRAINT myname UNIQUE NULLS DISTINCT)", "myname", false},
		{"CREATE TABLE t (a int CONSTRAINT myname UNIQUE NULLS NOT DISTINCT)", "myname", true},
		{"CREATE TABLE t (a int CONSTRAINT u_a UNIQUE NULLS NOT DISTINCT, b text)", "u_a", true},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ct := stmts[0].(*CreateTableStmt)
		col := ct.Columns[0]
		if !col.Unique {
			t.Fatalf("Parse(%q): column a Unique=false, want true", c.in)
		}
		if col.UniqueConstraintName != c.wantName {
			t.Errorf("Parse(%q): UniqueConstraintName=%q want %q", c.in, col.UniqueConstraintName, c.wantName)
		}
		if col.UniqueNullsNotDistinct != c.wantNND {
			t.Errorf("Parse(%q): UniqueNullsNotDistinct=%v want %v", c.in, col.UniqueNullsNotDistinct, c.wantNND)
		}
	}
}

// TestParseTableNamedUniqueNullsNotDistinct pins the NAMED table-level UNIQUE
// form (`CONSTRAINT name UNIQUE [NULLS NOT DISTINCT] (cols)`) — DU-002 slice
// 138. Before this slice the named table-level UNIQUE case did NOT parse the
// optional PG15+ NULLS [NOT] DISTINCT clause that precedes the column list, so
// the `(` lookahead failed and the whole named constraint was silently dropped
// from the table. Now the flag is captured on TableConstraintDef.NullsNotDistinct
// (parallel to the anonymous form's TableUniqueNullsNotDistinct).
func TestParseTableNamedUniqueNullsNotDistinct(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantNND  bool
	}{
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE (a))", "u_a", false},
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE NULLS DISTINCT (a))", "u_a", false},
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE NULLS NOT DISTINCT (a))", "u_a", true},
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE NULLS NOT DISTINCT (a) INCLUDE (b))", "u_a", true},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if len(ct.NamedConstraints) != 1 {
			t.Fatalf("Parse(%q): expected 1 NamedConstraint, got %d", c.in, len(ct.NamedConstraints))
		}
		nc := ct.NamedConstraints[0]
		if nc.Name != c.wantName {
			t.Errorf("Parse(%q): Name=%q want %q", c.in, nc.Name, c.wantName)
		}
		if nc.IsPrimary || nc.IsExclusion {
			t.Errorf("Parse(%q): expected plain UNIQUE, got IsPrimary=%v IsExclusion=%v", c.in, nc.IsPrimary, nc.IsExclusion)
		}
		if len(nc.Columns) != 1 || nc.Columns[0] != "a" {
			t.Errorf("Parse(%q): Columns=%v want [a]", c.in, nc.Columns)
		}
		if nc.NullsNotDistinct != c.wantNND {
			t.Errorf("Parse(%q): NullsNotDistinct=%v want %v", c.in, nc.NullsNotDistinct, c.wantNND)
		}
	}
}

// TestParseTableNamedUniqueDeferrable pins the DEFERRABLE [INITIALLY DEFERRED |
// INITIALLY IMMEDIATE] capture on a NAMED table-level UNIQUE constraint
// (`CONSTRAINT name UNIQUE (cols) DEFERRABLE …`) — DU-002 slice 140. The clause
// trails the column list (and any INCLUDE list); before this slice the named
// UNIQUE case parsed NO trailing DEFERRABLE, so the keyword was a HARD PARSE
// ERROR (trailing tokens after the column list). The two flags ride on
// TableConstraintDef.Deferrable / InitiallyDeferred so the backing index records
// them and pg_get_constraintdef / pg_constraint re-emit the clause. INITIALLY
// DEFERRED implies DEFERRABLE; INITIALLY IMMEDIATE is the default (both false).
func TestParseTableNamedUniqueDeferrable(t *testing.T) {
	cases := []struct {
		in           string
		wantDefer    bool
		wantDeferred bool
	}{
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE (a))", false, false},
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE (a) NOT DEFERRABLE)", false, false},
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE (a) DEFERRABLE)", true, false},
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE (a) DEFERRABLE INITIALLY IMMEDIATE)", true, false},
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE (a) DEFERRABLE INITIALLY DEFERRED)", true, true},
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE (a) INITIALLY DEFERRED)", true, true},
		{"CREATE TABLE t (a int, b int, CONSTRAINT u_a UNIQUE (a) INCLUDE (b) DEFERRABLE INITIALLY DEFERRED)", true, true},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ct := stmts[0].(*CreateTableStmt)
		if len(ct.NamedConstraints) != 1 {
			t.Fatalf("Parse(%q): expected 1 NamedConstraint, got %d", c.in, len(ct.NamedConstraints))
		}
		nc := ct.NamedConstraints[0]
		if nc.Name != "u_a" {
			t.Errorf("Parse(%q): Name=%q want u_a", c.in, nc.Name)
		}
		if nc.IsPrimary || nc.IsExclusion {
			t.Errorf("Parse(%q): expected plain UNIQUE, got IsPrimary=%v IsExclusion=%v", c.in, nc.IsPrimary, nc.IsExclusion)
		}
		if nc.Deferrable != c.wantDefer {
			t.Errorf("Parse(%q): Deferrable=%v want %v", c.in, nc.Deferrable, c.wantDefer)
		}
		if nc.InitiallyDeferred != c.wantDeferred {
			t.Errorf("Parse(%q): InitiallyDeferred=%v want %v", c.in, nc.InitiallyDeferred, c.wantDeferred)
		}
	}
}

// TestParseColumnUniqueDeferrable pins the DEFERRABLE [INITIALLY DEFERRED |
// INITIALLY IMMEDIATE] capture on an INLINE column UNIQUE constraint
// (`a int UNIQUE DEFERRABLE …`, plus the named `CONSTRAINT n UNIQUE DEFERRABLE`
// form) — DU-002 slice 141. Before this slice the inline column UNIQUE case
// parsed only the optional NULLS [NOT] DISTINCT clause; a trailing DEFERRABLE
// fell through to the default arm of the column-constraint loop and became a
// HARD PARSE ERROR for the whole CREATE TABLE. The two flags ride on
// ColumnDef.UniqueDeferrable / UniqueInitiallyDeferred so the backing index
// records them and pg_get_constraintdef / pg_constraint re-emit the clause.
// INITIALLY DEFERRED implies DEFERRABLE; INITIALLY IMMEDIATE is the default.
func TestParseColumnUniqueDeferrable(t *testing.T) {
	cases := []struct {
		in           string
		wantName     string
		wantDefer    bool
		wantDeferred bool
	}{
		{"CREATE TABLE t (a int UNIQUE)", "", false, false},
		{"CREATE TABLE t (a int UNIQUE NOT DEFERRABLE)", "", false, false},
		{"CREATE TABLE t (a int UNIQUE DEFERRABLE)", "", true, false},
		{"CREATE TABLE t (a int UNIQUE DEFERRABLE INITIALLY IMMEDIATE)", "", true, false},
		{"CREATE TABLE t (a int UNIQUE DEFERRABLE INITIALLY DEFERRED)", "", true, true},
		{"CREATE TABLE t (a int UNIQUE INITIALLY DEFERRED)", "", true, true},
		// Composes with NULLS NOT DISTINCT and with a following column.
		{"CREATE TABLE t (a int UNIQUE NULLS NOT DISTINCT DEFERRABLE INITIALLY DEFERRED, b text)", "", true, true},
		// Named inline column UNIQUE.
		{"CREATE TABLE t (a int CONSTRAINT u_a UNIQUE DEFERRABLE INITIALLY DEFERRED)", "u_a", true, true},
		{"CREATE TABLE t (a int CONSTRAINT u_a UNIQUE DEFERRABLE)", "u_a", true, false},
		{"CREATE TABLE t (a int CONSTRAINT u_a UNIQUE NOT DEFERRABLE)", "u_a", false, false},
		// DEFERRABLE composes with a following NOT NULL constraint on the column.
		{"CREATE TABLE t (a int UNIQUE DEFERRABLE NOT NULL)", "", true, false},
	}
	for _, c := range cases {
		stmts, err := Parse(c.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.in, err)
		}
		ct := stmts[0].(*CreateTableStmt)
		col := ct.Columns[0]
		if !col.Unique {
			t.Fatalf("Parse(%q): column a Unique=false, want true", c.in)
		}
		if col.UniqueConstraintName != c.wantName {
			t.Errorf("Parse(%q): UniqueConstraintName=%q want %q", c.in, col.UniqueConstraintName, c.wantName)
		}
		if col.UniqueDeferrable != c.wantDefer {
			t.Errorf("Parse(%q): UniqueDeferrable=%v want %v", c.in, col.UniqueDeferrable, c.wantDefer)
		}
		if col.UniqueInitiallyDeferred != c.wantDeferred {
			t.Errorf("Parse(%q): UniqueInitiallyDeferred=%v want %v", c.in, col.UniqueInitiallyDeferred, c.wantDeferred)
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

// TestParseAlterIndexAlterColumnSet pins two parser fixes in ddl.go (M0097-0023):
//  1. The column name after ALTER COLUMN may be an unreserved keyword — the
//     parser now accepts IsColNameKeyword tokens in addition to TokenIdent.
//  2. SET is a keyword token (KwSet), not an identifier — the parser now
//     accepts p.acceptKeyword(KwSet) in addition to p.acceptIdentKeyword("set").
//
// The resulting AST must have Name="myidx" and exactly one action of kind
// AlterTableAlterColumnSet.
func TestParseAlterIndexAlterColumnSet(t *testing.T) {
	stmts, err := Parse("ALTER INDEX myidx ALTER COLUMN id SET (n_distinct=100)")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("expected 1 statement, got %d", len(stmts))
	}
	at, ok := stmts[0].(*AlterTableStmt)
	if !ok {
		t.Fatalf("expected *AlterTableStmt, got %T", stmts[0])
	}
	if at.Name.Name != "myidx" {
		t.Errorf("Name.Name = %q, want %q", at.Name.Name, "myidx")
	}
	if len(at.Actions) != 1 {
		t.Fatalf("Actions len=%d, want 1", len(at.Actions))
	}
	if at.Actions[0].Kind != AlterTableAlterColumnSet {
		t.Errorf("Actions[0].Kind = %v, want AlterTableAlterColumnSet", at.Actions[0].Kind)
	}
}

// TestParseColumnDefCompression verifies that COMPRESSION method is silently
// ignored in column definitions (PG 14+). goopg v0 doesn't track compression.
func TestParseColumnDefCompression(t *testing.T) {
	sql := `CREATE TABLE t (b varchar COMPRESSION pglz, c text COMPRESSION lz4 NOT NULL)`
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("expected 1 statement, got %d", len(stmts))
	}
	ct, ok := stmts[0].(*CreateTableStmt)
	if !ok {
		t.Fatalf("expected *CreateTableStmt, got %T", stmts[0])
	}
	if len(ct.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(ct.Columns))
	}
	if ct.Columns[0].Name != "b" || ct.Columns[0].Type.Name != "varchar" {
		t.Errorf("col[0]=%+v", ct.Columns[0])
	}
	if ct.Columns[1].Name != "c" || ct.Columns[1].Type.Name != "text" {
		t.Errorf("col[1]=%+v", ct.Columns[1])
	}
	if !ct.Columns[1].NotNull {
		t.Errorf("col[1].NotNull=false, want true")
	}
}
