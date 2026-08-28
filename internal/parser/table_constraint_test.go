package parser

import "testing"

// TestTableConstraintParity pins CREATE TABLE's table-level constraint
// elements against legacy.
//
// These were parsed and then dropped on the floor: create_table_stmt's element
// loop consumed only e.pk and e.uq, so tableElem.namedPk / namedUq / check /
// checkName were built by table_element and never read. Worse, the anonymous
// `UNIQUE (cols)` alternative returned a bare &tableElem{} and threw its
// columns away, so CreateTableStmt.TableUniques was ALWAYS nil on the routed
// path even though the loop assigned it.
//
// The parallel slices matter: legacy appends to all five UNIQUE slices and
// both CHECK slices per anonymous constraint (internal/parser/ddl.go:3958-4016,
// :4018-4058), and canonDump distinguishes ∅ from [false]. A named CHECK goes
// to TableNamedChecks INSTEAD and leaves the anonymous slices untouched
// (ddl.go:4191-4238); a named PRIMARY KEY fills both PrimaryKey and
// NamedConstraints (ddl.go:4103-4123).
func TestTableConstraintParity(t *testing.T) {
	for _, q := range []string{
		// anonymous
		"CREATE TABLE t (a int, PRIMARY KEY (a))",
		"CREATE TABLE t (a int, b int, PRIMARY KEY (a, b))",
		"CREATE TABLE t (a int, UNIQUE (a))",
		"CREATE TABLE t (a int, b int, UNIQUE (a))",
		"CREATE TABLE t (a int, b int, UNIQUE (a, b))",
		"CREATE TABLE t (a int, b int, PRIMARY KEY (a), UNIQUE (b))",
		// named
		"CREATE TABLE t (a int, CONSTRAINT pk PRIMARY KEY (a))",
		"CREATE TABLE t (a int, b int, CONSTRAINT pk PRIMARY KEY (a, b))",
		"CREATE TABLE t (a int, CONSTRAINT u_a UNIQUE (a))",
		"CREATE TABLE t (a int, b int, CONSTRAINT u UNIQUE (a, b))",
		"CREATE TABLE t (a int, CONSTRAINT ck CHECK (a > 0))",
		"CREATE TABLE t (a text, CONSTRAINT ck CHECK (a <> 'x'))",
		// table-level CHECK and FOREIGN KEY — no grammar until 2026-08-27, so
		// `CREATE TABLE t (a int, b int, FOREIGN KEY (b) REFERENCES o (id))`
		// was a hard syntax error on the routed path. FOREIGN was one of the
		// reserved tokens no production consumed (TestReservedKeywordsReachable).
		"CREATE TABLE t (a int, CHECK (a > 0))",
		"CREATE TABLE t (a int, check (a < 10))",
		"CREATE TABLE t (a text, CHECK (a <> 'x'))",
		"CREATE TABLE t (a int, CHECK (a > 0), UNIQUE (a))",
		"CREATE TABLE t (a int, b int, FOREIGN KEY (b) REFERENCES o (id))",
		"CREATE TABLE t (a int, b int, FOREIGN KEY (b) REFERENCES o)",
		"CREATE TABLE t (a int, b int, FOREIGN KEY (a, b) REFERENCES o (x, y))",
		"CREATE TABLE t (a int, b int, FOREIGN KEY (b) REFERENCES o (id) ON DELETE CASCADE)",
		"CREATE TABLE t (a int, b int, foreign key (b) references o (id) on update set null)",
		"CREATE TABLE t (a int, b int, CONSTRAINT fk FOREIGN KEY (b) REFERENCES o (id))",
		"CREATE TABLE t (a int, b int, PRIMARY KEY (a), FOREIGN KEY (b) REFERENCES o (id))",
		// mixed with column-level constraints
		"CREATE TABLE t (a int PRIMARY KEY, b int, CONSTRAINT u UNIQUE (b))",
		"CREATE TABLE t (a int NOT NULL, b int DEFAULT 0, UNIQUE (a), CONSTRAINT ck CHECK (b >= 0))",
	} {
		assertParity(t, q)
	}
}
