package executor

// operators_generate_series.go — generate_series(start, stop[, step]) and
// generate_subscripts(arr, dim[, reverse]) SRF operators.
// Produces one int8 row per integer value from start to stop inclusive
// with optional step. Used by INSERT … SELECT * FROM generate_series(...)
// and similar patterns. M0096-0006.

import (
	"github.com/goopg/goopg/internal/optimizer"
)

type generateSeriesOp struct {
	plan    *optimizer.GenerateSeries
	ctx     *Context
	current int64
	stop    int64
	step    int64
	started bool
	done    bool
}

func newGenerateSeriesOp(p *optimizer.GenerateSeries) *generateSeriesOp {
	return &generateSeriesOp{plan: p}
}

func (o *generateSeriesOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *generateSeriesOp) Open(ctx *Context) error {
	o.ctx = ctx
	// Reset iteration state so re-opening (e.g. in a lateral loop) starts fresh.
	o.started = false
	o.done = false
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
		return nil, &ExecError{Code: "2201F", Message: "step size cannot equal zero"}
	}

	val := NewIntDatum(o.current)
	o.current += o.step
	return SlotFromRow(nil, Row{val}), nil
}

// generateSubscriptsOp implements generate_subscripts(arr, dim[, rev]) SRF.
// Returns integer subscripts 1..array_length(arr, 1) for dim=1.  M0097-0117.
type generateSubscriptsOp struct {
	plan    *optimizer.GenerateSubscripts
	ctx     *Context
	current int64
	stop    int64
	step    int64
	started bool
	done    bool
}

func newGenerateSubscriptsOp(p *optimizer.GenerateSubscripts) *generateSubscriptsOp {
	return &generateSubscriptsOp{plan: p}
}

func (o *generateSubscriptsOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *generateSubscriptsOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.started = false
	o.done = false
	return nil
}

func (o *generateSubscriptsOp) Close() error { return nil }

func (o *generateSubscriptsOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	if !o.started {
		arrDatum, err := evalExpr(o.plan.ArrExpr, nil, o.ctx)
		if err != nil {
			return nil, err
		}
		if arrDatum.IsNull() {
			o.done = true
			return nil, EOF
		}
		elems := parseTextArray(arrDatum.StringValue())
		n := int64(len(elems))
		reversed := false
		if o.plan.Reversed != nil {
			rv, rerr := evalExpr(o.plan.Reversed, nil, o.ctx)
			if rerr == nil && rv.Kind == KindBool && rv.BoolValue() {
				reversed = true
			}
		}
		if reversed {
			o.current = n
			o.stop = 1
			o.step = -1
		} else {
			o.current = 1
			o.stop = n
			o.step = 1
		}
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
