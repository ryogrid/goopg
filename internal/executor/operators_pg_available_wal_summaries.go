package executor

// operators_pg_available_wal_summaries.go — pg_available_wal_summaries() SRF.
//
// Returns (tli int8, start_lsn pg_lsn, end_lsn pg_lsn) for each WAL summary
// file present on disk. goopg v0 never produces WAL summaries because
// summarize_wal is always off; this operator always returns 0 rows. M0095-0002.

import "github.com/goopg/goopg/internal/planner"

type pgAvailableWalSummariesOp struct {
	plan *planner.PgAvailableWalSummaries
}

func newPgAvailableWalSummariesOp(p *planner.PgAvailableWalSummaries) *pgAvailableWalSummariesOp {
	return &pgAvailableWalSummariesOp{plan: p}
}

func (o *pgAvailableWalSummariesOp) Schema() planner.Schema { return o.plan.Output() }

func (o *pgAvailableWalSummariesOp) Open(_ *Context) error { return nil }

func (o *pgAvailableWalSummariesOp) Next() (TupleSlot, error) { return nil, EOF }

func (o *pgAvailableWalSummariesOp) Close() error { return nil }
