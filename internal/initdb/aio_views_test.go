package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/aio"
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
// attached, the view returns one row whose columns reflect
// the engine's live counters. Submitting one read bumps
// submitted/completed; the method column matches the engine.
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

	// Pre-Submit snapshot: zero counters.
	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1", len(rows))
	}
	if got := rows[0][0]; got != "sync" {
		t.Errorf("method=%q want sync", got)
	}
	if rows[0][1] != "0" || rows[0][2] != "0" {
		t.Errorf("pre-submit counters submitted=%q completed=%q want 0/0", rows[0][1], rows[0][2])
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
	if rows[0][1] != "1" {
		t.Errorf("post-submit submitted=%q want 1", rows[0][1])
	}
	if rows[0][2] != "1" {
		t.Errorf("post-submit completed=%q want 1", rows[0][2])
	}
	if rows[0][4] != "0" {
		t.Errorf("post-submit in_flight=%q want 0", rows[0][4])
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
