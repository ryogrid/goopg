package executor

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/adt/array"
	"github.com/goopg/goopg/internal/nodes"
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
// The table itself lives in internal/pgarray so the logical-replication decoder
// (internal/wal/pgoutput.go), which cannot import the executor, reads the same
// on-disk blobs through the same element table and renderer. M0119-0006.
func arrayElemTypeInfo(elemName string) (oid uint32, size, align int, varlena, ok bool) {
	return array.ElemTypeInfo(elemName)
}

// encodeArrayValuePG encodes an array-typed datum (t.IsArray) into a PG-native
// ArrayType varlena blob. A pre-built blob (KindBytes) passes through verbatim
// — that is how catalog seeders inject ready-made arrays.
func encodeArrayValuePG(t catalog.Type, d Datum) ([]byte, error) {
	return encodeArrayValuePGCtx(t, d, nil, 0)
}

// encodeArrayValuePGCtx is the ctx+pos-carrying sibling of encodeArrayValuePG:
// a reg* element resolves its name→OID through the session catalog
// (regIdentifierInput), so the heap-write paths thread their Context in; the
// no-ctx wrapper (nil ctx, pos 0) is what the test/codec callers that never
// write a user reg*[] column use. M0119-0006 reg* element slice.
func encodeArrayValuePGCtx(t catalog.Type, d Datum, ctx *Context, pos int) ([]byte, error) {
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
		eb, err := encodeArrayElem(t.Name, e, varlena, ctx, pos)
		if err != nil {
			return nil, err
		}
		body = append(body, eb...)
	}
	// Upstream construct_md_array (postgres/src/backend/utils/adt/arrayfuncs.c)
	// re-aligns the running length AFTER each element, the last one included, so
	// a PG array's VARSIZE is padded up to the element alignment. goopg used to
	// stop at the final element, which was invisible to any reader (elements are
	// found by count, never by scanning to the end) but made the blob a different
	// SIZE from the one PG writes for the same value — visible to pg_column_size,
	// to a byte-compare against a PG-written tuple, and to pg_amcheck. Pinned on
	// PG 18.3: a 1-element timetz array is 40 bytes = MAXALIGN(24 + 12), a
	// 1-element `\x01` bytea array is 32 = align4(24 + 5). M0119-0006.
	if pad := alignPad(len(body), align); pad > 0 {
		body = append(body, make([]byte, pad)...)
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
func encodeArrayElem(elemName, e string, varlena bool, ctx *Context, pos int) ([]byte, error) {
	lower := strings.ToLower(elemName)
	if varlena {
		// numeric is the one varlena element whose body is NOT the text the
		// user typed: like the scalar arm in encodeValuePG it stores PG's
		// base-10000 NumericData, so the varlena header wraps that body
		// instead of the decimal string. Every other varlena element type
		// goopg supports (text/varchar/bpchar) IS its own text.
		if lower == "numeric" || lower == "decimal" {
			body, nerr := nodes.NumericBodyFromText(strings.TrimSpace(e))
			if nerr != nil {
				return nil, &ExecError{Code: "22P02",
					Message: fmt.Sprintf("invalid input syntax for type numeric: %q", e)}
			}
			return array4ByteVarlenaBytes(body), nil
		}
		if lower == "bytea" {
			// Sibling of encodeValuePG's "bytea" arm: an element arrives as the
			// TEXT inside the array literal, which upstream routes through
			// byteain, and the stored body is the RAW bytes. Note the header is
			// array4ByteVarlenaBytes, not the scalar arm's varlenaPayloadBytes:
			// a scalar bytea may use PG's 1-byte short header, while inside an
			// array body the elements are the always-4-byte form at align 4
			// (pinned on PG 18.3: three 1-byte bytea elements make a 48-byte
			// array = 24-byte header + 3 × MAXALIGN-padded 8).
			raw, err := byteaIn(e, 0)
			if err != nil {
				return nil, err
			}
			return array4ByteVarlenaBytes(raw), nil
		}
		return array4ByteVarlena(e), nil
	}
	switch lower {
	case "date", "time", "timestamp", "timestamptz", "timetz":
		// The date-time element images, delegated to the SCALAR encoder rather
		// than re-derived here (Hard-won Rule #2 — the element and the column
		// must produce identical bytes, and every one of these arms already
		// accepts the KindString an array element always is: date/timestamp go
		// through parseCopyTimestamp plus the ±infinity literals, time through
		// parseTimeString, timetz through parseTimeTZString). The widths the
		// element table declares (4/8/8/8/12) are exactly what those arms
		// return, which is what keeps the array body self-describing.
		return encodeValuePG(catalog.Type{Name: lower}, NewStringDatum(e))
	case "uuid":
		// Sibling of encodeValuePG's "uuid" arm: pg_uuid_t, 16 raw bytes.
		s := normalizeUUIDStr(strings.TrimSpace(e))
		raw, ok := uuidBytesFromCanonical(s)
		if !ok || !isValidUUIDStr(s) {
			return nil, &ExecError{Code: "22P02",
				Message: fmt.Sprintf("invalid input syntax for type uuid: %q", e)}
		}
		return append([]byte(nil), raw[:]...), nil
	case "interval":
		// Sibling of encodeValuePG's "interval" arm: {time int64, day int32,
		// month int32}. An array element always arrives as the text inside the
		// array literal, so interval_in's tokenizer is the only entry point.
		months, days, micros, ok := parser.ParseIntervalBody(strings.TrimSpace(e))
		if !ok {
			return nil, &ExecError{Code: "22007",
				Message: fmt.Sprintf("invalid input syntax for type interval: %q", e)}
		}
		var b [16]byte
		binary.LittleEndian.PutUint64(b[0:8], uint64(micros))
		binary.LittleEndian.PutUint32(b[8:12], uint32(days))
		binary.LittleEndian.PutUint32(b[12:16], uint32(months))
		return b[:], nil
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
	case "regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation":
		// Sibling of the scalar reg* arm in encodeValuePG (66th–68th slices,
		// deferral 1306): an array element arrives as the text inside the array
		// literal, so it resolves through the SAME regIdentifierInput the
		// scalar path uses (parseDashOrOid "-"/numeric first, then the per-type
		// catalog lookup), which yields the family's own miss SQLSTATEs (42P01
		// regclass, 42704 regtype/regrole/regcollation, 42883 regproc/
		// regprocedure, 42602 invalid-name-syntax) rather than a 22P02 wrap.
		// The stored body is the resolved 4-byte LE OID (typlen 4, align 'i'),
		// exactly the scalar family's image. A nil ctx (no-catalog writers such
		// as the toast/index-key paths that never carry a user reg*[] name)
		// resolves "-"/numeric and errors on a name, never silently storing it.
		d, err := regIdentifierInput(NewStringDatum(e), elemName, ctx, pos)
		if err != nil {
			return nil, err
		}
		oid, err := regIdentifierOIDFromDatum(d, strings.ToLower(elemName))
		if err != nil {
			return nil, err
		}
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], oid)
		return b[:], nil
	default:
		return array4ByteVarlena(e), nil
	}
}

