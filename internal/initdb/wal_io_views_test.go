package initdb

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/access/transam/xlog"
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
	w, err := xlog.NewWriter(xlog.Config{
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
	// wal_buffers_flush_drain_bytes, wal_segments_preallocated_total,
	// wal_init_zero_bytes_total, format_version.
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

// TestStatWALIOPreallocationCounters pins the M0007 preallocation
// counters follow-up: with Config.Preallocate on, opening the first
// segment bumps wal_segments_preallocated_total past 0 and
// wal_init_zero_bytes_total tracks it at exactly SegmentSize per
// segment; with Preallocate off (the default), both stay "0".
// AI-20261101-010139-001: the old "want exactly 1" pinned a race —
// eagerPreallocSegment(segNo+1) preallocates the NEXT segment in a
// background goroutine, legitimately adding a second counted segment
// whenever the goroutine wins the scheduling race (it did under the
// nightly lane load: 2/16384). The eager path is explicitly
// best-effort timing, so the deterministic invariant is: on ⇒ count
// ≥ 1 (the synchronous openSegment fill always counts) with
// bytes == count × SegmentSize (both counters move together per
// zero-fill, sync and eager); off ⇒ 0/0.
func TestStatWALIOPreallocationCounters(t *testing.T) {
	t.Run("preallocate-on", func(t *testing.T) {
		const segSize = 8192
		w, err := xlog.NewWriter(xlog.Config{
			WALDir:      filepath.Join(t.TempDir(), "pg_wal"),
			SegmentSize: segSize,
			Preallocate: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		if _, _, err := w.Append([]byte("first-record")); err != nil {
			t.Fatal(err)
		}
		cat := catalog.NewInMemory()
		if err := registerStatWALIOView(cat, w); err != nil {
			t.Fatal(err)
		}
		tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_wal_io"})
		row := tbl.VirtualRows()[0]
		segs, err := strconv.ParseInt(row[8], 10, 64)
		if err != nil || segs < 1 {
			t.Errorf("wal_segments_preallocated_total=%q, want >= 1 (err=%v)", row[8], err)
		}
		bytes, err := strconv.ParseInt(row[9], 10, 64)
		if err != nil {
			t.Errorf("wal_init_zero_bytes_total=%q, want integer (err=%v)", row[9], err)
		} else if segs >= 1 && bytes != segs*segSize {
			t.Errorf("wal_init_zero_bytes_total=%q, want %d (%d segments x SegmentSize)", bytes, segs*segSize, segs)
		}
	})
	t.Run("preallocate-off", func(t *testing.T) {
		w, err := xlog.NewWriter(xlog.Config{
			WALDir:      filepath.Join(t.TempDir(), "pg_wal"),
			SegmentSize: 8192,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		if _, _, err := w.Append([]byte("first-record")); err != nil {
			t.Fatal(err)
		}
		cat := catalog.NewInMemory()
		if err := registerStatWALIOView(cat, w); err != nil {
			t.Fatal(err)
		}
		tbl, _ := cat.LookupTable(parser.ObjectName{Schema: "pg_catalog", Name: "pg_stat_wal_io"})
		row := tbl.VirtualRows()[0]
		if row[8] != "0" {
			t.Errorf("wal_segments_preallocated_total=%q, want 0", row[8])
		}
		if row[9] != "0" {
			t.Errorf("wal_init_zero_bytes_total=%q, want 0", row[9])
		}
	})
}

// TestStatWALIOFormatVersionColumn pins the M0014-0004 step-2
// finalisation: the trailing format_version column reports the
// writer's active format. A9: the legacy frame is retired, so the
// default writer reports `pgcompat` too.
func TestStatWALIOFormatVersionColumn(t *testing.T) {
	t.Run("default_config", func(t *testing.T) {
		w, err := xlog.NewWriter(xlog.Config{
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
		if got != "pgcompat" {
			t.Errorf("format_version=%q, want pgcompat", got)
		}
	})
	t.Run("pgcompat", func(t *testing.T) {
		w, err := xlog.NewWriter(xlog.Config{
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
