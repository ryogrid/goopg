package executor

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mctx"
	"github.com/goopg/goopg/internal/storage"
)

// EncodeRowPG encodes a row in PG-native physical tuple format
// (M0105-0010). Used for catalog pages that PG must read directly,
// such as pg_authid. The format mirrors PostgreSQL's heap tuple
// layout: aligned per-type values in little-endian.
//
// The returned bytes are the column-data area only — the null bitmap,
// when needed, must be computed separately via NullBitmapPG and stored
// in the tuple header via storage.NewHeapTupleWithNulls. PG stores the
// bitmap between the fixed header and the t_hoff-aligned data region,
// not inline with the data.
//
// Most goopg code should continue using EncodeRow (goopg-internal
// format) for backward compatibility.
func EncodeRowPG(cols []catalog.Column, row Row) ([]byte, error) {
	if len(cols) != len(row) {
		return nil, fmt.Errorf("EncodeRowPG: %d cols vs %d datums", len(cols), len(row))
	}
	return encodeRowPG(cols, row)
}

// NullBitmapPG returns the PG-convention null bitmap for the row, or
// nil when the row has no NULL columns. The convention matches PG's
// heap_fill_tuple: bit i is set when column i is NOT NULL, cleared
// when column i is NULL. Bit numbering is little-endian within each
// byte (bit 0 = first attribute in the byte). Callers stamp the result
// into the tuple header via storage.NewHeapTupleWithNulls and the
// resulting HEAP_HASNULL flag.
func NullBitmapPG(row Row) []byte {
	hasNull := false
	for _, d := range row {
		if d.IsNull() {
			hasNull = true
			break
		}
	}
	if !hasNull {
		return nil
	}
	bmLen := (len(row) + 7) / 8
	bm := make([]byte, bmLen)
	for i, d := range row {
		if !d.IsNull() {
			bm[i/8] |= 1 << (i % 8)
		}
	}
	return bm
}

// encodeRowPG encodes a row's column-data area in PG-native physical
// tuple format. NULL columns are skipped (they consume no data bytes
// in PG's heap tuple); see NullBitmapPG for the null bitmap that must
// accompany the result whenever the row contains NULL.
func encodeRowPG(cols []catalog.Column, row Row) ([]byte, error) {
	out := make([]byte, 0, 256)
	off := 0
	for i, c := range cols {
		d := row[i]
		if d.IsNull() {
			continue
		}
		if d.Kind == KindToastPointer {
			// Encode as PG external varlena reference: VARATT_IS_1B_E (0x01)
			// followed by a 12-byte pointer = 13 bytes total.
			// 0x01 is reserved in PG's varlena encoding as the 1-byte external
			// marker; it can never be a data varlena (0x01>>1=0 = zero-length).
			// This avoids the 0x1B collision where any 12-char string also
			// produces header (13<<1)|1 = 0x1B and was misidentified as TOAST.
			// Aligned to 4 bytes (PG TOAST pointers are int-aligned).
			off = alignPhysicalPGOffset(off, 4)
			ptr := d.BytesValue()
			buf := make([]byte, 13)
			buf[0] = 0x01 // VARATT_IS_1B_E: external varlena, unambiguous
			copy(buf[1:], ptr)
			for len(out) < off+13 {
				out = append(out, 0)
			}
			copy(out[off:off+13], buf)
			off += 13
			continue
		}
		align := physicalPGTypeAlign(c.Type)
		off = alignPhysicalPGOffset(off, align)
		buf, err := encodeValuePG(c.Type, d)
		if err != nil {
			return nil, err
		}
		for len(out) < off+len(buf) {
			out = append(out, 0)
		}
		copy(out[off:off+len(buf)], buf)
		off += len(buf)
	}
	return out, nil
}

func coerceTextLikeDatum(t catalog.Type, d Datum) (string, error) {
	var s string
	switch d.Kind {
	case KindString:
		s = d.StringValue()
	case KindBytes:
		s = string(d.BytesValue())
	case KindInt:
		s = fmt.Sprintf("%d", d.Int)
	case KindNumeric:
		s = numericText(d)
	case KindBool:
		if d.BoolValue() {
			s = "t"
		} else {
			s = "f"
		}
	case KindTime:
		// KindTime → text: use goopg's standard timestamp text format.
		// Covers DEFAULT(now()) and similar timestamp-valued expressions
		// being stored in a text column. M0097-0029.
		s = d.Format()
	case KindInterval:
		s = d.Format()
	default:
		return "", fmt.Errorf("kind %d cannot encode as %s", d.Kind, t.Name)
	}

	tname := strings.ToLower(t.Name)
	if tname == "varchar" || tname == "character varying" {
		if len(t.Args) > 0 {
			n := int(t.Args[0])
			stripped := strings.TrimRight(s, " ")
			if len(stripped) > n {
				return "", &ExecError{Code: "22001",
					Message: fmt.Sprintf("value too long for type character varying(%d)", n)}
			}
			s = stripped
		}
	} else if tname == "char" || tname == "bpchar" || tname == "character" {
		n := 1
		if len(t.Args) > 0 {
			n = int(t.Args[0])
		}
		stripped := strings.TrimRight(s, " ")
		if len(stripped) > n {
			return "", &ExecError{Code: "22001",
				Message: fmt.Sprintf("value too long for type character(%d)", n)}
		}
		s = stripped
	}
	return s, nil
}

