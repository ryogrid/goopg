package executor

// operators_pg_get_sequence_data_test.go — pg_dump getSequences query
// parity. The pg_sequence virtual view + the pg_get_sequence_data FROM-clause
// SRF must let pg_dump's getSequences query parse, plan, and execute. With no
// dumpable sequences the result is 0 rows; with a sequence present, one row
// carrying its parameters and runtime last_value/is_called.
// M0110-0001 (DU-002 slices 32 + 115).

import "testing"

// TestPgGetSequenceDataGetSequencesQuery pins pg_dump's getSequences shape:
//
//	SELECT seqrelid, format_type(seqtypid, NULL), seqstart, seqincrement,
//	       seqmax, seqmin, seqcache, seqcycle, last_value, is_called
//	FROM pg_catalog.pg_sequence, pg_get_sequence_data(seqrelid)
//	ORDER BY seqrelid
//
// With no sequences registered, pg_sequence is empty, so the implicit-LATERAL
// SRF is never executed and the query yields 0 rows (no error).
func TestPgGetSequenceDataGetSequencesQuery(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	const sql = `SELECT seqrelid, format_type(seqtypid, NULL), seqstart, seqincrement, ` +
		`seqmax, seqmin, seqcache, seqcycle, last_value, is_called ` +
		`FROM pg_catalog.pg_sequence, pg_get_sequence_data(seqrelid) ORDER BY seqrelid`

	rows := runQueryRows(t, ctx, sql)
	if len(rows) != 0 {
		t.Fatalf("getSequences query row count = %d, want 0 (no sequences)", len(rows))
	}
}

// TestPgGetSequenceDataSchema pins the SRF output schema: a direct FROM-clause
// call resolves to two columns (last_value int8, is_called bool); a regclass
// that names no sequence (OID 0) returns 0 rows.
func TestPgGetSequenceDataSchema(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	rows := runQueryRows(t, ctx, "SELECT last_value, is_called FROM pg_get_sequence_data(0)")
	if len(rows) != 0 {
		t.Fatalf("pg_get_sequence_data(0) row count = %d, want 0", len(rows))
	}
}

// TestPgGetSequenceDataPopulated exercises the slice-115 downstream links of
// sequence-dump support: once a sequence exists, pg_sequence (singular) carries
// its parameter row (keyed by the sequence's pg_class OID = seqrelid) and
// pg_get_sequence_data(seqrelid) supplies the runtime last_value/is_called.
// This is exactly the pair pg_dump's getSequences comma-joins. (Surfacing the
// sequence in pg_class with relkind='S' so pg_dump *discovers* it is the
// follow-up slice 116.)
func TestPgGetSequenceDataPopulated(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx,
		"CREATE SEQUENCE public.s START WITH 5 INCREMENT BY 2 MINVALUE 1 MAXVALUE 100"); err != nil {
		t.Fatalf("CREATE SEQUENCE: %v", err)
	}

	// pg_sequence (singular) now carries one parameter row for the sequence.
	prows := runQueryRows(t, ctx,
		"SELECT seqtypid, seqstart, seqincrement, seqmax, seqmin, seqcache, seqcycle "+
			"FROM pg_catalog.pg_sequence")
	if len(prows) != 1 {
		t.Fatalf("pg_sequence row count = %d, want 1", len(prows))
	}
	got := prows[0]
	if got[0].Int != 20 { // seqtypid: bigint (default sequence type)
		t.Errorf("seqtypid = %d, want 20 (bigint)", got[0].Int)
	}
	if got[1].Int != 5 || got[2].Int != 2 || got[3].Int != 100 || got[4].Int != 1 || got[5].Int != 1 {
		t.Errorf("pg_sequence params = start=%d inc=%d max=%d min=%d cache=%d, want 5/2/100/1/1",
			got[1].Int, got[2].Int, got[3].Int, got[4].Int, got[5].Int)
	}
	if got[6].BoolValue() {
		t.Errorf("seqcycle = true, want false")
	}

	// The full getSequences join shape now yields the sequence's row, with the
	// runtime last_value/is_called from pg_get_sequence_data. Not yet called →
	// last_value = start (5), is_called = false.
	const getSeq = `SELECT format_type(seqtypid, NULL), seqstart, last_value, is_called ` +
		`FROM pg_catalog.pg_sequence, pg_get_sequence_data(seqrelid) ORDER BY seqrelid`
	jrows := runQueryRows(t, ctx, getSeq)
	if len(jrows) != 1 {
		t.Fatalf("getSequences join row count = %d, want 1", len(jrows))
	}
	if jrows[0][0].StringValue() != "bigint" {
		t.Errorf("format_type(seqtypid) = %q, want \"bigint\"", jrows[0][0].StringValue())
	}
	if jrows[0][1].Int != 5 {
		t.Errorf("seqstart = %d, want 5", jrows[0][1].Int)
	}
	if jrows[0][2].Int != 5 {
		t.Errorf("last_value = %d, want 5 (start, not yet called)", jrows[0][2].Int)
	}
	if jrows[0][3].BoolValue() {
		t.Errorf("is_called = true, want false (not yet called)")
	}

	// A direct regclass call resolves the sequence by name.
	drows := runQueryRows(t, ctx,
		"SELECT last_value, is_called FROM pg_get_sequence_data('s'::regclass)")
	if len(drows) != 1 {
		t.Fatalf("direct pg_get_sequence_data row count = %d, want 1", len(drows))
	}
	if drows[0][0].Int != 5 || drows[0][1].BoolValue() {
		t.Errorf("direct call = (%d, %v), want (5, false)", drows[0][0].Int, drows[0][1].BoolValue())
	}

	// Advancing the sequence flips is_called; last_value pins to the first
	// returned value (start = 5).
	_ = runQueryRows(t, ctx, "SELECT nextval('s')")
	arows := runQueryRows(t, ctx,
		"SELECT last_value, is_called FROM pg_get_sequence_data('s'::regclass)")
	if len(arows) != 1 {
		t.Fatalf("post-nextval row count = %d, want 1", len(arows))
	}
	if arows[0][0].Int != 5 || !arows[0][1].BoolValue() {
		t.Errorf("post-nextval = (%d, %v), want (5, true)", arows[0][0].Int, arows[0][1].BoolValue())
	}
}
