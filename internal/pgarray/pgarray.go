// Package pgarray holds the element-type table and the blob→text renderer for
// goopg's PG-native ArrayType varlena, shared by the two decoders that must
// agree on it byte for byte:
//
//   - the heap codec (internal/executor/codec_array.go), which renders an array
//     column for a local SELECT, and
//   - the logical-replication decoder (internal/wal/pgoutput.go), which renders
//     the SAME on-disk bytes for a subscriber's apply worker.
//
// internal/wal cannot import internal/executor, so before this package the
// pgoutput decoder simply had no array support at all: it switched on
// catalog.Type.Name alone, ignoring IsArray, and therefore read a `uuid[]`
// column's ArrayType blob as a bare 16-byte pg_uuid_t — shipping garbage AND
// mis-advancing the offset of every following column in the tuple. Rather than
// re-port the renderer (a second sibling to drift, cf. the project's
// "sibling paths must change together" rule), the renderer lives here once and
// both callers strip their own varlena header and call RenderText. Same
// motivation as executor.formatInterval delegating to pgdatetime.FormatInterval.
// M0119-0006.
package pgarray

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/pgdatetime"
	"github.com/goopg/goopg/internal/pgnodes"
)

// OutputStyle carries the three session GUCs that upstream's array element
// output functions read. array_out (postgres/src/backend/utils/adt/
// arrayfuncs.c) does not format anything itself: it calls the ELEMENT type's
// own output function per element, so `timestamptz_out` inside an array honours
// `TimeZone` and `DateStyle` exactly as it does for a scalar column, and
// `date_out`/`timestamp_out` honour `DateStyle`. Verified against PG 18.3:
//
//	SET TimeZone='America/Los_Angeles'; SET DateStyle='Postgres, DMY';
//	SELECT ARRAY['2020-06-15 10:00:00+00']::timestamptz[];
//	  → {"Mon 15 Jun 03:00:00 2020 PDT"}
//
// goopg renders an array to text at HEAP-DECODE time rather than at output
// time, and most decode sites (catalog reload, VACUUM, ANALYZE, DDL rescans)
// have no session at all — hence an explicit parameter with a documented
// default rather than an ambient lookup. The scan operators that DO hold a
// *executor.Context pass the session's values; everything else passes
// DefaultOutputStyle. M0119-0006.
type OutputStyle struct {
	// Style and Order are the pair config.ParseDateStyleValue returns
	// ("ISO"/"MDY", "German"/"DMY", …).
	Style, Order string
	// Zone is the raw `TimeZone` GUC spelling; "" means the boot default, UTC
	// (config.FormatTimestampTZ resolves it).
	Zone string
}

// DefaultOutputStyle is the boot-default rendering — ISO/MDY dates in UTC —
// used by every decode site that has no session to read GUCs from. It is what
// this package did unconditionally before OutputStyle existed, so a caller that
// passes it gets byte-identical output to the pre-M0119-0006-42nd-slice code.
func DefaultOutputStyle() OutputStyle { return OutputStyle{Style: "ISO", Order: "MDY"} }

// pgEpochUnixSec is 2000-01-01T00:00:00Z as a Unix timestamp — the origin both
// the `date` (days) and `timestamp`/`timestamptz` (microseconds) images count
// from.
const pgEpochUnixSec = 946684800

// FormatDateElem renders a `date` array element's stored day count the way
// date_out does under the session DateStyle. The ±infinity sentinels are
// DateStyle-independent, matching upstream's EncodeSpecialDate.
//
// The day count is applied with AddDate, not a Duration: `date` spans
// 4713 BC .. 5874897 AD and that many days does not fit a time.Duration's
// nanosecond int64.
func FormatDateElem(days int32, st OutputStyle) string {
	switch days {
	case math.MaxInt32:
		return "infinity"
	case math.MinInt32:
		return "-infinity"
	}
	t := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(days))
	return config.FormatDate(t, st.Style, st.Order)
}