// encodeValuePG encodes a single datum in PG-native format.
func encodeValuePG(t catalog.Type, d Datum) ([]byte, error) {
	switch strings.ToLower(t.Name) {
	case "bool", "boolean":
		if d.Kind != KindBool {
			return nil, fmt.Errorf("expected bool, got kind %d", d.Kind)
		}
		if d.BoolValue() {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case "int2", "smallint":
		var v int64
		switch d.Kind {
		case KindInt:
			v = d.Int
		case KindString:
			var err error
			v, err = coerceStringToInt64(d.StringValue(), "smallint")
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("expected int, got kind %d", d.Kind)
		}
		if v < -32768 || v > 32767 {
			// Mirror PostgreSQL int2in: report the offending input string
			// (e.g. INSERT INTO t(int2col) VALUES ('100000')). The bare
			// "smallint out of range" wording is reserved for arithmetic
			// overflow (int2pl/int2mul), which is raised in expr eval before
			// this storage-encode path. Sibling to the int4 arm below.
			return nil, &ExecError{Code: "22003",
				Message: fmt.Sprintf("value %q is out of range for type smallint", strings.TrimSpace(d.StringValue()))}
		}
		var buf [2]byte
		binary.LittleEndian.PutUint16(buf[:], uint16(int16(v)))
		return buf[:], nil
	case "int4", "integer", "int", "serial":
		var v int64
		switch d.Kind {
		case KindInt:
			v = d.Int
		case KindString:
			var err error
			v, err = coerceStringToInt64(d.StringValue(), "integer")
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("expected int, got kind %d", d.Kind)
		}
		if v < -2147483648 || v > 2147483647 {
			return nil, &ExecError{Code: "22003",
				Message: fmt.Sprintf("value %q is out of range for type integer", strings.TrimSpace(d.StringValue()))}
		}
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], uint32(int32(v)))
		return buf[:], nil
	case "int8", "bigint", "bigserial":
		var v int64
		switch d.Kind {
		case KindInt:
			v = d.Int
		case KindString:
			var err error
			v, err = coerceStringToInt64(d.StringValue(), "bigint")
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("expected int, got kind %d", d.Kind)
		}
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(v))
		return buf[:], nil
	case "pg_lsn":
		var u uint64
		switch d.Kind {
		case KindInt:
			u = uint64(d.Int)
		case KindString:
			var err error
			u, err = parsePgLSN(d.StringValue())
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("expected pg_lsn, got kind %d", d.Kind)
		}
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], u)
		return buf[:], nil
	case "oid", "regproc":
		var v int64
		switch d.Kind {
		case KindInt:
			v = d.Int
		case KindString:
			var err error
			v, err = coerceStringToInt64(d.StringValue(), "oid")
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("expected int, got kind %d", d.Kind)
		}
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], uint32(v))
		return buf[:], nil
	case "timestamp", "timestamptz":
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		t := d.TimeValue()
		// PG epoch: 2000-01-01 UTC, in microseconds
		// goopg stores UnixNano internally; we encode PG-compatible
		// microseconds since PG epoch.
		micros := t.UnixMicro() - pgEpochUnixMicros
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(micros))
		return buf[:], nil
	case "date":
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		// PG date: days since 2000-01-01 (Julian-style)
		t := d.TimeValue()
		micros := t.UnixMicro() - pgEpochUnixMicros
		days := int32(micros / (24 * 3600 * 1000000))
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], uint32(days))
		return buf[:], nil
	case "time":
		if d.Kind == KindString {
			ts, err := parseTimeString(d.StringValue())
			if err != nil {
				return nil, err
			}
			d = NewTimeDatum(ts)
		}
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(pgTimeMicros(d.TimeValue())))
		return buf[:], nil
	case "timetz":
		if d.Kind == KindString {
			ts, offsetSecs, err := parseTimeTZString(d.StringValue())
			if err != nil {
				return nil, err
			}
			d = NewTimeTZDatum(ts, offsetSecs)
		}
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		var buf [12]byte
		binary.LittleEndian.PutUint64(buf[:8], uint64(pgTimeMicros(d.TimeValue())))
		// PG wire stores timezone offset as int32 seconds, positive = west of UTC.
		// Our Scale stores minutes east of UTC; convert: pgOffset = -Scale*60.
		pgOffset := int32(-d.TimeTZOffsetSecs())
		binary.LittleEndian.PutUint32(buf[8:], uint32(pgOffset))
		return buf[:], nil
	case "name":
		// PG NameData: fixed 64 bytes, '\0'-padded. The name type silently
		// truncates input to NAMEDATALEN-1 = 63 bytes (one byte reserved for
		// the NUL terminator), matching PostgreSQL's namein() and the
		// storage-encode path below. Without the truncation a 64-char input
		// fills all 64 bytes with no terminator and decodes back as 64 chars,
		// widening name columns by one and breaking row counts (M0111-0007).
		var s string
		switch d.Kind {
		case KindString:
			s = d.StringValue()
		case KindBytes:
			s = string(d.BytesValue())
		case KindInt:
			s = fmt.Sprintf("%d", d.Int)
		default:
			s = d.StringValue()
		}
		if len(s) > 63 {
			s = s[:63]
		}
		buf := make([]byte, 64)
		copy(buf, s)
		return buf, nil
	case "char":
		// Without a length modifier, "char" is PG's internal single-byte
		// "char" type (OID 18, typalign='c', typlen=1).
		// With a length modifier (e.g. char(84) = character(84) = bpchar):
		// encode as a PG varlena, same as "character"/"bpchar".
		if len(t.Args) == 0 {
			// Single-byte internal "char" type.
			if d.Kind == KindString {
				s := d.StringValue()
				if len(s) > 0 {
					return []byte{s[0]}, nil
				}
				return []byte{0}, nil
			}
			if d.Kind == KindInt {
				return []byte{byte(d.Int)}, nil
			}
			return nil, fmt.Errorf("expected string or int for char, got kind %d", d.Kind)
		}
		// char(N) = character(N) = bpchar: PG varlena (same as "character").
		s, err := coerceTextLikeDatum(t, d)
		if err != nil {
			return nil, err
		}
		return varlenaTextBytes(s), nil
	case "float4", "real":
		// goopg stores float4 as varlena text for v0 compatibility (same as
		// goopg-format encodeValue).  PG binary float4 (4-byte IEEE 754 LE)
		// is deferred until the decode path also supports binary float.
		// M0111-0002.
		var s string
		switch d.Kind {
		case KindInt:
			s = strconv.FormatInt(d.Int, 10)
		case KindString:
			raw := strings.TrimSpace(d.StringValue())
			if _, err := strconv.ParseFloat(raw, 32); err != nil {
				return nil, &ExecError{Code: "22P02",
					Message: fmt.Sprintf("invalid input syntax for type real: %q", d.StringValue())}
			}
			s = raw
		case KindNumeric:
			s = numericText(d)
		default:
			return nil, fmt.Errorf("kind %d cannot encode as float4", d.Kind)
		}
		return varlenaTextBytes(s), nil
	case "float8", "double precision", "double":
		// goopg stores float8 as varlena text for v0 compatibility (same as
		// goopg-format encodeValue).  PG binary float8 (8-byte IEEE 754 LE)
		// is deferred until the decode path also supports binary float.
		// M0111-0002.
		var s string
		switch d.Kind {
		case KindInt:
			s = strconv.FormatInt(d.Int, 10)
		case KindString:
			raw := strings.TrimSpace(d.StringValue())
			if _, err := strconv.ParseFloat(raw, 64); err != nil {
				return nil, &ExecError{Code: "22P02",
					Message: fmt.Sprintf("invalid input syntax for type double precision: %q", d.StringValue())}
			}
			s = raw
		case KindNumeric:
			s = numericText(d)
		default:
			return nil, fmt.Errorf("kind %d cannot encode as float8", d.Kind)
		}
		return varlenaTextBytes(s), nil
	case "xid", "xid8":
		// PG TransactionId: 4-byte unsigned LE
		var v uint32
		switch d.Kind {
		case KindInt:
			v = uint32(d.Int)
		default:
			return nil, fmt.Errorf("expected int for xid, got kind %d", d.Kind)
		}
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], v)
		return buf[:], nil
	case "numeric", "decimal":
		// Store as PG varlena-text. coerceTextLikeDatum yields the decimal
		// string for KindNumeric/KindInt and passes a KindString through
		// verbatim (e.g. INSERT ... VALUES ('123.45') arriving as text) —
		// numericText alone reads only the Int/Scale fields and would emit
		// "0" for a KindString. M0111-0002 (restores the string→numeric path
		// the removed legacy encodeValue had).
		s, err := coerceTextLikeDatum(t, d)
		if err != nil {
			return nil, err
		}
		return varlenaTextBytes(s), nil
	case "aclitem[]", "_aclitem":
		// PG binary empty ArrayType, elemtype = aclitem (1033).
		// PG's deconstruct_array asserts on ARR_ELEMTYPE; a text varlena
		// "{}" is not a valid ArrayType*. Match construct_empty_array.
		if d.Kind == KindBytes {
			return d.BytesValue(), nil
		}
		return emptyArrayTypeBytes(1033), nil
	case "text[]", "_text":
		// PG binary empty ArrayType, elemtype = text (25). M0106-0010
		// Step 3dk lets callers (initdb pg_proc seed) pass a pre-built
		// ArrayType blob via KindBytes so non-empty proargnames/proconfig
		// arrays land on disk in PG-native form. Mirrors the oidvector
		// passthrough pattern already used by pg_proc.proargtypes.
		if d.Kind == KindBytes {
			return d.BytesValue(), nil
		}
		return emptyArrayTypeBytes(25), nil
	case "oid[]", "_oid":
		// Step 3dk: KindBytes passthrough for non-empty oid[] blobs
		// (e.g. pg_proc.proallargtypes on SRFs).
		if d.Kind == KindBytes {
			return d.BytesValue(), nil
		}
		return emptyArrayTypeBytes(26), nil
	case "int2[]", "_int2":
		if d.Kind == KindBytes {
			return d.BytesValue(), nil
		}
		return emptyArrayTypeBytes(21), nil
	case "float4[]", "_float4":
		if d.Kind == KindBytes {
			return d.BytesValue(), nil
		}
		return emptyArrayTypeBytes(700), nil
	case "anyarray":
		// anyarray can hold any element type. Callers pass a pre-built
		// ArrayType blob via KindBytes (e.g. pg_statistic.stavalues*).
		// NullDatum is written via the null bitmap by EncodeRowPG, so
		// we only reach here for non-null datums.
		if d.Kind == KindBytes {
			return d.BytesValue(), nil
		}
		// Fallback: empty anyarray with elemtype=text (safe placeholder).
		return emptyArrayTypeBytes(25), nil
	case "char[]", "_char":
		// PG binary empty ArrayType, elemtype = char (18).
		// Used for pg_proc.proargmodes when no per-arg modes are set.
		// Step 3dk: KindBytes passthrough for non-empty char[] blobs
		// (e.g. pg_proc.proargmodes on SRFs with OUT args).
		if d.Kind == KindBytes {
			return d.BytesValue(), nil
		}
		return emptyArrayTypeBytes(18), nil
	case "oidvector":
		// oidvector is a fixed-shape varlena ArrayType: 1-D, lbound=0,
		// elemtype=OID(26). The caller (initdb) pre-encodes the entire
		// blob via oidVectorBytes() and passes it through as KindBytes
		// so this codec only needs to splice the pre-built buffer.
		if d.Kind != KindBytes {
			return nil, fmt.Errorf("expected bytes for oidvector, got kind %d", d.Kind)
		}
		return d.BytesValue(), nil
	case "int2vector":
		// int2vector mirrors oidvector but with elemtype=INT2(21) and
		// 2-byte payload elements. The caller (initdb) pre-encodes the
		// blob via int2VectorBytes() and passes KindBytes through here.
		// Used by pg_index.indkey and pg_index.indoption.
		if d.Kind != KindBytes {
			return nil, fmt.Errorf("expected bytes for int2vector, got kind %d", d.Kind)
		}
		return d.BytesValue(), nil
	case "uuid":
		// Validate and normalize UUID to canonical lowercase-with-dashes format.
		// M0097-0029.
		if d.Kind != KindString {
			return nil, fmt.Errorf("expected string for uuid, got kind %d", d.Kind)
		}
		s := d.StringValue()
		if !isValidUUIDStr(s) {
			return nil, &ExecError{Code: "22P02",
				Message: fmt.Sprintf("invalid input syntax for type uuid: %q", s)}
		}
		return varlenaTextBytes(normalizeUUIDStr(s)), nil
	case "pg_node_tree":
		// KindBytes passthrough: pre-encoded varlena bytes (e.g. PGLZ-compressed
		// varlena produced by pglzVarlenaDatum in initdb bootstrap).
		if d.Kind == KindBytes {
			return d.BytesValue(), nil
		}
		// pg_node_tree is varlena-text; PG only reads it conditionally
		// (e.g. relpartbound when relispartition=true). Empty varlena.
		s := d.StringValue()
		return varlenaTextBytes(s), nil
	default:
		// text, varchar, char, bpchar, unknown, numeric, etc.
		// Use PG varlena format (LE): 1-byte header for short values,
		// 4-byte header for longer ones.
		s, err := coerceTextLikeDatum(t, d)
		if err != nil {
			return nil, err
		}
		return varlenaTextBytes(s), nil
	}
}

