package executor

import "testing"

// TestPgStatIORowCount asserts the pg_stat_io view emits upstream's exact
// 79-row valid-combination shape (verified against a real PostgreSQL 18.3
// instance: 14 tracked backend types × their valid (object, context)
// combinations), not the empty result the earlier static VirtualRows stub
// returned. M0122-0003.
func TestPgStatIORowCount(t *testing.T) {
	rows := fetchIOStatRows(nil)
	if len(rows) != 79 {
		t.Fatalf("fetchIOStatRows(nil) = %d rows, want 79", len(rows))
	}
}

// TestPgStatIOExcludesInvalidCombination confirms a combination upstream
// never tracks for any BackendType — WAL under a strategy IOContext, which
// pgstat_tracks_io_object rejects for every object except IOCONTEXT_NORMAL/
// IOCONTEXT_INIT — never appears as a row.
func TestPgStatIOExcludesInvalidCombination(t *testing.T) {
	rows := fetchIOStatRows(nil)
	for _, r := range rows {
		if r[1] == "wal" && r[2] == "vacuum" {
			t.Fatalf("unexpected wal/vacuum row: %v", r)
		}
	}
}

// TestPgStatIOClientBackendRelationNormalShape pins the one row goopg
// actually instruments: client backend / relation / normal must have
// reads/read_bytes/hits populated as real counts (not NULL), while an op
// upstream never tracks for this combination (REUSE, which is only valid
// under a BufferAccessStrategy context) stays NULL.
func TestPgStatIOClientBackendRelationNormalShape(t *testing.T) {
	rows := fetchIOStatRows(nil)
	var found []string
	for _, r := range rows {
		if r[0] == "client backend" && r[1] == "relation" && r[2] == "normal" {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("no client backend/relation/normal row found")
	}
	// reads(3), read_bytes(4), hits(14) are tracked → real counts, not NULL.
	for _, idx := range []int{3, 4, 14} {
		if found[idx] == "\x00\x00NULL\x00\x00" {
			t.Errorf("column %d unexpectedly NULL: %v", idx, found)
		}
	}
	// reuses(16) is never tracked outside a strategy context → NULL.
	if found[16] != "\x00\x00NULL\x00\x00" {
		t.Errorf("reuses column = %q, want NULL sentinel", found[16])
	}
}

// TestPgStatIOWalSummarizerRows verifies the shape upstream reports for
// walsummarizer — 2 rows (wal/init, wal/normal), all-zero counts — is
// present unconditionally (goopg has no WAL summarizer process, matching a
// real cluster with summarize_wal left at its default off). See
// TestPort_PgWalsummary002Blocks (internal/testport) for the end-to-end SQL
// assertion of the same fact.
func TestPgStatIOWalSummarizerRows(t *testing.T) {
	rows := fetchIOStatRows(nil)
	var got []string
	for _, r := range rows {
		if r[0] == "walsummarizer" {
			got = append(got, r[1]+"/"+r[2])
		}
	}
	if len(got) != 2 {
		t.Fatalf("walsummarizer rows = %v, want 2 entries (wal/init, wal/normal)", got)
	}
}

// TestPgStatIOLiveCounters wires real storage.Pool shared-buffer counters
// into the client backend/relation/normal row via a live query context,
// confirming the SELECT surface (valuesOp.Open's "pg_stat_io" case in
// operators.go) actually swaps in fetchIOStatRows instead of the static
// catalog fallback.
func TestPgStatIOLiveCounters(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE iostat_probe (data int)",
		"INSERT INTO iostat_probe VALUES (1)",
		"INSERT INTO iostat_probe VALUES (2)",
	)
	commitTx(t, ctx)
	beginTx(t, ctx)
	// Touch the table's pages through the buffer pool so the pool-wide
	// hit/read counters (the one signal goopg instruments) are non-zero.
	_ = runQueryRows(t, ctx, "SELECT data FROM iostat_probe")

	rows := runQueryRows(t, ctx,
		"SELECT reads, hits FROM pg_stat_io WHERE backend_type = 'client backend' AND object = 'relation' AND context = 'normal'")
	if len(rows) != 1 {
		t.Fatalf("client backend/relation/normal row count = %d, want 1", len(rows))
	}
	reads, hits := rows[0][0], rows[0][1]
	if reads.IsNull() || hits.IsNull() {
		t.Fatalf("reads/hits unexpectedly NULL: reads=%v hits=%v", reads, hits)
	}
}
