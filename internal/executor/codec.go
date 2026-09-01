package executor

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/mmgr"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/utils/adt/array"
	"github.com/goopg/goopg/internal/utils/adt/datetime"
	"github.com/goopg/goopg/internal/access/common/pglz"
	"github.com/goopg/goopg/internal/nodes"
	"github.com/goopg/goopg/internal/storage"
)

// PGFloatOut renders a float in PostgreSQL's float4out/float8out shortest
// round-trip format. Relocated to catalog (M0111-0002) so wal's pgoutput
// decoder shares the single implementation; this alias keeps the executor's
// historical call sites unchanged.
func PGFloatOut(f float64, bitSize int) string { return catalog.PGFloatOut(f, bitSize) }

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
	return EncodeRowPGCtx(cols, row, nil, 0)
}

// EncodeRowPGCtx is the ctx+pos-carrying sibling of EncodeRowPG: a reg*[]
// column resolves its element names through the session catalog
// (regIdentifierInput), so the heap-write paths thread their Context in; the
// no-ctx wrapper is what the test/toast/index-key callers that never write a
// user reg*[] name use. M0119-0006 reg* element slice.
func EncodeRowPGCtx(cols []catalog.Column, row Row, ctx *Context, pos int) ([]byte, error) {
	if len(cols) != len(row) {
		return nil, fmt.Errorf("EncodeRowPG: %d cols vs %d datums", len(cols), len(row))
	}
	return encodeRowPGCtx(cols, row, ctx, pos)
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
func encodeRowPGCtx(cols []catalog.Column, row Row, ctx *Context, pos int) ([]byte, error) {
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
		buf, err := encodeValuePGCtx(c.Type, d, ctx, pos)
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

	// jsonb is canonicalised at the input boundary (M0119-0006, 64th slice):
	// PG stores a parsed tree and re-emits jsonb_out's canonical form on every
	// read, so `'{"b":1,"a":2}'::jsonb` renders `{"a": 2, "b": 1}`. goopg stored
	// the text verbatim; canonicalising here makes a jsonb column hold the same
	// text PG would show, and adds the 22P02 validation the bare pass-through
	// was missing. `json` (text) is deliberately left with its input spelling
	// untouched (no re-serialisation) but still validated for syntax — must
	// agree with evalCast's `::json` arm (Hard-won Rule #2). M0134-0120.
	if tname == "jsonb" {
		return canonicalizeJSONB(s)
	}
	if tname == "json" {
		if err := validateJSONText(s); err != nil {
			return "", err
		}
		return s, nil
	}

	// The declared length of a varchar(n)/char(n) counts CHARACTERS, not bytes:
	// upstream varchar_input and bpchar_input both measure with
	// pg_mbstrlen_with_len before converting maxlen to a byte length
	// (postgres/src/backend/utils/adt/varchar.c). Measured on PG 18.3:
	// `'あいうえお'::varchar(5)` is accepted and occupies 15 bytes, and
	// `'あい'::char(5)` is 9 bytes / length() 2. Counting bytes here rejected
	// both with a spurious 22001, and disagreed with the rune-counting
	// truncation the explicit-cast path already did (expr.go) and with the
	// rune-counting pad catalog.PadBpchar now applies at every render boundary.
	// M0119-0006 (57th slice).
	if tname == "varchar" || tname == "character varying" {
		if len(t.Args) > 0 {
			n := int(t.Args[0])
			stripped := strings.TrimRight(s, " ")
			if utf8.RuneCountInString(stripped) > n {
				return "", &ExecError{Code: "22001",
					Message: fmt.Sprintf("value too long for type character varying(%d)", n)}
			}
			s = stripped
		}
	} else if tname == "char" || tname == "bpchar" || tname == "character" {
		// A typmod-less type carries NO length limit upstream: bpchar_input's
		// `atttypmod < VARHDRSZ` arm sets maxlen to the value's own length and
		// neither truncates nor errors (postgres/src/backend/utils/adt/
		// varchar.c). The implicit length-1 default below belongs to the
		// GRAMMAR, not the type: `char` and `character` really do mean
		// `character(1)` (gram.y CHARACTER opt_charset → bpchar with typmod 1,
		// which parseColumnType mirrors at internal/parser/ddl.go), whereas the
		// internal name `bpchar` spelled directly takes typmod -1. Measured on
		// PG 18.3: `CREATE TABLE t(a bpchar)` gives atttypmod -1 / format_type
		// `bpchar` and accepts 'abc', while `b char` and `c character` both give
		// atttypmod 5 / `character(1)`. Applying the default to `bpchar` raised
		// a spurious 22001 on every value longer than one character.
		//
		// Empty Args cannot mean "declared bpchar(N) whose N was lost": the heap
		// reload decodes the typmod back into Args for OID 1042
		// (pgTypeArgsFromTypmod, internal/initdb/catalog_heap_reload.go:1240)
		// and CREATE TABLE copies the parsed Args verbatim, so a restored
		// `char(3)` column arrives as Name "bpchar" with Args [3].
		// M0119-0006 (58th slice).
		n := -1
		if len(t.Args) > 0 {
			n = int(t.Args[0])
		} else if tname != "bpchar" {
			n = 1
		}
		if n >= 0 {
			// goopg stores a WIDTH-CARRYING bpchar trimmed (deliberate and
			// load-bearing — M0103-0007 rung 24; compareDatum's
			// padding-insensitive equality and the compact heap image rest on
			// it) and re-pads to Args[0] at every render boundary via
			// catalog.PadBpchar. An unbounded bpchar has no width to re-pad
			// FROM, so trimming it would destroy the trailing blanks instead of
			// deferring them: measured on PG 18.3, `bpchar` holding 'ab  ' is
			// octet_length 4 where a char(6) holding the same is 6. Both survive
			// only if the unbounded value is stored verbatim.
			stripped := strings.TrimRight(s, " ")
			if utf8.RuneCountInString(stripped) > n {
				return "", &ExecError{Code: "22001",
					Message: fmt.Sprintf("value too long for type character(%d)", n)}
			}
			s = stripped
		}
	} else if tname == "bit" {
		// bit(n): fixed-length, EXACT bit count — no padding/truncation on
		// assignment. Mirrors varbit.c bit_in's binary-digit scan (only '0'/
		// '1' accepted, ERRCODE_INVALID_TEXT_REPRESENTATION 22P02 on anything
		// else) followed by the exact-length check (ERRCODE_STRING_DATA_
		// LENGTH_MISMATCH 22026 when atttypmod is set and bitlen != atttypmod).
		// A B'...'/X'...' literal (decodeBitStringLit, internal/parser/expr.go)
		// already validated its own digit set at parse time, but this is the
		// single chokepoint every OTHER path into a bit(n) column also passes
		// through — a plain string literal (`'10'::bit(11)`, implicit
		// assignment coercion) or a computed text value never went through the
		// parser's literal decoder at all. M0134-0092.
		if err := validateBitDigits(s); err != nil {
			return "", err
		}
		if len(t.Args) > 0 {
			n := int(t.Args[0])
			if len(s) != n {
				return "", &ExecError{Code: "22026",
					Message: fmt.Sprintf("bit string length %d does not match type bit(%d)", len(s), n)}
			}
		}
	} else if tname == "varbit" || tname == "bit varying" {
		// bit varying(n): like bit(n) but the length check is an upper bound
		// (ERRCODE_STRING_DATA_RIGHT_TRUNCATION 22001, "too long"), not an
		// exact match — varbit.c varbit_in's `bitlen > atttypmod` arm.
		if err := validateBitDigits(s); err != nil {
			return "", err
		}
		if len(t.Args) > 0 {
			n := int(t.Args[0])
			if len(s) > n {
				return "", &ExecError{Code: "22001",
					Message: fmt.Sprintf("bit string too long for type bit varying(%d)", n)}
			}
		}
	} else if tname == "box" {
		// box: validate + canonicalize on assignment, mirroring box_in
		// (postgres/src/backend/utils/adt/geo_ops.c) — the single chokepoint
		// every path into a box column shares (plain string literal,
		// implicit coercion, computed text value), not just a `box '...'`
		// typed literal (which routes through evalTypedStringLit instead,
		// same parseBoxLiteral). Previously a box column was raw-varlena
		// pass-through: any string, well-formed or not, was stored
		// verbatim with no reordering of corners. M0134-0094.
		hx, hy, lx, ly, ok := parseBoxLiteral(s)
		if !ok {
			return "", &ExecError{Code: "22P02",
				Message: fmt.Sprintf("invalid input syntax for type box: %q", s)}
		}
		s = boxCanonicalText(hx, hy, lx, ly)
	} else if tname == "circle" {
		// circle: validate + canonicalize on assignment, mirroring circle_in
		// (postgres/src/backend/utils/adt/geo_ops.c), same chokepoint
		// pattern as box above. M0134-0098.
		cx, cy, r, ok := parseCircleLiteral(s)
		if !ok {
			return "", &ExecError{Code: "22P02",
				Message: fmt.Sprintf("invalid input syntax for type circle: %q", s)}
		}
		s = circleCanonicalText(cx, cy, r)
	} else if tname == "line" {
		// line: validate + canonicalize on assignment, mirroring line_in
		// (postgres/src/backend/utils/adt/geo_ops.c), same chokepoint
		// pattern as box/circle above. M0134-0136.
		la, lb, lc, lerr := parseLineLiteral(s)
		if lerr != nil {
			return "", lerr
		}
		s = lineCanonicalText(la, lb, lc)
	} else if tname == "lseg" {
		// lseg: validate + canonicalize on assignment, mirroring lseg_in
		// (postgres/src/backend/utils/adt/geo_ops.c), same chokepoint
		// pattern as box/circle/line above — but lseg_in has no coefficient
		// form and no distinct-points check (unlike line). M0134-0137.
		x1, y1, x2, y2, lerr := parseLsegLiteral(s)
		if lerr != nil {
			return "", lerr
		}
		s = lsegCanonicalText(x1, y1, x2, y2)
	} else if tname == "path" {
		// path: validate + canonicalize on assignment, mirroring path_in
		// (postgres/src/backend/utils/adt/geo_ops.c), same chokepoint
		// pattern as box/circle/line/lseg above. Previously a path column
		// was raw-varlena pass-through: any string, well-formed or not, was
		// stored verbatim with no open/closed normalization. M0134-0149.
		pts, closed, perr := parsePathLiteral(s)
		if perr != nil {
			return "", perr
		}
		s = pathCanonicalText(pts, closed)
	} else if tname == "point" {
		// point: validate + canonicalize on assignment, mirroring point_in
		// (postgres/src/backend/utils/adt/geo_ops.c), same chokepoint
		// pattern as box/circle/line/lseg/path above. Unlike those, point
		// had NO parser at all before this — a raw-varlena pass-through
		// accepting any string verbatim (garbage like "asdfasdf" or
		// "(10.0 10.0)" was stored unchanged). M0134-0150.
		px, py, perr := parsePointLiteral(s)
		if perr != nil {
			return "", perr
		}
		s = pointCanonicalText(px, py)
	} else if tname == "polygon" {
		// polygon: validate + canonicalize on assignment, mirroring poly_in
		// (postgres/src/backend/utils/adt/geo_ops.c), same chokepoint
		// pattern as box/circle/line/lseg/path/point above. polygon had NO
		// parser at all before this — a raw-varlena pass-through accepting
		// any string verbatim. M0134-0151.
		pts, perr := parsePolygonLiteral(s)
		if perr != nil {
			return "", perr
		}
		s = polygonCanonicalText(pts)
	} else if tname == "inet" || tname == "cidr" {
		// inet/cidr: validate + canonicalize on assignment, mirroring PG's
		// network_in/network_out (postgres/src/backend/utils/adt/network.c),
		// same chokepoint pattern as box/circle above. M0134-0130.
		out, eerr := normalizeInetCidrText(s, tname == "cidr")
		if eerr != nil {
			return "", eerr
		}
		s = out
	} else if tname == "macaddr" {
		// macaddr: validate + canonicalize on assignment, mirroring
		// macaddr_in/macaddr_out (postgres/src/backend/utils/adt/mac.c), same
		// chokepoint pattern as box/circle/line/lseg/inet above. Previously a
		// macaddr column was raw-varlena pass-through: any string was stored
		// verbatim, so '08-00-2b-01-02-03' and '08:00:2b:01:02:03' compared
		// unequal and 'not even close' inserted cleanly. M0134-0138.
		a, b2, c, d, e, f, merr := parseMacaddrLiteral(s)
		if merr != nil {
			return "", merr
		}
		s = macaddrCanonicalText(a, b2, c, d, e, f)
	} else if tname == "macaddr8" {
		// macaddr8: validate + canonicalize on assignment, mirroring
		// macaddr8_in/macaddr8_out (postgres/src/backend/utils/adt/mac8.c),
		// same chokepoint pattern as macaddr above — previously zero
		// executor support at all. M0134-0139.
		a, b2, c, d, e, f, g, h, merr := parseMacaddr8Literal(s)
		if merr != nil {
			return "", merr
		}
		s = macaddr8CanonicalText(a, b2, c, d, e, f, g, h)
	}
	return s, nil
}

// validateBitDigits checks that s contains only '0'/'1' characters, matching
// varbit.c bit_in/varbit_in's binary-digit scan. Reports the first offending
// character with PG's exact wording and SQLSTATE (22P02,
// ERRCODE_INVALID_TEXT_REPRESENTATION).
func validateBitDigits(s string) error {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' && s[i] != '1' {
			return &ExecError{Code: "22P02",
				Message: fmt.Sprintf("%q is not a valid binary digit", string(s[i]))}
		}
	}
	return nil
}