// emptyArrayTypeBytes returns the 16-byte PG-native serialization of
// `construct_empty_array(elemType)`: a 4-byte uncompressed varlena
// header containing total size 16, then ndim=0, dataoffset=0,
// elemtype=elemType. Required so that PG's deconstruct_array does not
// fail its ARR_ELEMTYPE assertion when reading nailed pg_class
// rows where relacl/reloptions are conceptually empty.
func emptyArrayTypeBytes(elemType uint32) []byte {
	var buf [16]byte
	// PG SET_VARSIZE (uncompressed, LE machine): low 2 bits = 00,
	// total size in upper 30 bits.
	binary.LittleEndian.PutUint32(buf[0:4], 16<<2)
	// ndim = 0, dataoffset = 0
	// (already zero-initialised)
	binary.LittleEndian.PutUint32(buf[12:16], elemType)
	return buf[:]
}

// varlenaTextBytes returns a PG-native varlena serialisation of the
// given text payload using the 1-byte header for short values and the
// 4-byte header otherwise. PG's SET_VARSIZE_1B encodes the TOTAL size
// (data+header), not just the data length; bit 0 = 1 marks the 1-byte
// form (and the body shifts by one).
func varlenaTextBytes(s string) []byte {
	total := len(s) + 1 // data + 1-byte header
	if total <= 127 {   // 1-byte header: 7 bits for size (max 127)
		buf := make([]byte, total)
		buf[0] = byte(total<<1) | 1
		copy(buf[1:], s)
		return buf
	}
	// 4-byte header (LE): low 2 bits = 00 (uncompressed), total size
	// in upper 30 bits.
	total = len(s) + 4
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	copy(buf[4:], s)
	return buf
}

