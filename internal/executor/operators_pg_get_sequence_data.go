package executor

// operators_pg_get_sequence_data.go — pg_get_sequence_data(regclass) SRF.
//
// Returns (last_value int8, is_called bool) for the given sequence relation.
// pg_dump's getSequences comma-joins it with pg_catalog.pg_sequence
// (`FROM pg_sequence, pg_get_sequence_data(seqrelid)`). goopg's pg_sequence
// virtual view is empty (sequences are not surfaced in pg_class, so pg_dump
// never discovers a relkind='S' relation to dump), so the implicit-LATERAL
// right side is never executed and this operator always returns 0 rows.
// M0110-0001 (DU-002 slice 32).

import "github.com/goopg/goopg/internal/planner"

type pgGetSequenceDataOp struct {
	plan *planner.PgGetSequenceData
}

func newPgGetSequenceDataOp(p *planner.PgGetSequenceData) *pgGetSequenceDataOp {
	return &pgGetSequenceDataOp{plan: p}
}

func (o *pgGetSequenceDataOp) Schema() planner.Schema { return o.plan.Output() }

func (o *pgGetSequenceDataOp) Open(_ *Context) error { return nil }

func (o *pgGetSequenceDataOp) Next() (TupleSlot, error) { return nil, EOF }

func (o *pgGetSequenceDataOp) Close() error { return nil }
