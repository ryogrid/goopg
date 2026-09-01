package executor

// operators_generate_series.go — generate_series(start, stop[, step]) and
// generate_subscripts(arr, dim[, reverse]) SRF operators.
// Produces one int8 row per integer value from start to stop inclusive
// with optional step. Used by INSERT … SELECT * FROM generate_series(...)
// and similar patterns. M0096-0006.

import (
	"math"

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

	// row and slot are reused across emissions (review/260831 EO1-10): the
	// series used to allocate a one-column Row AND a MaterializedSlot per
	// value. Same "valid until the next Next()" contract as indexScanOp
	// (M0092-0007).
	row  Row
	slot MaterializedSlot
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
		// PG: int8.c generate_series_int8, ERRCODE_INVALID_PARAMETER_VALUE
		// (22023). review/260831-2 EO2-3: this used to report 2201F.
		return nil, &ExecError{Code: "22023", Message: "step size cannot equal zero"}
	}

	val := NewIntDatum(o.current)
	// review/260831-2 EO2-3: `o.current += o.step` wraps at the int64 ceiling,
	// and a wrapped current is back inside the bounds — the series then ran
	// forever. PG stops the iteration when the addition overflows
	// (pg_add_s64_overflow in generate_series_int8).
	if (o.step > 0 && o.current > math.MaxInt64-o.step) ||
		(o.step < 0 && o.current < math.MinInt64-o.step) {
		o.done = true
	} else {
		o.current += o.step
	}
	if o.row == nil {
		o.row = make(Row, 1)
	}
	o.row[0] = val
	o.slot.row = o.row
	return &o.slot, nil
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

	// row and slot are reused across emissions (review/260831 EO1-10).
	row  Row
	slot MaterializedSlot
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
	if o.row == nil {
		o.row = make(Row, 1)
	}
	o.row[0] = val
	o.slot.row = o.row
	return &o.slot, nil
}
