package executor

// operators_pg_sequence_parameters.go — pg_sequence_parameters(regclass) SRF.
//
// Returns the persisted DDL parameters of a sequence: (start_value int8,
// minimum_value int8, maximum_value int8, increment int8, cycle_option bool,
// cache_size int8, data_type oid). Unlike pg_get_sequence_data (last_value /
// is_called — the live runtime state), this reads the sequence's catalog
// parameters, the same source pg_sequence's virtual view row derives from
// (catalog.PGSequenceRowsForDBOid). PG oracle:
// postgres/src/backend/commands/sequence.c:1740 pg_sequence_parameters;
// postgres/src/include/catalog/pg_proc.dat:3426-3431.
//
// The argument is a plain constant regclass (not lateral-correlated — PG's
// grammar allows pg_sequence_parameters() as an ordinary FROM-clause SRF with
// no comma-join convention like pg_get_sequence_data's pg_dump usage).
//
// M0134-0069.

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

type pgSequenceParametersOp struct {
	plan    *optimizer.PgSequenceParameters
	row     Row // the single output row, or nil for 0 rows
	emitted bool
}

func newPgSequenceParametersOp(p *optimizer.PgSequenceParameters) *pgSequenceParametersOp {
	return &pgSequenceParametersOp{plan: p}
}

func (o *pgSequenceParametersOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *pgSequenceParametersOp) Open(ctx *Context) error {
	o.row = nil
	o.emitted = false
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	if o.plan.Arg == nil {
		return nil
	}
	argVal, err := evalExprSlot(o.plan.Arg, nil, ctx)
	if err != nil {
		return err
	}
	if argVal.IsNull() {
		return nil
	}
	tbl, ok := verifyHeapamResolveTable(argVal, im, ctx.CurrentDatabaseOid)
	if !ok || !tbl.IsSequence {
		// Not a sequence relation → no rows. PG raises 42809 "is not a
		// sequence" here; goopg mirrors pg_get_sequence_data's precedent of
		// returning an empty SRF result rather than building a general
		// relkind-check error path (out of scope, see brief). M0134-0069.
		return nil
	}
	if catalog.SequenceParamsFunc == nil {
		return nil
	}
	schema := tbl.Schema
	if schema == "" {
		schema = "public"
	}
	p, ok := catalog.SequenceParamsFunc(schema+"."+tbl.Name, tbl.DBOid)
	if !ok {
		return nil
	}
	o.row = Row{
		NewIntDatum(p.Start),
		NewIntDatum(p.Min),
		NewIntDatum(p.Max),
		NewIntDatum(p.Increment),
		NewBoolDatum(p.Cycle),
		NewIntDatum(p.Cache),
		NewIntDatum(int64(p.TypeOID)),
	}
	return nil
}

func (o *pgSequenceParametersOp) Next() (TupleSlot, error) {
	if o.row == nil || o.emitted {
		return nil, EOF
	}
	o.emitted = true
	return SlotFromRow(nil, o.row), nil
}

func (o *pgSequenceParametersOp) Close() error { return nil }
