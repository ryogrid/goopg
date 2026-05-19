// Package executor runs goopg plan trees with a Volcano-style
// Open/Next/Close iterator model. Scope and growth path are
// documented in docs/design/0012-executor.md.
package executor

import (
	"fmt"
	"math/big"
	"strconv"
	"time"
	"unsafe"

	"github.com/goopg/goopg/internal/mctx"
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
	// counts of days/months/years). v0 packs months and days
	// into the int64 inline payload (months in the high half,
	// days in the low half) — see (Datum).IntervalMonthsValue
	// / IntervalDaysValue. Sub-day arithmetic waits on the
	// type system; see
	// docs/design/0003-0006-date-interval-arithmetic.md.
	KindInterval
	// KindNumeric is goopg's v0 NUMERIC/DECIMAL carrier. v0
	// stores the value as `mantissa * 10^-scale` where mantissa
	// is an int64 (Datum.Int) and scale is the number of
	// digits to the right of the decimal point (Datum.Scale).
	// Arithmetic uses int64 math after aligning scales —
	// sufficient for TPC-H SF1 magnitudes (~10^17 worst-case
	// accumulator) but bounded by int64 overflow; the rare
	// big-number lane spills to Datum.Big (e.g. Q8's
	// `1.00000000000000000000`). See
	// docs/design/0003-0012-numeric-arithmetic.md and
	// docs/design/0068-0001-datum-compact-layout.md.
	KindNumeric
	// KindToastPointer is an unresolved TOAST reference (M0046-0006).
	// Datum.Buf carries the 12-byte on-disk TOAST pointer:
	// [toast_oid(4)|total_len(4)|num_chunks(4)]. The value must be
	// detoasted (via DetoastRow) before it is used in expressions.
	// Scan operators detoast automatically; callers that operate on
	// raw decoded rows (e.g. UPDATE predicates) receive already-detoasted
	// datums from the underlying scan.
	KindToastPointer
	// KindStringArena is an arena-backed string Datum (M0073-0001).
	// Buf is unused; arena != nil. Datum.Int packs (offset << 32) |
	// (length & 0xFFFFFFFF) — offset is the arena-relative byte
	// offset returned by Arena.Allocate; length is the byte count.
	// StringValue() resolves via arena.Bytes(offset, length).
	//
	// Lifetime: the underlying bytes are valid until the arena's
	// next Reset() call. Callers that retain the Datum across a
	// Reset MUST go through slot.Materialize(), which calls
	// cloneRowOwned to deep-copy arena bytes into a regular
	// KindString Datum (Buf-backed). See
	// docs/design/0073-0001-datum-arena-field.md.
	KindStringArena
	// KindBytesArena mirrors KindStringArena for raw bytes payload.
	// BytesValue() resolves via arena.Bytes(offset, length).
	KindBytesArena
)

// Datum is one column value flowing through the operator tree.
//
// M0068-0001 compact layout: ~48 bytes, ≤ 2 GC-traced pointers
// (was ~120 bytes / 4 pointers). The compaction saves Q5 SF=1 ~96 M
// Datums × 2 fewer pointers = ~192 M fewer pointer-field scans per
// MHJ build. See docs/design/0068-0001-datum-compact-layout.md.
//
// Field semantics by Kind:
//
//	KindNull             — all zero.
//	KindBool             — Int = 1 for true, 0 for false.
//	KindInt              — Int = the integer value.
//	KindString           — Buf = UTF-8 bytes.
//	KindBytes            — Buf = raw bytes (mutability semantics
//	                       vary by producer; readers MUST treat
//	                       Buf as read-only unless they own the
//	                       slice).
//	KindTime             — Int = Unix nanos (UTC).
//	KindInterval         — Int = (int64(months) << 32) |
//	                       (int64(days) & 0xFFFFFFFF) using
//	                       signed semantics; v0 rejects sub-day.
//	KindNumeric          — Big != nil → big.Int mantissa, else
//	                       Int = int64 mantissa fast path.
//	                       Scale = digits after decimal.
//	KindToastPointer     — Buf = 12-byte TOAST pointer.
//
// Concurrency: a Datum is a value type; copying it is cheap. Buf
// and Big are aliased on copy — readers must not mutate either
// without owning the producer's storage.
type Datum struct {
	Kind  DatumKind // 8B (Go int)
	Int   int64     // 8B (arena-Datum: (offset<<32)|length)
	Buf   []byte    // 24B (slice header; nil for arena variants)
	Big   *big.Int  // 8B
	Scale int16     // 2B
	// mctx is non-nil only when Kind is KindStringArena /
	// KindBytesArena. Datum.Int packs the (offset, length) into
	// the mctx's byte stream; StringValue() / BytesValue() resolve
	// via mctx.Bytes(offset, length). For all other Kinds, mctx
	// is nil and the legacy Buf-based path applies.
	//
	// M0073-0001 — see docs/design/0073-0001-datum-arena-field.md.
	// M0107-0001: arena *Arena renamed to mctx *mctx.Context.
	mctx *mctx.Context // 8B
	// 6B padding → 64B exact.
}

