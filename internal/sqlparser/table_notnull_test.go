package sqlparser

import "testing"

// TestTableLevelNotNull pins PG 18's table-level NOT NULL constraint element
// (`[CONSTRAINT name] NOT NULL colname [NO INHERIT]`), which is a different
// element from the same-spelled column constraint: it names a column declared
// elsewhere in the list, lands in the parallel TableNotNull{Names,Cols,
// NoInherit} slices, and contributes nothing to BodyOrder. Its absence failed
// nine testport cases in setup (the TestPort_NotNull* family and
// TestPort_CreateTableInheritsNoInheritCheckNotPropagated).
func TestTableLevelNotNull(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE ni_p (a int, b int, NOT NULL a)",
		"CREATE TABLE ap_c (a int, CONSTRAINT ap_nn NOT NULL a, b int)",
		"CREATE TABLE x (a int, NOT NULL a NO INHERIT)",
		"CREATE TABLE x (a int, CONSTRAINT c NOT NULL a NO INHERIT)",
		"CREATE TABLE y (a int, b int, NOT NULL a, NOT NULL b NO INHERIT)",
		// The column-level constraint of the same spelling must still land on
		// ColumnDef.NotNull, not in the table-level slices.
		"CREATE TABLE z (a int NOT NULL)",
		"CREATE TABLE z (a int NOT NULL, b int, NOT NULL b)",
	} {
		assertParity(t, q)
	}
}

// TestEmptyTargetList pins `SELECT FROM t` and `SELECT`, both of which are
// legal upstream (opt_target_list has an empty alternative) and yield a
// zero-column result — errors.sql's `select;` expects "(1 row)", not an
// error. Only target_list had been ported, so a zero-column join raised
// 42601 (TestPort_ZeroColumnJoinDoesNotCrashBackend/empty_target_list).
func TestEmptyTargetList(t *testing.T) {
	for _, q := range []string{
		"SELECT FROM zc_a a, zc_b b",
		"SELECT FROM t",
		"SELECT FROM t WHERE a = 1",
		"SELECT",
		"SELECT DISTINCT FROM t",
		// The empty alternative must not disturb DISTINCT ON, which resolves
		// the same lookahead by shifting.
		"SELECT DISTINCT ON (a) a, b FROM t",
		"SELECT DISTINCT ON (a, b) a FROM t ORDER BY a",
	} {
		assertParity(t, q)
	}
}

// TestReturningNeedsTargets guards the other side of that change: RETURNING
// takes a MANDATORY target_list upstream (gram.y :12377), so sharing
// opt_target_list with it would have made the yacc parser accept a bare
// RETURNING that legacy rejects.
func TestReturningNeedsTargets(t *testing.T) {
	assertBothReject(t, "INSERT INTO t VALUES (1) RETURNING")
}
