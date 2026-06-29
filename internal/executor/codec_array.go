package executor

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// User-table array columns (e.g. `p int4[]`) carry catalog.Type{Name:"int4",
// IsArray:true}: Name holds the ELEMENT type and IsArray marks the array-ness.
// Their runtime value is the array text literal "{1,2,3}" produced by the
// array_construct evaluator (expr.go) as a KindString datum.
//
// On disk an array is a PG-native ArrayType varlena blob so the heap layout,
// pg_attribute.atttypid (_int4 = OID 1007) and any external reader stay
// PG-faithful. The blob round-trips back to the same "{1,2,3}" text on read.
// M0118-0002 (predicate-gin enabler, design 0118-0138).
//
// Layout of a 1-D, no-NULL ArrayType (the only shape goopg writes today):
//
//	[0:4]   varlena header   = total_size << 2   (uncompressed, LE)
//	[4:8]   ndim             = 1
//	[8:12]  dataoffset       = 0   (no null bitmap)
//	[12:16] elemtype         = element pg_type OID
//	[16:20] dims[0]          = element count
//	[20:24] lbound[0]        = 1   (PG arrays are 1-based by default)
//	[24:]   element data, each at its typalign boundary (relative to offset 24,
//	        which is already MAXALIGN'd so 8-byte elements stay aligned)
const arrayHeaderSize = 24 // bytes 0..23: varlena + ndim + dataoffset + elemtype + dims[0] + lbound[0]

// arrayElemTypeInfo returns the element pg_type OID, fixed byte width (-1 for
// varlena), and storage alignment for an array element type name. ok is false
// for an unsupported element type.
func arrayElemTypeInfo(elemName string) (oid uint32, size, align int, varlena, ok bool) {
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
	default:
		return 0, 0, 0, false, false
	}
}

// encodeArrayValuePG encodes an array-typed datum (t.IsArray) into a PG-native
// ArrayType varlena blob. A pre-built blob (KindBytes) passes through verbatim
// — that is how catalog seeders inject ready-made arrays.
func encodeArrayValuePG(t catalog.Type, d Datum) ([]byte, error) {
	if d.Kind == KindBytes {
		return d.BytesValue(), nil
	}
	oid, _, align, varlena, ok := arrayElemTypeInfo(t.Name)
	if !ok {
		// Unknown element type: fall back to a text-element array so the value
		// still round-trips (the executor never type-checks element contents).
		oid, align, varlena = 25, 4, true
	}
	elems := parseTextArray(d.StringValue())
	if len(elems) == 0 {
		return emptyArrayTypeBytes(oid), nil
	}
	var body []byte
	for _, e := range elems {
		if e == "NULL" {
			return nil, &ExecError{Code: "0A000",
				Message: "NULL array elements are not supported"}
		}
		// Align this element relative to the body start (== absolute offset 24,
		// which is MAXALIGN'd, so body-relative alignment matches PG).
		if pad := alignPad(len(body), align); pad > 0 {
			body = append(body, make([]byte, pad)...)
		}
		eb, err := encodeArrayElem(t.Name, e, varlena)
		if err != nil {
			return nil, err
		}
		body = append(body, eb...)
	}
	total := arrayHeaderSize + len(body)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)                  // ndim
	binary.LittleEndian.PutUint32(buf[8:12], 0)                 // dataoffset (no nulls)
	binary.LittleEndian.PutUint32(buf[12:16], oid)              // elemtype
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(elems))) // dims[0]
	binary.LittleEndian.PutUint32(buf[20:24], 1)               // lbound[0]
	copy(buf[arrayHeaderSize:], body)
	return buf, nil
}

func alignPad(off, align int) int {
	if align <= 1 {
		return 0
	}
	mask := align - 1
	return ((off + mask) &^ mask) - off
}

// encodeArrayElem encodes one array element to its PG in-array representation.
// Fixed-length elements are stored raw (no per-element length header); varlena
// elements use a 4-byte varlena header (the form decodeTextArray expects).
func encodeArrayElem(elemName, e string, varlena bool) ([]byte, error) {
	if varlena {
		return array4ByteVarlena(e), nil
	}
	switch strings.ToLower(elemName) {
	case "int2", "smallint":
		v, err := strconv.ParseInt(strings.TrimSpace(e), 10, 64)
		if err != nil {
			return nil, &ExecError{Code: "22P02", Message: fmt.Sprintf("invalid input syntax for type smallint: %q", e)}
		}
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(int16(v)))
		return b[:], nil
	case "int4", "integer", "int", "serial", "serial4":
		v, err := strconv.ParseInt(strings.TrimSpace(e), 10, 64)
		if err != nil {
			return nil, &ExecError{Code: "22P02", Message: fmt.Sprintf("invalid input syntax for type integer: %q", e)}
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(int32(v)))
		return b[:], nil
	case "int8", "bigint", "bigserial", "serial8":
		v, err := strconv.ParseInt(strings.TrimSpace(e), 10, 64)
		if err != nil {
			return nil, &ExecError{Code: "22P02", Message: fmt.Sprintf("invalid input syntax for type bigint: %q", e)}
		}
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], uint64(v))
		return b[:], nil
	case "oid":
		v, err := strconv.ParseUint(strings.TrimSpace(e), 10, 64)
		if err != nil {
			return nil, &ExecError{Code: "22P02", Message: fmt.Sprintf("invalid input syntax for type oid: %q", e)}
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], uint32(v))
		return b[:], nil
	case "float4", "real":
		f, err := strconv.ParseFloat(strings.TrimSpace(e), 32)
		if err != nil {
			return nil, &ExecError{Code: "22P02", Message: fmt.Sprintf("invalid input syntax for type real: %q", e)}
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(float32(f)))
		return b[:], nil
	case "float8", "double precision", "double", "float":
		f, err := strconv.ParseFloat(strings.TrimSpace(e), 64)
		if err != nil {
			return nil, &ExecError{Code: "22P02", Message: fmt.Sprintf("invalid input syntax for type double precision: %q", e)}
		}
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(f))
		return b[:], nil
	case "bool", "boolean":
		switch strings.ToLower(strings.TrimSpace(e)) {
		case "t", "true", "1", "y", "yes", "on":
			return []byte{1}, nil
		default:
			return []byte{0}, nil
		}
	default:
		return array4ByteVarlena(e), nil
	}
}

