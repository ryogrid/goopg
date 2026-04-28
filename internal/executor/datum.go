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
	}
	return fmt.Sprintf("?datum kind=%d?", d.Kind)
}

// Row is one tuple in flight: a slice of Datums aligned with the
// emitting operator's Schema.
type Row []Datum
