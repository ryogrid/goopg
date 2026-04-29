// AIO observability virtual view: `pg_stat_aio` exposes the
// AIO engine's aggregate counters. v0 ships the aggregate
// flavour first because it covers the typical operator
// question — "is AIO running, and is it making progress?" —
// without needing per-handle bookkeeping. The per-I/O `pg_aios`
// view (one row per outstanding I/O) lands in a follow-up
// alongside per-handle tracking on the engine.
//
// Column shape mirrors what an operator would expect from
// pg_stat_* views: a method label plus monotonic counter
// columns. There is one row per engine — v0 has one engine
// per server, so the view is either zero rows (no engine
// attached) or one row.
//
// See docs/design/0009-0004-aio-observability.md.

package initdb

import (
	"fmt"

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
