package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/storage/aio"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestStatAIOViewEmptyWithoutEngine: with no engine attached
// the view registers cleanly and emits zero rows. SELECT *
// against pg_stat_aio still works on synchronous deployments.
func TestStatAIOViewEmptyWithoutEngine(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerStatAIOView(cat, nil); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_aio"})
	if !ok {
		t.Fatal("pg_stat_aio not registered")
	}
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("rows=%d want 0 (no engine)", len(rows))
	}
}

// TestStatAIOViewReflectsEngineCounters: with an engine
// attached, the view returns two rows (one per direction)
// reflecting the engine's per-direction counters. Submitting
// one read bumps the read row; the write row's counters stay
// at zero. Column ordering: method, operation, submitted,
// completed, errored, in_flight.
func TestStatAIOViewReflectsEngineCounters(t *testing.T) {
	eng, err := aio.NewEngine(aio.EngineConfig{Method: aio.MethodSync})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	cat := catalog.NewInMemory()
	if err := registerStatAIOView(cat, eng); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_aio"})

	// Pre-Submit snapshot: zero counters across both rows.
	rows := tbl.VirtualRows()
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2 (read + write)", len(rows))
	}
	for i, r := range rows {
		if r[0] != "sync" {
			t.Errorf("row %d method=%q want sync", i, r[0])
		}
	}
	if rows[0][1] != "read" || rows[1][1] != "write" {
		t.Errorf("operations=%q,%q want read,write", rows[0][1], rows[1][1])
	}
	for _, r := range rows {
		if r[2] != "0" || r[3] != "0" {
			t.Errorf("pre-submit counters %s submitted=%q completed=%q want 0/0",
				r[1], r[2], r[3])
		}
	}

	// Submit one read against an in-memory file.
	f := &memViewFile{buf: make([]byte, 16)}
	h := eng.Submit(aio.Op{
		File: f, Buffer: make([]byte, 4),
		Offset: 0, Direction: aio.DirRead,
	})
	if r := h.Wait(); r.Err != nil {
		t.Fatal(r.Err)
	}
	rows = tbl.VirtualRows()
	// Read row should reflect the submit; write row stays zero.
	if rows[0][2] != "1" || rows[0][3] != "1" {
		t.Errorf("post-submit read submitted=%q completed=%q want 1/1",
			rows[0][2], rows[0][3])
	}
	if rows[1][2] != "0" || rows[1][3] != "0" {
		t.Errorf("post-submit write counters %q/%q should still be 0",
			rows[1][2], rows[1][3])
	}
	if rows[0][5] != "0" {
		t.Errorf("post-submit in_flight=%q want 0", rows[0][5])
	}
}

// memViewFile satisfies aio.File for the view test. Mirrors
// the in-memory File used by internal/aio's own tests but kept
// here because Go test packages can't share helpers.
type memViewFile struct{ buf []byte }

func (f *memViewFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(f.buf)) {
		return 0, nil
	}
	return copy(p, f.buf[off:]), nil
}
func (f *memViewFile) WriteAt(p []byte, off int64) (int, error) {
	return copy(f.buf[off:], p), nil
}

// TestPgAiosViewEmptyWithoutEngine: pg_aios with a nil engine
// emits zero rows. SELECT * still works on synchronous
// deployments.
func TestPgAiosViewEmptyWithoutEngine(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgAiosView(cat, nil); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_aios"})
	if !ok {
		t.Fatal("pg_aios not registered")
	}
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("rows=%d want 0", len(rows))
	}
}

