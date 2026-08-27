package parser

import "testing"

// TestLikeTableElementParity — `LIKE source_table [INCLUDING|EXCLUDING opt ...]`
// (gram.y TableLikeClause).
//
// The AST field is only CreateTableStmt.LikeTables (the table NAME), but the
// options are NOT discarded: legacy ENCODES them into the BodyOrder marker as
// `@@LIKE:<dotted name>` followed by `:+name` per INCLUDING, in source order,
// dropping every EXCLUDING, with `INCLUDING ALL` expanding to a fixed nine.
// BodyOrder therefore has to be built inside the element loop so the marker
// lands at the element's position, rather than from the column list afterwards.
//
// LIKE is reserved, so the element cannot collide with a column definition.
func TestLikeTableElementParity(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t (LIKE s)",
		"CREATE TABLE t (LIKE sch.s)",
		// option encoding
		"CREATE TABLE t (LIKE s INCLUDING ALL)",
		"CREATE TABLE t (LIKE s EXCLUDING ALL)",
		"CREATE TABLE t (LIKE s INCLUDING DEFAULTS)",
		"CREATE TABLE t (LIKE s EXCLUDING DEFAULTS)",
		"CREATE TABLE t (LIKE s INCLUDING DEFAULTS EXCLUDING CONSTRAINTS)",
		"CREATE TABLE t (LIKE s INCLUDING IDENTITY INCLUDING STORAGE)",
		"CREATE TABLE t (LIKE s INCLUDING INDEXES INCLUDING STORAGE INCLUDING COMMENTS)",
		// position within the element list
		"CREATE TABLE t (a int, LIKE s)",
		"CREATE TABLE t (LIKE s, b int)",
		"CREATE TABLE t (a int, LIKE s, b int)",
		// guards: BodyOrder for ordinary tables must be unchanged
		"CREATE TABLE t (a int)",
		"CREATE TABLE t (a int, b text, PRIMARY KEY (a))",
		"SELECT a FROM t WHERE a LIKE 'x'",
	} {
		assertParity(t, q)
	}
}