// Compile-time assertion that Datum is exactly 64 bytes after
// M0073-0001 added the arena pointer. Pre-M0073 the struct was
// 56 B with 8 B padding; the new field consumed all the headroom.
// Future field additions trigger the M0074 packed-layout work.
const _ uintptr = 64 - unsafe.Sizeof(Datum{}) // compile error if > 64

// NullDatum is the canonical null value. Any Kind with IsNull() true
// is treated as SQL NULL.
var NullDatum = Datum{Kind: KindNull}

// IsNull reports whether d represents SQL NULL.
func (d Datum) IsNull() bool { return d.Kind == KindNull }

// ---------- M0068-0001 accessors ----------

// BoolValue extracts the bool payload of a KindBool Datum.
func (d Datum) BoolValue() bool { return d.Int != 0 }

// StringValue extracts the string payload of a KindString /
// KindStringArena Datum. Returned string aliases the underlying
// bytes (zero-copy); callers must not mutate the source while the
// returned string is live, and MUST treat the returned string as
// invalid past the arena's next Reset() when the source is an
// arena-backed Datum (M0073-0001).
func (d Datum) StringValue() string {
	if d.Kind == KindStringArena {
		buf := d.mctx.Bytes(uint32(d.Int>>32), uint32(d.Int&0xFFFFFFFF))
		if len(buf) == 0 {
			return ""
		}
		return unsafe.String(&buf[0], len(buf))
	}
	if len(d.Buf) == 0 {
		return ""
	}
	return unsafe.String(&d.Buf[0], len(d.Buf))
}

// BytesValue returns the byte payload of KindBytes / KindBytesArena
// / KindToastPointer. The slice aliases the underlying bytes (Buf
// for non-arena variants, arena page for arena variants); callers
// MUST NOT mutate it, and MUST treat arena-backed bytes as invalid
// past the arena's next Reset().
func (d Datum) BytesValue() []byte {
	if d.Kind == KindBytesArena {
		return d.mctx.Bytes(uint32(d.Int>>32), uint32(d.Int&0xFFFFFFFF))
	}
	return d.Buf
}

// TimeValue returns the timestamp payload of a KindTime Datum,
// reconstructed from the Unix nanoseconds stored in Int. Always UTC.
func (d Datum) TimeValue() time.Time {
	return time.Unix(0, d.Int).UTC()
}

// IntervalMonthsValue extracts the months component of KindInterval.
// Encoded as the high 32 bits of Int with signed semantics.
func (d Datum) IntervalMonthsValue() int32 {
	return int32(d.Int >> 32)
}

// IntervalDaysValue extracts the days component of KindInterval.
// Encoded as the low 32 bits of Int with signed semantics.
func (d Datum) IntervalDaysValue() int32 {
	return int32(d.Int)
}

// NumericMantissaValue is the int64 fast-path mantissa of KindNumeric
// (valid only when Big == nil).
func (d Datum) NumericMantissaValue() int64 { return d.Int }

// NumericBigValue is the big.Int overflow lane of KindNumeric, or
// nil when the int64 fast path applies.
func (d Datum) NumericBigValue() *big.Int { return d.Big }

// NumericScaleValue is the scale (digits after decimal) of KindNumeric.
func (d Datum) NumericScaleValue() int16 { return d.Scale }

// ---------- M0068-0001 constructors ----------

// NewBoolDatum constructs a KindBool Datum with the given value.
func NewBoolDatum(b bool) Datum {
	d := Datum{Kind: KindBool}
	if b {
		d.Int = 1
	}
	return d
}

// NewIntDatum constructs a KindInt Datum.
func NewIntDatum(i int64) Datum {
	return Datum{Kind: KindInt, Int: i}
}

// NewStringDatum constructs a KindString Datum. The string body is
// re-aliased into Buf; callers must not mutate the source string
// (Go strings are immutable so this is naturally safe).
func NewStringDatum(s string) Datum {
	if s == "" {
		return Datum{Kind: KindString}
	}
	// unsafe.StringData is read-only; Datum.BytesValue / StringValue
	// callers must treat Buf as read-only. The producer-mutates-shared-
	// buffer pitfall is documented in the Datum struct comment.
	return Datum{Kind: KindString, Buf: unsafe.Slice(unsafe.StringData(s), len(s))}
}

