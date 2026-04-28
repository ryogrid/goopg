package executor

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// EncodeRow serialises a Datum row into the heap-tuple data area.
//
// v0 uses a private fixed-format encoding rather than upstream's
// per-type typlen/typalign rules. Each column emits one null-flag
// byte (0 = present, 1 = NULL) followed by the value bytes when
// present:
//
//   - int4              : 4-byte big-endian
//   - int8 / timestamp  : 8-byte big-endian
//   - bool              : 1 byte
//   - text / varchar /
//     char / unknown    : 4-byte big-endian length prefix + raw bytes
//
// We diverge from upstream's wire-shaped tuple body intentionally —
// matching upstream's encoding requires the type system milestone 7
// is going to land. Until then this codec is the goopg-internal
// contract between Insert/SeqScan and storage.
func EncodeRow(cols []catalog.Column, row Row) ([]byte, error) {
	if len(cols) != len(row) {
		return nil, fmt.Errorf("EncodeRow: %d cols vs %d datums", len(cols), len(row))
	}
	var out []byte
	for i, c := range cols {
		d := row[i]
		if d.IsNull() {
			out = append(out, 1)
			continue
		}
		out = append(out, 0)
		buf, err := encodeValue(c.Type, d)
		if err != nil {
			return nil, err
		}
		out = append(out, buf...)
	}
	return out, nil
}

// DecodeRow inverts EncodeRow.
func DecodeRow(cols []catalog.Column, data []byte) (Row, error) {
	row := make(Row, len(cols))
	off := 0
	for i, c := range cols {
		if off >= len(data) {
			// ALTER TABLE ADD COLUMN appends new logical columns without
			// rewriting old heap tuples; missing trailing values read as
			// NULL.
			row[i] = NullDatum
			continue
		}
		flag := data[off]
		off++
		if flag == 1 {
			row[i] = NullDatum
			continue
		}
		v, n, err := decodeValue(c.Type, data[off:])
		if err != nil {
			return nil, fmt.Errorf("DecodeRow: %s: %w", c.Name, err)
		}
		row[i] = v
		off += n
	}
	return row, nil
}

func encodeValue(t catalog.Type, d Datum) ([]byte, error) {
	switch t.Name {
	case "int4", "integer", "int":
		if d.Kind != KindInt {
			return nil, fmt.Errorf("expected int for %s, got kind %d", t.Name, d.Kind)
		}
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], uint32(int32(d.Int)))
		return buf[:], nil
	case "int8", "bigint":
		if d.Kind != KindInt {
			return nil, fmt.Errorf("expected int for %s, got kind %d", t.Name, d.Kind)
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(d.Int))
		return buf[:], nil
	case "bool", "boolean":
		if d.Kind != KindBool {
			return nil, fmt.Errorf("expected bool, got kind %d", d.Kind)
		}
		if d.Bool {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case "timestamp", "timestamptz":
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(d.Time.UnixNano()))
		return buf[:], nil
	case "numeric", "decimal":
		// v0 stores NUMERIC values as their decimal-string
		// representation in the same varlen text frame as
		// varchar/char. Real precision-tracking arithmetic
		// requires the type system milestone-7 will land;
		// until then this lossless string round-trip is what
		// HammerDB / TPC-H need to load and unload data
		// without spurious encode failures. Integer datums
		// (e.g. `INSERT INTO t (numeric_col) VALUES (1)`)
		// flow through `strconv.FormatInt` so the round-trip
		// preserves the literal.
		switch d.Kind {
		case KindInt:
			return encodeVarlen([]byte(strconv.FormatInt(d.Int, 10))), nil
		case KindString:
			return encodeVarlen([]byte(d.String)), nil
		case KindBytes:
			return encodeVarlen(d.Bytes), nil
		}
		return nil, fmt.Errorf("kind %d cannot encode as %s", d.Kind, t.Name)
	default:
		// Variable-length text-like fallback.
		var s string
		switch d.Kind {
		case KindString:
			s = d.String
		case KindBytes:
			return encodeVarlen(d.Bytes), nil
		case KindInt:
			return nil, fmt.Errorf("integer datum cannot encode as %s", t.Name)
		default:
			return nil, fmt.Errorf("kind %d cannot encode as %s", d.Kind, t.Name)
		}
		return encodeVarlen([]byte(s)), nil
	}
}

func encodeVarlen(b []byte) []byte {
	out := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(out[:4], uint32(len(b)))
	copy(out[4:], b)
	return out
}

func decodeValue(t catalog.Type, data []byte) (Datum, int, error) {
	switch t.Name {
	case "int4", "integer", "int":
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated int4")
		}
		v := int32(binary.BigEndian.Uint32(data[:4]))
		return Datum{Kind: KindInt, Int: int64(v)}, 4, nil
	case "int8", "bigint":
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated int8")
		}
		v := int64(binary.BigEndian.Uint64(data[:8]))
		return Datum{Kind: KindInt, Int: v}, 8, nil
	case "bool", "boolean":
		if len(data) < 1 {
			return Datum{}, 0, fmt.Errorf("truncated bool")
		}
		return Datum{Kind: KindBool, Bool: data[0] != 0}, 1, nil
	case "timestamp", "timestamptz":
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated timestamp")
		}
		ns := int64(binary.BigEndian.Uint64(data[:8]))
		return Datum{Kind: KindTime, Time: time.Unix(0, ns).UTC()}, 8, nil
	case "numeric", "decimal":
		// Stored as varlen text (see encodeValue). Surface as
		// KindString so the wire layer formats it verbatim;
		// callers that want comparison arithmetic must coerce
		// in the planner.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated numeric")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if 4+n > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated numeric body")
		}
		return Datum{Kind: KindString, String: string(data[4 : 4+n])}, 4 + n, nil
	default:
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated varlen header")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if 4+n > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated varlen body")
		}
		return Datum{Kind: KindString, String: string(data[4 : 4+n])}, 4 + n, nil
	}
}
