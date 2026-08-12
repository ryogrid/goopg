// Package executor runs goopg plan trees with a Volcano-style
// Open/Next/Close iterator model. Scope and growth path are
// documented in docs/design/0012-executor.md.
package executor

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"time"
	"unsafe"

	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/pgdatetime"
)

// DatumKind discriminates the value carrier in a Datum.
type DatumKind uint8

const (
	KindNull DatumKind = iota
	KindBool
	KindInt
	KindString
	KindBytes
	KindTime
	// KindInterval is goopg's interval-arithmetic carrier,
	// mirroring upstream's months-days-microseconds shape. Months
	// and days pack into the int64 inline payload (months in the
	// high half, days in the low half) — see
	// (Datum).IntervalMonthsValue / IntervalDaysValue. The sub-day
	// microsecond component rides in the reserved Hi word
	// (IntervalMicrosValue); it is populated by timestamp −
	// timestamp subtraction and zero for month/day-only literals.
	// Sub-day interval *literal* syntax is still parser-deferred;
	// see docs/design/0003-0006-date-interval-arithmetic.md.
	KindInterval
	// KindNumeric is goopg's v0 NUMERIC/DECIMAL carrier. v0
	// stores the value as `mantissa * 10^-scale` where mantissa
	// is an int64 (Datum.Int) and scale is the number of
	// digits to the right of the decimal point (Datum.Scale).
	// Arithmetic uses int64 math after aligning scales —
	// sufficient for TPC-H SF1 magnitudes (~10^17 worst-case
	// accumulator) but bounded by int64 overflow; the rare
	// big-number lane spills to flagBigNumeric in mctx
	// (e.g. Q8's `1.00000000000000000000`). See
	// docs/design/0003-0012-numeric-arithmetic.md and
	// docs/design/0068-0001-datum-compact-layout.md.
	// M0107-0002: Big *big.Int removed; big mantissa stored in mctx.
	KindNumeric
	// KindToastPointer is an unresolved TOAST reference (M0046-0006).
	// Datum.Buf carries the 12-byte on-disk TOAST pointer:
	// [toast_oid(4)|total_len(4)|num_chunks(4)]. The value must be
	// detoasted (via DetoastRow) before it is used in expressions.
	// Scan operators detoast automatically; callers that operate on
	// raw decoded rows (e.g. UPDATE predicates) receive already-detoasted
	// datums from the underlying scan.
	KindToastPointer
	// KindEnum stores an enum value for correct ORDER BY semantics.
	// Datum.Int = math.Float64bits(sortOrder) for comparison.
	// Datum.Buf = []byte(label) for display.
	// compareDatum compares by sort order; Format/StringValue return the label.
	KindEnum

	// datumKindCount is one past the last kind. It exists so
	// TestSpillDatumRoundTripCoversEveryKind can assert that the spill codec
	// has an arm for EVERY declared kind rather than for the ones someone
	// happened to think of. M0127-P5.9-u added it after finding that KindEnum
	// and KindToastPointer had no arm at all — encodeDatum wrote a bare kind
	// byte and decodeDatum then rejected the frame with "unknown datum kind",
	// so a query that had to spill an enum column simply failed.
	datumKindCount
)

// Datum flag bits (Datum.Flags).
const (
	// flagBigNumeric indicates KindNumeric uses the mctx big-mantissa
	// path: ArenaID + Int=(offset<<32|length) encode the sign+BE-bytes
	// representation. When clear, Int holds the int64 fast-path mantissa.
	flagBigNumeric uint8 = 1 << 0

	// (bit 1 was flagDate until M0127-P5.9-u; it is now Datum.TimeSub, a
	// structural field rather than a decoration on Flags. Do not reuse the bit
	// without reading the TimeSubtype comment below.)
)