// FormatTimestampElem renders a `timestamp` array element under the session
// DateStyle (timestamp_out; print_tz=false — a zone-less timestamp never grows
// an offset, whatever TimeZone says).
func FormatTimestampElem(micros int64, st OutputStyle) string {
	switch micros {
	case math.MaxInt64:
		return "infinity"
	case math.MinInt64:
		return "-infinity"
	}
	return config.FormatTimestamp(timestampMicrosToTime(micros), st.Style, st.Order)
}

// FormatTimestampTZElem renders a `timestamptz` array element under BOTH the
// session TimeZone and DateStyle (timestamptz_out). This is the arm the
// 2026-08-12 deferral row was filed against: it used to render in UTC
// unconditionally, so any session with another zone read a correct instant back
// under the wrong offset.
func FormatTimestampTZElem(micros int64, st OutputStyle) string {
	switch micros {
	case math.MaxInt64:
		return "infinity"
	case math.MinInt64:
		return "-infinity"
	}
	return config.FormatTimestampTZ(timestampMicrosToTime(micros), st.Style, st.Order, st.Zone)
}

// timestampMicrosToTime converts the stored microseconds-since-2000 image to an
// absolute instant. Seconds and nanoseconds are split rather than multiplied
// into a Duration for the same reason FormatDateElem uses AddDate: the
// timestamp range reaches 294276 AD, whose nanosecond count overflows int64.
func timestampMicrosToTime(micros int64) time.Time {
	sec := micros / 1_000_000
	rem := micros % 1_000_000
	if rem < 0 {
		rem += 1_000_000
		sec--
	}
	return time.Unix(pgEpochUnixSec+sec, rem*1000).UTC()
}

// ElemTypeInfo returns the element pg_type OID, fixed byte width (-1 for
// varlena), and storage alignment for an array element type name. ok is false
// for an unsupported element type (the caller then falls back to text
// elements). Note goopg spells an array column as
// catalog.Type{Name:<ELEMENT type>, IsArray:true}, so elemName here is the
// element name — never "int4[]" and never "_int4".
func ElemTypeInfo(elemName string) (oid uint32, size, align int, varlena, ok bool) {
	switch strings.ToLower(elemName) {
	case "int2", "smallint":
		return 21, 2, 2, false, true
	case "int4", "integer", "int", "serial", "serial4":
		return 23, 4, 4, false, true
	case "int8", "bigint", "bigserial", "serial8":
		return 20, 8, 8, false, true
	case "oid":
		return 26, 4, 4, false, true
	case "float4", "real":
		return 700, 4, 4, false, true
	case "float8", "double precision", "double", "float":
		return 701, 8, 8, false, true
	case "bool", "boolean":
		return 16, 1, 1, false, true
	case "text":
		return 25, -1, 4, true, true
	case "varchar", "character varying":
		return 1043, -1, 4, true, true
	case "bpchar", "char", "character":
		return 1042, -1, 4, true, true
	// M0119-0006 array-element slice. The three types whose SCALAR storage was
	// flipped to their PG physical image (interval → uuid → numeric) had left
	// their array elements behind on the text path: this function had no arm
	// for any of them, so `interval[]`/`uuid[]`/`numeric[]` all fell to the
	// unknown-element fallback and were written as an ArrayType whose elemtype
	// said text (25) while pg_attribute.atttypid said _interval / _uuid /
	// _numeric. The sizes here are verified against PG 18.3: pg_column_size of
	// a 2-element array is 56 / 56 / 44 for {'1 mon','2 hours'} / two uuids /
	// {'1.50','-2500'}, which is exactly 24-byte header + two 16-byte fields at
	// align 8, + two 16-byte fields at align 1, and + a 10-byte varlena padded
	// to 12 plus an 8-byte varlena at align 4.
	case "uuid":
		return 2950, 16, 1, false, true // pg_type 2950: typlen 16, typalign 'c'
	case "interval":
		return 1186, 16, 8, false, true // pg_type 1186: typlen 16, typalign 'd'
	case "numeric", "decimal":
		return 1700, -1, 4, true, true // pg_type 1700: varlena, typalign 'i'
	// M0119-0006 array-element slice, part 2 — the date-time types and bytea.
	// Same defect as the interval/uuid/numeric arms above, one type family
	// later: with no arm here a `date[]`/`time[]`/`timestamp[]`/`timestamptz[]`/
	// `timetz[]`/`bytea[]` column fell to the unknown-element fallback and was
	// written as an ArrayType whose elemtype field says 25 (text) over element
	// TEXT bodies, while pg_attribute.atttypid for the same column says
	// _date/_time/_timestamp/_timestamptz/_timetz/_bytea. goopg read its own
	// text back correctly, so nothing inside the engine was wrong — the blob and
	// the catalog simply disagreed, which is exactly what a descriptor-trusting
	// reader (a PG 18.3 standby, pg_amcheck's heap tier) deforms wrongly.
	// Widths/alignments are pg_type's own (typlen/typalign for OIDs 1082/1083/
	// 1114/1184/1266/17), cross-checked against PG 18.3 pg_column_size for
	// 2-element arrays: 32 = 24 + 4 + 4 (date), 40 = 24 + 8 + 8 (time,
	// timestamp, timestamptz), 56 = MAXALIGN(24 + 12 + pad4 + 12) (timetz), and bytea
	// elements carrying the same 4-byte varlena header at align 4 that the text
	// elements use (a 3×1-byte bytea array is 48 = 24 + 3×8).
	case "date":
		return 1082, 4, 4, false, true
	case "time":
		return 1083, 8, 8, false, true
	case "timestamp":
		return 1114, 8, 8, false, true
	case "timestamptz":
		return 1184, 8, 8, false, true
	case "timetz":
		return 1266, 12, 8, false, true
	case "bytea":
		return 17, -1, 4, true, true
	default:
		return 0, 0, 0, false, false
	}
}

