// Package executor runs goopg plan trees with a Volcano-style
// Open/Next/Close iterator model. Scope and growth path are
// documented in docs/design/0012-executor.md.
package executor

import (
	"fmt"
	"strconv"
	"time"
)

// DatumKind discriminates the value carrier in a Datum.
type DatumKind int

const (
	KindNull DatumKind = iota
	KindBool
	KindInt
	KindString
	KindBytes
	KindTime
	// KindInterval is goopg's v0 interval-arithmetic carrier.
	// Mirrors upstream's months-days-microseconds shape but
	// drops sub-day precision (TPC-H literals are integer
	// counts of days/months/years). Months and days live on
	// dedicated fields. Sub-day arithmetic waits on the type
	// system; see
	// docs/design/0003-0006-date-interval-arithmetic.md.
	KindInterval
	// KindNumeric is goopg's v0 NUMERIC/DECIMAL carrier. v0
	// stores the value as `mantissa * 10^-scale` where mantissa
	// is an int64 and scale is the number of digits to the
	// right of the decimal point. Arithmetic uses int64 math
	// after aligning scales — sufficient for TPC-H SF1 magnitudes
	// (~10^17 worst-case accumulator) but bounded by int64
	// overflow; arbitrary-precision support waits on the type
	// system milestone. See
	// docs/design/0003-0012-numeric-arithmetic.md.
	KindNumeric
)

// Datum is one column value flowing through the operator tree. v0
// uses a union-style struct; the runtime cost is dwarfed by per-row
// heap allocation, so this stays simple until profiling justifies a
// Datum interface.
type Datum struct {
	Kind   DatumKind
	Int    int64
	Bool   bool
	String string
	Bytes  []byte
	Time   time.Time

	// Interval components, populated when Kind == KindInterval.
	// Months handles year/month grain (1 year = 12 months);
	// Days is the residual day count. v0 rejects sub-day
	// intervals at parse time so this carries everything.
	IntervalMonths int32
	IntervalDays   int32

	// Numeric components, populated when Kind == KindNumeric.
	// Value is `NumericMantissa * 10^-NumericScale`. `123.45`
	// is mantissa=12345, scale=2; `1500` (NUMERIC literal) is
	// mantissa=1500, scale=0. Negative values carry the sign in
	// mantissa; scale is non-negative.
	NumericMantissa int64
	NumericScale    int8
}

// NullDatum is the canonical null value. Any Kind with IsNull() true
// is treated as SQL NULL.
var NullDatum = Datum{Kind: KindNull}

// IsNull reports whether d represents SQL NULL.
func (d Datum) IsNull() bool { return d.Kind == KindNull }

// Format renders the value the way text-mode wire protocol expects.
// Time values use upstream's `2006-01-02 15:04:05.000000` layout.
func (d Datum) Format() string {
	switch d.Kind {
	case KindNull:
		return ""
	case KindBool:
		if d.Bool {
			return "t"
		}
		return "f"
	case KindInt:
		return strconv.FormatInt(d.Int, 10)
	case KindString:
		return d.String
	case KindBytes:
		return string(d.Bytes)
	case KindTime:
		return d.Time.UTC().Format("2006-01-02 15:04:05.000000")
	case KindInterval:
		return fmt.Sprintf("%d months %d days", d.IntervalMonths, d.IntervalDays)
	case KindNumeric:
		return formatNumeric(d.NumericMantissa, d.NumericScale)
	}
	return fmt.Sprintf("?datum kind=%d?", d.Kind)
}

// formatNumeric renders mantissa * 10^-scale as the decimal string
// upstream PG emits: trailing zeros after the decimal point are
// preserved exactly as the scale tracks them, sign comes from the
// mantissa, and scale=0 emits no decimal point. Examples:
//
//	(12345, 2) → "123.45"
//	(-12345, 2) → "-123.45"
//	(1500, 0) → "1500"
//	(5, 2) → "0.05"
//	(0, 3) → "0.000"
func formatNumeric(mantissa int64, scale int8) string {
	if scale == 0 {
		return strconv.FormatInt(mantissa, 10)
	}
	neg := mantissa < 0
	abs := mantissa
	if neg {
		abs = -mantissa
	}
	digits := strconv.FormatInt(abs, 10)
	if int(scale) >= len(digits) {
		// Pad with leading zeros so we have at least scale+1
		// digits, e.g. (5, 2) → "005" → "0.05".
		pad := int(scale) - len(digits) + 1
		digits = padZeros(pad) + digits
	}
	intPart := digits[:len(digits)-int(scale)]
	fracPart := digits[len(digits)-int(scale):]
	out := intPart + "." + fracPart
	if neg {
		out = "-" + out
	}
	return out
}

func padZeros(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = '0'
	}
	return string(out)
}

// Row is one tuple in flight: a slice of Datums aligned with the
// emitting operator's Schema.
type Row []Datum
