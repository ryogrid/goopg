package executor

import (
	"math"
	"math/big"
	"time"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

func init() {
	optimizer.EstimateSeriesRows = estimateSeriesRows
}

// estimateSeriesRows is the SupportRequestRows half of generate_series'
// support functions (int.c, int8.c, numeric.c, timestamp.c). The arguments are
// evaluated as estimate_expression_value would, so a stable call such as now()
// takes its current value. A NULL argument means no rows; any failure gives
// no estimate.
func estimateSeriesRows(args []optimizer.Expr) (rows float64, ok bool) {
	defer func() {
		if recover() != nil {
			rows, ok = 0, false
		}
	}()
	ctx := &Context{CmdID: storage.InvalidCommandId, Now: time.Now()}
	vals := make([]Datum, len(args))
	for i, a := range args {
		d, err := evalExprSlot(a, nil, ctx)
		if err != nil {
			return 0, false
		}
		if d.IsNull() {
			return 0, true
		}
		vals[i] = d
	}
	if vals[0].Kind == KindTime {
		return timestampSeriesRows(vals, ctx)
	}
	for _, v := range vals {
		if v.Kind == KindNumeric {
			return numericSeriesRows(vals)
		}
	}
	for _, v := range vals {
		if v.Kind != KindInt {
			return 0, false
		}
	}
	start, finish, step := float64(vals[0].Int), float64(vals[1].Int), 1.0
	if len(vals) > 2 {
		step = float64(vals[2].Int)
	}
	if step == 0 {
		return 0, false
	}
	return math.Floor((finish - start + step) / step), true
}

// numericSeriesRows is generate_series_numeric_support: floor((stop - start) /
// step) + 1 when step has the sign of stop - start (zero counting as
// positive), else no rows.
func numericSeriesRows(vals []Datum) (float64, bool) {
	rat := func(d Datum) (*big.Rat, bool) {
		if d.Kind == KindInt {
			return new(big.Rat).SetInt64(d.Int), true
		}
		if d.Kind != KindNumeric {
			return nil, false
		}
		return new(big.Rat).SetString(numericText(d))
	}
	start, ok1 := rat(vals[0])
	stop, ok2 := rat(vals[1])
	if !ok1 || !ok2 {
		return 0, false // NaN or infinity: generate_series raises
	}
	step := big.NewRat(1, 1)
	if len(vals) > 2 {
		var ok bool
		if step, ok = rat(vals[2]); !ok {
			return 0, false
		}
	}
	if step.Sign() == 0 {
		return 0, false
	}
	diff := new(big.Rat).Sub(stop, start)
	if (step.Sign() < 0) != (diff.Sign() < 0) {
		return 0, true
	}
	q := new(big.Rat).Quo(diff, step)
	n := new(big.Int).Quo(q.Num(), q.Denom()) // div_var at scale 0 truncates
	f, _ := new(big.Float).SetInt(n).Float64()
	return f + 1, true
}

// timestampSeriesRows is generate_series_timestamp_support: the span over the
// step, both measured with 30-day months, plus one.
func timestampSeriesRows(vals []Datum, ctx *Context) (float64, bool) {
	if len(vals) < 3 {
		return 0, false
	}
	start, stop, step := vals[0], vals[1], vals[2]
	if stop.Kind == KindString {
		var err error
		target := "timestamp"
		if start.TimeSub == TimeSubTimestampTZ {
			target = "timestamptz"
		}
		if stop, err = evalCast(stop, target, 0, ctx); err != nil {
			return 0, false
		}
	}
	if step.Kind == KindString {
		var err error
		if step, err = evalCast(step, "interval", 0, ctx); err != nil {
			return 0, false
		}
	}
	if stop.Kind != KindTime || step.Kind != KindInterval || step.IsIntervalNotFinite() ||
		start.IsTimestampNotFinite() || stop.IsTimestampNotFinite() {
		return 0, false
	}
	diff := float64(stop.Int) - float64(start.Int)
	const usecsPerDayF = 86400e6
	dstep := (float64(step.IntervalMonthsValue())*30+float64(step.IntervalDaysValue()))*usecsPerDayF +
		float64(step.IntervalMicrosValue())
	if dstep == 0 {
		return 0, false
	}
	return math.Floor(diff/dstep + 1.0), true
}
