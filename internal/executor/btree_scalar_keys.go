package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
)

// B-tree key encodings for the small fixed-width / byte-string types that
// `isSupportedBTreeKeyType` used to reject outright — int2, oid, bool, bytea and
// `time without time zone`. Before this slice (M0119-0006) a plain
// `CREATE INDEX ON t(smallint_col)` failed with
// `0A000 btree v0 only supports int4 / numeric keys`, so none of these columns
// could be indexed at all — including the `oid` system-ish columns pg_amcheck's
// own fixtures index.
//
// Each encoding reproduces the ordering of the type's default operator class in
// PG 18.3, NOT the ordering of its text form:
//
//	int2  (btint2cmp,  nbtree int2_ops)  — signed 16-bit, widened to the
//	      order-preserving int4 key (4 bytes). Widening is safe because every
//	      int2 value is representable as int32 and the map is monotonic.
//	oid   (btoidcmp,   oid_ops)          — UNSIGNED 32-bit compare
//	      (`postgres/src/backend/utils/adt/oid.c:oidcmp` compares Oid, an
//	      unsigned type). The int4 key would sort OIDs >= 2^31 before OID 0, so
//	      an oid key widens to int8 over the 0..2^32-1 range instead (8 bytes).
//	bool  (btboolcmp,  bool_ops)         — false < true, encoded as the int4
//	      key of 0/1 (4 bytes).
//	bytea (byteacmp,   bytea_ops)        — memcmp over the common prefix, then
//	      shorter-first (`varlena.c:byteacmp`). EncodeVarchar's escaped,
//	      0x00-terminated form has exactly that order for ARBITRARY bytes: the
//	      terminator 0x00 is below every escaped byte (escapes lead with 0x01,
//	      all other bytes are >= 0x02), so a prefix sorts first, and embedded
//	      NUL/0x01 bytes cannot forge a terminator.
//	time  (bttimecmp,  time_ops)         — int64 microseconds since midnight,
//	      encoded through the timestamp/int8 key (8 bytes).
//
// `timetz` is deliberately NOT here: its comparison is two-part
// (`timetz_cmp_internal` compares time-minus-zone first and only then the zone
// itself), so a single ordered key column cannot represent it. See the
// deferral-ledger row for the resume point.

// isInt2Type returns true for the 2-byte signed integer type. `smallserial` /
// `serial2` are the sequence-backed spellings of int2, matching how isInt4Type
// folds in `serial`.
func isInt2Type(name string) bool {
	switch strings.ToLower(name) {
	case "int2", "smallint", "smallserial", "serial2":
		return true
	default:
		return false
	}
}

// isOidType returns true for the object-identifier type. `regproc` shares oid's
// physical form in the codec (codec.go), and therefore its key encoding.
func isOidType(name string) bool {
	switch strings.ToLower(name) {
	case "oid", "regproc":
		return true
	default:
		return false
	}
}

// isBoolType returns true for the boolean type.
func isBoolType(name string) bool {
	switch strings.ToLower(name) {
	case "bool", "boolean":
		return true
	default:
		return false
	}
}

// isByteaType returns true for the binary-string type.
func isByteaType(name string) bool {
	return strings.ToLower(name) == "bytea"
}

// isTimeOfDayType returns true for `time without time zone` ONLY. It must not
// answer for `timetz`, whose ordering is not single-key representable (see the
// file comment), nor for `timestamp`, which has its own predicate.
func isTimeOfDayType(name string) bool {
	switch strings.ToLower(name) {
	case "time", "time without time zone":
		return true
	default:
		return false
	}
}

// coerceScalarKeyStringDatum resolves an unknown-literal probe (`WHERE b =
// '\xdeadbeef'`) into the runtime kind the stored rows carry for these types,
// the same job the int4/numeric/timestamp arms of encodeBTreeKeyForColumn's
// pre-switch does. Returns the datum unchanged when the column is not one of
// this file's types.
func coerceScalarKeyStringDatum(v Datum, col *catalog.Column, pos int) (Datum, *ExecError) {
	s := v.StringValue()
	switch {
	case isInt2Type(col.Type.Name), isOidType(col.Type.Name):
		if p := tryParseStringAs(KindInt, s); p.Kind == KindInt {
			return p, nil
		}
		typ := "smallint"
		if isOidType(col.Type.Name) {
			typ = "oid"
		}
		return v, &ExecError{Code: "22P02", Pos: pos,
			Message: fmt.Sprintf("invalid input syntax for type %s: %q", typ, s)}
	case isBoolType(col.Type.Name):
		// evalCast owns the accepted boolean spellings ("t"/"yes"/"on"/…);
		// duplicating that list here is exactly the sibling-drift this file's
		// encodings are meant to avoid.
		d, err := evalCast(v, "bool", pos, nil)
		if err != nil {
			return v, &ExecError{Code: "22P02", Pos: pos,
				Message: fmt.Sprintf("invalid input syntax for type boolean: %q", s)}
		}
		return d, nil
	case isByteaType(col.Type.Name):
		b, err := byteaIn(s, pos)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				return v, ee
			}
			return v, &ExecError{Code: "22P02", Pos: pos,
				Message: fmt.Sprintf("invalid input syntax for type bytea: %q", s)}
		}
		return NewBytesDatum(b), nil
	case isTimeOfDayType(col.Type.Name):
		t, err := parseTimeString(s)
		if err != nil {
			if ee, ok := err.(*ExecError); ok {
				return v, ee
			}
			return v, &ExecError{Code: "22007", Pos: pos,
				Message: fmt.Sprintf("invalid input syntax for type time: %q", s)}
		}
		return NewTimeDatum(t), nil
	}
	return v, nil
}