// array4ByteVarlena serialises a text element with the always-4-byte varlena
// header used inside array data (PG never uses the 1-byte short header for
// elements stored with 'i' alignment in an array body).
func array4ByteVarlena(s string) []byte {
	return array4ByteVarlenaBytes([]byte(s))
}

// array4ByteVarlenaBytes is array4ByteVarlena over an arbitrary body — used by
// the numeric element arm, whose body is a NumericData image rather than text.
func array4ByteVarlenaBytes(body []byte) []byte {
	total := len(body) + 4
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	copy(buf[4:], body)
	return buf
}

// decodeArrayValuePG inverts encodeArrayValuePG: it reads the ArrayType varlena
// at the head of data and renders the canonical PG array text "{e1,e2,...}",
// returning it as a KindString datum plus the number of bytes consumed.
// arrayOutputStyle reads the two session GUCs an array element's output
// function consults (upstream: array_out → the ELEMENT type's typoutput, so
// `date_out`/`timestamp_out` see DateStyle and `timestamptz_out` sees both
// DateStyle and TimeZone). Same GUC spellings and same boot-default fallbacks
// as RunCopyTo's pair (internal/executor/copy.go), which is its sibling: a
// COPY … TO and a SELECT of the same array column must print the same text.
func arrayOutputStyle(ctx *Context) array.OutputStyle {
	st := array.DefaultOutputStyle()
	// Reg* element renderer, bound FIRST so a catalog-carrying ctx gets it even
	// when the style GUCs are unreadable (nil GetSetting): an array of
	// regclass/regtype/… is flattened to text at heap-decode time, so the
	// OID→name renderer must be bound here, where the session catalog +
	// search-path qualify + connection dbOid reach. It is the SAME renderer
	// the scalar SELECT/COPY paths use, which is what keeps the array and
	// scalar forms byte-identical — and SELECT and COPY TO (both consume the
	// decoded text) agreeing with each other.
	st.RegOut = arrayRegOutRenderer(ctx)
	if ctx == nil || ctx.GetSetting == nil {
		return st
	}
	if v, ok := ctx.GetSetting("datestyle"); ok {
		st.Style, st.Order = misc.ParseDateStyleValue(v)
	}
	if v, ok := ctx.GetSetting("timezone"); ok {
		st.Zone = v
	}
	if v, ok := ctx.GetSetting("bytea_output"); ok {
		st.ByteaMode = v
	}
	return st
}

