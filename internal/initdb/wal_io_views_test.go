package initdb

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/wal"
)

// TestStatWALIOEmptyWithoutWriter: registering the view with a
// nil writer makes SELECT return zero rows (so a SELECT against
// a no-WAL test environment isn't a missing-table error). Pins
// the "view exists, just empty" contract every other pg_stat_*
// view in the codebase honours.
func TestStatWALIOEmptyWithoutWriter(t *testing.T) {
	cat := catalog.NewInMemory()
	if err := registerStatWALIOView(cat, nil); err != nil {
		t.Fatal(err)
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_wal_io"})
	if !ok {
		t.Fatal("view not registered")
	}
	if rows := tbl.VirtualRows(); len(rows) != 0 {
		t.Errorf("nil writer must yield 0 rows, got %d", len(rows))
	}
}

// TestStatWALIORendersWriterCounters: with a real wal.Writer
// configured for the in-memory ring AND a probed direct-I/O
// state, the view emits one row whose columns reflect the live
// counters. Doesn't require the probe to succeed (we accept
// either active or fallback) — just pins that the column shape
// is correct and the values move when the underlying state
// moves.
func TestStatWALIORendersWriterCounters(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := wal.NewWriter(wal.Config{
		WALDir:             walDir,
		SegmentSize:        4096,
		SenderMemoryBuffer: 1 << 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	cat := catalog.NewInMemory()
	if err := registerStatWALIOView(cat, w); err != nil {
		t.Fatal(err)
	}
	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_wal_io"})
	rows := tbl.VirtualRows()
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	row := rows[0]

	// Column order: direct_io_active, direct_io_fallback_reason,
	// direct_writes, tail_rmw_writes, send_buffer_capacity_bytes,
	// send_buffer_bytes_resident, send_buffer_hits,
	// send_buffer_misses.
	if row[0] != "f" {
		t.Errorf("direct_io_active=%q, want f (DirectIO not requested)", row[0])
	}
	if row[1] != "" {
		t.Errorf("direct_io_fallback_reason=%q, want empty", row[1])
	}
	if row[2] != "0" {
		t.Errorf("direct_writes=%q, want 0", row[2])
	}
	if row[4] != "65536" {
		t.Errorf("send_buffer_capacity_bytes=%q, want 65536", row[4])
	}
	if row[5] != "0" {
		t.Errorf("send_buffer_bytes_resident=%q, want 0 before any append", row[5])
	}

	// Append a record so the ring fills, then re-render.
	if _, _, err := w.Append([]byte("ring-fill")); err != nil {
		t.Fatal(err)
	}
	rows = tbl.VirtualRows()
	row = rows[0]
	resident, err := strconv.ParseInt(row[5], 10, 64)
	if err != nil || resident == 0 {
		t.Errorf("send_buffer_bytes_resident=%q, want > 0 after Append (err=%v)", row[5], err)
	}
}

// TestStatWALIOActiveWhenProbeOk: when DirectIO is requested
// and the probe succeeds (the dev-host /tmp is ext4), the
// active column is "t" and the fallback reason is empty.
// Skips on probe fallback so the test passes under
// tmpfs / overlayfs / non-Linux.
func TestStatWALIOActiveWhenProbeOk(t *testing.T) {
	walDir := filepath.Join(t.TempDir(), "pg_wal")
	w, err := wal.NewWriter(wal.Config{
		WALDir:      walDir,
		SegmentSize: 4096,
		DirectIO:    true,
		Preallocate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if reason := w.DirectIOFallbackReason(); reason != "" {
		t.Skipf("O_DIRECT unsupported here: %s", reason)
	}
	cat := catalog.NewInMemory()
	if err := registerStatWALIOView(cat, w); err != nil {
		t.Fatal(err)
	}

	// Drive a write so direct_writes / tail_rmw_writes bump.
	if _, _, err := w.Append([]byte("xyz")); err != nil {
		t.Fatal(err)
	}

	tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_wal_io"})
	row := tbl.VirtualRows()[0]
	if row[0] != "t" {
		t.Errorf("direct_io_active=%q, want t (probe succeeded)", row[0])
	}
	if row[1] != "" {
		t.Errorf("direct_io_fallback_reason=%q, want empty", row[1])
	}
	directWrites, _ := strconv.ParseInt(row[2], 10, 64)
	if directWrites == 0 {
		t.Errorf("direct_writes=0 after Append, want > 0")
	}
	tailRMW, _ := strconv.ParseInt(row[3], 10, 64)
	if tailRMW == 0 {
		t.Errorf("tail_rmw_writes=0 after small Append, want > 0 (3-byte payload definitely not block-aligned)")
	}
}