// pgEpochUnixMicros is 2000-01-01 UTC in Unix microseconds (used by
// the PG timestamp encoding).
var pgEpochUnixMicros = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).UnixMicro()

func pgTimeMicros(t time.Time) int64 {
	u := t.UTC()
	return int64(u.Hour())*int64(time.Hour/time.Microsecond) +
		int64(u.Minute())*int64(time.Minute/time.Microsecond) +
		int64(u.Second())*int64(time.Second/time.Microsecond) +
		int64(u.Nanosecond()/1000)
}

func pgTimeFromMicros(micros int64) time.Time {
	return time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(micros) * time.Microsecond)
}

// DecodeRow inverts EncodeRow.
func DecodeRow(cols []catalog.Column, data []byte) (Row, error) {
	row := make(Row, len(cols))
	if err := DecodeRowInto(row, cols, data); err != nil {
		return nil, err
	}
	return row, nil
}

// DecodeRowInto fills an existing Row slice from encoded tuple data.
// Avoids the allocation that DecodeRow would make. The caller must
// ensure len(dst) == len(cols). M0027-0001.
//
// When the producer wants varchar / char / text / bytea payload to
// land in a per-batch arena instead of in fresh per-column
// `make([]byte)` allocations, use DecodeRowIntoArena (M0073-0002).
// The arena variant emits KindStringArena / KindBytesArena Datums
// whose payload lives in the arena's pages until Reset().
func DecodeRowInto(dst Row, cols []catalog.Column, data []byte) error {
	return decodeRowIntoMctx(dst, cols, data, nil)
}

// DecodeRowIntoArena is the arena-aware sibling of DecodeRowInto.
// When arena != nil, variable-length columns (varchar, char, text,
// bytea, numeric text storage) emit Datums whose Buf payload lives
// in the arena's pages — a single allocation per page (typically
// 64 KiB) amortises across thousands of strings, eliminating the
// per-column heap alloc that dominated Q5 / Q9 post-M0072
// (`acquireRow` 25 % cum heap on Q5).
//
// Caller invariants:
//   - arena.Reset() bound to the producer's batch boundary
//     (per-page in seqScanOp; per-Rescan in indexScanOp).
//   - Consumers retaining slots past the next Reset MUST go
//     through slot.Materialize(), which calls cloneRowOwned to
//     deep-copy arena bytes into owned []byte. The 4 retention
//     sites (executor.Run, sortOp.Open, windowOp.Open,
//     lockRowsOp.drainAndStamp) already do this.
//
// arena == nil falls back to the legacy `make([]byte)` path —
// behaviour byte-for-byte identical to DecodeRowInto.
//
// M0073-0002 — see docs/design/0073-0002-decode-arena-binding.md.
// DecodeRowIntoMctx is the mctx-aware sibling of DecodeRowInto.
// Variable-length columns (varchar, char, text, bytea) are backed by sctx;
// callers must not access those Datums after sctx.Reset() or sctx.Release().
func DecodeRowIntoMctx(dst Row, cols []catalog.Column, data []byte, sctx *mctx.Context) error {
	return decodeRowIntoMctx(dst, cols, data, sctx)
}