// pgBoolIn reproduces PostgreSQL's boolean input conversion —
// `parse_bool_with_len` (postgres/src/backend/utils/adt/bool.c), which boolin
// calls after trimming surrounding whitespace. The accepted spellings are the
// unambiguous prefixes of "true"/"false"/"yes"/"no"/"on"/"off" plus "1"/"0";
// note "o" alone is NOT accepted (it cannot distinguish on from off).
// Returns (value, true) on success, (false, false) for unrecognised input.
//
// Single source of truth for the four sites that used to carry their own copy
// of this table (evalTypedStringLit, evalCast, isValidBoolInput, and the
// encodeValuePG bool arm) — Hard-won Rule #2.
func pgBoolIn(s string) (bool, bool) {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "t", "tr", "tru", "true", "y", "ye", "yes", "on", "1":
		return true, true
	case "f", "fa", "fal", "fals", "false", "n", "no", "of", "off", "0":
		return false, true
	}
	return false, false
}

// encodeValuePG encodes a single datum in PG-native format.
func encodeValuePG(t catalog.Type, d Datum) ([]byte, error) {
	return encodeValuePGCtx(t, d, nil, 0)
}

// encodeValuePGCtx is the ctx+pos-carrying sibling of encodeValuePG (see
// EncodeRowPGCtx for why): a reg*[] element resolves its name→OID through the
// session catalog (regIdentifierInput), and (M0134-0026 round 2) a zone-less
// timestamptz string resolves the session TimeZone GUC the same way. The
// no-ctx wrapper (nil ctx, pos 0) is what the non-writer callers (tests,
// toast chunk rows, index keys, catalog heap tuples) use; both ctx-consuming
// arms fall back to their pre-ctx behaviour (no reg* qualification / UTC
// session) when ctx is nil, so this remains safe. M0119-0006 reg* element
// slice.
func encodeValuePGCtx(t catalog.Type, d Datum, ctx *Context, pos int) ([]byte, error) {
	// A user array column (e.g. `p int4[]`) carries Type.Name="int4" plus
	// Type.IsArray=true; its value is the array text "{1,2}". Encode it as a
	// PG-native ArrayType varlena blob BEFORE the element-type switch (which
	// would otherwise try to parse "{1,2}" as a scalar int4). M0118-0002.
	if t.IsArray {
		return encodeArrayValuePGCtx(t, d, ctx, pos)
	}
	switch strings.ToLower(t.Name) {
	case "bool", "boolean":
		var b bool
		switch d.Kind {
		case KindBool:
			b = d.BoolValue()
		case KindString:
			// A bare quoted literal (`INSERT INTO t(b) VALUES ('true')`) is
			// typed `unknown` upstream and reaches the column through boolin
			// (postgres/src/backend/utils/adt/bool.c), so PG loads every
			// pg_dump / COPY-style script that quotes its booleans. Sibling to
			// the KindString arms every other scalar case below already has.
			var ok bool
			b, ok = pgBoolIn(d.StringValue())
			if !ok {
				return nil, &ExecError{Code: "22P02",
					Message: fmt.Sprintf("invalid input syntax for type boolean: %q", d.StringValue())}
			}
		default:
			return nil, fmt.Errorf("expected bool, got kind %d", d.Kind)
		}
		if b {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case "int2", "smallint", "smallserial", "serial2":
		var v int64
		switch d.Kind {
		case KindInt:
			v = d.Int
		case KindNumeric:
			var err error
			v, err = roundNumericToInt(d, 0)
			if err != nil {
				return nil, err
			}
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
	case "int4", "integer", "int", "serial", "serial4":
		var v int64
		switch d.Kind {
		case KindInt:
			v = d.Int
		case KindNumeric:
			var err error
			v, err = roundNumericToInt(d, 0)
			if err != nil {
				return nil, err
			}
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
	case "int8", "bigint", "bigserial", "serial8":
		var v int64
		switch d.Kind {
		case KindInt:
			v = d.Int
		case KindNumeric:
			var err error
			v, err = roundNumericToInt(d, 0)
			if err != nil {
				return nil, err
			}
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
	case "oid", "regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation", "cid":
		v, err := regIdentifierOIDFromDatum(d, strings.ToLower(t.Name))
		if err != nil {
			return nil, err
		}
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], v)
		return buf[:], nil
	case "timestamp", "timestamptz":
		if d.Kind == KindString {
			// 'infinity' / '-infinity' / 'today' / ... (#5(d-iv), M0134-0182).
			if inf, ok := parseTimestampSpecialLiteral(d.StringValue(), nowFromCtx(ctx), isTimestampTZTypeName(t.Name)); ok {
				d = inf
			} else {
				// A zone in the text belongs to the value only for timestamptz;
				// `timestamp` decodes and discards it (tsZoneMode).
				//
				// M0134-0026 EXTENSION (round 2, coordinator-authorised): a
				// zone-less timestamptz reads as local wall-clock time in the
				// SESSION TimeZone GUC and converts to UTC (DecodeDateTime,
				// postgres/src/backend/utils/adt/datetime.c:1573-1583), exactly
				// as the typed-literal (evalTypedStringLit) and CAST (evalCast)
				// paths were fixed in round 1. Without this, goopg was
				// internally INCONSISTENT: `INSERT INTO t(tstz) VALUES
				// ('2006-08-13 12:34:56')` (reaches this arm) stored a
				// different instant than `... VALUES ('2006-08-13
				// 12:34:56'::timestamptz)` (reaches evalCast) — worse than
				// uniformly wrong. timeZoneFromCtx(ctx) is nil-ctx-safe (falls
				// back to "" = UTC, matching the pre-extension behaviour
				// exactly): several callers of encodeValuePGCtx pass a nil ctx
				// (EncodeRowPG's toast.go/sys_pg_sequence.go/
				// sys_pg_database.go/operators_vacuum_datfrozenxid.go chunk and
				// catalog-row encoders, all of internal/initdb's bootstrap row
				// encoders) — none of those are user INSERT/UPDATE paths and
				// none carry a KindString timestamptz value through this arm in
				// practice, but the fallback keeps them behaviourally identical
				// either way. The real INSERT/UPDATE row encoder
				// (operators_storage.go's EncodeRowPGCtx call sites) DOES pass
				// a live session ctx.
				ts, err := parseCopyTimestampZoneSession(d.StringValue(), tsZoneModeForType(t.Name), timeZoneFromCtx(ctx))
				if err != nil {
					return nil, &ExecError{Code: "22007", Pos: 0, Message: fmt.Sprintf("invalid input syntax for type timestamp: %q", d.StringValue())}
				}
				d = NewTimeDatum(ts.UTC())
			}
		}
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		// PG epoch: 2000-01-01 UTC, in microseconds. goopg stores UnixNano
		// internally; we encode PG-compatible microseconds since the PG epoch.
		// The ±infinity sentinels serialise to PG's DT_NOEND / DT_NOBEGIN wire
		// value = PG_INT64_MAX / PG_INT64_MIN micros (timestamp_send). (#5(d-iv))
		var micros int64
		switch {
		case d.IsTimestampPosInf():
			micros = math.MaxInt64
		case d.IsTimestampNegInf():
			micros = math.MinInt64
		default:
			micros = d.TimeValue().UnixMicro() - pgEpochUnixMicros
		}
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(micros))
		return buf[:], nil
	case "date":
		if d.Kind == KindString {
			// 'infinity' / '-infinity' / 'today' / ... (#5(d-iv), M0134-0182).
			if inf, ok := parseDateSpecialLiteral(d.StringValue(), nowFromCtx(ctx)); ok {
				d = inf
			} else {
				// date_in never looks at the decoded zone, nor at an hour-24 /
				// leap-second day carry, so the wall clock as written picks the
				// day (parseDateInputText).
				ts, err := parseDateInputText(d.StringValue())
				if err != nil {
					return nil, &ExecError{Code: "22007", Pos: 0, Message: fmt.Sprintf("invalid input syntax for type date: %q", d.StringValue())}
				}
				d = NewTimeDatum(ts.UTC())
			}
		}
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		// PG date: days since 2000-01-01 (Julian-style). The ±infinity sentinels
		// serialise to PG's DATEVAL_NOEND / DATEVAL_NOBEGIN wire value =
		// PG_INT32_MAX / PG_INT32_MIN days (date_send). (#5(d-iv))
		var days int32
		switch {
		case d.IsTimestampPosInf():
			days = math.MaxInt32
		case d.IsTimestampNegInf():
			days = math.MinInt32
		default:
			t := d.TimeValue()
			micros := t.UnixMicro() - pgEpochUnixMicros
			days = int32(micros / (24 * 3600 * 1000000))
		}
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
		// Apply the column's declared precision here too, as the storage-choke
		// point: the input sites (coerce/copy/cast) already round, and this arm
		// is the safety net for paths that bypass them — a DEFAULT or generated
		// column's value is skipped by coerceRowForConstraintChecks's
		// !insertMissing filter, and reaches here as a KindString. Rounding is
		// idempotent, so a value already rounded at input is untouched.
		// M0119-0006 (62nd slice).
		d = roundTimeDatumToPrecision(d, timeColumnPrecision(t))
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(pgTimeMicros(d.TimeValue())))
		return buf[:], nil
	case "timetz":
		if d.Kind == KindString {
			ts, offsetSecs, err := parseTimeTZString(d.StringValue(), "")
			if err != nil {
				return nil, err
			}
			d = NewTimeTZDatum(ts, offsetSecs)
		}
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		d = roundTimeDatumToPrecision(d, timeColumnPrecision(t))
		var buf [12]byte
		binary.LittleEndian.PutUint64(buf[:8], uint64(pgTimeMicros(d.TimeValue())))
		// PG wire stores timezone offset as int32 seconds, positive = west of UTC.
		// Our Scale stores minutes east of UTC; convert: pgOffset = -Scale*60.
		pgOffset := int32(-d.TimeTZOffsetSecs())
		binary.LittleEndian.PutUint32(buf[8:], uint32(pgOffset))
		return buf[:], nil
	case "interval":
		// PG's Interval is a fixed 16-byte, 8-byte-aligned struct
		// (postgres/src/include/datatype/timestamp.h):
		//
		//	typedef struct { TimeOffset time; int32 day; int32 month; } Interval;
		//
		// so time (int64 microseconds) sits at offset 0, day at 8, month at 12
		// — pg_type row {OID 1186, typlen 16, typalign 'd', typbyval false},
		// seeded that way in internal/initdb/pg_type_seed_data.go since the
		// beginning. goopg nevertheless stored an interval column through the
		// varlena default arm, i.e. as the *text* the user typed, which made
		// every runtime interval operation lexicographic: `ORDER BY i` sorted
		// '2 hours' after '10 days', `i > interval '10 days'` kept '2 hours'
		// and dropped '1 mon', `i = interval '30 days'` missed the '1 mon' PG
		// calls equal, and the value echoed back verbatim ('2 hours') where PG
		// prints '02:00:00'. Storing the three fields is what makes compareDatum's
		// existing interval_cmp_value port (expr.go, KindInterval arm) and
		// formatInterval reachable for a stored column at all.
		//
		// The ±infinity sentinels need no special case: INTERVAL_NOEND /
		// INTERVAL_NOBEGIN *are* all-fields-at-their-extreme, so field-wise
		// storage round-trips them exactly.
		// Apply the column's declared typmod here too, as the storage-choke
		// point: the input sites (coerce/copy) already round, and this arm is
		// the safety net for paths that bypass them — a DEFAULT or generated
		// column's value is skipped by coerceRowForConstraintChecks's
		// !insertMissing filter and reaches here as a KindString. Rounding is
		// idempotent, so a value already rounded at input is untouched.
		// M0119-0006 (63rd slice).
		d, err := roundIntervalDatumToTypmod(d, intervalColumnTypmod(t))
		if err != nil {
			return nil, err
		}
		months, days, micros, err := pgIntervalFieldsFromDatum(d)
		if err != nil {
			return nil, err
		}
		var buf [16]byte
		binary.LittleEndian.PutUint64(buf[:8], uint64(micros))
		binary.LittleEndian.PutUint32(buf[8:12], uint32(days))
		binary.LittleEndian.PutUint32(buf[12:16], uint32(months))
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
		// M0111-0002 CLOSED: PG-binary float4 (4-byte IEEE-754 LE), matching
		// pgPhysicalTypeIsVarlena + the attlen=4 descriptors goopg has
		// always written. The former text-varlena encoding misaligned every
		// column AFTER a float on real-PG reads (SRF pg_proc rows; pg_enum's
		// "ppy" corruption) — the descriptors said fixed, the bytes said
		// varlena, and only padding luck hid it.
		f, err := pgFloatFromDatum(d, 32)
		if err != nil {
			return nil, err
		}
		var buf4 [4]byte
		binary.LittleEndian.PutUint32(buf4[:], math.Float32bits(float32(f)))
		return buf4[:], nil
	case "float8", "double precision", "double", "float":
		// M0111-0002 CLOSED: PG-binary float8 (8-byte IEEE-754 LE) — see the
		// float4 arm above.
		f, err := pgFloatFromDatum(d, 64)
		if err != nil {
			return nil, err
		}
		var buf8 [8]byte
		binary.LittleEndian.PutUint64(buf8[:], math.Float64bits(f))
		return buf8[:], nil
	case "xid":
		// PG TransactionId: 4-byte unsigned LE (pg_type OID 28, typlen 4).
		v, err := pgUnsignedIDFromDatum(d, "xid", 32)
		if err != nil {
			return nil, err
		}
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], uint32(v))
		return buf[:], nil
	case "xid8":
		// M0119-0006 (54th slice): xid8 shared the `xid` arm and was written as
		// FOUR bytes, silently truncating a FullTransactionId to its low 32
		// bits. pg_type OID 5069 is typlen **8**, typbyval FLOAT8PASSBYVAL,
		// typalign 'd' (postgres/src/include/catalog/pg_type.dat) — goopg's own
		// internal/initdb/pg_type_seed_data.go:190 already seeds Len: 8 — so the
		// heap disagreed with the catalog it ships by four bytes AND by the
		// alignment of every column after it, which is exactly what a hosted PG
		// deforming the tuple with its own descriptor reads. See
		// physicalPGTypeAlign, whose 'd' arm this slice also gained.
		v, err := pgUnsignedIDFromDatum(d, "xid8", 64)
		if err != nil {
			return nil, err
		}
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], v)
		return buf[:], nil
	case "numeric", "decimal":
		// M0119-0006: PG's base-10000 NumericData varlena (numeric.c
		// make_result), not the decimal string this arm used to store. The
		// string was a heap-format divergence of exactly the class the uuid
		// and interval slices closed — pg_type says numeric is a varlena, so
		// the descriptor never caught it, but a reader that trusts the TYPE
		// (a PG 18.3 standby, pg_amcheck's heap tier, a logical subscriber)
		// hands the payload to numeric_out as a NumericData and reads the
		// first two ASCII characters as n_header. It also mis-ORDERED a
		// PG-format index tuple, which is why pgIndexKeyImageIsPGFaithful had
		// to refuse numeric until now.
		//
		// coerceTextLikeDatum still produces the text first: it yields the
		// decimal string for KindNumeric/KindInt and passes a KindString
		// through verbatim (e.g. INSERT ... VALUES ('123.45') arriving as
		// text), where numericText alone reads only the Int/Scale fields and
		// would emit "0" for a KindString (M0111-0002). The text is then the
		// input to numeric_in's port, so no precision is lost that the Datum
		// was carrying.
		s, err := coerceTextLikeDatum(t, d)
		if err != nil {
			return nil, err
		}
		body, nerr := nodes.NumericBodyFromText(s)
		if nerr != nil {
			return nil, &ExecError{Code: "22P02",
				Message: fmt.Sprintf("invalid input syntax for type numeric: %q", s)}
		}
		return varlenaBytes(body), nil
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
	case "bytea":
		// A bytea column value is BYTES. Before M0125-0021 this fell to the
		// default text arm, so `INSERT INTO t VALUES ('\xaabb')` stored the six
		// characters of the escape text and `length(b)` answered 6 where PG
		// answers 2. A KindString reaching here is an unknown-type literal that
		// PG would have routed through byteain, so route it through byteaIn —
		// the same helper `'\xaabb'::bytea` uses (Hard-won Rule #2: the cast
		// and the storage encoder are siblings).
		switch d.Kind {
		case KindBytes:
			return varlenaPayloadBytes(d.BytesValue()), nil
		case KindString:
			b, err := byteaIn(d.StringValue(), 0)
			if err != nil {
				return nil, err
			}
			return varlenaPayloadBytes(b), nil
		default:
			s, err := coerceTextLikeDatum(t, d)
			if err != nil {
				return nil, err
			}
			return varlenaTextBytes(s), nil
		}
	case "uuid":
		// PG's uuid is a fixed 16-byte, 1-byte-aligned, PLAIN-storage type
		// (pg_type OID 2950: typlen 16, typalign 'c', typstorage 'p' —
		// postgres/src/include/utils/uuid.h `struct pg_uuid_t { unsigned char
		// data[UUID_LEN]; }`), and internal/initdb/pg_type_seed_data.go has
		// seeded exactly that row since initdb existed. goopg nevertheless
		// stored the 36-character canonical TEXT through varlenaTextBytes, so
		// the heap disagreed with its own catalog by 21 bytes and by the
		// varlena header: a PG standby reading a goopg uuid column takes the
		// first text byte as a varlena length header and returns garbage,
		// and the attcacheoff fast path (heaptuple.c nocachegetattr) walks
		// past the column with attlen 16 while the value occupies 37.
		//
		// Only the STORAGE changes here — the in-memory Datum stays the
		// canonical KindString the rest of the engine (index keys,
		// comparisons, output) already speaks, and lowercase-hex text order is
		// the same order as uuid_cmp's memcmp over these bytes, so no answer
		// moves. M0119-0006 (was M0097-0029).
		if d.Kind != KindString {
			return nil, fmt.Errorf("expected string for uuid, got kind %d", d.Kind)
		}
		s := d.StringValue()
		if !isValidUUIDStr(s) {
			return nil, &ExecError{Code: "22P02",
				Message: fmt.Sprintf("invalid input syntax for type uuid: %q", s)}
		}
		raw, ok := uuidBytesFromCanonical(normalizeUUIDStr(s))
		if !ok {
			return nil, &ExecError{Code: "22P02",
				Message: fmt.Sprintf("invalid input syntax for type uuid: %q", s)}
		}
		return append([]byte(nil), raw[:]...), nil
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
	case "xml":
		// M0134-0188: the physical-encode path is a SIBLING of evalCast's
		// `::xml` arm (xmltypes.go) and must apply the same well-formedness
		// gate — otherwise `INSERT INTO t(x xml) VALUES ('<wrong')` (an
		// IMPLICIT column coercion, which never routes through evalCast)
		// stored the malformed fragment while an explicit `::xml` cast on
		// the same string correctly raised 2200N/2200M
		// (pattern_sibling_paths_must_agree).
		s, err := coerceTextLikeDatum(t, d)
		if err != nil {
			return nil, err
		}
		if ee := xmlValidate(s, xmlOptionFromCtx(ctx)); ee != nil {
			ee.Pos = pos
			return nil, ee
		}
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
// varlenaPayloadBytes is varlenaTextBytes for a byte payload that is not text
// (bytea). Same on-disk shape — the distinction is only in what the caller
// means by it.
func varlenaPayloadBytes(b []byte) []byte {
	return varlenaTextBytes(string(b))
}

