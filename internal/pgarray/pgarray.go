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

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/pgdatetime"
	"github.com/goopg/goopg/internal/pgnodes"
)

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
		s, adv, err := DecodeElem(elemName, elemData[min(off, len(elemData)):], varlena, size)
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
	default:
		return "", 0, fmt.Errorf("unsupported fixed array element type %q", elemName)
	}
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