// DecodeRowIntoMctxPGTuple decodes a heap tuple's body, using the tuple HEADER
// (storedNatts + null bitmap) as the authoritative format discriminator. It
// NEVER guesses the format from the column bytes — guessing is the source of
// the cross-acceptance corruption class (see docs/design/0111-0002-...).
//
// Discriminator (M0111-0002): a goopg legacy-format tuple has no null bitmap
// and leaves natts unset (0); a PG-physical tuple always sets natts (>= 1) and
// carries a null bitmap when it has NULLs. PostgreSQL never writes natts == 0,
// so (storedNatts == 0 && bitmap == nil) unambiguously means "legacy". Anything
// else is PG-physical. bitmap may be nil for an all-non-null PG row (then natts
// distinguishes it from legacy). storedNatts < len(cols) means ALTER TABLE ADD
// COLUMN — trailing columns decode as NULL.
func DecodeRowIntoMctxPGTuple(dst Row, cols []catalog.Column, data, bitmap []byte, storedNatts int, sctx *mctx.Context) error {
	// PG-physical decode with null-bitmap and natts awareness. M0111-0002 S3:
	// the goopg legacy format has been removed, so there is a single on-disk
	// format. A PG-physical tuple always records natts; storedNatts==0 means a
	// header-less body (e.g. via the bare DecodeRow wrappers in tests) — treat
	// all columns as present.
	n := len(cols)
	if storedNatts == 0 {
		storedNatts = n
	}
	off := 0
	for i, c := range cols {
		// Columns beyond stored natts were added via ALTER TABLE ADD COLUMN.
		if i >= storedNatts {
			dst[i] = NullDatum
			continue
		}
		// Check null bitmap: bit i = 0 means column i is NULL.
		if len(bitmap) > 0 && (bitmap[i/8]>>(uint(i)%8))&1 == 0 {
			dst[i] = NullDatum
			continue
		}
		off = alignPhysicalPGOffset(off, physicalPGTypeAlign(c.Type))
		if off >= len(data) {
			// Data exhausted — treat remaining columns as NULL.
			dst[i] = NullDatum
			continue
		}
		v, consumed, err := decodePhysicalPGValueMctx(c.Type, data[off:], sctx)
		if err != nil {
			return fmt.Errorf("DecodePhysicalPGRow: %s: %w", c.Name, err)
		}
		dst[i] = v
		off += consumed
	}
	return nil
}

// DecodeHeapTupleRowInto fills dst from a heap tuple, selecting the row format
// deterministically from the tuple header (natts + null bitmap) rather than
// guessing from the bytes. This is the header-driven replacement for the bare
// DecodeRowInto on any read path that holds the storage.HeapTuple. M0111-0002.
func DecodeHeapTupleRowInto(dst Row, cols []catalog.Column, tuple storage.HeapTuple, sctx *mctx.Context) error {
	natts := int(tuple.Header.Infomask2 & storage.HeapNattsMask)
	return DecodeRowIntoMctxPGTuple(dst, cols, tuple.Data, tuple.Bitmap, natts, sctx)
}

// DecodeHeapTupleRow is the allocating sibling of DecodeHeapTupleRowInto: it
// returns a freshly-allocated Row. Use it where the bare DecodeRow was used on
// a path that holds the storage.HeapTuple. M0111-0002.
func DecodeHeapTupleRow(cols []catalog.Column, tuple storage.HeapTuple, sctx *mctx.Context) (Row, error) {
	row := make(Row, len(cols))
	if err := DecodeHeapTupleRowInto(row, cols, tuple, sctx); err != nil {
		return nil, err
	}
	return row, nil
}

func decodeRowIntoMctx(dst Row, cols []catalog.Column, data []byte, sctx *mctx.Context) error {
	// M0111-0002 S3: single on-disk format. The bare DecodeRow*/DecodeRowInto*
	// wrappers (header-less paths, used by tests) decode PG-physical bodies
	// only; the goopg legacy format and the format-guessing fallback were
	// removed once all writes became PG-physical and re-init was mandated.
	return decodePhysicalPGRowIntoMctx(dst, cols, data, sctx)
}

func decodePhysicalPGRowIntoMctx(dst Row, cols []catalog.Column, data []byte, sctx *mctx.Context) error {
	off := 0
	for i, c := range cols {
		off = alignPhysicalPGOffset(off, physicalPGTypeAlign(c.Type))
		if off > len(data) {
			return fmt.Errorf("DecodePhysicalPGRow: %s: truncated at offset %d", c.Name, off)
		}
		v, n, err := decodePhysicalPGValueMctx(c.Type, data[off:], sctx)
		if err != nil {
			return fmt.Errorf("DecodePhysicalPGRow: %s: %w", c.Name, err)
		}
		dst[i] = v
		off += n
	}
	for _, b := range data[off:] {
		if b != 0 {
			return fmt.Errorf("DecodePhysicalPGRow: trailing bytes")
		}
	}
	return nil
}

func alignPhysicalPGOffset(off, align int) int {
	if align <= 1 {
		return off
	}
	mask := align - 1
	return (off + mask) &^ mask
}

func physicalPGTypeAlign(t catalog.Type) int {
	switch strings.ToLower(t.Name) {
	case "bool", "boolean":
		return 1
	case "char":
		// Single-byte internal "char" type: alignment 1.
		// char(N) with length modifier is bpchar (varlena): alignment 4.
		if len(t.Args) == 0 {
			return 1
		}
		return 4
	case "int2", "smallint":
		return 2
	case "int4", "integer", "int", "serial", "oid", "regproc", "float4", "real", "date", "xid":
		return 4
	case "int8", "bigint", "bigserial", "pg_lsn", "float8", "double precision", "double", "timestamp", "timestamptz", "time", "timetz":
		return 8
	case "name":
		return 1 // PG 'c' alignment (fixed-size, 1-byte aligned)
	case "aclitem[]", "_aclitem", "text[]", "_text", "oid[]", "_oid", "int2[]", "_int2", "char[]", "_char", "float4[]", "_float4", "anyarray", "pg_node_tree", "oidvector", "int2vector":
		return 4 // PG 'i' alignment for varlena ArrayType / pg_node_tree / oidvector / int2vector
	default:
		return 4
	}
}

