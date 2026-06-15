package executor

// operators_pg_get_sequence_data_test.go — pg_dump getSequences query
// parity. The empty pg_sequence virtual view + the pg_get_sequence_data
// FROM-clause SRF must let pg_dump's getSequences query parse, plan, and
// execute, returning 0 rows on a cluster with no dumpable sequences.
// M0110-0001 (DU-002 slice 32).

import "testing"

// TestPgGetSequenceDataGetSequencesQuery pins pg_dump's getSequences shape:
//
//	SELECT seqrelid, format_type(seqtypid, NULL), seqstart, seqincrement,
//	       seqmax, seqmin, seqcache, seqcycle, last_value, is_called
//	FROM pg_catalog.pg_sequence, pg_get_sequence_data(seqrelid)
//	ORDER BY seqrelid
//
// goopg's pg_sequence view is empty, so the implicit-LATERAL SRF is never
// executed and the query yields 0 rows (no error).
func TestPgGetSequenceDataGetSequencesQuery(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	const sql = `SELECT seqrelid, format_type(seqtypid, NULL), seqstart, seqincrement, ` +
		`seqmax, seqmin, seqcache, seqcycle, last_value, is_called ` +
		`FROM pg_catalog.pg_sequence, pg_get_sequence_data(seqrelid) ORDER BY seqrelid`

	rows := runQueryRows(t, ctx, sql)
	if len(rows) != 0 {
		t.Fatalf("getSequences query row count = %d, want 0 (empty pg_sequence)", len(rows))
	}
}

// TestPgGetSequenceDataSchema pins the SRF output schema: a direct
// FROM-clause call resolves to two columns (last_value int8, is_called bool)
// and returns 0 rows in goopg.
func TestPgGetSequenceDataSchema(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx, "SELECT last_value, is_called FROM pg_get_sequence_data(0)")
	if len(rows) != 0 {
		t.Fatalf("pg_get_sequence_data(0) row count = %d, want 0", len(rows))
	}
}