// TestPgAiosViewReflectsInFlightHandles: with two outstanding
// Ops, the view returns two rows in submit-order with the
// right operation / offset / length columns. After the Ops
// land, the view returns zero rows.
func TestPgAiosViewReflectsInFlightHandles(t *testing.T) {
	eng, err := aio.NewEngine(aio.EngineConfig{
		Method: aio.MethodWorker, Workers: 2, MaxConcurrency: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	cat := catalog.NewInMemory()
	if err := registerPgAiosView(cat, eng); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_aios"})

	// Use a gate-style file so we can sample the view
	// mid-flight without racing the workers.
	g := &gatedViewFile{buf: make([]byte, 64), gate: make(chan struct{})}
	h1 := eng.Submit(aio.Op{File: g, Buffer: make([]byte, 8), Offset: 0, Direction: aio.DirRead})
	h2 := eng.Submit(aio.Op{File: g, Buffer: make([]byte, 16), Offset: 32, Direction: aio.DirRead})

	// Spin briefly until both registrations are visible.
	deadline := 1000
	for len(eng.InFlight()) < 2 && deadline > 0 {
		deadline--
	}
	rows := tbl.VirtualRows()
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	// Column ordering: io_id, operation, off, length,
	// submitted_at, elapsed_us. Both rows are reads.
	if rows[0][1] != "read" || rows[1][1] != "read" {
		t.Errorf("operation=%q,%q want read,read", rows[0][1], rows[1][1])
	}
	if rows[0][2] != "0" || rows[1][2] != "32" {
		t.Errorf("off=%q,%q want 0,32", rows[0][2], rows[1][2])
	}
	if rows[0][3] != "8" || rows[1][3] != "16" {
		t.Errorf("length=%q,%q want 8,16", rows[0][3], rows[1][3])
	}

	close(g.gate)
	h1.Wait()
	h2.Wait()
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("post-Wait rows=%d want 0", len(rows))
	}
}

// gatedViewFile blocks every ReadAt until gate is closed —
// lets the test observe the in-flight view rows while ops are
// stalled on the gate.
type gatedViewFile struct {
	buf  []byte
	gate chan struct{}
}

func (f *gatedViewFile) ReadAt(p []byte, off int64) (int, error) {
	<-f.gate
	if off >= int64(len(f.buf)) {
		return 0, nil
	}
	return copy(p, f.buf[off:]), nil
}
func (f *gatedViewFile) WriteAt(p []byte, off int64) (int, error) {
	return copy(f.buf[off:], p), nil
}

// TestPgStatAIOTargetsViewEmptyWithoutEngine: nil engine
// produces 0 rows so SELECT * works on synchronous
// deployments.
func TestPgStatAIOTargetsViewEmptyWithoutEngine(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerPgStatAIOTargetsView(cat, nil); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_aio_targets"})
	if !ok {
		t.Fatal("pg_stat_aio_targets not registered")
	}
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("rows=%d want 0", len(rows))
	}
}

// TestPgStatAIOTargetsViewRendersRows: with two distinct
// targets seen by the engine, the view returns two rows in
// target-name order with correct counter columns.
func TestPgStatAIOTargetsViewRendersRows(t *testing.T) {
	eng, err := aio.NewEngine(aio.EngineConfig{Method: aio.MethodSync})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	cat := catalog.NewInMemory()
	if err := registerPgStatAIOTargetsView(cat, eng); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_aio_targets"})

	f := &memViewFile{buf: make([]byte, 32)}
	eng.Submit(aio.Op{File: f, Buffer: make([]byte, 4), Direction: aio.DirRead, Target: "alpha"}).Wait()
	eng.Submit(aio.Op{File: f, Buffer: []byte("hi"), Direction: aio.DirWrite, Target: "beta"}).Wait()

	rows := tbl.VirtualRows()
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2", len(rows))
	}
	// Column ordering: target, submitted, completed,
	// errored, bytes, avg_latency_us, max_latency_us.
	if rows[0][0] != "alpha" || rows[1][0] != "beta" {
		t.Errorf("targets=%q,%q want alpha,beta", rows[0][0], rows[1][0])
	}
	if rows[0][1] != "1" || rows[0][2] != "1" {
		t.Errorf("alpha submitted/completed=%q/%q want 1/1", rows[0][1], rows[0][2])
	}
	if rows[0][4] != "4" {
		t.Errorf("alpha bytes=%q want 4", rows[0][4])
	}
	if rows[1][4] != "2" {
		t.Errorf("beta bytes=%q want 2", rows[1][4])
	}
}
