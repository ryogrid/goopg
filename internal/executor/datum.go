// Package executor runs goopg plan trees with a Volcano-style
// Open/Next/Close iterator model. Scope and growth path are
// documented in docs/design/0012-executor.md.
package executor

import (
	"fmt"
	"math/big"
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
	// KindToastPointer is an unresolved TOAST reference (M0046-0006).
	// The Bytes field carries the 12-byte on-disk TOAST pointer:
	// [toast_oid(4)|total_len(4)|num_chunks(4)]. The value must be
	// detoasted (via DetoastRow) before it is used in expressions.
	// Scan operators detoast automatically; callers that operate on
	// raw decoded rows (e.g. UPDATE predicates) receive already-detoasted
	// datums from the underlying scan.
	KindToastPointer
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
	// Value is `mantissa * 10^-NumericScale` where:
	//   - if NumericBig != nil, mantissa = NumericBig (arbitrary
	//     precision, used when the result of an arithmetic
	//     operation overflows int64 — typically NUMERIC division at
	//     scales ≥ 19, e.g. TPC-H Q8's `1.00000000000000000000`).
	//   - otherwise mantissa = NumericMantissa (int64 fast path,
	//     covers the per-row scan/hash-join hot path where values
	//     fit comfortably in int64). `123.45` is mantissa=12345
	//     scale=2; `1500` is mantissa=1500 scale=0. Sign rides on
	//     the mantissa; scale is non-negative.
	// NumericScale was widened from int8 to int16 to cover
	// upstream's NUMERIC_MAX_DISPLAY_SCALE = 1000.
	NumericMantissa int64
	NumericScale    int16
	NumericBig      *big.Int
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
		if d.NumericBig != nil {
			return formatNumericBig(d.NumericBig, d.NumericScale)
		}
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
func formatNumeric(mantissa int64, scale int16) string {
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

// numericText returns d formatted as the canonical NUMERIC text,
// transparently dispatching to formatNumericBig for the *big.Int
// lane and formatNumeric for the int64 fast path.
func numericText(d Datum) string {
	if d.NumericBig != nil {
		return formatNumericBig(d.NumericBig, d.NumericScale)
	}
	return formatNumeric(d.NumericMantissa, d.NumericScale)
}

// formatNumericBig is the *big.Int variant of formatNumeric, used
// when a NUMERIC value's mantissa exceeds int64 range (TPC-H Q8's
// `1.00000000000000000000` produces mantissa=10^20 ≈ 1e20 ≥ max
// int64). Behaviour matches formatNumeric exactly.
func formatNumericBig(mantissa *big.Int, scale int16) string {
	if scale == 0 {
		return mantissa.Text(10)
	}
	neg := mantissa.Sign() < 0
	abs := new(big.Int).Abs(mantissa)
	digits := abs.Text(10)
	if int(scale) >= len(digits) {
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

// cloneRow returns a fresh `Row` that shares no backing array with
// `src`. Used by leaf scan operators (seqScan, indexScan,
// spillReader) when their internal decode buffer is reused across
// `Next()` calls and the caller may retain the returned row beyond
// the next call (M0054-0005a). The Datum element type is a value
// (no pointer-bearing fields beyond strings/bytes which are
// immutable), so a `copy` is sufficient — no deep-copy of nested
// data needed.
func cloneRow(src Row) Row {
	if src == nil {
		return nil
	}
	dst := make(Row, len(src))
	copy(dst, src)
	return dst
}
