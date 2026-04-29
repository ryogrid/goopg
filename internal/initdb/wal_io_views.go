// WAL direct-I/O + walsender ring observability view:
// `pg_stat_wal_io`. One row when a WAL writer is attached,
// zero rows otherwise. Surfaces M0010-0001's direct-write
// counters and M0010-0002's in-memory ring metrics in one
// place so an operator triaging WAL throughput / cache pressure
// can SELECT one view rather than chasing two log lines and a
// strace.
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
// when the writer is non-nil; zero rows otherwise (so a SELECT
// against the view in a no-WAL test environment doesn't surface
// a missing-table error).
//
// Columns:
//   - direct_io_active: "t" / "f" — true when wal_direct_io=on
//     AND the probe succeeded; effective mode at runtime.
//   - direct_io_fallback_reason: human-readable reason the probe
//     fell back, or "" when (a) DirectIO=off, or (b) DirectIO=on
//     and probe ok.
//   - direct_writes: lifetime count of writeAtDirectIO regions
//     that committed via O_DIRECT pwrite. Always 0 in buffered
//     mode.
//   - tail_rmw_writes: subset of direct_writes where the user
//     range wasn't already block-aligned and the writer paid
//     for a real read-modify-write. tail_rmw_writes /
//     direct_writes is the alignment-induced read amplification
//     the operator can compare against the baseline.
//   - send_buffer_capacity_bytes: the wal_sender_memory_buffer
//     GUC value. 0 when the ring is disabled.
//   - send_buffer_bytes_resident: bytes currently in the ring
//     (≤ capacity). Always 0 when ring is disabled.
//   - send_buffer_hits / send_buffer_misses: lifetime
//     RecordIterator.readBytesAt outcomes against the ring.
//     hits / (hits + misses) is the cache hit-rate the
//     operator should keep > 95% to make the ring worthwhile.
func registerStatWALIOView(cat *catalog.InMemory, w *wal.Writer) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_stat_wal_io",
		Columns: []catalog.Column{
			{Name: "direct_io_active", Type: catalog.Type{Name: "text"}},
			{Name: "direct_io_fallback_reason", Type: catalog.Type{Name: "text"}},
			{Name: "direct_writes", Type: catalog.Type{Name: "text"}},
			{Name: "tail_rmw_writes", Type: catalog.Type{Name: "text"}},
			{Name: "send_buffer_capacity_bytes", Type: catalog.Type{Name: "text"}},
			{Name: "send_buffer_bytes_resident", Type: catalog.Type{Name: "text"}},
			{Name: "send_buffer_hits", Type: catalog.Type{Name: "text"}},
			{Name: "send_buffer_misses", Type: catalog.Type{Name: "text"}},
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
		active := "f"
		if w.DirectIORequested() && w.DirectIOFallbackReason() == "" {
			active = "t"
		}
		return [][]string{{
			active,
			w.DirectIOFallbackReason(),
			fmt.Sprintf("%d", w.DirectIOWrites()),
			fmt.Sprintf("%d", w.TailRMWWrites()),
			fmt.Sprintf("%d", capBytes),
			fmt.Sprintf("%d", resident),
			fmt.Sprintf("%d", hits),
			fmt.Sprintf("%d", misses),
		}}
	}
	return cat.RegisterVirtualTable(tbl)
}