// arrayRegOutRenderer closes the executor's RegOut over the session catalog,
// the search-path qualify rule COPY TO uses (!RegObjectSchemaVisible — the
// sibling of the server package's publicSchemaVisible) and the connection's
// dbOid (the 75th-slice per-DB scoping). Nil when no catalog is reachable
// (pure-codec/pgoutput callers) — pgarray then falls back to "-"/numeric.
func arrayRegOutRenderer(ctx *Context) func(typeName string, oid uint32) string {
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	qualify := !RegObjectSchemaVisible(ctx, "public")
	connDBOid := catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)
	return func(typeName string, oid uint32) string {
		return RegOut(typeName, oid, ctx.Catalog, qualify, connDBOid)
	}
}

// colsHaveArray reports whether any column is an array, so a scan pays the GUC
// lookup only for the relations that can actually render one.
func colsHaveArray(cols []catalog.Column) bool {
	for i := range cols {
		if cols[i].Type.IsArray {
			return true
		}
	}
	return false
}

func decodeArrayValuePG(t catalog.Type, data []byte) (Datum, int, error) {
	return decodeArrayValuePGStyled(t, data, array.DefaultOutputStyle())
}

// decodeArrayValuePGStyled is decodeArrayValuePG under an explicit session
// output style. goopg renders an array to its "{…}" text at DECODE time, while
// upstream renders it at OUTPUT time via array_out, so the session's
// DateStyle/TimeZone — which a date/timestamp/timestamptz element's output
// function reads — has to be carried down here. Decode sites with no session
// (catalog reload, VACUUM, ANALYZE, DDL rescans) keep passing the default; see
// pgarray.OutputStyle. M0119-0006.
func decodeArrayValuePGStyled(t catalog.Type, data []byte, st array.OutputStyle) (Datum, int, error) {
	payload, consumed, err := decodePhysicalPGVarlena(data)
	if err != nil {
		return Datum{}, 0, err
	}
	// payload starts at ndim (the varlena header was stripped):
	//   [0:4] ndim [4:8] dataoffset [8:12] elemtype [12:16] dims[0] [16:20] lbound[0]
	// The rendering itself lives in internal/pgarray, shared with the pgoutput
	// decoder (M0119-0006).
	text, err := array.RenderTextStyled(t.Name, payload, st)
	if err != nil {
		return Datum{}, 0, err
	}
	return NewStringDatum(text), consumed, nil
}

// quoteArrayTextElem applies PG array-output quoting: an element is double-
// quoted when empty, equal to the literal NULL, or containing a character that
// would otherwise be ambiguous ({ } , " \ or whitespace).
func quoteArrayTextElem(s string) string { return array.QuoteTextElem(s) }
