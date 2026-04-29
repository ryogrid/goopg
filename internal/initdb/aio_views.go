// AIO observability virtual views.
//
// `pg_stat_aio` exposes the AIO engine's aggregate counters
// (one row per engine; v0 has at most one). Useful for
// "is AIO running, and is it making progress?" triage.
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
		return [][]string{{
			s.Method,
			fmt.Sprintf("%d", s.Submitted),
			fmt.Sprintf("%d", s.Completed),
			fmt.Sprintf("%d", s.Errored),
			fmt.Sprintf("%d", s.InFlight),
		}}
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
			})
		}
		return out
	}
	return cat.RegisterVirtualTable(tbl)
}
