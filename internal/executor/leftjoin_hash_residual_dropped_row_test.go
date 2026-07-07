package executor

import "testing"

// TestLeftJoinHashResidualFilterPreservesUnmatchedRow is the executor-level
// regression for the M0119-0004 pg_dump DU-002 "invalid column numbering"
// gap: pg_dump's getTableAttrs joins pg_attribute LEFT JOIN pg_constraint ON
// (a.attrelid = co.conrelid AND co.contype = 'n' AND co.conkey =
// array[a.attnum]). When two attributes of the same table share the
// attrelid=conrelid hash key but only one of them satisfies the residual
// `conkey = array[attnum]` conjunct, nextLazy's hash-match loop iterated every
// candidate in lazyMatches, found none passing the residual Predicate, and
// fell straight through to fetching the next probe row — silently dropping
// the outer (pg_attribute) row instead of null-padding it. The
// len(matches)==0 branch only guards the hash-level miss; it never covers
// "matched the hash key, but every match failed the residual filter".
// Reproduced directly against pg_attribute/pg_constraint in
// internal/testport (TestPort_PgDumpConnectionSetup); this test pins the
// same shape with plain user tables so it stays fast and catalog-free.
func TestLeftJoinHashResidualFilterPreservesUnmatchedRow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range []string{
		"CREATE TABLE outer2_t (id int, k int)",
		"CREATE TABLE inner2_t (rid int, k2 int)",
		// Both outer rows share id=100 (the hash-join equi-key); only k=2
		// also matches the single inner row's k2, mirroring two columns of
		// the same table (shared attrelid) where only one has a matching
		// pg_constraint row for its attnum.
		"INSERT INTO outer2_t VALUES (100, 1), (100, 2)",
		"INSERT INTO inner2_t VALUES (100, 2)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", ddl, err)
		}
	}

	const q = `SELECT outer2_t.k, inner2_t.rid
	             FROM outer2_t
	             LEFT JOIN inner2_t
	               ON outer2_t.id = inner2_t.rid
	              AND outer2_t.k = inner2_t.k2
	            ORDER BY outer2_t.k`

	rows := runQuery(t, ctx, q)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2 (k=1 row must survive null-padded): %v", len(rows), rows)
	}

	// k=1: hash key (id=100) matches inner2_t's row, but the residual
	// k=inner2_t.k2 (1 vs 2) fails — must still be emitted with rid=NULL.
	if rows[0][0].Int != 1 {
		t.Fatalf("row 0: k = %v, want 1", rows[0][0])
	}
	if rows[0][1].Kind != KindNull {
		t.Fatalf("row 0 (k=1): rid = %v, want NULL (hash key matched but residual filter should have produced an unmatched LEFT JOIN row, not dropped it)", rows[0][1])
	}

	// k=2: both the hash key and the residual condition match.
	if rows[1][0].Int != 2 {
		t.Fatalf("row 1: k = %v, want 2", rows[1][0])
	}
	if rows[1][1].Kind == KindNull || rows[1][1].Int != 100 {
		t.Fatalf("row 1 (k=2): rid = %v, want 100", rows[1][1])
	}
}
