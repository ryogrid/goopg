package pgdatetime

import "math"

// AdjustIntervalForTypmod applies an interval column's declared typmod to the
// interval's three fields — months, days and the sub-day micros — exactly as
// PostgreSQL's AdjustIntervalForTypmod does
// (postgres/src/backend/utils/adt/timestamp.c:1355): every field to the RIGHT
// of the range's low field is ZEROED (toward zero), the fractional-second field
// is ROUNDED half-away-from-zero to the declared SECOND(p) precision, and the
// ±infinity sentinel is untouched.
//
// typmod is the packed INTERVAL_TYPMOD(p,r) value PG stores in
// pg_attribute.atttypmod:
//
//	((range & 0x7FFF) << 16) | (precision & 0xFFFF)
//
// where range is the OR of INTERVAL_MASK(field) = 1<<field bits. The field bit
// positions are datetime.h's MONTH=1, YEAR=2, DAY=3, HOUR=10, MINUTE=11,
// SECOND=12 (NOT the DTK_* output enum — the two are different), so
// INTERVAL_MASK(YEAR)=1<<2, INTERVAL_MASK(MONTH)=1<<1, and so on. A typmod of
// -1 — the "no modifier declared" convention for every data type — is a no-op,
// as is a precision of 0xFFFF (INTERVAL_FULL_PRECISION).
//
// This is the twin of AdjustTimeForTypmod (adjust_typmod.go) for the interval
// column typmod: the one divergence from the time function is that an interval
// typmod carries a RANGE, so "apply at input" means zeroing fields, not merely
// rounding one. M0119-0006 (63rd slice).
func AdjustIntervalForTypmod(months, days int32, micros int64, typmod int32) (int32, int32, int64) {
	if typmod < 0 {
		return months, days, micros
	}
	// Typmod has no effect on infinite intervals (PG INTERVAL_NOT_FINITE): the
	// ±infinity sentinel is all three fields at their signed extreme.
	if (months == math.MaxInt32 && days == math.MaxInt32 && micros == math.MaxInt64) ||
		(months == math.MinInt32 && days == math.MinInt32 && micros == math.MinInt64) {
		return months, days, micros
	}

	rng := intervalRange(typmod)
	prec := intervalPrecision(typmod)

	// Range truncation. The only combinations that reach here are the ones
	// intervaltypmodin accepts (each singular field, YEAR TO MONTH, DAY TO
	// {HOUR,MINUTE,SECOND}, HOUR TO {MINUTE,SECOND}, MINUTE TO SECOND) and the
	// full range; a bogus range is PG's elog(ERROR) "unrecognized interval
	// typmod", unreachable here because the value is a declared column typmod
	// that either our parser or PG's own intervaltypmodin validated.
	switch rng {
	case intervalFullRange:
		// nothing to zero — full range.
	case intervalMaskYear:
		months = (months / monthsPerYear) * monthsPerYear
		days = 0
		micros = 0
	case intervalMaskMonth, intervalMaskYear | intervalMaskMonth:
		days = 0
		micros = 0
	case intervalMaskDay:
		micros = 0
	case intervalMaskHour, intervalMaskDay | intervalMaskHour:
		micros = (micros / usecsPerHour) * usecsPerHour
	case intervalMaskMinute, intervalMaskDay | intervalMaskHour | intervalMaskMinute, intervalMaskHour | intervalMaskMinute:
		micros = (micros / usecsPerMinute) * usecsPerMinute
	case intervalMaskSecond,
		intervalMaskDay | intervalMaskHour | intervalMaskMinute | intervalMaskSecond,
		intervalMaskHour | intervalMaskMinute | intervalMaskSecond,
		intervalMaskMinute | intervalMaskSecond:
		// fractional-second rounding dealt with below.
	default:
		// PG errors "unrecognized interval typmod" — unreachable for a declared
		// column typmod (see above); leave unchanged rather than guessing.
		return months, days, micros
	}

	// Sub-second precision rounding, only when a precision was declared.
	if prec != intervalFullPrecision {
		if prec < 0 || prec > maxIntervalPrecision {
			return months, days, micros
		}
		scale := intervalScales[prec]
		offset := intervalOffsets[prec]
		if micros >= 0 {
			micros += offset
		} else {
			micros -= offset
		}
		micros -= micros % scale
	}
	return months, days, micros
}

// Interval typmod packing/unpacking constants (postgres/src/include/utils/timestamp.h).
const (
	intervalFullRange     = 0x7FFF
	intervalFullPrecision = 0xFFFF
	maxIntervalPrecision  = 6
	monthsPerYear         = 12
)

// intervalRange returns the range bitmask of a packed INTERVAL_TYPMOD value:
// INTERVAL_RANGE(t) == (t >> 16) & INTERVAL_RANGE_MASK.
func intervalRange(typmod int32) int {
	return (int(typmod) >> 16) & intervalFullRange
}

// intervalPrecision returns the precision of a packed INTERVAL_TYPMOD value:
// INTERVAL_PRECISION(t) == t & INTERVAL_PRECISION_MASK.
func intervalPrecision(typmod int32) int {
	return int(typmod) & intervalFullPrecision
}

// INTERVAL_MASK(field) bits — datetime.h's MONTH=1, YEAR=2, DAY=3, HOUR=10,
// MINUTE=11, SECOND=12. Keep in sync with internal/parser's
// intervalFieldTypmodBit (the two must name the same bit positions, or a cast
// typmod and a column typmod would truncate differently).
const (
	intervalMaskYear   = 1 << 2
	intervalMaskMonth  = 1 << 1
	intervalMaskDay    = 1 << 3
	intervalMaskHour   = 1 << 10
	intervalMaskMinute = 1 << 11
	intervalMaskSecond = 1 << 12
)

// usecsPerHour / usecsPerMinute are shared with the date/time formatters in
// pg_datetime_format.go (int64 3600·1e6 and 60·1e6 respectively).

// intervalScales[p] = 10^(6-p): the quantum a SECOND(p) precision rounds the
// sub-day micros to. Mirrors IntervalScales in PostgreSQL's
// AdjustIntervalForTypmod.
var intervalScales = [maxIntervalPrecision + 1]int64{1000000, 100000, 10000, 1000, 100, 10, 1}

// intervalOffsets[p] = 10^(6-p) / 2: the half-quantum added before the integer
// division to produce half-away-from-zero rounding. Mirrors IntervalOffsets.
var intervalOffsets = [maxIntervalPrecision + 1]int64{500000, 50000, 5000, 500, 50, 5, 0}