// pgPhysicalTypeIsVarlena reports whether the PG18 on-disk representation
// for t uses the varlena (variable-length) layout — i.e. PG's TupleDesc
// for the catalog stores attlen == -1 for the column. It must agree with
// the varlena branches of encodeValuePG; the fast-path attcacheoff
// walker in PG18 nocachegetattr (heaptuple.c:642 — `Assert(j > attnum)`)
// will trip if HEAP_HASVARWIDTH is unset and any column on the
// fixed-prefix path turns out to be varlena per the TupleDesc. M0106-0010
// batched-49.
func pgPhysicalTypeIsVarlena(t catalog.Type) bool {
	switch strings.ToLower(t.Name) {
	case "char":
		// Single-byte internal "char": fixed-length (not varlena).
		// char(N) with length modifier = bpchar (varlena).
		return len(t.Args) > 0
	case "bool", "boolean",
		"int2", "smallint",
		"int4", "integer", "int", "serial",
		"int8", "bigint", "bigserial",
		"pg_lsn",
		"oid", "regproc",
		"timestamp", "timestamptz", "date", "time", "timetz",
		"name",
		"float4", "real",
		"float8", "double precision", "double",
		"xid", "xid8":
		return false
	default:
		// text, varchar, bpchar, numeric, unknown, and all varlena
		// arrays / oidvector / int2vector / pg_node_tree fall through
		// to varlena. Mirrors the default branch of encodeValuePG.
		return true
	}
}

// pgRowHasVarWidth reports whether row, encoded with cols via
// EncodeRowPG, contains at least one non-null varlena value. Used to
// drive the HEAP_HASVARWIDTH bit on heap tuples written in the
// PG18-canonical layout. Mirrors PG's heap_fill_tuple which sets the
// flag only for non-null varlena values
// (postgres/src/backend/access/common/heaptuple.c:326). M0106-0010
// batched-49.
func pgRowHasVarWidth(cols []catalog.Column, row Row) bool {
	n := len(cols)
	if len(row) < n {
		n = len(row)
	}
	for i := 0; i < n; i++ {
		d := row[i]
		if d.IsNull() {
			continue
		}
		if d.Kind == KindToastPointer {
			return true
		}
		if pgPhysicalTypeIsVarlena(cols[i].Type) {
			return true
		}
	}
	return false
}

