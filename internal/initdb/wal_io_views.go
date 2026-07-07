// WAL walsender ring + buffer observability view: `pg_stat_wal_io`.
// One row when a WAL writer is attached, zero rows otherwise.
// Surfaces M0010-0002's in-memory ring metrics and M0013-0003's WAL
// buffer counters in one place so an operator triaging WAL throughput
// can SELECT one view rather than chasing log lines.
//
// O_DIRECT columns (direct_io_active, direct_io_fallback_reason,
// direct_writes, tail_rmw_writes) were removed as part of M0042-0002
// (Buffered-I/O migration). The ring and WAL-buffer columns are retained.
//
// See docs/design/0010-0003-wal-direct-io-observability-and-operations.md.

package initdb

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// registerStatWALIOView installs `pg_catalog.pg_stat_wal_io`
// backed by the process-wide *wal.Writer. Emits exactly one row
// when the writer is non-nil; zero rows otherwise.
//
// Columns:
//   - send_buffer_capacity_bytes: the wal_sender_memory_buffer GUC value.
//   - send_buffer_bytes_resident: bytes currently in the ring.
//   - send_buffer_hits / send_buffer_misses: RecordIterator ring outcomes.
//   - wal_buffers_capacity_bytes (M0013-0003): the wal_buffers GUC value.
//   - wal_buffers_bytes_resident (M0013-0003): live byte count in buffer.
//   - wal_buffers_overflow_drain_bytes (M0013-0003): lifetime bytes drained on overflow.
//   - wal_buffers_flush_drain_bytes (M0013-0003): lifetime bytes drained by FlushUpTo.
//   - wal_segments_preallocated_total (M0007 follow-up): lifetime count of new WAL
//     segments zero-filled by preallocateSegment.
//   - wal_init_zero_bytes_total (M0007 follow-up): lifetime bytes written zero-filling
//     new WAL segments.
//   - format_version (M0014-0004): active on-disk WAL format (`legacy` / `pgcompat`).
func registerStatWALIOView(cat *catalog.InMemory, w *wal.Writer) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_stat_wal_io",
		Columns: []catalog.Column{
			{Name: "send_buffer_capacity_bytes", Type: catalog.Type{Name: "text"}},
			{Name: "send_buffer_bytes_resident", Type: catalog.Type{Name: "text"}},
			{Name: "send_buffer_hits", Type: catalog.Type{Name: "text"}},
			{Name: "send_buffer_misses", Type: catalog.Type{Name: "text"}},
			{Name: "wal_buffers_capacity_bytes", Type: catalog.Type{Name: "text"}},
			{Name: "wal_buffers_bytes_resident", Type: catalog.Type{Name: "text"}},
			{Name: "wal_buffers_overflow_drain_bytes", Type: catalog.Type{Name: "text"}},
			{Name: "wal_buffers_flush_drain_bytes", Type: catalog.Type{Name: "text"}},
			{Name: "wal_segments_preallocated_total", Type: catalog.Type{Name: "text"}},
			{Name: "wal_init_zero_bytes_total", Type: catalog.Type{Name: "text"}},
			{Name: "format_version", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		if w == nil {
			return nil
		}
		ring := w.MemRing()
		var capBytes, resident int64
		var hits, misses uint64
		if ring != nil {
			capBytes = ring.Cap()
			resident = ring.BytesResident()
			hits = ring.Hits()
			misses = ring.Misses()
		}
		return [][]string{{
			fmt.Sprintf("%d", capBytes),
			fmt.Sprintf("%d", resident),
			fmt.Sprintf("%d", hits),
			fmt.Sprintf("%d", misses),
			fmt.Sprintf("%d", w.WALBuffersCapacity()),
			fmt.Sprintf("%d", w.WALBuffersBytesResident()),
			fmt.Sprintf("%d", w.WALBuffersOverflowDrainBytes()),
			fmt.Sprintf("%d", w.WALBuffersFlushDrainBytes()),
			fmt.Sprintf("%d", w.SegmentsPreallocated()),
			fmt.Sprintf("%d", w.PreallocatedBytes()),
			w.Format().String(),
		}}
	}
	return cat.RegisterVirtualTable(tbl)
}
