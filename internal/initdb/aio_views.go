// AIO observability virtual views.
//
// `pg_stat_aio` exposes the AIO engine's counters split by
// operation (read vs write). Two rows per engine — one per
// direction — mirroring the upstream `pg_stat_io` row shape.
// Useful for "is AIO running, and which side is hot?" triage.
//
// `pg_aios` exposes one row per currently-outstanding I/O —
// the same shape upstream's `pg_aios()` set-returning function
// has. Backed by `aio.Engine.InFlight()`, which is populated
// by per-handle tracking on Submit and cleared on completion.
// Useful for "what is this query stuck on?" triage.
//
// See docs/design/0009-0004-aio-observability.md.

package initdb

import (
	"fmt"
	"time"

	"github.com/goopg/goopg/internal/aio"
	"github.com/goopg/goopg/internal/catalog"
)

// registerStatAIOView installs `pg_catalog.pg_stat_aio` backed
// by the supplied *aio.Engine. nil engine produces a view that
// emits zero rows — operator can still SELECT * without an
// `unrecognized configuration parameter`-style error.
func registerStatAIOView(cat *catalog.InMemory, eng *aio.Engine) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_stat_aio",
		Columns: []catalog.Column{
			{Name: "method", Type: catalog.Type{Name: "text"}},
			{Name: "operation", Type: catalog.Type{Name: "text"}},
			{Name: "submitted", Type: catalog.Type{Name: "text"}},
			{Name: "completed", Type: catalog.Type{Name: "text"}},
			{Name: "errored", Type: catalog.Type{Name: "text"}},
			{Name: "in_flight", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		if eng == nil {
			return nil
		}
		s := eng.Stats()
		// Two rows per engine: one for reads, one for writes.
		// `in_flight` is the engine's aggregate (we don't yet
		// track in-flight per-direction); rendering it on the
		// read row only would mislead, so we put the aggregate
		// on the read row and "0" on the write row's column —
		// the column header `in_flight` semantically belongs
		// to the engine, not the direction. (A future refactor
		// can split InFlight by direction too.)
		return [][]string{
			{
				s.Method, "read",
				fmt.Sprintf("%d", s.ReadSubmitted),
				fmt.Sprintf("%d", s.ReadCompleted),
				fmt.Sprintf("%d", s.ReadErrored),
				fmt.Sprintf("%d", s.InFlight),
			},
			{
				s.Method, "write",
				fmt.Sprintf("%d", s.WriteSubmitted),
				fmt.Sprintf("%d", s.WriteCompleted),
				fmt.Sprintf("%d", s.WriteErrored),
				"0",
			},
		}
	}
	return cat.RegisterVirtualTable(tbl)
}

// registerPgAiosView installs `pg_catalog.pg_aios` backed by
// aio.Engine.InFlight(). Zero rows when no engine is attached
// or no Ops are outstanding. Mirrors the upstream `pg_aios`
// column shape closely enough that an operator's `\watch
// pg_aios` muscle memory transfers — though some upstream
// fields (target, target_desc, raw_result) are not yet tracked
// and render as blank text.
func registerPgAiosView(cat *catalog.InMemory, eng *aio.Engine) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_aios",
		Columns: []catalog.Column{
			{Name: "io_id", Type: catalog.Type{Name: "text"}},
			{Name: "operation", Type: catalog.Type{Name: "text"}},
			{Name: "off", Type: catalog.Type{Name: "text"}},
			{Name: "length", Type: catalog.Type{Name: "text"}},
			{Name: "submitted_at", Type: catalog.Type{Name: "text"}},
			{Name: "elapsed_us", Type: catalog.Type{Name: "text"}},
			{Name: "target_desc", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		if eng == nil {
			return nil
		}
		now := time.Now()
		snap := eng.InFlight()
		out := make([][]string, 0, len(snap))
		for _, e := range snap {
			out = append(out, []string{
				fmt.Sprintf("%d", e.ID),
				e.Direction.String(),
				fmt.Sprintf("%d", e.Offset),
				fmt.Sprintf("%d", e.Length),
				e.SubmittedAt.UTC().Format(time.RFC3339Nano),
				fmt.Sprintf("%d", now.Sub(e.SubmittedAt).Microseconds()),
				e.Target,
			})
		}
		return out
	}
	return cat.RegisterVirtualTable(tbl)
}
