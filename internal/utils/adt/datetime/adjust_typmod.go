package datetime

// AdjustTimeForTypmod rounds a time-of-day value (microseconds since midnight,
// PG's TimeADT) to the declared precision, exactly as PostgreSQL's
// AdjustTimeForTypmod does (postgres/src/backend/utils/adt/date.c:1710).
//
// typmod is the fractional-second precision (0..6), the value a `time(N)` /
// `timetz(N)` column's typmod or a `::time(N)` cast carries directly. A typmod
// outside [0,6] — notably -1, the "no precision declared" convention — is a
// no-op. Rounding is half away from zero, so `'23:59:59.999999'` at precision 2
// becomes 24:00:00 (usecsPerDay) rather than 23:59:59.99: the carry through the
// second into the next day, which goopg's former OUTPUT-side truncation could
// not express. M0119-0006 (62nd slice).
func AdjustTimeForTypmod(micros int64, typmod int32) int64 {
	if typmod < 0 || typmod > maxTimePrecision {
		return micros
	}
	scale := timeTypmodScales[typmod]
	offset := timeTypmodOffsets[typmod]
	if micros >= 0 {
		return ((micros + offset) / scale) * scale
	}
	return -((((-micros) + offset) / scale) * scale)
}

const maxTimePrecision = 6

// timeTypmodScales[p] = 10^(6-p): the quantum a time(N) precision rounds the
// TimeADT to. Mirrors TimeScales in PostgreSQL's AdjustTimeForTypmod.
var timeTypmodScales = [maxTimePrecision + 1]int64{1000000, 100000, 10000, 1000, 100, 10, 1}

// timeTypmodOffsets[p] = 10^(6-p) / 2: the half-quantum added before the
// integer division to produce half-away-from-zero rounding. Mirrors TimeOffsets.
var timeTypmodOffsets = [maxTimePrecision + 1]int64{500000, 50000, 5000, 500, 50, 5, 0}
