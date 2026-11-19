package executor

import (
	"fmt"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/misc"
)

// The functions behind the time and timetz operators (date.c). A time of day is
// held as a KindTime value on 1970-01-01, which pgTimeFromMicros builds, so the
// time since midnight is the distance from that day's start.

var timeOfDayBase = timeToCarrier(pgTimeFromMicros(0))

func timeOperandMicros(d Datum) (int64, error) {
	if d.Kind == KindString {
		t, err := parseTimeString(d.StringValue())
		if err != nil {
			return 0, err
		}
		return timeToCarrier(t) - timeOfDayBase, nil
	}
	return d.Int - timeOfDayBase, nil
}

// wrapTimeOfDay reduces a time to one day, as time_pl_interval does.
func wrapTimeOfDay(micros int64) int64 {
	micros %= usecsPerDay
	if micros < 0 {
		micros += usecsPerDay
	}
	return micros
}

func timeRangeError(message string) error {
	return &ExecError{Code: "22008", Message: message}
}

// sessionClock returns the wall clock of an instant in the session time zone, as
// a time value on 1970-01-01, and that zone's offset east of UTC in seconds.
func sessionClock(instant time.Time, ctx *Context) (time.Time, int) {
	wall := misc.TimestampTZToTimestamp(instant, timeZoneFromCtx(ctx))
	offset := int(wall.Sub(instant) / time.Second)
	clock := time.Date(1970, 1, 1, wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), time.UTC)
	return clock, offset
}

func isTimeOfDaySource(sourceType string) bool {
	return strings.EqualFold(sourceType, "time") || strings.EqualFold(sourceType, "time without time zone")
}

// timeToInterval is time_interval, the cast from time to interval.
func timeToInterval(d Datum) (Datum, error) {
	micros, err := timeOperandMicros(d)
	if err != nil {
		return Datum{}, err
	}
	return NewIntervalDatumFull(0, 0, micros), nil
}

// timePlusInterval is time_pl_interval, time_mi_interval and their timetz forms.
func timePlusInterval(t, iv Datum, subtract, withZone bool) (Datum, error) {
	if iv.IsIntervalNoBegin() || iv.IsIntervalNoEnd() {
		if subtract {
			return Datum{}, timeRangeError("cannot subtract infinite interval from time")
		}
		return Datum{}, timeRangeError("cannot add infinite interval to time")
	}
	micros, err := timeOperandMicros(t)
	if err != nil {
		return Datum{}, err
	}
	span := iv.IntervalMicrosValue()
	if subtract {
		span = -span
	}
	result := pgTimeFromMicros(wrapTimeOfDay(micros%usecsPerDay + span%usecsPerDay))
	if withZone {
		return NewTimeTZDatum(result, t.TimeTZOffsetSecs()), nil
	}
	return NewTimeOfDayDatum(result), nil
}

// dateWithTime is datetime_timestamp and datetimetz_timestamptz.
func dateWithTime(date, tod Datum, withZone bool) (Datum, error) {
	var sub TimeSubtype
	if withZone {
		sub = TimeSubTimestampTZ
	}
	if date.IsTimestampNotFinite() {
		return Datum{Kind: KindTime, Int: date.Int, TimeSub: sub}, nil
	}
	if isFarDateCarrier(date.Int) {
		return Datum{}, dateOutOfRangeForTimestamp()
	}
	micros, err := timeOperandMicros(tod)
	if err != nil {
		return Datum{}, err
	}
	if withZone {
		micros -= int64(tod.TimeTZOffsetSecs()) * timeCarrierUnitsPerSecond
	}
	result, ok := addTimeCarrierMicros(date.Int, micros)
	if !ok {
		if withZone {
			return Datum{}, dateOutOfRangeForTimestamp()
		}
		return Datum{}, timeRangeError("timestamp out of range")
	}
	return Datum{Kind: KindTime, Int: result, TimeSub: sub}, nil
}

func evalTimeOperator(name string, x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return NullDatum, nil
	}
	l, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	r, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if l.IsNull() || r.IsNull() {
		return NullDatum, nil
	}
	withZone := strings.Contains(name, "tz")
	switch name {
	case "time_pl_interval", "timetz_pl_interval":
		return timePlusInterval(l, r, false, withZone)
	case "time_mi_interval", "timetz_mi_interval":
		return timePlusInterval(l, r, true, withZone)
	case "time_mi_time":
		a, err := timeOperandMicros(l)
		if err != nil {
			return Datum{}, err
		}
		b, err := timeOperandMicros(r)
		if err != nil {
			return Datum{}, err
		}
		return NewIntervalDatumFull(0, 0, a-b), nil
	}
	return dateWithTime(l, r, withZone)
}

// evalMulDInterval is mul_d_interval: a number, as float8, times an interval.
func evalMulDInterval(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	if len(x.Args) != 2 {
		return NullDatum, nil
	}
	n, err := evalExprSlot(x.Args[0], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	iv, err := evalExprSlot(x.Args[1], slot, ctx)
	if err != nil {
		return Datum{}, err
	}
	if n.IsNull() || iv.IsNull() {
		return NullDatum, nil
	}
	factor, ok := datumToFloat64(n)
	if !ok {
		return Datum{}, &ExecError{Code: "22P02", Pos: x.Pos(),
			Message: fmt.Sprintf("invalid input syntax for type double precision: %q", n.Format())}
	}
	return intervalMul(iv, factor, x.Pos())
}