// encodeScalarBTreeKey produces the stored key bytes for the types listed in
// this file's comment. handled=false means the column is not one of them and
// the caller must continue its own type switch.
func encodeScalarBTreeKey(v Datum, col *catalog.Column, pos int) (key []byte, handled bool, encErr *ExecError) {
	switch {
	case isInt2Type(col.Type.Name):
		if v.Kind != KindInt {
			return nil, true, &ExecError{Code: "42804", Pos: pos,
				Message: fmt.Sprintf("column %q is not smallint at runtime", col.Name)}
		}
		if v.Int < -32768 || v.Int > 32767 {
			return nil, true, &ExecError{Code: "22003", Pos: pos,
				Message: fmt.Sprintf("value %d out of int2 range for index key", v.Int)}
		}
		return btree.EncodeInt4(int32(v.Int)), true, nil
	case isOidType(col.Type.Name):
		if v.Kind != KindInt {
			return nil, true, &ExecError{Code: "42804", Pos: pos,
				Message: fmt.Sprintf("column %q is not oid at runtime", col.Name)}
		}
		if v.Int < 0 || v.Int > 4294967295 {
			return nil, true, &ExecError{Code: "22003", Pos: pos,
				Message: fmt.Sprintf("value %d out of oid range for index key", v.Int)}
		}
		// Unsigned compare: the value is already in 0..2^32-1, so the int8 key
		// orders it exactly as oidcmp does.
		return btree.EncodeInt8(v.Int), true, nil
	case isBoolType(col.Type.Name):
		if v.Kind != KindBool {
			return nil, true, &ExecError{Code: "42804", Pos: pos,
				Message: fmt.Sprintf("column %q is not boolean at runtime", col.Name)}
		}
		if v.BoolValue() {
			return btree.EncodeInt4(1), true, nil
		}
		return btree.EncodeInt4(0), true, nil
	case isByteaType(col.Type.Name):
		if v.Kind != KindBytes {
			return nil, true, &ExecError{Code: "42804", Pos: pos,
				Message: fmt.Sprintf("column %q is not bytea at runtime", col.Name)}
		}
		return btree.EncodeVarchar(v.BytesValue()), true, nil
	case isTimeOfDayType(col.Type.Name):
		if v.Kind != KindTime {
			return nil, true, &ExecError{Code: "42804", Pos: pos,
				Message: fmt.Sprintf("column %q is not time at runtime", col.Name)}
		}
		// pgTimeMicros is the codec's own time-of-day extraction, so the key
		// derives from the same microseconds the heap stores.
		return btree.EncodeInt8(pgTimeMicros(v.TimeValue())), true, nil
	}
	return nil, false, nil
}

// decodeScalarBTreeKey inverts encodeScalarBTreeKey and reports the byte width
// consumed, which the composite key walk needs. handled=false means the column
// is not one of this file's types.
//
// Both key-decode siblings (decodeIndexKeyColumn for the composite/amcheck walk
// and decodeBTreeKeyToDatum for single-column index-only scans) route here, so
// the two cannot drift — and, more importantly, neither falls through to their
// shared `default:` arm, which reads any 8 leading bytes as an enum float8 and
// never errors (the M0119-0006 NUMERIC slice found that exact latent misread).
func decodeScalarBTreeKey(key []byte, typeName string) (d Datum, n int, handled bool, err error) {
	switch {
	case isInt2Type(typeName):
		if len(key) < 4 {
			return NullDatum, 0, true, fmt.Errorf("btree: int2 key truncated, got %d bytes", len(key))
		}
		v, derr := btree.DecodeInt4(key[:4])
		return Datum{Kind: KindInt, Int: int64(v)}, 4, true, derr
	case isOidType(typeName):
		if len(key) < 8 {
			return NullDatum, 0, true, fmt.Errorf("btree: oid key truncated, got %d bytes", len(key))
		}
		v, derr := btree.DecodeInt8(key[:8])
		return Datum{Kind: KindInt, Int: v}, 8, true, derr
	case isBoolType(typeName):
		if len(key) < 4 {
			return NullDatum, 0, true, fmt.Errorf("btree: bool key truncated, got %d bytes", len(key))
		}
		v, derr := btree.DecodeInt4(key[:4])
		if derr != nil {
			return NullDatum, 0, true, derr
		}
		return NewBoolDatum(v != 0), 4, true, nil
	case isByteaType(typeName):
		raw, n, derr := btree.DecodeVarcharLen(key)
		if derr != nil {
			return NullDatum, 0, true, derr
		}
		return NewBytesDatum(append([]byte(nil), raw...)), n, true, nil
	case isTimeOfDayType(typeName):
		if len(key) < 8 {
			return NullDatum, 0, true, fmt.Errorf("btree: time key truncated, got %d bytes", len(key))
		}
		v, derr := btree.DecodeInt8(key[:8])
		if derr != nil {
			return NullDatum, 0, true, derr
		}
		return NewTimeDatum(pgTimeFromMicros(v)), 8, true, nil
	}
	return NullDatum, 0, false, nil
}