// varlenaBytes is varlenaTextBytes for a payload that is not text — the same
// header rule (PG's heap_fill_tuple prefers the 1-byte short header whenever
// the value fits and the attribute's storage is not 'p'), applied to raw bytes.
// Used by the numeric arm, whose payload is a NumericData body. M0119-0006.
func varlenaBytes(b []byte) []byte {
	total := len(b) + 1
	if total <= 127 {
		buf := make([]byte, total)
		buf[0] = byte(total<<1) | 1
		copy(buf[1:], b)
		return buf
	}
	total = len(b) + 4
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	copy(buf[4:], b)
	return buf
}

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

// pgTimeMicros extracts the microseconds-since-midnight that PG's TimeADT holds
// from the time.Time carrier of a `time`/`timetz` Datum.
//
// M0119-0006 (50th slice): the next-day probe is load-bearing, not defensive.
// `24:00:00` is a real TimeADT value on PG — time_in accepts it and
// AdjustTimeForTypmod's range check admits exactly USECS_PER_DAY
// (postgres/src/backend/utils/adt/date.c) — and goopg's parsers carry it as
// 1970-01-02 00:00:00 rather than as an hour field of 24, because time.Date
// normalises the hour. Reading only Hour/Minute/Second therefore reports 0 for
// it, which silently rewrote a STORED `'24:00:00'` to `'00:00:00'` (heap
// encode, codec.go "time"/"timetz") and sorted it BELOW `00:00:01` in a btree
// key.
//
// The probe belongs HERE rather than in each caller: row encode, the scalar and
// timetz btree keys (btree_scalar_keys.go — whose comment already states the key
// must derive from "the same microseconds the heap stores") and the array
// element renderer (btree_array_key.go) all want the identical USECS_PER_DAY,
// and copy_text.go's copyTimeOfDayMicros previously had to carry a private copy
// of it for exactly that reason.
func pgTimeMicros(t time.Time) int64 {
	u := t.UTC()
	micros := int64(u.Hour())*int64(time.Hour/time.Microsecond) +
		int64(u.Minute())*int64(time.Minute/time.Microsecond) +
		int64(u.Second())*int64(time.Second/time.Microsecond) +
		int64(u.Nanosecond()/1000)
	if micros == 0 && u.Year() == 1970 && u.Month() == time.January && u.Day() == 2 {
		return usecsPerDay
	}
	return micros
}