// array4ByteVarlena serialises a text element with the always-4-byte varlena
// header used inside array data (PG never uses the 1-byte short header for
// elements stored with 'i' alignment in an array body).
func array4ByteVarlena(s string) []byte {
	total := len(s) + 4
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	copy(buf[4:], s)
	return buf
}

// decodeArrayValuePG inverts encodeArrayValuePG: it reads the ArrayType varlena
// at the head of data and renders the canonical PG array text "{e1,e2,...}",
// returning it as a KindString datum plus the number of bytes consumed.
func decodeArrayValuePG(t catalog.Type, data []byte) (Datum, int, error) {
	payload, consumed, err := decodePhysicalPGVarlena(data)
	if err != nil {
		return Datum{}, 0, err
	}
	// payload starts at ndim (the varlena header was stripped):
	//   [0:4] ndim [4:8] dataoffset [8:12] elemtype [12:16] dims[0] [16:20] lbound[0]
	if len(payload) < 20 {
		// Empty / 0-dim array (ndim==0) → "{}".
		return NewStringDatum("{}"), consumed, nil
	}
	ndim := int32(binary.LittleEndian.Uint32(payload[0:4]))
	if ndim == 0 {
		return NewStringDatum("{}"), consumed, nil
	}
	n := int(binary.LittleEndian.Uint32(payload[12:16]))
	if n <= 0 {
		return NewStringDatum("{}"), consumed, nil
	}
	_, size, align, varlena, ok := arrayElemTypeInfo(t.Name)
	if !ok {
		size, align, varlena = -1, 4, true
	}
	elemData := payload[20:]
	var sb strings.Builder
	sb.WriteByte('{')
	off := 0
	for i := 0; i < n; i++ {
		if pad := alignPad(off, align); pad > 0 {
			off += pad
		}
		s, adv, derr := decodeArrayElem(t.Name, elemData[min(off, len(elemData)):], varlena, size)
		if derr != nil {
			return Datum{}, 0, derr
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(s)
		off += adv
	}
	sb.WriteByte('}')
	return NewStringDatum(sb.String()), consumed, nil
}

func decodeArrayElem(elemName string, data []byte, varlena bool, size int) (string, int, error) {
	if varlena {
		if len(data) < 4 {
			return "", 0, fmt.Errorf("truncated array text element")
		}
		sz := int(binary.LittleEndian.Uint32(data[0:4]) >> 2)
		if sz < 4 || sz > len(data) {
			return "", 0, fmt.Errorf("truncated array text element body")
		}
		return quoteArrayTextElem(string(data[4:sz])), sz, nil
	}
	if len(data) < size {
		return "", 0, fmt.Errorf("truncated fixed array element")
	}
	switch strings.ToLower(elemName) {
	case "int2", "smallint":
		return strconv.FormatInt(int64(int16(binary.LittleEndian.Uint16(data[:2]))), 10), 2, nil
	case "int4", "integer", "int", "serial", "serial4":
		return strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(data[:4]))), 10), 4, nil
	case "int8", "bigint", "bigserial", "serial8":
		return strconv.FormatInt(int64(binary.LittleEndian.Uint64(data[:8])), 10), 8, nil
	case "oid":
		return strconv.FormatUint(uint64(binary.LittleEndian.Uint32(data[:4])), 10), 4, nil
	case "float4", "real":
		return PGFloatOut(float64(math.Float32frombits(binary.LittleEndian.Uint32(data[:4]))), 32), 4, nil
	case "float8", "double precision", "double", "float":
		return PGFloatOut(math.Float64frombits(binary.LittleEndian.Uint64(data[:8])), 64), 8, nil
	case "bool", "boolean":
		if data[0] != 0 {
			return "t", 1, nil
		}
		return "f", 1, nil
	default:
		return "", 0, fmt.Errorf("unsupported fixed array element type %q", elemName)
	}
}

// quoteArrayTextElem applies PG array-output quoting: an element is double-
// quoted when empty, equal to the literal NULL, or containing a character that
// would otherwise be ambiguous ({ } , " \ or whitespace).
func quoteArrayTextElem(s string) string {
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