func decodePhysicalPGValueMctx(t catalog.Type, data []byte, sctx *mctx.Context) (Datum, int, error) {
	switch strings.ToLower(t.Name) {
	case "bool", "boolean":
		if len(data) < 1 {
			return Datum{}, 0, fmt.Errorf("truncated bool")
		}
		return NewBoolDatum(data[0] != 0), 1, nil
	case "int2", "smallint":
		if len(data) < 2 {
			return Datum{}, 0, fmt.Errorf("truncated int2")
		}
		return NewIntDatum(int64(int16(binary.LittleEndian.Uint16(data[:2])))), 2, nil
	case "int4", "integer", "int", "serial":
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated int4")
		}
		return NewIntDatum(int64(int32(binary.LittleEndian.Uint32(data[:4])))), 4, nil
	case "int8", "bigint", "bigserial":
		// Mirror encodeValuePG's 8-byte LE int8 encoding. Without this
		// case, any int8/bigint value (including count(*)/sum() results
		// stored via CTAS, and plain INSERTs into bigint columns) fell
		// through to the default branch and errored with "unsupported
		// PostgreSQL physical type", so the seqscan silently dropped the
		// row. M0111-0004.
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated int8")
		}
		return NewIntDatum(int64(binary.LittleEndian.Uint64(data[:8]))), 8, nil
	case "pg_lsn":
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated pg_lsn")
		}
		return NewStringDatum(formatPgLSN(binary.LittleEndian.Uint64(data[:8]))), 8, nil
	case "oid", "regproc":
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated oid")
		}
		return NewIntDatum(int64(binary.LittleEndian.Uint32(data[:4]))), 4, nil
	case "xid", "xid8":
		// encodeValuePG writes xid/xid8 as a 4-byte LE TransactionId.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated xid")
		}
		return NewIntDatum(int64(binary.LittleEndian.Uint32(data[:4]))), 4, nil
	case "name":
		// PG NameData: fixed 64 bytes, '\0'-padded (see encodeValuePG).
		if len(data) < 64 {
			return Datum{}, 0, fmt.Errorf("truncated name")
		}
		end := 64
		for i := 0; i < 64; i++ {
			if data[i] == 0 {
				end = i
				break
			}
		}
		if sctx != nil {
			moff, mlen := sctx.AllocBytes(data[:end])
			return newStringArenaDatum(sctx, moff, mlen), 64, nil
		}
		return NewStringDatum(string(data[:end])), 64, nil
	case "timestamp", "timestamptz":
		// encodeValuePG stores 8-byte LE microseconds since the PG epoch
		// (2000-01-01 UTC). Reconstruct the absolute instant. M0111-0004.
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated timestamp")
		}
		micros := int64(binary.LittleEndian.Uint64(data[:8]))
		return NewTimeDatum(time.UnixMicro(micros + pgEpochUnixMicros).UTC()), 8, nil
	case "date":
		// encodeValuePG stores 4-byte LE days since the PG epoch. M0111-0004.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated date")
		}
		days := int32(binary.LittleEndian.Uint32(data[:4]))
		micros := int64(days)*24*3600*1000000 + pgEpochUnixMicros
		return NewTimeDatum(time.UnixMicro(micros).UTC()), 4, nil
	case "time":
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated time")
		}
		micros := int64(binary.LittleEndian.Uint64(data[:8]))
		const maxTimeMicros = int64(24 * time.Hour / time.Microsecond)
		if micros < 0 || micros > maxTimeMicros {
			return Datum{}, 0, fmt.Errorf("invalid time micros")
		}
		return NewTimeDatum(pgTimeFromMicros(micros)), 8, nil
	case "timetz":
		if len(data) < 12 {
			return Datum{}, 0, fmt.Errorf("truncated timetz")
		}
		micros := int64(binary.LittleEndian.Uint64(data[:8]))
		const maxTimeMicros = int64(24 * time.Hour / time.Microsecond)
		if micros < 0 || micros > maxTimeMicros {
			return Datum{}, 0, fmt.Errorf("invalid timetz micros")
		}
		// PG wire stores offset as int32 seconds, positive = west of UTC.
		// Convert to our convention: positive = east of UTC.
		pgOffset := int32(binary.LittleEndian.Uint32(data[8:12]))
		offsetSecs := int(-pgOffset)
		return NewTimeTZDatum(pgTimeFromMicros(micros), offsetSecs), 12, nil
	case "text", "varchar", "character varying", "bpchar", "character", "char", "unknown":
		// PG external varlena (VARATT_IS_1B_E = 0x01): our TOAST pointer.
		// 0x01 is unambiguous: 0x01>>1=0 is an invalid data varlena length,
		// so no legitimate data string can produce this header byte.
		// (The previous marker 0x1B collided with 12-char strings.)
		if len(data) >= 13 && data[0] == 0x01 {
			ptr := make([]byte, 12)
			copy(ptr, data[1:13])
			return NewToastPointerDatum(ptr), 13, nil
		}

		payload, n, err := decodePhysicalPGVarlena(data)
		if err != nil {
			return Datum{}, 0, err
		}
		if sctx != nil {
			moff, mlen := sctx.AllocBytes(payload)
			return newStringArenaDatum(sctx, moff, mlen), n, nil
		}
		return NewStringDatum(string(payload)), n, nil
	case "float4", "real", "float8", "double precision", "double":
		// goopg stores float4/float8 as varlena text for v0 compatibility.
		// Decode the text payload back into KindNumeric (NaN/Inf fall back
		// to KindString) so numeric ORDER BY and comparison behave
		// correctly. Returning KindString here — as the shared text case
		// used to — sorted float columns lexicographically; this mirrors
		// the legacy decodeValue / arena decoders. M0111-0006: regression
		// of the M0097-0003 KindNumeric decode, lost when M0111-0002
		// switched float storage to varlena text in this PG-physical path.
		payload, n, err := decodePhysicalPGVarlena(data)
		if err != nil {
			return Datum{}, 0, err
		}
		text := string(payload)
		if v, scale, ok := parseNumericFast(text); ok {
			return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
		}
		if m, s, perr := parseNumeric(text); perr == nil {
			return newNumeric(m, int(s)), n, nil
		}
		// NaN / Infinity / other non-decimal text falls back to string.
		return NewStringDatum(text), n, nil
	case "uuid":
		// goopg stores UUID as varlena-text (canonical lowercase-with-dashes
		// format). Decode: read the varlena payload and return as KindString.
		// UUID rows were previously silently dropped because this case was
		// missing and the default returned "unsupported PostgreSQL physical
		// type". M0097-0029.
		payload, n, err := decodePhysicalPGVarlena(data)
		if err != nil {
			return Datum{}, 0, err
		}
		if sctx != nil {
			moff, mlen := sctx.AllocBytes(payload)
			return newStringArenaDatum(sctx, moff, mlen), n, nil
		}
		return NewStringDatum(string(payload)), n, nil
	case "bytea":
		payload, n, err := decodePhysicalPGVarlena(data)
		if err != nil {
			return Datum{}, 0, err
		}
		if sctx != nil {
			moff, mlen := sctx.AllocBytes(payload)
			return newBytesArenaDatum(sctx, moff, mlen), n, nil
		}
		return NewBytesDatum(append([]byte(nil), payload...)), n, nil
	case "numeric", "decimal":
		// goopg encodes numeric as varlena-text (via the default encodeValuePG
		// path which calls varlenaTextBytes). Decode: read the varlena payload
		// as a plain text string and parse it back to KindNumeric.
		payload, n, err := decodePhysicalPGVarlena(data)
		if err != nil {
			return Datum{}, 0, err
		}
		text := string(payload)
		if v, scale, ok := parseNumericFast(text); ok {
			return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
		}
		m, s, err := parseNumeric(text)
		if err != nil {
			return Datum{}, 0, fmt.Errorf("decode numeric %q: %w", text, err)
		}
		return newNumeric(m, int(s)), n, nil
	default:
		return Datum{}, 0, fmt.Errorf("unsupported PostgreSQL physical type %q", t.Name)
	}
}

func decodePhysicalPGVarlena(data []byte) ([]byte, int, error) {
	if len(data) == 0 {
		return nil, 0, fmt.Errorf("truncated varlena")
	}
	header := data[0]
	if header&0x01 == 0x01 {
		if header == 0x01 {
			return nil, 0, fmt.Errorf("external varlena not supported")
		}
		total := int(header >> 1)
		if total < 1 || total > len(data) {
			return nil, 0, fmt.Errorf("truncated short varlena")
		}
		return data[1:total], total, nil
	}
	if len(data) < 4 {
		return nil, 0, fmt.Errorf("truncated 4-byte varlena header")
	}
	if header&0x03 == 0x02 {
		return nil, 0, fmt.Errorf("compressed varlena not supported")
	}
	total := int(binary.LittleEndian.Uint32(data[:4]) >> 2)
	if total < 4 || total > len(data) {
		return nil, 0, fmt.Errorf("truncated 4-byte varlena")
	}
	return data[4:total], total, nil
}