func pgTimeFromMicros(micros int64) time.Time {
	return time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(micros) * time.Microsecond)
}

// roundTimeDatumToPrecision rounds a `time`/`timetz` Datum to the declared
// fractional-second precision (0..6) the way upstream time_in/timetz_in round
// via AdjustTimeForTypmod (postgres/src/backend/utils/adt/date.c:1710). It is
// a no-op for a non-time datum or a precision outside [0,6].
//
// The round-trip pgTimeMicros → AdjustTimeForTypmod → pgTimeFromMicros is what
// expresses the carry goopg's former OUTPUT-side truncation could not: at
// precision 2, `'23:59:59.999999'` becomes 24:00:00 (usecsPerDay), because the
// hour-24 rule the 50th slice moved into pgTimeMicros/pgTimeFromMicros reads the
// rounded value back as next-day midnight. The timetz subtype's offset rides in
// Datum.Scale and is preserved untouched, exactly as AdjustTimeForTypmod leaves
// the zone half of a TimeTzADT alone. M0119-0006 (62nd slice).
func roundTimeDatumToPrecision(d Datum, prec int64) Datum {
	if d.Kind != KindTime || prec < 0 || prec > 6 {
		return d
	}
	micros := datetime.AdjustTimeForTypmod(pgTimeMicros(d.TimeValue()), int32(prec))
	tv := pgTimeFromMicros(micros)
	if d.TimeSub == TimeSubTimeTZ {
		return NewTimeTZDatum(tv, d.TimeTZOffsetSecs())
	}
	return NewTimeDatum(tv)
}

// timeColumnPrecision returns the declared fractional-second precision of a
// `time`/`timetz` column (t.Args[0]), or -1 when no precision is declared.
func timeColumnPrecision(t catalog.Type) int64 {
	if len(t.Args) == 0 {
		return -1
	}
	return t.Args[0]
}

// intervalColumnTypmod returns the declared interval typmod of an interval
// column (t.Args[0], the packed INTERVAL_TYPMOD the parser stores for
// `interval(N)` / `interval <field> [TO <lo>] [(p)]`), or -1 when the column
// is a bare `interval` with no modifier.
func intervalColumnTypmod(t catalog.Type) int64 {
	if len(t.Args) == 0 {
		return -1
	}
	return t.Args[0]
}