// TimeSubtype names which SQL type a KindTime Datum actually is.
//
// M0127-P5.9-u. goopg carries five SQL types — timestamp, date, timestamptz,
// time and timetz — on the single KindTime carrier, so the carrier alone does
// not say what a value IS. Upstream has no equivalent problem: a PG Datum
// travels with the TupleDesc that names its type OID, and `date`, `timestamp`
// and `timetz` are separate types with separate typoutput functions
// (postgres/src/backend/utils/adt/date.c, timestamp.c).
//
// This used to be one bit on Datum.Flags (`flagDate`), which made the
// distinction look optional — an adornment a serializer could forget. It could,
// and did: `encodeDatum` (spill.go) wrote a KindTime datum's value and never
// its flags, so every DATE that passed through a hash-join batch spill came
// back a bare timestamp. TPC-DS Q72's `d3.d_date > d1.d_date + 5` then failed
// with "operator + requires integer operands", while the same query at
// work_mem='2GB' answered correctly. It survived unmeasured for as long as it
// did because a spilled date still COMPARES correctly — only the type is
// forgotten — so a round-trip test that checked values stayed green.
//
// Making it a field is what lets the round-trip contract be stated
// exhaustively: TestSpillDatumRoundTripCoversEveryKind walks every DatumKind
// and every TimeSubtype, so a subtype added here without a matching arm in the
// spill codec fails a test instead of silently degrading a value.
//
// NOTE (deferred, see the ledger row for M0127-P5.9-u): TimeSubTime is declared
// but not yet populated by its producers — nothing today branches on it, and the
// time-of-day distinction that DOES have behaviour (timetz's UTC-normalised
// comparison) is carried by TimeSubTimeTZ. It is declared now so the round-trip
// guard covers it the day a producer starts setting it.
//
// TimeSubTimestampTZ WAS in that same not-yet-populated state until M0119-0006's
// 40th slice, which gave it producers (NewTimestampTZDatum) so that the
// type-agnostic renderers can run timestamptz_out instead of timestamp_out.
type TimeSubtype uint8

const (
	// TimeSubTimestamp is the zero value: a bare `timestamp without time
	// zone`. Datum.Format() renders it "2006-01-02 15:04:05.000000".
	TimeSubTimestamp TimeSubtype = iota
	// TimeSubDate marks the datum as a DATE. Format() renders it in
	// PostgreSQL's Postgres,MDY output style ("MM-DD-YYYY"), matching the
	// pg_regress default DateStyle (M0097-0063), and `date + integer`
	// (expr.go, upstream date_pli) dispatches on it.
	TimeSubDate
	// TimeSubTimestampTZ marks `timestamp with time zone`. Not yet populated.
	TimeSubTimestampTZ
	// TimeSubTime marks `time without time zone`. Not yet populated.
	TimeSubTime
	// TimeSubTimeTZ marks `time with time zone`; the offset east of UTC rides
	// in Datum.Scale as minutes (see NewTimeTZDatum).
	TimeSubTimeTZ

	// timeSubtypeCount is one past the last subtype. It exists so the spill
	// round-trip guard can assert it has an arm for every declared subtype;
	// adding a subtype without extending that test's table is a test failure.
	timeSubtypeCount
)

// Datum is one column value flowing through the operator tree.
//
// M0107-0002 layout: 48 B, ≤ 1 GC-traced field (Buf, nil for
// arena-backed and numeric datums). Reduces from 64 B / 3 GC pointers.
// Hot-path arena-backed KindString/KindBytes have Buf=nil → 0 GC
// pointers per Datum. See docs/design/perf-optimize/02-datum-pointer-free.md.
//
// Field semantics by Kind:
//
//	KindNull         — all zero.
//	KindBool         — Int = 1 for true, 0 for false.
//	KindInt          — Int = the integer value (signed int64 semantics).
//	KindString       — ArenaID=0: Buf = UTF-8 bytes (Buf-backed, cold path).
//	                   ArenaID≠0: Int=(offset<<32|length) into mctx (hot path).
//	KindBytes        — same encoding as KindString.
//	KindTime         — Int = Unix nanoseconds (UTC).
//	KindInterval     — Int = (int64(months)<<32)|(int64(days)&0xFFFFFFFF);
//	                   Hi = sub-day microseconds (0 for month/day-only).
//	KindNumeric      — Flags&flagBigNumeric=0: Int = int64 fast-path mantissa.
//	                   Flags&flagBigNumeric≠0: ArenaID+Int=(off<<32|len) encode
//	                   sign+BE-magnitude bytes in mctx. Scale = digits after '.'.
//	KindToastPointer — Buf = 12-byte on-disk TOAST pointer.
//
// Concurrency: Datum is a value type; copying it is a 48-byte memmove.
// Buf (when non-nil) is aliased on copy — treat as read-only.
type Datum struct {
	Kind    DatumKind      // 1B @ 0 (was int = 8B; M0107-0002)
	Flags   uint8          // 1B @ 1 (flagBigNumeric; reserved others)
	ArenaID mctx.ContextID // 2B @ 2 (replaces *mctx.Context; 0 = no mctx payload)
	Scale   int16          // 2B @ 4
	TimeSub TimeSubtype    // 1B @ 6 (KindTime only; else zero. M0127-P5.9-u —
	//                        carved out of the alignment pad, so the 48 B
	//                        layout below is unchanged.)
	_pad0 [1]byte // 1B @ 7 (alignment pad before Int)
	Int   int64   // 8B @ 8
	Buf     []byte         // 24B @ 16 (nil when arena-backed)
	Hi      uint64         // 8B @ 40 (KindInterval sub-day micros; else reserved/UUID-hi)
	// Total: 48 B. GC-traced fields: Buf (nil for arena-backed rows → 0 scans).
}

