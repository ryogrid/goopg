package executor

// operators_generate_series.go — generate_series(start, stop[, step]) SRF.
// Produces one int8 row per integer value from start to stop inclusive
// with optional step. Used by INSERT … SELECT * FROM generate_series(...)
// and similar patterns. M0096-0006.

import (
	"github.com/goopg/goopg/internal/planner"
)

type generateSeriesOp struct {
	plan    *planner.GenerateSeries
	ctx     *Context
	current int64
	stop    int64
	step    int64
	started bool
	done    bool
}

func newGenerateSeriesOp(p *planner.GenerateSeries) *generateSeriesOp {
	return &generateSeriesOp{plan: p}
}

func (o *generateSeriesOp) Schema() planner.Schema { return o.plan.Output() }

func (o *generateSeriesOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *generateSeriesOp) Close() error { return nil }

func (o *generateSeriesOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	if !o.started {
		// Evaluate start, stop, step expressions on first call.
		start, err := evalExpr(o.plan.Start, nil, o.ctx)
		if err != nil {
			return nil, err
		}
		stop, err := evalExpr(o.plan.Stop, nil, o.ctx)
		if err != nil {
			return nil, err
		}
		step := int64(1)
		if o.plan.Step != nil {
			sv, serr := evalExpr(o.plan.Step, nil, o.ctx)
			if serr != nil {
				return nil, serr
			}
			if sv.Kind == KindInt {
				step = sv.Int
			}
		}
		startI, _ := datumInt64(start)
		stopI, _ := datumInt64(stop)
		o.current = startI
		o.stop = stopI
		o.step = step
		o.started = true
	}

	if o.step > 0 && o.current > o.stop {
		o.done = true
		return nil, EOF
	}
	if o.step < 0 && o.current < o.stop {
		o.done = true
		return nil, EOF
	}
	if o.step == 0 {
		o.done = true
		return nil, EOF
	}

	val := NewIntDatum(o.current)
	o.current += o.step
	return SlotFromRow(nil, Row{val}), nil
}
