package sqlparser

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
		// mixed with column-level constraints
		"CREATE TABLE t (a int PRIMARY KEY, b int, CONSTRAINT u UNIQUE (b))",
		"CREATE TABLE t (a int NOT NULL, b int DEFAULT 0, UNIQUE (a), CONSTRAINT ck CHECK (b >= 0))",
	} {
		assertParity(t, q)
	}
}