// NewBytesDatum constructs a KindBytes Datum.
func NewBytesDatum(b []byte) Datum {
	return Datum{Kind: KindBytes, Buf: b}
}

// newStringArenaDatum constructs a KindStringArena Datum encoding
// the (offset, length) pair into the Int field. Used by the
// arena-aware decode path (M0073-0002 wires this through).
func newStringArenaDatum(sctx *mctx.Context, offset, length uint32) Datum {
	return Datum{
		Kind: KindStringArena,
		Int:  int64(offset)<<32 | int64(length)&0xFFFFFFFF,
		mctx: sctx,
	}
}

// newBytesArenaDatum constructs a KindBytesArena Datum.
func newBytesArenaDatum(sctx *mctx.Context, offset, length uint32) Datum {
	return Datum{
		Kind: KindBytesArena,
		Int:  int64(offset)<<32 | int64(length)&0xFFFFFFFF,
		mctx: sctx,
	}
}

// MaterializeArena promotes an arena-backed Datum to a regular
// Buf-backed Datum (KindString / KindBytes) by copying the arena
// bytes into a fresh []byte. Non-arena Datums pass through
// unchanged. Used at retention boundaries inside operator state
// (e.g. aggregateOp.applyAgg's st.value for min/max over varchar
// — the source arena pages may be reset before the aggregate
// finishes draining).
//
// M0073-0004.
func (d Datum) MaterializeArena() Datum {
	switch d.Kind {
	case KindStringArena:
		length := uint32(d.Int & 0xFFFFFFFF)
		if length == 0 || d.mctx == nil {
			return Datum{Kind: KindString}
		}
		offset := uint32(d.Int >> 32)
		src := d.mctx.Bytes(offset, length)
		buf := make([]byte, length)
		copy(buf, src)
		return Datum{Kind: KindString, Buf: buf}
	case KindBytesArena:
		length := uint32(d.Int & 0xFFFFFFFF)
		if length == 0 || d.mctx == nil {
			return Datum{Kind: KindBytes}
		}
		offset := uint32(d.Int >> 32)
		src := d.mctx.Bytes(offset, length)
		buf := make([]byte, length)
		copy(buf, src)
		return Datum{Kind: KindBytes, Buf: buf}
	}
	return d
}

// rowHasArena reports whether any Datum in r is arena-backed.
// Used by Materialize's fast-path skip — most pipeline rows mid-
// batch are arena-backed (post-M0073-0004 producer wiring); at the
// retention boundary (sortOp / windowOp / lockRowsOp / executor.Run)
// Materialize calls cloneRowOwned to deep-copy arena bytes into
// owned []byte. Pre-M0073-0004 (this commit) the helper is unused
// in production; the test surface exercises it directly.
func rowHasArena(r Row) bool {
	for _, d := range r {
		if d.Kind == KindStringArena || d.Kind == KindBytesArena {
			return true
		}
	}
	return false
}

// cloneRowOwned produces a Row with the same logical values as
// src but with arena-backed Datums promoted to regular KindString
// / KindBytes Datums whose Buf is freshly allocated. Non-arena
// Datums pass through unchanged.
//
// Used by MaterializedSlot.Materialize at the slot pipeline's
// retention boundaries so consumers that hold rows past the
// producer's next Reset() see independent storage.
func cloneRowOwned(src Row) Row {
	dst := acquireRow(len(src))
	for i, d := range src {
		switch d.Kind {
		case KindStringArena:
			length := uint32(d.Int & 0xFFFFFFFF)
			if length == 0 || d.mctx == nil {
				dst[i] = Datum{Kind: KindString}
				continue
			}
			offset := uint32(d.Int >> 32)
			s := d.mctx.Bytes(offset, length)
			buf := make([]byte, length)
			copy(buf, s)
			dst[i] = Datum{Kind: KindString, Buf: buf}
		case KindBytesArena:
			length := uint32(d.Int & 0xFFFFFFFF)
			if length == 0 || d.mctx == nil {
				dst[i] = Datum{Kind: KindBytes}
				continue
			}
			offset := uint32(d.Int >> 32)
			s := d.mctx.Bytes(offset, length)
			buf := make([]byte, length)
			copy(buf, s)
			dst[i] = Datum{Kind: KindBytes, Buf: buf}
		default:
			dst[i] = d
		}
	}
	return dst
}