// M0107-0002: Datum is exactly 48 bytes.
const _ uintptr = 48 - unsafe.Sizeof(Datum{})

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
	if d.ArenaID != 0 {
		// mctx-backed (hot path from seqScan/indexScan decode).
		ctx := mctx.Lookup(d.ArenaID)
		if ctx == nil {
			return ""
		}
		buf := ctx.Bytes(uint32(d.Int>>32), uint32(d.Int&0xFFFFFFFF))
		if len(buf) == 0 {
			return ""
		}
		return unsafe.String(&buf[0], len(buf))
	}
	// Buf-backed (cold path: literal construction, catalog rows, etc.).
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
	if d.ArenaID != 0 {
		ctx := mctx.Lookup(d.ArenaID)
		if ctx == nil {
			return nil
		}
		return ctx.Bytes(uint32(d.Int>>32), uint32(d.Int&0xFFFFFFFF))
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

// IntervalMicrosValue extracts the sub-day (time) component of
// KindInterval, in microseconds, mirroring upstream's Interval.time
// field. Stored in the reserved Hi word (int64 semantics). Zero for
// intervals built from month/day-only literals, so existing callers
// that never populate it observe no change. Populated by
// timestamp − timestamp subtraction (see evalBinary in expr.go).
func (d Datum) IntervalMicrosValue() int64 {
	return int64(d.Hi)
}

// IsIntervalNoEnd / IsIntervalNoBegin report whether a KindInterval Datum
// carries PostgreSQL's +infinity / -infinity sentinel (INTERVAL_NOEND /
// INTERVAL_NOBEGIN, all three fields at their signed extreme;
// postgres/src/include/datatype/timestamp.h). IsIntervalNotFinite is the union.
// (unimplemented_feat #5(d-iv))
func (d Datum) IsIntervalNoEnd() bool {
	return d.Kind == KindInterval && d.IntervalMonthsValue() == math.MaxInt32 &&
		d.IntervalDaysValue() == math.MaxInt32 && d.IntervalMicrosValue() == math.MaxInt64
}

func (d Datum) IsIntervalNoBegin() bool {
	return d.Kind == KindInterval && d.IntervalMonthsValue() == math.MinInt32 &&
		d.IntervalDaysValue() == math.MinInt32 && d.IntervalMicrosValue() == math.MinInt64
}

func (d Datum) IsIntervalNotFinite() bool {
	return d.IsIntervalNoEnd() || d.IsIntervalNoBegin()
}

// NewIntervalInfinity constructs the +infinity (positive) or -infinity
// KindInterval sentinel Datum.
func NewIntervalInfinity(positive bool) Datum {
	if positive {
		return NewIntervalDatumFull(math.MaxInt32, math.MaxInt32, math.MaxInt64)
	}
	return NewIntervalDatumFull(math.MinInt32, math.MinInt32, math.MinInt64)
}

// IsTimestampPosInf / IsTimestampNegInf report whether a KindTime Datum
// carries PostgreSQL's +infinity / -infinity timestamp sentinel. Upstream
// (postgres/src/include/datatype/timestamp.h) uses TIMESTAMP_END/BEGIN =
// PG_INT64_MAX/MIN in its micros-since-2000 domain; goopg stores KindTime as
// Unix nanoseconds, so the same INT64 extremes are used as sentinels — a real
// timestamp saturates ~year 2262, far below MaxInt64 ns, so they never collide.
// IsTimestampNotFinite is the union. (unimplemented_feat #5(d-iv))
func (d Datum) IsTimestampPosInf() bool {
	return d.Kind == KindTime && d.Int == math.MaxInt64
}

func (d Datum) IsTimestampNegInf() bool {
	return d.Kind == KindTime && d.Int == math.MinInt64
}

func (d Datum) IsTimestampNotFinite() bool {
	return d.IsTimestampPosInf() || d.IsTimestampNegInf()
}

// NewTimestampInfinity constructs the +infinity (positive) or -infinity
// KindTime sentinel Datum. Rendered as 'infinity' / '-infinity' by Format /
// AppendValueText, which intercept the sentinel before the normal time shape.
func NewTimestampInfinity(positive bool) Datum {
	if positive {
		return Datum{Kind: KindTime, Int: math.MaxInt64}
	}
	return Datum{Kind: KindTime, Int: math.MinInt64}
}

// NewDateInfinity constructs the +infinity / -infinity DATE sentinel. The date
// type shares the KindTime INT64-extremes carrier with timestamp — Format /
// AppendValueText (both intercept Int==MaxInt64/MinInt64 before the TimeSubDate
// shape), IsTimestampNotFinite (Kind/Int only), and compareDatum (KindTime by
// Int) are all already sentinel-aware, so no per-domain internal value is
// needed. TimeSub is set to TimeSubDate to keep the datum tagged as a date
// (matching ordinary date datums). On the wire the sentinel serialises to PG's DATEVAL_NOEND /
// DATEVAL_NOBEGIN = PG_INT32_MAX / PG_INT32_MIN days (date_send,
// postgres/src/include/utils/date.h); see codec.go. (unimplemented_feat #5(d-iv))
func NewDateInfinity(positive bool) Datum {
	if positive {
		return Datum{Kind: KindTime, Int: math.MaxInt64, TimeSub: TimeSubDate}
	}
	return Datum{Kind: KindTime, Int: math.MinInt64, TimeSub: TimeSubDate}
}

// NumericMantissaValue is the int64 fast-path mantissa of KindNumeric
// (valid only when Big == nil).
func (d Datum) NumericMantissaValue() int64 { return d.Int }

// NumericBigValue is the big.Int overflow lane of KindNumeric, or
// nil when the int64 fast path applies.
func (d Datum) NumericBigValue() *big.Int {
	if d.Flags&flagBigNumeric == 0 {
		return nil
	}
	ctx := mctx.Lookup(d.ArenaID)
	if ctx == nil {
		return new(big.Int)
	}
	buf := ctx.Bytes(uint32(d.Int>>32), uint32(d.Int&0xFFFFFFFF))
	if len(buf) == 0 {
		return new(big.Int)
	}
	sign := buf[0] & 0x80
	bi := new(big.Int).SetBytes(buf[1:])
	if sign != 0 {
		bi.Neg(bi)
	}
	return bi
}

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
		Kind:    KindString,
		ArenaID: sctx.ID(),
		Int:     int64(offset)<<32 | int64(length)&0xFFFFFFFF,
	}
}

