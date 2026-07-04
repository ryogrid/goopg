package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

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

// TestPgStatIOReadTimeAndWritesRendered pins the M0122-0003 track_io_timing
// follow-up: fetchIOStatRows must render real read_time (col 5, from
// Pool.ReadTimeNanos, accumulated by the OnPinDone hook wired in
// internal/initdb/open.go) and writes/write_bytes (cols 6/7, from
// Pool.BufferCounters' pre-existing written tally) for the one row goopg
// instruments, instead of the previous hardcoded "0"/unused write count.
func TestPgStatIOReadTimeAndWritesRendered(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	// 2.5ms of accumulated read wait — mirrors what OnPinDone would add via
	// a real track_io_timing-gated WaitEventEnd duration.
	pool.AddReadTimeNanos(2_500_000)

	rel := storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork}
	seedPage := make(storage.Page, storage.BlockSize)
	if err := storage.InitPage(seedPage); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := mgr.Extend(rel, seedPage); err != nil {
			t.Fatal(err)
		}
	}
	// Force a backend-driven eviction so sharedWrittenCount advances: dirty
	// one page, then pin enough cold pages in the 4-slot pool to evict it.
	s, err := pool.Pin(storage.BufferTag{Rel: rel, Block: 0})
	if err != nil {
		t.Fatal(err)
	}
	pool.MarkDirty(s)
	pool.Unpin(s)
	coldRel := storage.RelFileNode{DBOid: 1, RelOid: 2, Fork: storage.MainFork}
	for i := 0; i < 8; i++ {
		if _, err := mgr.Extend(coldRel, seedPage); err != nil {
			t.Fatal(err)
		}
	}
	for blk := storage.BlockNumber(0); blk < 8; blk++ {
		cs, err := pool.Pin(storage.BufferTag{Rel: coldRel, Block: blk})
		if err != nil {
			continue
		}
		pool.Unpin(cs)
	}
	_, _, _, written := pool.BufferCounters()
	if written == 0 {
		t.Fatal("setup failed to force a backend-driven eviction (written count still 0)")
	}

	ctx := &Context{Pool: pool}
	rows := fetchIOStatRows(ctx)
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
	if found[5] != "2.500" {
		t.Errorf("read_time (col 5) = %q, want %q", found[5], "2.500")
	}
	if found[6] == "0" {
		t.Errorf("writes (col 6) = %q, want a nonzero count reflecting the forced eviction", found[6])
	}
}

// TestPgStatIOEvictionsAndExtendsRendered pins the M0122-0003 pg_stat_io
// follow-up: fetchIOStatRows must render real evictions (col 15, from
// Pool.EvictionCount) and extends/extend_bytes (cols 11/12, from
// Pool.ExtendCount) for the one row goopg instruments, instead of the
// previous hardcoded "0".
func TestPgStatIOEvictionsAndExtendsRendered(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 1, Fork: storage.MainFork}
	// 2 PinNew calls fill the pool's 2 slots (no eviction), a 3rd forces one.
	for i := 0; i < 3; i++ {
		s, _, err := pool.PinNew(rel)
		if err != nil {
			t.Fatal(err)
		}
		pool.Unpin(s)
	}
	if got := pool.ExtendCount(); got != 3 {
		t.Fatalf("setup: ExtendCount = %d, want 3", got)
	}
	if got := pool.EvictionCount(); got != 1 {
		t.Fatalf("setup: EvictionCount = %d, want 1", got)
	}

	ctx := &Context{Pool: pool}
	rows := fetchIOStatRows(ctx)
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
	if found[15] != "1" {
		t.Errorf("evictions (col 15) = %q, want %q", found[15], "1")
	}
	if found[11] != "3" {
		t.Errorf("extends (col 11) = %q, want %q", found[11], "3")
	}
	if found[12] != "24576" {
		t.Errorf("extend_bytes (col 12) = %q, want %q", found[12], "24576")
	}
}

// TestPgStatIOWriteTimeRendered is write_time's TestPgStatIOReadTimeAndWritesRendered
// analogue: fetchIOStatRows must render real write_time (col 8, from
// Pool.WriteTimeNanos, accumulated by the OnFlushDone hook wired in
// internal/initdb/open.go around evictVictim's dirty-victim flush) instead
// of the previous hardcoded "0".
func TestPgStatIOWriteTimeRendered(t *testing.T) {
	dir := t.TempDir()
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dir})
	pool, err := storage.NewPool(mgr, storage.PoolConfig{Slots: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pool.Close(); _ = mgr.Close() }()

	// 1.25ms of accumulated flush wait — mirrors what OnFlushDone would add
	// via a real track_io_timing-gated WaitEventEnd duration.
	pool.AddWriteTimeNanos(1_250_000)

	ctx := &Context{Pool: pool}
	rows := fetchIOStatRows(ctx)
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
	if found[8] != "1.250" {
		t.Errorf("write_time (col 8) = %q, want %q", found[8], "1.250")
	}
}