// HeaderSize is the byte length of the ArrayType header goopg writes:
//
//	[0:4]   varlena header = total_size << 2 (uncompressed, LE)
//	[4:8]   ndim           = 1
//	[8:12]  dataoffset     = 0 (no null bitmap)
//	[12:16] elemtype       = element pg_type OID
//	[16:20] dims[0]        = element count
//	[20:24] lbound[0]      = 1
const HeaderSize = 24

// RenderText renders the canonical PG array text "{e1,e2,...}" for the payload
// of an ArrayType varlena — i.e. the bytes AFTER the 4-byte varlena header, so
// payload starts at ndim. elemName is the catalog element type name.
func RenderText(elemName string, payload []byte) (string, error) {
	return RenderTextStyled(elemName, payload, DefaultOutputStyle())
}

// RenderTextStyled is RenderText with the session's DateStyle/TimeZone, which
// upstream's array_out reaches through the element output functions it calls.
func RenderTextStyled(elemName string, payload []byte, st OutputStyle) (string, error) {
	if len(payload) < 20 {
		// Empty / 0-dim array (ndim==0) → "{}".
		return "{}", nil
	}
	if ndim := int32(binary.LittleEndian.Uint32(payload[0:4])); ndim == 0 {
		return "{}", nil
	}
	n := int(binary.LittleEndian.Uint32(payload[12:16]))
	if n <= 0 {
		return "{}", nil
	}
	wantOID, size, align, varlena, ok := ElemTypeInfo(elemName)
	if !ok {
		size, align, varlena = -1, 4, true
	}
	// Legacy-blob compatibility for the interval/uuid/numeric element flip.
	// Before those arms existed, ElemTypeInfo returned !ok for all three and the
	// encoder wrote the unknown-element fallback: elemtype 25 (text) with
	// 4-byte-varlena text bodies at align 4. The flip has no on-disk migration,
	// so such blobs are still out there (every cluster predating it). The
	// discrimination is exact rather than heuristic — the blob states its own
	// element type in the header, and the ONLY way an interval/uuid/numeric
	// column can carry elemtype 25 is that it was written by the pre-flip
	// encoder — so an old array decodes on the text path it was written on.
	if ok {
		if got := binary.LittleEndian.Uint32(payload[8:12]); got != wantOID && got == 25 {
			elemName, size, align, varlena = "text", -1, 4, true
		}
	}
	elemData := payload[20:]
	var sb strings.Builder
	sb.WriteByte('{')
	off := 0
	for i := 0; i < n; i++ {
		off += alignPad(off, align)
		s, adv, err := DecodeElemStyled(elemName, elemData[min(off, len(elemData)):], varlena, size, st)
		if err != nil {
			return "", err
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(s)
		off += adv
	}
	sb.WriteByte('}')
	return sb.String(), nil
}

// DecodeElem renders one in-array element and reports how many bytes it spans.
func DecodeElem(elemName string, data []byte, varlena bool, size int) (string, int, error) {
	return DecodeElemStyled(elemName, data, varlena, size, DefaultOutputStyle())
}

// DecodeElemStyled is DecodeElem under an explicit session output style; only
// the date/timestamp/timestamptz arms consult it (upstream's time_out and
// timetz_out are DateStyle-independent — confirmed against PG 18.3, where
// `SET DateStyle='German'` leaves a time[] element as 10:00:00).
func DecodeElemStyled(elemName string, data []byte, varlena bool, size int, st OutputStyle) (string, int, error) {
	lower := strings.ToLower(elemName)
	if varlena {
		if len(data) < 4 {
			return "", 0, fmt.Errorf("truncated array text element")
		}
		sz := int(binary.LittleEndian.Uint32(data[0:4]) >> 2)
		if sz < 4 || sz > len(data) {
			return "", 0, fmt.Errorf("truncated array text element body")
		}
		if lower == "numeric" || lower == "decimal" {
			// Sibling of the numeric arm in encodeArrayElem. As on the scalar
			// side, NumericTextFromStoredPayload also accepts the pre-flip
			// decimal string, so a numeric element survives even in a blob that
			// (wrongly) claims elemtype 1700 over a text body.
			text, err := pgnodes.NumericTextFromStoredPayload(data[4:sz])
			if err != nil {
				return "", 0, err
			}
			return text, sz, nil
		}
		if lower == "bytea" {
			// Sibling of encodeArrayElem's "bytea" arm: the body is the RAW
			// bytes, so it renders through byteaout's hex form rather than being
			// treated as text (which would emit the raw bytes and could not even
			// be quoted safely). The backslash makes array_out quote it, so the
			// element comes back as PG prints it: {"\\x0102"}.
			return QuoteTextElem(ByteaOutHex(data[4:sz])), sz, nil
		}
		return QuoteTextElem(string(data[4:sz])), sz, nil
	}
	if len(data) < size {
		return "", 0, fmt.Errorf("truncated fixed array element")
	}
	switch lower {
	case "uuid":
		// Sibling of encodeArrayElem's "uuid" arm (uuid_out's port). No array
		// quoting: a canonical uuid rendering is hex and dashes only.
		return uuidCanonical(data[:16]), 16, nil
	case "interval":
		// Sibling of encodeArrayElem's "interval" arm. Unlike the pre-flip text
		// path this renders interval_out's spelling ('2 hours' comes back as
		// 02:00:00, matching PG 18.3's ARRAY['1 mon','30 days','2 hours']
		// ::interval[] = {"1 mon","30 days",02:00:00}) — which is also why the
		// result must go through array_out's quoting: '1 mon' contains a space.
		micros := int64(binary.LittleEndian.Uint64(data[0:8]))
		days := int32(binary.LittleEndian.Uint32(data[8:12]))
		months := int32(binary.LittleEndian.Uint32(data[12:16]))
		return QuoteTextElem(pgdatetime.FormatInterval(months, days, micros)), 16, nil
	case "int2", "smallint":
		return strconv.FormatInt(int64(int16(binary.LittleEndian.Uint16(data[:2]))), 10), 2, nil
	case "int4", "integer", "int", "serial", "serial4":
		return strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(data[:4]))), 10), 4, nil
	case "int8", "bigint", "bigserial", "serial8":
		return strconv.FormatInt(int64(binary.LittleEndian.Uint64(data[:8])), 10), 8, nil
	case "oid":
		return strconv.FormatUint(uint64(binary.LittleEndian.Uint32(data[:4])), 10), 4, nil
	case "float4", "real":
		return catalog.PGFloatOut(float64(math.Float32frombits(binary.LittleEndian.Uint32(data[:4]))), 32), 4, nil
	case "float8", "double precision", "double", "float":
		return catalog.PGFloatOut(math.Float64frombits(binary.LittleEndian.Uint64(data[:8])), 64), 8, nil
	case "bool", "boolean":
		if data[0] != 0 {
			return "t", 1, nil
		}
		return "f", 1, nil
	// Siblings of encodeArrayElem's date-time arms (M0119-0006). Each renders
	// the same integer image the SCALAR column stores, through the shared
	// pgdatetime renderers, so the element text is upstream's type output
	// function verbatim; QuoteTextElem then applies array_out's quoting, which
	// is not optional for timestamp/timestamptz (their text contains a space).
	case "date":
		return QuoteTextElem(FormatDateElem(int32(binary.LittleEndian.Uint32(data[:4])), st)), 4, nil
	case "time":
		return QuoteTextElem(pgdatetime.FormatTime(int64(binary.LittleEndian.Uint64(data[:8])))), 8, nil
	case "timestamp":
		return QuoteTextElem(FormatTimestampElem(int64(binary.LittleEndian.Uint64(data[:8])), st)), 8, nil
	case "timestamptz":
		return QuoteTextElem(FormatTimestampTZElem(int64(binary.LittleEndian.Uint64(data[:8])), st)), 8, nil
	case "timetz":
		micros := int64(binary.LittleEndian.Uint64(data[:8]))
		zone := int32(binary.LittleEndian.Uint32(data[8:12]))
		return QuoteTextElem(pgdatetime.FormatTimeTZ(micros, zone)), 12, nil
	default:
		return "", 0, fmt.Errorf("unsupported fixed array element type %q", elemName)
	}
}

