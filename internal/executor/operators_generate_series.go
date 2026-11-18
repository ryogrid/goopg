package executor

// operators_generate_series.go — generate_series(start, stop[, step]) and
// generate_subscripts(arr, dim[, reverse]) SRF operators.
// Produces one int8 row per integer value from start to stop inclusive
// with optional step. Used by INSERT … SELECT * FROM generate_series(...)
// and similar patterns. M0096-0006.

import (
	"math"
	"strings"

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
	// stepper runs the numeric and timestamp forms; nil for the integer ones.
	stepper *datumSeries
	// outerSlot carries the outer row for arguments of a LATERAL call.
	outerSlot SlotView

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

// BindLateralOuter binds the outer row's slot for correlated arguments.
func (o *generateSeriesOp) BindLateralOuter(slot SlotView) { o.outerSlot = slot }

func (o *generateSeriesOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	if !o.started {
		// Evaluate start, stop, step expressions on first call.
		o.started = true
		o.stepper = nil
		args, isNull, err := evalSeriesArgs(o.plan.Start, o.plan.Stop, o.plan.Step, o.plan.Zone, o.outerSlot, o.ctx)
		if err != nil {
			return nil, err
		}
		if isNull { // the functions are strict
			o.done = true
			return nil, EOF
		}
		typeName := o.plan.Output()[0].Type.Name
		if seriesIsInteger(typeName) {
			o.current, _ = datumInt64(args[0])
			o.stop, _ = datumInt64(args[1])
			o.step = 1
			if o.plan.Step != nil {
				o.step, _ = datumInt64(args[2])
			}
		} else if o.stepper, err = newDatumSeries(typeName, args, o.plan.Step != nil, o.plan.Zone != nil, o.ctx); err != nil {
			return nil, err
		}
	}

	if o.stepper != nil {
		val, ok, err := o.stepper.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			o.done = true
			return nil, EOF
		}
		return o.emit(val), nil
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
	return o.emit(val), nil
}

func (o *generateSeriesOp) emit(val Datum) TupleSlot {
	if o.row == nil {
		o.row = make(Row, 1)
	}
	o.row[0] = val
	o.slot.row = o.row
	return &o.slot
}

func seriesIsInteger(typeName string) bool {
	switch strings.ToLower(typeName) {
	case "numeric", "timestamp", "timestamptz":
		return false
	}
	return true
}

// evalSeriesArgs evaluates the arguments of generate_series. isNull reports a NULL
// among those present.
func evalSeriesArgs(start, stop, step, zone optimizer.Expr, slot SlotView, ctx *Context) (args [4]Datum, isNull bool, err error) {
	for i, e := range [...]optimizer.Expr{start, stop, step, zone} {
		if e == nil {
			continue
		}
		if args[i], err = evalExprSlot(e, slot, ctx); err != nil {
			return args, false, err
		}
		isNull = isNull || args[i].IsNull()
	}
	return args, isNull, nil
}

// datumSeries is generate_series_step_numeric, generate_series_timestamp and
// generate_series_timestamptz_internal.
type datumSeries struct {
	numeric       bool
	current, stop Datum
	step          Datum
	descending    bool
	zone          string
}

func seriesParamError(message string) error {
	return &ExecError{Code: "22023", Message: message}
}

// seriesFiniteNumeric casts to numeric and rejects NaN and the infinities, which
// are carried as text.
func seriesFiniteNumeric(d Datum, what string, ctx *Context) (Datum, error) {
	v, err := evalCast(d, "numeric", 0, ctx)
	if err != nil {
		return Datum{}, err
	}
	if v.Kind == KindString {
		if v.StringValue() == "NaN" {
			return Datum{}, seriesParamError(what + " cannot be NaN")
		}
		return Datum{}, seriesParamError(what + " cannot be infinity")
	}
	return v, nil
}

func newDatumSeries(typeName string, args [4]Datum, hasStep, hasZone bool, ctx *Context) (*datumSeries, error) {
	s := &datumSeries{}
	var err error
	if strings.EqualFold(typeName, "numeric") {
		s.numeric = true
		if s.current, err = seriesFiniteNumeric(args[0], "start value", ctx); err != nil {
			return nil, err
		}
		if s.stop, err = seriesFiniteNumeric(args[1], "stop value", ctx); err != nil {
			return nil, err
		}
		s.step = NewNumericInt64Datum(1, 0)
		if hasStep {
			if s.step, err = seriesFiniteNumeric(args[2], "step size", ctx); err != nil {
				return nil, err
			}
		}
		sign, err := numericCmp(s.step, NewNumericInt64Datum(0, 0))
		if err != nil {
			return nil, err
		}
		if sign == 0 {
			return nil, seriesParamError("step size cannot equal zero")
		}
		s.descending = sign < 0
		return s, nil
	}
	target := strings.ToLower(typeName)
	if s.current, err = evalCast(args[0], target, 0, ctx); err != nil {
		return nil, err
	}
	if s.stop, err = evalCast(args[1], target, 0, ctx); err != nil {
		return nil, err
	}
	if s.step, err = evalCast(args[2], "interval", 0, ctx); err != nil {
		return nil, err
	}
	if target == "timestamptz" {
		s.zone = timeZoneFromCtx(ctx)
		if hasZone {
			if s.zone, err = lookupTimeZoneName(args[3].StringValue()); err != nil {
				return nil, err
			}
		}
	}
	sign, err := compareDatum(s.step, NewIntervalDatumFull(0, 0, 0), 0)
	if err != nil {
		return nil, err
	}
	if sign == 0 {
		return nil, seriesParamError("step size cannot equal zero")
	}
	if s.step.IsIntervalNoBegin() || s.step.IsIntervalNoEnd() {
		return nil, seriesParamError("step size cannot be infinite")
	}
	s.descending = sign < 0
	return s, nil
}

// next returns the current value and moves on, as the per-call half of the C
// functions does, so an overflow in that step is raised with the value unsent.
func (s *datumSeries) next() (Datum, bool, error) {
	var cmp int
	var err error
	if s.numeric {
		if cmp, err = numericCmp(s.current, s.stop); err != nil {
			return Datum{}, false, err
		}
	} else if s.current.Int < s.stop.Int {
		cmp = -1
	} else if s.current.Int > s.stop.Int {
		cmp = 1
	}
	if (!s.descending && cmp > 0) || (s.descending && cmp < 0) {
		return Datum{}, false, nil
	}
	result := s.current
	if s.numeric {
		s.current, err = numericAdd(s.current, s.step)
	} else {
		s.current, err = addTimeIntervalInZone(s.current, s.step, false, 0, s.zone)
	}
	if err != nil {
		return Datum{}, false, err
	}
	return result, true, nil
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