// NewTimeDatum constructs a KindTime Datum from a time.Time.
// The timestamp is normalized to UTC and stored as Unix nanoseconds.
func NewTimeDatum(t time.Time) Datum {
	return Datum{Kind: KindTime, Int: t.UTC().UnixNano()}
}

// NewIntervalDatum constructs a KindInterval Datum from
// month/day components. Sub-day grain is rejected at parse time.
func NewIntervalDatum(months, days int32) Datum {
	return Datum{
		Kind: KindInterval,
		Int:  (int64(months) << 32) | (int64(days) & 0xFFFFFFFF),
	}
}

// NewNumericInt64Datum constructs a KindNumeric Datum using the
// int64 fast path. mant is the mantissa, scale is the number of
// digits after the decimal point.
func NewNumericInt64Datum(mant int64, scale int16) Datum {
	return Datum{Kind: KindNumeric, Int: mant, Scale: scale}
}

// NewNumericBigDatum constructs a KindNumeric Datum on the big.Int
// overflow lane (used when the result of arithmetic exceeds int64
// range, e.g. TPC-H Q8's `1.00000000000000000000`).
func NewNumericBigDatum(big *big.Int, scale int16) Datum {
	return Datum{Kind: KindNumeric, Big: big, Scale: scale}
}

// NewToastPointerDatum constructs a KindToastPointer Datum.
func NewToastPointerDatum(p []byte) Datum {
	return Datum{Kind: KindToastPointer, Buf: p}
}

// ---------- formatting ----------

// Format renders the value the way text-mode wire protocol expects.
// Time values use upstream's `2006-01-02 15:04:05.000000` layout.
// AppendValueText appends d's text-format wire representation
// directly to dst and returns the extended slice. The protocol-
// layer hot path uses this instead of Format() to avoid the
// `strconv.FormatInt → string → []byte` allocation chain
// (M0092-0004): KindInt no longer allocates a fresh string per
// row, and KindString/KindBytes/KindStringArena/KindBytesArena
// skip the string→[]byte conversion. Behaviour matches Format().
func (d Datum) AppendValueText(dst []byte) []byte {
	switch d.Kind {
	case KindNull:
		return dst
	case KindBool:
		if d.BoolValue() {
			return append(dst, 't')
		}
		return append(dst, 'f')
	case KindInt:
		return strconv.AppendInt(dst, d.Int, 10)
	case KindString:
		return append(dst, d.Buf...)
	case KindStringArena:
		return append(dst, d.mctx.Bytes(uint32(d.Int>>32), uint32(d.Int&0xFFFFFFFF))...)
	case KindBytes:
		return append(dst, d.Buf...)
	case KindBytesArena:
		return append(dst, d.mctx.Bytes(uint32(d.Int>>32), uint32(d.Int&0xFFFFFFFF))...)
	case KindTime:
		return d.TimeValue().AppendFormat(dst, "2006-01-02 15:04:05.000000")
	}
	// Fallback for KindInterval / KindNumeric / unknown kinds —
	// the slow lane allocates but is off the simple-query
	// pgbench hot path (which only emits KindInt for abalance).
	return append(dst, d.Format()...)
}

func (d Datum) Format() string {
	switch d.Kind {
	case KindNull:
		return ""
	case KindBool:
		if d.BoolValue() {
			return "t"
		}
		return "f"
	case KindInt:
		return strconv.FormatInt(d.Int, 10)
	case KindString, KindStringArena:
		return d.StringValue()
	case KindBytes, KindBytesArena:
		return string(d.BytesValue())
	case KindTime:
		return d.TimeValue().Format("2006-01-02 15:04:05.000000")
	case KindInterval:
		return fmt.Sprintf("%d months %d days", d.IntervalMonthsValue(), d.IntervalDaysValue())
	case KindNumeric:
		if d.Big != nil {
			return formatNumericBig(d.Big, d.Scale)
		}
		return formatNumeric(d.Int, d.Scale)
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
	if d.Big != nil {
		return formatNumericBig(d.Big, d.Scale)
	}
	return formatNumeric(d.Int, d.Scale)
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
// (no pointer-bearing fields beyond Buf which is treated as
// read-only by convention), so a `copy` is sufficient — no
// deep-copy of nested data needed.
//
// M0068-0004: backing slice acquired from `rowPool` when width
// fits the pool, else falls back to `make`.
func cloneRow(src Row) Row {
	if src == nil {
		return nil
	}
	dst := acquireRow(len(src))
	copy(dst, src)
	return dst
}
