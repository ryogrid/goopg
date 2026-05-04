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
// configured for the in-memory ring, the view emits one row whose
// columns reflect the live counters.
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

	// Column order (M0042-0002: O_DIRECT columns removed):
	// send_buffer_capacity_bytes, send_buffer_bytes_resident,
	// send_buffer_hits, send_buffer_misses, wal_buffers_capacity_bytes,
	// wal_buffers_bytes_resident, wal_buffers_overflow_drain_bytes,
	// wal_buffers_flush_drain_bytes, format_version.
	if row[0] != "65536" {
		t.Errorf("send_buffer_capacity_bytes=%q, want 65536", row[0])
	}
	if row[1] != "0" {
		t.Errorf("send_buffer_bytes_resident=%q, want 0 before any append", row[1])
	}

	// Append a record so the ring fills, then re-render.
	if _, _, err := w.Append([]byte("ring-fill")); err != nil {
		t.Fatal(err)
	}
	rows = tbl.VirtualRows()
	row = rows[0]
	resident, err := strconv.ParseInt(row[1], 10, 64)
	if err != nil || resident == 0 {
		t.Errorf("send_buffer_bytes_resident=%q, want > 0 after Append (err=%v)", row[1], err)
	}
}

// TestStatWALIOFormatVersionColumn pins the M0014-0004 step-2
// finalisation: the trailing format_version column reports
// `legacy` for the default writer and `pgcompat` when
// PageHeaders=true.
func TestStatWALIOFormatVersionColumn(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		w, err := wal.NewWriter(wal.Config{
			WALDir:      filepath.Join(t.TempDir(), "pg_wal"),
			SegmentSize: 4096,
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
		row := tbl.VirtualRows()[0]
		got := row[len(row)-1]
		if got != "legacy" {
			t.Errorf("format_version=%q, want legacy", got)
		}
	})
	t.Run("pgcompat", func(t *testing.T) {
		w, err := wal.NewWriter(wal.Config{
			WALDir:      filepath.Join(t.TempDir(), "pg_wal"),
			SegmentSize: 4096,
			PageHeaders: true,
			TimelineID:  1,
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
		row := tbl.VirtualRows()[0]
		got := row[len(row)-1]
		if got != "pgcompat" {
			t.Errorf("format_version=%q, want pgcompat", got)
		}
	})
}