// ByteaOutHex is byteaout under the default `bytea_output = hex` GUC: the two
// literal characters `\x` followed by lowercase hex. It lives in this leaf
// package for the same reason the element table does — the array element
// decoder here and the executor's scalar bytea renderer must produce the same
// text, and internal/wal's pgoutput (which reads array elements too) cannot
// import the executor. executor.byteaOutHex delegates here. M0119-0006.
func ByteaOutHex(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 2+len(b)*2)
	out = append(out, '\\', 'x')
	for _, c := range b {
		out = append(out, hexdigits[c>>4], hexdigits[c&0x0f])
	}
	return string(out)
}

// QuoteTextElem applies PG array-output quoting: an element is double-quoted
// when empty, equal to the literal NULL, or containing a character that would
// otherwise be ambiguous ({ } , " \ or whitespace).
func QuoteTextElem(s string) string {
	if s == "" || strings.EqualFold(s, "null") || strings.ContainsAny(s, "{},\"\\ \t\n\r") {
		var sb strings.Builder
		sb.WriteByte('"')
		for i := 0; i < len(s); i++ {
			if s[i] == '"' || s[i] == '\\' {
				sb.WriteByte('\\')
			}
			sb.WriteByte(s[i])
		}
		sb.WriteByte('"')
		return sb.String()
	}
	return s
}

func alignPad(off, align int) int {
	if align <= 1 {
		return 0
	}
	mask := align - 1
	return ((off + mask) &^ mask) - off
}

// uuidCanonical is the port of PG's uuid_out (uuid.c): 32 lowercase hex digits
// with hyphens after bytes 4, 6, 8 and 10.
func uuidCanonical(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i := 0; i < 16; i++ {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexdigits[b[i]>>4], hexdigits[b[i]&0x0f])
	}
	return string(out)
}