// newBytesArenaDatum constructs a KindBytesArena Datum.
func newBytesArenaDatum(sctx *mctx.Context, offset, length uint32) Datum {
	return Datum{
		Kind:    KindBytes,
		ArenaID: sctx.ID(),
		Int:     int64(offset)<<32 | int64(length)&0xFFFFFFFF,
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
	if d.ArenaID == 0 {
		return d
	}
	// Big-numeric (flagBigNumeric) is mctx-backed just like an arena
	// string, but on a different Kind. It must be detached at the same
	// retention boundary or a producer arena reset corrupts its
	// sign+BE-magnitude bytes at scale (the sibling-path gap that only
	// String/Bytes were covering; see rowHasArena/cloneRowOwned). Re-store
	// it in the permanent arena, which never resets.
	if d.Kind == KindNumeric && d.Flags&flagBigNumeric != 0 {
		return newBigNumericInCtx(mctx.Perm(), d.NumericBigValue(), d.Scale)
	}
	if d.Kind != KindString && d.Kind != KindBytes {
		return d
	}
	length := uint32(d.Int & 0xFFFFFFFF)
	if length == 0 {
		return Datum{Kind: d.Kind}
	}
	ctx := mctx.Lookup(d.ArenaID)
	if ctx == nil {
		return Datum{Kind: d.Kind}
	}
	offset := uint32(d.Int >> 32)
	src := ctx.Bytes(offset, length)
	buf := make([]byte, length)
	copy(buf, src)
	return Datum{Kind: d.Kind, Buf: buf}
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
		// Any arena-backed Datum needs materialising at the retention
		// boundary — String/Bytes AND big-numeric (flagBigNumeric) all set
		// ArenaID. Keying on ArenaID != 0 (rather than a Kind allow-list)
		// closes the sibling-path gap that silently corrupted big-numeric
		// build rows at scale.
		if d.ArenaID != 0 {
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
		// MaterializeArena detaches EVERY arena-backed kind (String/Bytes
		// and big-numeric) from a resettable producer arena. Previously
		// this inlined only the String/Bytes case, silently leaving
		// big-numeric build rows aliasing an arena that resets at scale.
		dst[i] = d.MaterializeArena()
	}
	return dst
}

// NewTimeDatum constructs a KindTime Datum from a time.Time.
// The timestamp is normalized to UTC and stored as Unix nanoseconds.
func NewTimeDatum(t time.Time) Datum {
	return Datum{Kind: KindTime, Int: t.UTC().UnixNano()}
}

// NewDateDatum constructs a KindTime Datum tagged as a DATE
// (TimeSub == TimeSubDate).
// Date and timestamp values share the KindTime carrier, so the subtype is what
// distinguishes them for type-agnostic rendering: Datum.Format() emits the
// date-only shape ("MM-DD-YYYY", M0097-0063) for a tagged datum and the full
// timestamp shape otherwise. Use this at every date-producing site (literals,
// casts, on-disk decode) so a date is indistinguishable regardless of origin.
func NewDateDatum(t time.Time) Datum {
	return Datum{Kind: KindTime, Int: t.UTC().UnixNano(), TimeSub: TimeSubDate}
}

// IsDate reports whether d is a KindTime Datum carrying a DATE. Prefer this to
// testing TimeSub directly: it also pins the Kind, so a stray non-time Datum
// whose TimeSub byte happens to be set cannot be mistaken for a date.
func (d Datum) IsDate() bool { return d.Kind == KindTime && d.TimeSub == TimeSubDate }

// NewTimestampTZDatum constructs a KindTime Datum tagged as a
// `timestamp with time zone` (TimeSub == TimeSubTimestampTZ). The instant is
// stored in UTC exactly as NewTimeDatum stores it — the subtype is a *display*
// discriminator, not a different carrier: timestamptz_out converts into the
// session TimeZone and prints the offset, timestamp_out does neither
// (postgres/src/backend/utils/adt/timestamp.c, EncodeDateTime's print_tz arg).
//
// M0119-0006 (40th slice): use this at every site that KNOWS the value's SQL
// type is timestamptz — the typed literal, the cast, the on-disk decode and the
// now()-family functions — so the type-agnostic renderers (CAST-to-text, FK
// violation DETAIL, string concat) can tell it apart from a plain timestamp.
// Sites that only have a bare instant with no declared type keep NewTimeDatum;
// tagging them from a guess is the exact mislabelling this discriminator exists
// to prevent.
func NewTimestampTZDatum(t time.Time) Datum {
	return Datum{Kind: KindTime, Int: t.UTC().UnixNano(), TimeSub: TimeSubTimestampTZ}
}

// IsTimestampTZ reports whether d is a KindTime Datum carrying a
// `timestamp with time zone`. Sibling of IsDate; pins the Kind for the same
// reason.
func (d Datum) IsTimestampTZ() bool {
	return d.Kind == KindTime && d.TimeSub == TimeSubTimestampTZ
}

// NewTimeTZDatum constructs a KindTime Datum for a timetz column.
// The local time is stored as nanoseconds since 1970-01-01 00:00:00 UTC.
// offsetSecs is the timezone offset east of UTC in seconds (e.g., PDT = -25200).
// The offset is stored in Datum.Scale as minutes (int16 range ≥ ±840 covers ±14h).
// Callers with sub-minute offsets (historical oddities) lose the remainder.
func NewTimeTZDatum(t time.Time, offsetSecs int) Datum {
	return Datum{
		Kind:    KindTime,
		Int:     t.UTC().UnixNano(),
		Scale:   int16(offsetSecs / 60),
		TimeSub: TimeSubTimeTZ,
	}
}

// TimeTZOffsetSecs returns the timezone offset in seconds east of UTC for a
// timetz Datum (stored in Scale as minutes). Returns 0 for plain time datums.
func (d Datum) TimeTZOffsetSecs() int {
	return int(d.Scale) * 60
}

// NewIntervalDatum constructs a KindInterval Datum from
// month/day components (no sub-day/time grain). Thin wrapper over
// NewIntervalDatumFull with micros=0 — the common literal path.
func NewIntervalDatum(months, days int32) Datum {
	return NewIntervalDatumFull(months, days, 0)
}

// NewIntervalDatumFull constructs a KindInterval Datum carrying the
// full month/day/microsecond triple. Months+days pack into Int
// exactly as before (high/low 32 bits); the sub-day microsecond
// component rides in the reserved Hi word — mirroring upstream's
// months/days/microseconds Interval representation
// (postgres/src/include/datatype/timestamp.h). Used by
// timestamp − timestamp subtraction, which yields an interval with a
// time component (see docs/design/0003-0006-date-interval-arithmetic.md).
func NewIntervalDatumFull(months, days int32, micros int64) Datum {
	return Datum{
		Kind: KindInterval,
		Int:  (int64(months) << 32) | (int64(days) & 0xFFFFFFFF),
		Hi:   uint64(micros),
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
func NewNumericBigDatum(bi *big.Int, scale int16) Datum {
	return newBigNumericInCtx(mctx.Perm(), bi, scale)
}

// newBigNumericInCtx stores bi's sign+BE-magnitude in ctx and returns
// a KindNumeric Datum with flagBigNumeric set. ctx must outlive the Datum.
func newBigNumericInCtx(ctx *mctx.Context, bi *big.Int, scale int16) Datum {
	mag := bi.Bytes() // big-endian magnitude; one alloc inside big.Int
	sign := byte(0)
	if bi.Sign() < 0 {
		sign = 0x80
	}
	buf := make([]byte, 1+len(mag))
	buf[0] = sign
	copy(buf[1:], mag)
	off, length := ctx.AllocBytes(buf)
	return Datum{
		Kind:    KindNumeric,
		Flags:   flagBigNumeric,
		ArenaID: ctx.ID(),
		Scale:   scale,
		Int:     int64(off)<<32 | int64(length)&0xFFFFFFFF,
	}
}

// NewToastPointerDatum constructs a KindToastPointer Datum.
func NewToastPointerDatum(p []byte) Datum {
	return Datum{Kind: KindToastPointer, Buf: p}
}

// ---------- formatting ----------

// Format renders the value the way text-mode wire protocol expects.
// Unqualified KindTime values default to PostgreSQL's timestamp text shape;
// time-only datums are formatted by typed callers such as casts and the wire
// protocol layer.
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
		if d.ArenaID != 0 {
			ctx := mctx.Lookup(d.ArenaID)
			if ctx != nil {
				return append(dst, ctx.Bytes(uint32(d.Int>>32), uint32(d.Int&0xFFFFFFFF))...)
			}
			return dst
		}
		return append(dst, d.Buf...)
	case KindBytes:
		if d.ArenaID != 0 {
			ctx := mctx.Lookup(d.ArenaID)
			if ctx != nil {
				return append(dst, ctx.Bytes(uint32(d.Int>>32), uint32(d.Int&0xFFFFFFFF))...)
			}
			return dst
		}
		return append(dst, d.Buf...)
	case KindTime:
		if d.Int == math.MaxInt64 {
			return append(dst, "infinity"...)
		}
		if d.Int == math.MinInt64 {
			return append(dst, "-infinity"...)
		}
		return d.TimeValue().AppendFormat(dst, "2006-01-02 15:04:05.000000")
	}
	// Fallback for KindInterval / KindNumeric / unknown kinds.
	return append(dst, d.Format()...)
}

func isTimeOnlyValue(t time.Time) bool {
	return (t.Year() == 1970 && t.Month() == time.January && t.Day() == 1) ||
		(t.Year() == 1970 && t.Month() == time.January && t.Day() == 2 &&
			t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0)
}

func appendTimeOnlyValueText(dst []byte, t time.Time) []byte {
	h, m, s := t.Hour(), t.Minute(), t.Second()
	ns := t.Nanosecond()
	if t.Year() == 1970 && t.Month() == time.January && t.Day() == 2 && h == 0 && m == 0 && s == 0 && ns == 0 {
		return append(dst, "24:00:00"...)
	}
	dst = append(dst, byte('0'+h/10), byte('0'+h%10), ':',
		byte('0'+m/10), byte('0'+m%10), ':',
		byte('0'+s/10), byte('0'+s%10))
	if ns == 0 {
		return dst
	}
	micro := ns / 1000
	var frac [6]byte
	for i := 5; i >= 0; i-- {
		frac[i] = byte('0' + micro%10)
		micro /= 10
	}
	end := len(frac)
	for end > 0 && frac[end-1] == '0' {
		end--
	}
	if end == 0 {
		return dst
	}
	dst = append(dst, '.')
	return append(dst, frac[:end]...)
}

// NewEnumDatum creates an enum datum with the given sort order and label.
// KindEnum stores sort order in Int (as float64 bits) and label in Buf.
// compareDatum uses sort order; Format/StringValue return the label.
func NewEnumDatum(sortOrder float64, label string) Datum {
	return Datum{Kind: KindEnum, Int: int64(math.Float64bits(sortOrder)), Buf: []byte(label)}
}

// EnumSortOrder extracts the sort order from a KindEnum datum.
func (d Datum) EnumSortOrder() float64 {
	return math.Float64frombits(uint64(d.Int))
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
	case KindString:
		return d.StringValue()
	case KindBytes:
		return string(d.BytesValue())
	case KindEnum:
		// Display the label (Buf), not the sort order (Int).
		return string(d.Buf)
	case KindTime:
		// ±infinity timestamp/date sentinel renders identically to upstream
		// EncodeSpecialTimestamp / EncodeSpecialDate regardless of TimeSub.
		if d.Int == math.MaxInt64 {
			return "infinity"
		}
		if d.Int == math.MinInt64 {
			return "-infinity"
		}
		if d.TimeSub == TimeSubDate {
			// DATE type: render as Postgres MDY style "MM-DD-YYYY". M0097-0063.
			return d.TimeValue().Format("01-02-2006")
		}
		return d.TimeValue().Format("2006-01-02 15:04:05.000000")
	case KindInterval:
		return formatInterval(d.IntervalMonthsValue(), d.IntervalDaysValue(), d.IntervalMicrosValue())
	case KindNumeric:
		if d.Flags&flagBigNumeric != 0 {
			return formatNumericBig(d.NumericBigValue(), d.Scale)
		}
		return formatNumeric(d.Int, d.Scale)
	}
	return fmt.Sprintf("?datum kind=%d?", d.Kind)
}

// formatInterval renders (months, days, micros) as PG's EncodeInterval does
// under the default 'postgres' IntervalStyle. The implementation moved to the
// leaf pgdatetime package when interval COLUMNS gained PG's native 16-byte
// on-disk layout: the logical-replication decoder in internal/wal decodes the
// same bytes and must render the same text, and it cannot import the executor.
// See pgdatetime.FormatInterval for the full behavioural contract.
func formatInterval(months, days int32, micros int64) string {
	return pgdatetime.FormatInterval(months, days, micros)
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
	if d.Flags&flagBigNumeric != 0 {
		return formatNumericBig(d.NumericBigValue(), d.Scale)
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