// roundIntervalDatumToTypmod applies an interval column's declared typmod to a
// Datum the way upstream interval_in/interval_recv do at INPUT via
// AdjustIntervalForTypmod (timestamp.c:1355): it parses a KindString body
// through the SAME pgIntervalFieldsFromDatum tokenizer encodeValuePG uses
// (parser.ParseIntervalBody, the full grammar — not evalCast's limited
// `<n> <unit>` arm), zeroes the range fields outside the declared span, and
// rounds the sub-second field to the declared SECOND(p) precision. A null
// datum, an absent typmod (typmod < 0) and the ±infinity sentinel are all
// returned unchanged — the last because AdjustIntervalForTypmod no-ops on it,
// exactly as upstream. M0119-0006 (63rd slice).
func roundIntervalDatumToTypmod(d Datum, typmod int64) (Datum, error) {
	if d.IsNull() || typmod < 0 {
		return d, nil
	}
	months, days, micros, err := pgIntervalFieldsFromDatum(d)
	if err != nil {
		return Datum{}, err
	}
	months, days, micros = datetime.AdjustIntervalForTypmod(months, days, micros, int32(typmod))
	return NewIntervalDatumFull(months, days, micros), nil
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
func DecodeRowIntoMctx(dst Row, cols []catalog.Column, data []byte, sctx *mmgr.Context) error {
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
func DecodeRowIntoMctxPGTuple(dst Row, cols []catalog.Column, data, bitmap []byte, storedNatts int, sctx *mmgr.Context) error {
	return DecodeRowIntoMctxPGTupleStyled(dst, cols, data, bitmap, storedNatts, sctx, array.DefaultOutputStyle())
}

// DecodeRowIntoMctxPGTupleStyled is DecodeRowIntoMctxPGTuple carrying the
// session's array output style. Only ARRAY columns of a date/timestamp/
// timestamptz element type read it — every other column type's text is
// GUC-independent at this layer, because the scalar date-time types keep their
// KindTime carrier and are formatted at OUTPUT time (internal/server's
// appendTypedCellText, executor's datumToCopyText), where the GUCs already
// reach. An array is the one type goopg flattens to text during the heap
// decode, so it is the one type that needs the GUCs here.
//
// The plain (unstyled) entry point stays the default for the ~70 session-less
// decode sites — catalog reload, VACUUM, ANALYZE, DDL rescans — which have no
// GUCs to read and must not acquire a dependency on any. M0119-0006.
func DecodeRowIntoMctxPGTupleStyled(dst Row, cols []catalog.Column, data, bitmap []byte, storedNatts int, sctx *mmgr.Context, st array.OutputStyle) error {
	_, err := DecodeRowRangeIntoMctxPGTupleStyled(dst, cols, data, bitmap, storedNatts, sctx, st, 0, len(cols), 0)
	return err
}

// DecodeRowRangeIntoMctxPGTupleStyled decodes columns [from, to) of a tuple,
// resuming at byte offset `off`, and returns the offset reached. Decoding the
// whole row is the `from=0, to=len(cols), off=0` case, which is what
// DecodeRowIntoMctxPGTupleStyled asks for.
//
// The range form exists so a scan can deform only the columns its own
// predicate reads, test that predicate, and pay for the remaining columns only
// on the rows that survive — PostgreSQL's slot_getsomeattrs discipline
// (postgres/src/backend/executor/execTuples.c). A physical tuple has no column
// offset array, so a suffix can be skipped but a prefix cannot; the returned
// offset is what makes resumption exact, and it must be threaded back
// unchanged or the second half decodes garbage.
//
// Callers that stop early MUST NOT let the partially-filled row escape:
// entries at or past `to` still hold whatever the previous tuple left there.
// See seqScanOp.Next, which keeps the partial row strictly inside its own
// prefilter (docs/design/not_ralph/tpch-q6-numeric-decode/).
func DecodeRowRangeIntoMctxPGTupleStyled(dst Row, cols []catalog.Column, data, bitmap []byte, storedNatts int, sctx *mmgr.Context, st array.OutputStyle, from, to, off int) (int, error) {
	return decodeRowRangeInfo(dst, cols, nil, data, bitmap, storedNatts, sctx, st, from, to, off)
}

// decodeRowRangeInfo is DecodeRowRangeIntoMctxPGTupleStyled with an optional
// pre-resolved per-column type descriptor (resolveColTypeInfo). Pass nil and it
// resolves each column's type name per value exactly as before; pass a slice
// resolved once in the operator's Open and the per-value string work
// disappears. See colTypeInfo.
func decodeRowRangeInfo(dst Row, cols []catalog.Column, info []colTypeInfo, data, bitmap []byte, storedNatts int, sctx *mmgr.Context, st array.OutputStyle, from, to, off int) (int, error) {
	// PG-physical decode with null-bitmap and natts awareness. M0111-0002 S3:
	// the goopg legacy format has been removed, so there is a single on-disk
	// format. A PG-physical tuple always records natts; storedNatts==0 means a
	// header-less body (e.g. via the bare DecodeRow wrappers in tests) — treat
	// all columns as present.
	n := len(cols)
	if storedNatts == 0 {
		storedNatts = n
	}
	if to > n {
		to = n
	}
	for i := from; i < to; i++ {
		c := cols[i]
		// Columns beyond stored natts were added via ALTER TABLE ADD COLUMN.
		// M0097-0077: when the column has a precomputed MissingValue Datum
		// (set by ALTER TABLE ADD COLUMN … DEFAULT <const>), surface it
		// instead of NULL — the "fast default" path that avoids a table
		// rewrite, mirroring PostgreSQL's `attmissingval`.
		if i >= storedNatts {
			if mv, ok := c.MissingValue.(Datum); ok {
				dst[i] = mv
			} else {
				dst[i] = NullDatum
			}
			continue
		}
		// Check null bitmap: bit i = 0 means column i is NULL.
		if len(bitmap) > 0 && (bitmap[i/8]>>(uint(i)%8))&1 == 0 {
			dst[i] = NullDatum
			continue
		}
		var (
			align int
			tname string
		)
		if info != nil {
			align, tname = info[i].align, info[i].lower
		} else {
			tname = strings.ToLower(c.Type.Name)
			align = physicalPGTypeAlignLowered(c.Type, tname)
		}
		off = alignPhysicalPGOffset(off, align)
		if off >= len(data) {
			// Data exhausted — treat remaining columns as NULL.
			dst[i] = NullDatum
			continue
		}
		v, consumed, err := decodePhysicalPGValueLowered(c.Type, tname, data[off:], sctx, st)
		if err != nil {
			return off, fmt.Errorf("DecodePhysicalPGRow: %s: %w", c.Name, err)
		}
		dst[i] = v
		off += consumed
	}
	return off, nil
}

// DecodeHeapTupleRowInto fills dst from a heap tuple, selecting the row format
// deterministically from the tuple header (natts + null bitmap) rather than
// guessing from the bytes. This is the header-driven replacement for the bare
// DecodeRowInto on any read path that holds the storage.HeapTuple. M0111-0002.
func DecodeHeapTupleRowInto(dst Row, cols []catalog.Column, tuple storage.HeapTuple, sctx *mmgr.Context) error {
	natts := int(tuple.Header.Infomask2 & storage.HeapNattsMask)
	return DecodeRowIntoMctxPGTuple(dst, cols, tuple.Data, tuple.Bitmap, natts, sctx)
}

// DecodeHeapTupleRow is the allocating sibling of DecodeHeapTupleRowInto: it
// returns a freshly-allocated Row. Use it where the bare DecodeRow was used on
// a path that holds the storage.HeapTuple. M0111-0002.
func DecodeHeapTupleRow(cols []catalog.Column, tuple storage.HeapTuple, sctx *mmgr.Context) (Row, error) {
	row := make(Row, len(cols))
	if err := DecodeHeapTupleRowInto(row, cols, tuple, sctx); err != nil {
		return nil, err
	}
	return row, nil
}

func decodeRowIntoMctx(dst Row, cols []catalog.Column, data []byte, sctx *mmgr.Context) error {
	// M0111-0002 S3: single on-disk format. The bare DecodeRow*/DecodeRowInto*
	// wrappers (header-less paths, used by tests) decode PG-physical bodies
	// only; the goopg legacy format and the format-guessing fallback were
	// removed once all writes became PG-physical and re-init was mandated.
	return decodePhysicalPGRowIntoMctx(dst, cols, data, sctx)
}

func decodePhysicalPGRowIntoMctx(dst Row, cols []catalog.Column, data []byte, sctx *mmgr.Context) error {
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
	return physicalPGTypeAlignLowered(t, strings.ToLower(t.Name))
}

// physicalPGTypeAlignLowered is physicalPGTypeAlign with the lowercased type
// name supplied. See decodePhysicalPGValueLowered for why.
func physicalPGTypeAlignLowered(t catalog.Type, tname string) int {
	// All array columns store a varlena ArrayType blob → PG 'i' (4-byte) align.
	// M0118-0002.
	if t.IsArray {
		return 4
	}
	switch tname {
	case "bool", "boolean":
		return 1
	case "char":
		// Single-byte internal "char" type: alignment 1.
		// char(N) with length modifier is bpchar (varlena): alignment 4.
		if len(t.Args) == 0 {
			return 1
		}
		return 4
	case "int2", "smallint", "smallserial", "serial2":
		return 2
	case "int4", "integer", "int", "serial", "serial4", "oid", "regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation", "cid", "float4", "real", "date", "xid":
		return 4
	case "int8", "bigint", "bigserial", "serial8", "pg_lsn", "float8", "double precision", "double", "timestamp", "timestamptz", "time", "timetz",
		// interval is typalign 'd' (pg_type OID 1186) even though its 16 bytes
		// exceed a Datum — the struct's leading field is an int64.
		"interval",
		// xid8 is typalign 'd' (pg_type OID 5069, typlen 8) — it had been
		// falling through to the default 4 while its 4-byte encode hid the
		// consequence. M0119-0006 (54th slice); `xid` (OID 28) stays 'i' above.
		"xid8":
		return 8
	case "name",
		// uuid is pg_type OID 2950: typlen 16, typalign 'c'. Its 16 bytes
		// exceed a Datum but carry no field wider than a byte. M0119-0006.
		"uuid":
		return 1 // PG 'c' alignment (fixed-size, 1-byte aligned)
	case "aclitem[]", "_aclitem", "text[]", "_text", "oid[]", "_oid", "int2[]", "_int2", "char[]", "_char", "float4[]", "_float4", "pg_node_tree", "oidvector", "int2vector":
		return 4 // PG 'i' alignment for varlena ArrayType / pg_node_tree / oidvector / int2vector
	case "anyarray":
		// anyarray (OID 2277) is typalign='d' — 8 bytes, NOT the 'i' every
		// other varlena array uses (postgres/src/include/catalog/pg_type.dat:573).
		// Its two catalog users are pg_attribute.attmissingval and
		// pg_statistic.stavalues1..5; a hosted PG deforms both with its own
		// compiled descriptor, so 4-byte padding here put every following
		// byte one word early. Sibling of initdb.pgTypeAlignChar(2277), which
		// declares the same 'd' in the nailed self-description. M0131-S14.2.
		return 8
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
		"int2", "smallint", "smallserial", "serial2",
		"int4", "integer", "int", "serial", "serial4",
		"int8", "bigint", "bigserial", "serial8",
		"pg_lsn",
		"oid", "regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation", "cid",
		"timestamp", "timestamptz", "date", "time", "timetz",
		"interval", // typlen 16, not varlena
		"uuid",     // typlen 16, typalign 'c', typstorage 'p' — not varlena
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

// pgRowHasExternal reports whether row contains at least one KindToastPointer
// value, meaning the encoded heap tuple carries an external TOAST reference.
// This drives the HEAP_HASEXTERNAL bit in the tuple infomask, mirroring PG's
// heap_fill_tuple (postgres/src/backend/access/common/heaptuple.c:343) which
// stamps the bit when a varlena value's storage is TOAST-external.
func pgRowHasExternal(cols []catalog.Column, row Row) bool {
	n := len(cols)
	if len(row) < n {
		n = len(row)
	}
	for i := 0; i < n; i++ {
		if row[i].Kind == KindToastPointer {
			return true
		}
	}
	return false
}

func decodePhysicalPGValueMctx(t catalog.Type, data []byte, sctx *mmgr.Context) (Datum, int, error) {
	return decodePhysicalPGValueMctxStyled(t, data, sctx, array.DefaultOutputStyle())
}

func decodePhysicalPGValueMctxStyled(t catalog.Type, data []byte, sctx *mmgr.Context, st array.OutputStyle) (Datum, int, error) {
	// User array column: decode the ArrayType varlena blob back to the
	// canonical "{1,2}" text (sibling of encodeValuePG's IsArray branch).
	// M0118-0002. The session DateStyle/TimeZone rides along because goopg
	// renders the element text here, where upstream's array_out would render it
	// at output time (M0119-0006).
	return decodePhysicalPGValueLowered(t, strings.ToLower(t.Name), data, sctx, st)
}

// decodePhysicalPGValueLowered is decodePhysicalPGValueMctxStyled with the
// lowercased type name supplied by the caller.
//
// The type of a column does not change between the rows of one scan, but this
// switch used to recompute strings.ToLower(t.Name) for EVERY VALUE of every
// row — as did physicalPGTypeAlign and isTimestampTZTypeName, giving three
// scans of the same string per value. Together they measured 4.64 % of TPC-H
// Q14's CPU and 6.13 % of Q3's.
//
// PostgreSQL has no string on this path at all: heap_deform_tuple reads
// attlen / attbyval / attalignby out of the TupleDesc, resolved once when the
// relation is opened (postgres/src/backend/access/common/heaptuple.c).
// resolveColTypeInfo is goopg's equivalent, and callers that hold a column list
// should resolve once and pass it down.
// (docs/design/not_ralph/perf-optimize-take6/README.md candidate A.)
func decodePhysicalPGValueLowered(t catalog.Type, tname string, data []byte, sctx *mmgr.Context, st array.OutputStyle) (Datum, int, error) {
	if t.IsArray {
		return decodeArrayValuePGStyled(t, data, st)
	}
	switch tname {
	case "bool", "boolean":
		if len(data) < 1 {
			return Datum{}, 0, fmt.Errorf("truncated bool")
		}
		return NewBoolDatum(data[0] != 0), 1, nil
	case "int2", "smallint", "smallserial", "serial2":
		if len(data) < 2 {
			return Datum{}, 0, fmt.Errorf("truncated int2")
		}
		return NewIntDatum(int64(int16(binary.LittleEndian.Uint16(data[:2])))), 2, nil
	case "int4", "integer", "int", "serial", "serial4":
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated int4")
		}
		return NewIntDatum(int64(int32(binary.LittleEndian.Uint32(data[:4])))), 4, nil
	case "int8", "bigint", "bigserial", "serial8":
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
	case "oid", "regproc", "regprocedure", "regclass", "regtype", "regrole", "regcollation", "cid":
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated oid")
		}
		return NewIntDatum(int64(binary.LittleEndian.Uint32(data[:4]))), 4, nil
	case "xid":
		// encodeValuePG writes xid as a 4-byte LE TransactionId.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated xid")
		}
		return NewIntDatum(int64(binary.LittleEndian.Uint32(data[:4]))), 4, nil
	case "xid8":
		// Decode twin of the "xid8" encode arm — 8 LE bytes, pg_type 5069's
		// typlen. Splitting it out of the shared "xid" arm is the decode half of
		// the 54th slice's truncation fix; leaving it here would have read four
		// bytes of an eight-byte column and left the next column's offset short.
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated xid8")
		}
		return NewIntDatum(int64(binary.LittleEndian.Uint64(data[:8]))), 8, nil
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
		// PG's DT_NOEND / DT_NOBEGIN sentinels (PG_INT64_MAX / PG_INT64_MIN
		// micros) decode to the ±infinity KindTime carrier; adding the epoch
		// offset would overflow, so intercept first. (unimplemented_feat #5(d-iv))
		switch micros {
		case math.MaxInt64:
			return NewTimestampInfinity(true), 8, nil
		case math.MinInt64:
			return NewTimestampInfinity(false), 8, nil
		}
		// M0119-0006 (40th slice): tag the timestamptz half, exactly as the
		// "date" case below tags TimeSubDate and for the same reason — a
		// storage-decoded timestamptz must render identically to a timestamptz
		// literal in the type-agnostic paths (Datum.Format(), CAST-to-text,
		// string concat). The wire path re-derives the type from the column and
		// is unaffected either way.
		ts := time.UnixMicro(micros + pgEpochUnixMicros).UTC()
		// tname, not isTimestampTZTypeName(t.Name): this arm is only reached
		// via `case "timestamp", "timestamptz"`, so tname is already exactly
		// one of those two literals and the general predicate — which
		// TrimSpaces and ToLowers the raw name on every value — can only
		// return the same answer. Removing it is the last of the three
		// per-value string scans colTypeInfo exists to eliminate.
		if tname == "timestamptz" {
			return NewTimestampTZDatum(ts), 8, nil
		}
		return NewTimeDatum(ts), 8, nil
	case "date":
		// encodeValuePG stores 4-byte LE days since the PG epoch. M0111-0004.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated date")
		}
		days := int32(binary.LittleEndian.Uint32(data[:4]))
		// PG's DATEVAL_NOEND / DATEVAL_NOBEGIN sentinels (PG_INT32_MAX /
		// PG_INT32_MIN days) decode to the ±infinity DATE carrier; the epoch
		// arithmetic below would overflow, so intercept first. (#5(d-iv))
		switch days {
		case math.MaxInt32:
			return NewDateInfinity(true), 4, nil
		case math.MinInt32:
			return NewDateInfinity(false), 4, nil
		}
		micros := int64(days)*24*3600*1000000 + pgEpochUnixMicros
		// Tag as DATE (TimeSubDate) so a storage-decoded date renders identically
		// to a date literal in type-agnostic paths (Datum.Format(): text casts,
		// string concat, array/composite element rendering). The wire path
		// (server dispatch) re-derives date formatting from the column type and
		// is unaffected. M0003 / 0003-0013 KindDate carrier gap.
		return NewDateDatum(time.UnixMicro(micros).UTC()), 4, nil
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
	case "interval":
		// Sibling of the "interval" arm in encodeValuePG: PG's fixed 16-byte
		// {time int64, day int32, month int32} at typalign 'd'. Every field is
		// stored raw, so the ±infinity sentinels round-trip without a case.
		if len(data) < 16 {
			return Datum{}, 0, fmt.Errorf("truncated interval")
		}
		micros := int64(binary.LittleEndian.Uint64(data[:8]))
		days := int32(binary.LittleEndian.Uint32(data[8:12]))
		months := int32(binary.LittleEndian.Uint32(data[12:16]))
		return NewIntervalDatumFull(months, days, micros), 16, nil
	case "char":
		// Single-byte internal "char" type (no length modifier): fixed 1-byte
		// field. "char(N)" (with args) is bpchar (varlena) and is handled by
		// the varlena branch below. M0097-0146.
		if len(t.Args) == 0 {
			if len(data) < 1 {
				return Datum{}, 0, fmt.Errorf("truncated char")
			}
			// Mirror encodeValuePGCtx's "char" branch (encodes "" as byte 0)
			// and charTypeDisplayForm's byte-0 rule (expr.go): a stored NUL
			// byte normalizes to the empty string, keeping encode/decode
			// symmetric. Other bytes decode to their raw 1-byte string form
			// unchanged (display-side octal escaping does not apply here).
			if data[0] == 0 {
				return NewStringDatum(""), 1, nil
			}
			return NewStringDatum(string(data[:1])), 1, nil
		}
		// char(N) = bpchar = varlena; fall through to varlena branch.
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
	case "text", "varchar", "character varying", "bpchar", "character", "unknown":
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
	case "float4", "real":
		// M0111-0002 CLOSED: fixed 4-byte IEEE-754 LE. Format via PGFloatOut
		// (shortest round-trip — exactly the text the old varlena encoding
		// stored) then reuse the numeric-parse tail, so downstream Datums
		// are byte-identical to the pre-flip decode.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("float4: short read")
		}
		f4 := float64(math.Float32frombits(binary.LittleEndian.Uint32(data[:4])))
		return floatTextDatum(PGFloatOut(f4, 32)), 4, nil
	case "float8", "double precision", "double", "float":
		// M0119-0006 (53rd slice): `float` was in the ENCODE arm's spelling list
		// but not this one, so a column whose DECLARED name is the bare `float`
		// (PG: float = float8, postgres/src/backend/parser/gram.y opt_float)
		// stored 8 fixed bytes and then failed to read them back — the decoder
		// fell to the varlena default and raised "truncated 4-byte varlena".
		// Encode and decode are twins (Hard-won Rule #2); the COPY-binary float
		// arms added this slice accept the same six spellings.
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("float8: short read")
		}
		f8 := math.Float64frombits(binary.LittleEndian.Uint64(data[:8]))
		return floatTextDatum(PGFloatOut(f8, 64)), 8, nil
	case "uuid":
		// Sibling of the "uuid" arm in encodeValuePG: PG's fixed 16-byte
		// pg_uuid_t at typalign 'c'. The Datum stays the canonical
		// lowercase-with-dashes KindString the engine speaks everywhere else,
		// rendered by uuid_out's port — so this is the only place the on-disk
		// bytes are seen. M0119-0006 (was M0097-0029, varlena-text).
		if len(data) < 16 {
			return Datum{}, 0, fmt.Errorf("truncated uuid")
		}
		s := uuidCanonicalFromBytes(data[:16])
		if sctx != nil {
			moff, mlen := sctx.AllocBytes([]byte(s))
			return newStringArenaDatum(sctx, moff, mlen), 16, nil
		}
		return NewStringDatum(s), 16, nil
	case "bytea":
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
			return newBytesArenaDatum(sctx, moff, mlen), n, nil
		}
		return NewBytesDatum(append([]byte(nil), payload...)), n, nil
	case "numeric", "decimal":
		// Sibling of the "numeric" arm in encodeValuePG: PG's base-10000
		// NumericData behind a varlena header (M0119-0006). The in-memory
		// Datum is unchanged — KindNumeric mantissa+scale — so this and the
		// encoder are the only two places the on-disk bytes are seen.
		//
		// NumericTextFromStoredPayload also accepts the pre-flip payload (the
		// decimal string): the flip has no on-disk migration, and every
		// cluster that predates it — the TPC-H and TPC-DS benchmark clusters
		// among them — holds text in its numeric columns. See that function
		// for why the two forms are exactly, not heuristically, disjoint.
		payload, n, err := decodePhysicalPGVarlena(data)
		if err != nil {
			return Datum{}, 0, err
		}
		// Decode the NumericData body straight into the mantissa+scale the
		// Datum carries. Both ends are exact integers with an explicit decimal
		// scale, so the old route through numeric_out text and math/big was
		// pure loss: it measured 46 % of all CPU and 6.07 allocations per
		// value on a TPC-H Q6 scan, whose lineitem has EIGHT numeric columns
		// (docs/design/not_ralph/tpch-q6-numeric-decode/DESIGN.md).
		//
		// ok=false hands the value to the unchanged text path below — legacy
		// text payloads, NaN/±Infinity, and mantissas past int64 all still go
		// exactly where they went before.
		if v, scale, ok := nodes.NumericInt64FromStoredPayload(payload); ok {
			return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
		}
		text, err := nodes.NumericTextFromStoredPayload(payload)
		if err != nil {
			return Datum{}, 0, err
		}
		if v, scale, ok := parseNumericFastInt(text); ok {
			return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
		}
		// Try known-scale fast path when the column type declares a scale
		// (all TPC-H NUMERIC columns).  Args[0]=precision, Args[1]=scale.
		// See docs/design/tpch-round5-fixes/06.
		if len(t.Args) >= 2 {
			if v, scale, ok := parseNumericFastScale(text, int16(t.Args[1])); ok {
				return Datum{Kind: KindNumeric, Int: v, Scale: scale}, n, nil
			}
		}
		// Fall through to big.Int slow path.
		m, s, err := parseNumeric(text)
		if err != nil {
			return Datum{}, 0, fmt.Errorf("decode numeric %q: %w", text, err)
		}
		return newNumeric(m, int(s)), n, nil
	case "pg_node_tree":
		// Sibling of encodeValuePG's "pg_node_tree" KindBytes passthrough.
		//
		// M0131-S20.2: a seeded pg_rewrite.ev_action whose inline form would
		// not fit a heap tuple is stored out of line, and the column then holds
		// an 18-byte VARTAG_ONDISK varatt_external pointer (varatt.h:38-48,
		// :89) into pg_toast.pg_toast_2618. Without this arm the value fell to
		// the generic `default` varlena branch, whose decodePhysicalPGVarlena
		// rejects header 0x01 with "external varlena not supported" — which
		// would have made initdb's pg_rewrite reload fail startup on a
		// directory goopg itself wrote (loadViewsFromHeapForDB decodes EVERY
		// pg_rewrite row before its ev_class filter discards the bootstrap
		// ones).
		//
		// The 18 bytes are returned verbatim as KindBytes rather than
		// resolved: reassembling them needs the TOAST heap and a buffer pool,
		// neither of which this decoder has, and no goopg reader consumes a
		// bootstrap ev_action (user rules store re-parsable SQL text and are
		// never toasted — "pg_node_tree" is not in isToastableType). A future
		// reader must detoast through the chunk index; see the deferral-ledger
		// row for M0131-S20.2.
		//
		// The 0x01/0x12 pair cannot collide with a data varlena: 0x01 alone is
		// a zero-length short varlena, which is unrepresentable.
		if len(data) >= 2 && data[0] == 0x01 && data[1] == 18 {
			if len(data) < 18 {
				return Datum{}, 0, fmt.Errorf("truncated on-disk TOAST pointer")
			}
			return NewBytesDatum(append([]byte(nil), data[:18]...)), 18, nil
		}
		payload, n, err := decodePhysicalPGVarlena(data)
		if err != nil {
			return Datum{}, 0, fmt.Errorf("decode pg_node_tree as varlena: %w", err)
		}
		if sctx != nil {
			moff, mlen := sctx.AllocBytes(payload)
			return newStringArenaDatum(sctx, moff, mlen), n, nil
		}
		return NewStringDatum(string(payload)), n, nil
	case "aclitem[]", "_aclitem":
		// A heap-backed catalog stores an ACL column (pg_type.typacl —
		// M0119-0004-ACLHEAP) as a PG-native _aclitem ArrayType varlena whose
		// 16-byte AclItem elements carry role/grantor OIDs, NOT text. The shared
		// `default` varlena branch below would hand back the raw bytes as a
		// KindString, which is meaningless to a SELECT. Return the FULL varlena
		// (including its 4-byte length header) as KindBytes so the pg_type scan
		// hook can render it as canonical aclitemout text via
		// decodeAclItemArrayText with role-name resolution from the catalog
		// (decodePhysicalPGValueMctx has no catalog handle of its own). Until a
		// type GRANT/REVOKE populates typacl this branch is dormant — every
		// existing pg_type row bakes typacl NULL, and pg_class.relacl is served
		// from the virtual builder, never decoded here.
		_, n, err := decodePhysicalPGVarlena(data)
		if err != nil {
			return Datum{}, 0, fmt.Errorf("decode %q as varlena: %w", t.Name, err)
		}
		return NewBytesDatum(append([]byte(nil), data[:n]...)), n, nil
	case "text[]", "_text":
		// A heap-backed catalog stores a text[] column (pg_class.reloptions)
		// as a PG-native ArrayType varlena built by pgTextArrayBytes, NOT as
		// plain text — the shared `default` varlena branch below would hand
		// back the raw ArrayType header+element bytes as an opaque KindString,
		// silently corrupting any reader (M0119-0004: this is what made
		// loadUserTablesFromHeap read back garbage for a non-empty
		// reloptions). Decode the elements and re-join them into the same
		// "{elem,elem,…}" external-literal form BuildTableReloptions/
		// arrayTextLiteral produce, via catalog.ArrayTextLiteral, so this
		// stays byte-for-format-identical with the live virtual pg_class row.
		elems, n, err := decodePGTextArrayElements(data)
		if err != nil {
			return Datum{}, 0, fmt.Errorf("decode %q as text[] ArrayType: %w", t.Name, err)
		}
		if len(elems) == 0 {
			return NewStringDatum(""), n, nil
		}
		return NewStringDatum(catalog.ArrayTextLiteral(elems)), n, nil
	default:
		// Unknown type (e.g. "point", "path", custom types).  goopg's
		// encodeValuePG stores them as PG varlena text (the default branch
		// calls varlenaTextBytes).  Decode symmetrically.  This mirrors the
		// pgPhysicalTypeIsVarlena default which returns true for unknown
		// types.  M0097-0046.
		payload, n, err := decodePhysicalPGVarlena(data)
		if err != nil {
			return Datum{}, 0, fmt.Errorf("decode %q as varlena: %w", t.Name, err)
		}
		if sctx != nil {
			moff, mlen := sctx.AllocBytes(payload)
			return newStringArenaDatum(sctx, moff, mlen), n, nil
		}
		return NewStringDatum(string(payload)), n, nil
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
		// VARATT_IS_4B_C: inline PGLZ-compressed varlena. Decompress to the
		// original payload (mirrors wal.pgoDecodePhysicalVarlena).
		return pglz.DecodeInlineCompressed(data)
	}
	total := int(binary.LittleEndian.Uint32(data[:4]) >> 2)
	if total < 4 || total > len(data) {
		return nil, 0, fmt.Errorf("truncated 4-byte varlena")
	}
	return data[4:total], total, nil
}