// parseIntegerInput parses a string as an integer supporting:
// - base-0 detection (0b binary, 0o octal, 0x hex, 0 prefix octal not supported by PG — decimal)
// - underscore separators (1_000 → 1000)
// - Validates underscore placement: no leading underscore in decimal, no trailing, no consecutive
// - For non-decimal (0b/0o/0x): leading underscore after prefix is OK (PG allows 0b_10)
// - Distinguishes syntax errors (22P02) from range errors (22003)
// M0097-0003.
func parseIntegerInput(raw, typeName string, bitSize int) (int64, error) {
	orig := raw
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, &ExecError{Code: "22P02",
			Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, orig)}
	}

	// Detect base prefix.
	isNonDecimal := false
	prefix := ""
	rest := s
	if len(s) >= 2 && (s[0] == '0') {
		switch {
		case s[1] == 'b' || s[1] == 'B':
			isNonDecimal = true
			prefix = s[:2]
			rest = s[2:]
		case s[1] == 'o' || s[1] == 'O':
			isNonDecimal = true
			prefix = s[:2]
			rest = s[2:]
		case s[1] == 'x' || s[1] == 'X':
			isNonDecimal = true
			prefix = s[:2]
			rest = s[2:]
		}
	}
	_ = prefix

	// Validate underscore rules.
	if !isNonDecimal {
		// Decimal: no leading underscore (after optional sign), no trailing, no consecutive.
		check := rest
		if len(check) > 0 && (check[0] == '-' || check[0] == '+') {
			check = check[1:]
		}
		if len(check) > 0 && check[0] == '_' {
			return 0, &ExecError{Code: "22P02",
				Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, orig)}
		}
	} else {
		// Non-decimal: leading underscore after prefix is OK per PG 16.
		// No trailing underscore, no consecutive __.
	}
	if len(s) > 0 && s[len(s)-1] == '_' {
		return 0, &ExecError{Code: "22P02",
			Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, orig)}
	}
	if strings.Contains(s, "__") {
		return 0, &ExecError{Code: "22P02",
			Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, orig)}
	}

	// Empty after prefix (e.g., "0b") is a syntax error.
	if isNonDecimal && (rest == "" || rest == "_") {
		return 0, &ExecError{Code: "22P02",
			Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, orig)}
	}

	// Strip underscores for parsing.
	cleaned := strings.ReplaceAll(s, "_", "")
	v, err := strconv.ParseInt(cleaned, 0, bitSize)
	if err != nil {
		numErr, ok := err.(*strconv.NumError)
		if ok && numErr.Err == strconv.ErrRange {
			return 0, &ExecError{Code: "22003",
				Message: fmt.Sprintf("value %q is out of range for type %s", orig, typeName)}
		}
		return 0, &ExecError{Code: "22P02",
			Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, orig)}
	}
	return v, nil
}

// coerceStringToInt64 parses a trimmed string as int64, returning a proper
// 22P02/22003 ExecError for invalid inputs. Used by encodeValue when a string
// datum is being stored in an integer column. M0097-0003.
func coerceStringToInt64(s, typeName string) (int64, error) {
	return parseIntegerInput(s, typeName, 64)
}

// parsePgLSN parses a "X/Y" pg_lsn string into a uint64.
// X and Y are 1–8 hex digits each, no leading spaces allowed.
// Returns an error with "22P02" code on invalid input.
func parsePgLSN(s string) (uint64, error) {
	slash := strings.IndexByte(s, '/')
	if slash < 1 || slash > 8 {
		return 0, &ExecError{Code: "22P02",
			Message: fmt.Sprintf("invalid input syntax for type pg_lsn: %q", s)}
	}
	hexHigh := s[:slash]
	hexLow := s[slash+1:]
	if len(hexLow) < 1 || len(hexLow) > 8 || len(hexLow)+slash+1 != len(s) {
		return 0, &ExecError{Code: "22P02",
			Message: fmt.Sprintf("invalid input syntax for type pg_lsn: %q", s)}
	}
	// Validate hex characters only
	for _, c := range hexHigh + hexLow {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return 0, &ExecError{Code: "22P02",
				Message: fmt.Sprintf("invalid input syntax for type pg_lsn: %q", s)}
		}
	}
	high, _ := strconv.ParseUint(hexHigh, 16, 32)
	low, _ := strconv.ParseUint(hexLow, 16, 32)
	return (high << 32) | low, nil
}

// formatPgLSN formats a uint64 WAL LSN as "X/Y" uppercase hex.
func formatPgLSN(v uint64) string {
	return fmt.Sprintf("%X/%X", v>>32, v&0xFFFFFFFF)
}

// isValidUUIDStr reports whether s is a valid UUID string in any of
// PostgreSQL's accepted formats:
//   - Standard: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx (36 chars)
//   - Braces:   {xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx} (38 chars)
//   - No-hyphen: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx (32 hex chars)
//
// M0097-0003.
func isValidUUIDStr(s string) bool {
	if len(s) == 38 && s[0] == '{' && s[37] == '}' {
		s = s[1:37] // strip braces
	}
	if len(s) == 32 {
		// No-hyphen: all must be hex.
		for _, c := range []byte(s) {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	}
	if len(s) != 36 {
		return false
	}
	for i, c := range []byte(s) {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// normalizeUUIDStr converts a UUID in any accepted format to the canonical
// lowercase xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx format. M0097-0003.
func normalizeUUIDStr(s string) string {
	s = strings.ToLower(s)
	if len(s) == 38 && s[0] == '{' && s[37] == '}' {
		s = s[1:37]
	}
	if len(s) == 32 {
		// No-hyphen: insert hyphens at 8, 12, 16, 20.
		return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:32]
	}
	return s
}

func encodeVarlen(b []byte) []byte {
	out := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(out[:4], uint32(len(b)))
	copy(out[4:], b)
	return out
}