// decodePGTextArrayElements parses a PG-native text[] ArrayType varlena
// produced by pgTextArrayBytes (or the empty form from emptyArrayTypeBytes)
// back into its element strings. Layout after the outer vl_len_ header:
// ndim(4) dataoffset(4) elemtype(4) [dims(4) lbound(4) elem...] — the last
// three are absent for an empty array (12-byte payload). Mirrors
// pgTextArrayBytes's exact element encoding (4-byte length-prefixed,
// 4-byte-aligned) rather than a general PG array decoder: goopg only ever
// needs to round-trip its own emitted content here. M0119-0004.
func decodePGTextArrayElements(data []byte) ([]string, int, error) {
	payload, total, err := decodePhysicalPGVarlena(data)
	if err != nil {
		return nil, 0, err
	}
	if len(payload) < 12 {
		return nil, 0, fmt.Errorf("truncated ArrayType header")
	}
	if len(payload) == 12 {
		return nil, total, nil // empty array (emptyArrayTypeBytes layout)
	}
	if len(payload) < 20 {
		return nil, 0, fmt.Errorf("truncated ArrayType dims/lbound")
	}
	nElem := int(binary.LittleEndian.Uint32(payload[12:16]))
	off := 20
	elems := make([]string, 0, nElem)
	for i := 0; i < nElem; i++ {
		if off+4 > len(payload) {
			return nil, 0, fmt.Errorf("truncated array element header")
		}
		n := int(binary.LittleEndian.Uint32(payload[off:off+4]) >> 2)
		if n < 4 || off+n > len(payload) {
			return nil, 0, fmt.Errorf("truncated array element")
		}
		elems = append(elems, string(payload[off+4:off+n]))
		off += (n + 3) &^ 3
	}
	return elems, total, nil
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

	// Detect base prefix. PG's scanner accepts a sign in front of it
	// ("-0x10" is -16), so the prefix is looked for past the sign.
	body := s
	if body[0] == '-' || body[0] == '+' {
		body = body[1:]
	}
	isNonDecimal := false
	rest := body
	if len(body) >= 2 && body[0] == '0' {
		switch body[1] {
		case 'b', 'B', 'o', 'O', 'x', 'X':
			isNonDecimal = true
			rest = body[2:]
		}
	}

	// Validate underscore rules.
	if !isNonDecimal {
		// Decimal: no leading underscore (after optional sign), no trailing, no consecutive.
		// `rest` is already sign-stripped here.
		if len(rest) > 0 && rest[0] == '_' {
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
	// Base 10 unless an explicit 0b/0o/0x prefix said otherwise: a bare
	// leading zero is NOT octal to PG's integer input function ('0123' is
	// 123, '09' is 9), while Go's base-0 ParseInt reads both as octal
	// (review/260831-2 EC-1). Base 0 is still right for the prefixed forms,
	// sign included.
	base := 10
	if isNonDecimal {
		base = 0
	}
	v, err := strconv.ParseInt(cleaned, base, bitSize)
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

// uuidBytesFromCanonical packs the canonical lowercase-with-dashes rendering
// produced by normalizeUUIDStr into PG's on-disk pg_uuid_t — a bare
// UUID_LEN (16) byte array, big-endian in the sense that the leftmost text
// hex pair is byte 0 (postgres/src/backend/utils/adt/uuid.c, string_to_uuid).
// s must already have passed isValidUUIDStr + normalizeUUIDStr; ok is false
// only if it did not.
func uuidBytesFromCanonical(s string) ([16]byte, bool) {
	var out [16]byte
	if len(s) != 36 {
		return out, false
	}
	hexVal := func(c byte) (byte, bool) {
		switch {
		case c >= '0' && c <= '9':
			return c - '0', true
		case c >= 'a' && c <= 'f':
			return c - 'a' + 10, true
		}
		return 0, false
	}
	j := 0
	for i := 0; i < 36; {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return [16]byte{}, false
			}
			i++
			continue
		}
		hi, ok1 := hexVal(s[i])
		lo, ok2 := hexVal(s[i+1])
		if !ok1 || !ok2 {
			return [16]byte{}, false
		}
		out[j] = hi<<4 | lo
		j++
		i += 2
	}
	if j != 16 {
		return [16]byte{}, false
	}
	return out, true
}

// uuidCanonicalFromBytes is uuidBytesFromCanonical's inverse and the port of
// PG's uuid_out (uuid.c): 32 lowercase hex digits with hyphens after bytes
// 4, 6, 8 and 10.
func uuidCanonicalFromBytes(b []byte) string {
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

func encodeVarlen(b []byte) []byte {
	out := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(out[:4], uint32(len(b)))
	copy(out[4:], b)
	return out
}

// pgUnsignedIDFromDatum coerces a Datum to the unsigned integer an
// oid/regproc/xid/xid8 column stores. All four are PG's "unsigned identifier"
// family — typbyval, no varlena, and printed by oidout/xidout/xid8out with the
// UNSIGNED %u/UINT64_FORMAT conversion — while goopg's Datum carries a signed
// int64, so the whole family shares one coercion and one range rule.
//
// bits is 32 for oid/regproc/xid and 64 for xid8. The bound mirrors upstream
// uint32in_subr (postgres/src/backend/utils/adt/numutils.c, reached from oidin
// via oid.c:41) and xid8in's uint64in_subr. A value outside the type's range is
// 22003, but a value with a leading '-' is NOT out of range: strtoul/strtoull
// parse the sign and wrap, and uint32in_subr's PG_UINT32_MAX != ULONG_MAX block
// admits the result if it matches after signed OR unsigned extension. The
// accepted 32-bit range is therefore the union of int32 and uint32
// ([MinInt32, MaxUint32]); "-1040" stores 4294966256, exactly as the oid
// regress case (INSERT '-1040') expects. typeName is the name PG puts in the
// 22003 message.
//
// M0119-0006 (54th slice): extracted so the heap arms of encodeValuePG and the
// binary-COPY arms of datumToCopyBinary (copy_binary.go) cannot drift — the two
// are twins under Hard-won Rule #2, differing only in byte order. This mirrors
// the 53rd slice's pgFloatFromDatum extraction.
func pgUnsignedIDFromDatum(d Datum, typeName string, bits int) (uint64, error) {
	var v int64
	switch d.Kind {
	case KindInt:
		v = d.Int
	case KindString:
		s := strings.TrimSpace(d.StringValue())
		// xid/xid8's own *in functions (xidin/xid8in) route through
		// uint32in_subr/uint64in_subr, which call strto[u]l(s, &endptr, 0) —
		// base 0, so octal ("0NNN") and hex ("0xNNN") literals are valid
		// alongside decimal (postgres/src/backend/utils/adt/numutils.c:985-992,
		// xid.c:187-201). coerceStringToInt64 (below) is decimal-only, which
		// is right for oid/regproc's typed-literal path elsewhere but wrong
		// here for xid/xid8: an `INSERT INTO t(x xid8) VALUES ('0x2a')` (a
		// string literal implicitly coerced into the column at heap-encode
        // time, NOT through evalCast's `'…'::xid8` parseXid8) previously
		// raised a spurious 22003 "out of range" instead of decoding the hex.
		// M0134-0087 (xid.sql sizing) — the CastExpr path (evalCast, expr.go)
		// already had working parseXid/parseXid8; this was the sibling gap
		// (pattern_sibling_paths_must_agree).
		if typeName == "xid" || typeName == "xid8" {
			if s == "-1" {
				v = -1 // two's-complement image of all-ones, correct for both widths
				break
			}
			if bits == 32 {
				n, err := parseXid(s)
				if err != nil {
					return 0, &ExecError{Code: "22P02",
						Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, s)}
				}
				v = int64(n)
			} else {
				n, err := parseXid8(s)
				if err != nil {
					return 0, &ExecError{Code: "22P02",
						Message: fmt.Sprintf("invalid input syntax for type %s: %q", typeName, s)}
				}
				v = int64(n)
			}
			break
		}
		var err error
		v, err = coerceStringToInt64(s, typeName)
		if err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("expected int for %s, got kind %d", typeName, d.Kind)
	}
	if bits == 32 {
		// uint32in_subr accepts a value in the union of the signed-32 and
		// unsigned-32 ranges (see the comment above): negatives wrap via
		// uint32(v), only values outside [-2^31, 2^32-1] are 22003.
		// M-NIGHTLY AI-20260814-011711-002.
		if v < math.MinInt32 || v > math.MaxUint32 {
			return 0, &ExecError{Code: "22003",
				Message: fmt.Sprintf("value %q is out of range for type %s", strings.TrimSpace(d.Format()), typeName)}
		}
		return uint64(uint32(v)), nil
	}
	// bits == 64 (xid8): uint64in_subr wraps a negative through strtoull, so a
	// negative int64 is its two's-complement uint64 image — no 22003.
	return uint64(v), nil
}

// pgIntervalFieldsFromDatum coerces a Datum to the three fields PG's Interval
// struct holds — {TimeOffset time; int32 day; int32 month}
// (postgres/src/include/datatype/timestamp.h) — returned here as
// (months, days, micros) in goopg's usual field order.
//
// The KindString arm exists because a bare quoted literal
// (`INSERT INTO t(i) VALUES ('1 mon')`) is `unknown` upstream and reaches an
// interval column through interval_in; parser.ParseIntervalBody is the same
// tokenizer `'…'::interval` uses, so the two entry points cannot disagree.
//
// M0119-0006 (55th slice): extracted from encodeValuePG's "interval" arm so the
// heap encoder and the new binary-COPY interval arm (datumToCopyBinary,
// copy_binary.go) cannot drift — the two are twins under Hard-won Rule #2,
// differing only in byte order. Same extraction shape as pgFloatFromDatum
// (53rd) and pgUnsignedIDFromDatum (54th).
func pgIntervalFieldsFromDatum(d Datum) (months, days int32, micros int64, err error) {
	switch d.Kind {
	case KindInterval:
		return d.IntervalMonthsValue(), d.IntervalDaysValue(), d.IntervalMicrosValue(), nil
	case KindString:
		months, days, micros, ok := parser.ParseIntervalBody(d.StringValue())
		if !ok {
			return 0, 0, 0, &ExecError{Code: "22007",
				Message: fmt.Sprintf("invalid input syntax for type interval: %q", d.StringValue())}
		}
		return months, days, micros, nil
	default:
		return 0, 0, 0, fmt.Errorf("expected interval, got kind %d", d.Kind)
	}
}

// floatTextDatum converts a PGFloatOut rendering into the Datum shape the
// pre-M0111-0002 varlena-text decode produced: KindNumeric for finite
// values, KindString for NaN/Infinity.
// pgFloatFromDatum coerces a Datum to the float value a float4/float8 column
// stores. goopg has no float Datum Kind — PGFloatOut's shortest-round-trip text
// is parsed back into KindNumeric (floatTextDatum), so every float arrives here
// as KindNumeric/KindString/KindInt.
//
// M0119-0006 (53rd slice): extracted from the "float4"/"float8" arms of
// encodeValuePG so the heap encoder and the new binary-COPY float arms
// (datumToCopyBinary, copy_binary.go) cannot drift — the two are twins in the
// sense of Hard-won Rule #2, differing only in byte order. bits selects the
// ParseFloat width (and hence the float4 rounding) and the type name PG uses in
// its own 22P02 text (float.c float4in / float8in).
func pgFloatFromDatum(d Datum, bits int) (float64, error) {
	switch d.Kind {
	case KindInt:
		return float64(d.Int), nil
	case KindString, KindNumeric:
		raw := d.StringValue()
		if d.Kind == KindNumeric {
			raw = numericText(d)
		}
		// M0134-0166: this used to TrimSpace first and then report the
		// trimmed text, so an `INSERT INTO t(f float8) VALUES ('  ')` said
		// `… : ""` where PG says `… : "  "`, and it mapped strconv's ErrRange
		// onto 22P02 instead of PG's dedicated 22003 "is out of range".
		// floatIn (float_in.go) is float8in_internal and is now shared with
		// evalCast / evalTypedStringLit — Hard-won Rule #2.
		v, err := floatIn(raw, bits)
		if err != nil {
			return 0, err
		}
		if math.IsNaN(v) {
			// strconv.ParseFloat("NaN", 64) yields Go's NaN, whose payload bit is
			// SET (0x7ff8000000000001); PG's get_float8_nan() (postgres/src/
			// include/utils/float.h) yields the canonical quiet NaN
			// 0x7ff8000000000000. The two are equal as float64 but NOT as bytes,
			// and both goopg float paths are byte-visible to real PG: the heap
			// image (a PG standby reading goopg's pages) and float8send's binary
			// COPY payload. Measured 2026-08-13: a `COPY … (FORMAT binary)` of a
			// float8 'NaN' differed from PG 18.3's stream in exactly this one bit.
			// float4 was already identical — the float32 narrowing discards the
			// payload — so canonicalising here covers both widths.
			v = math.Float64frombits(0x7ff8000000000000)
		}
		return v, nil
	default:
		return 0, fmt.Errorf("kind %d cannot encode as float%d", d.Kind, bits/8)
	}
}

func floatTextDatum(text string) Datum {
	if v, scale, ok := parseNumericFast(text); ok {
		return Datum{Kind: KindNumeric, Int: v, Scale: scale}
	}
	if m, s, perr := parseNumeric(text); perr == nil {
		return newNumeric(m, int(s))
	}
	return NewStringDatum(text)
}
