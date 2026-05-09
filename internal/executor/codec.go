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
		// TOAST pointer: flag byte 0x02 followed by 12-byte pointer.
		if d.Kind == KindToastPointer {
			out = append(out, 2)
			out = append(out, d.BytesValue()...)
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
	if err := DecodeRowInto(row, cols, data); err != nil {
		return nil, err
	}
	return row, nil
}

// DecodeRowProjection (M0054-0005c-followup) decodes only the columns
// whose index is true in `keep`, skipping payload allocation for
// other columns. dst[i] for i where !keep[i] is set to NullDatum
// (a marker, NOT the SQL NULL — callers must not read those slots).
//
// The variable-length column codec means we cannot skip past a
// column without parsing its length header, so the savings come
// from the per-column heap allocations: varchar/char/text avoid
// the `string(data[4:4+n])` copy and numeric avoids the
// `parseNumeric` big.Int materialisation. For a TPC-H index-build
// path that needs only the indexed key column out of N, this
// removes the dominant allocation source flagged by
// `M0054-0004` pprof (DecodeRow 39 % cum in the `idx` window).
//
// Caller invariants:
//   - `len(dst) >= len(cols)`
//   - `len(keep) >= len(cols)`
//
// arena == nil falls back to the legacy `make([]byte)` path —
// behaviour byte-for-byte identical regardless of caller.
func DecodeRowProjection(dst Row, cols []catalog.Column, data []byte, keep []bool) error {
	return decodeRowProjectionArena(dst, cols, data, keep, nil)
}

// DecodeRowProjectionIntoArena is the arena-aware sibling of
// DecodeRowProjection. When arena != nil, projected variable-length
// columns (varchar / char / text / bytea, numeric text storage)
// emit Datums whose payload lives in the arena's pages — single
// allocation per page amortises across thousands of strings.
// Skipped columns produce no Datum allocation in either mode.
//
// Used by index-build paths (operators_ddl.go: collectBTreeEntries
// and backfillBTree) where the decoded Datum's lifetime ends at
// encodeBTreeKeyForColumn — the encoded key is a fresh []byte copy.
//
// Caller invariants (same as DecodeRowProjection plus):
//   - arena.Reset() bound to the producer's batch boundary
//     (per-page in DDL paths).
//   - Datums must not be retained past the next Reset.
//
// arena == nil falls back to the legacy `make([]byte)` path —
// behaviour byte-for-byte identical to DecodeRowProjection.
//
// M0074-0004 — see docs/design/0074-0004-decode-row-projection-arena.md.
func DecodeRowProjectionIntoArena(dst Row, cols []catalog.Column, data []byte, keep []bool, arena *Arena) error {
	return decodeRowProjectionArena(dst, cols, data, keep, arena)
}

func decodeRowProjectionArena(dst Row, cols []catalog.Column, data []byte, keep []bool, arena *Arena) error {
	off := 0
	for i, c := range cols {
		if off >= len(data) {
			dst[i] = NullDatum
			continue
		}
		flag := data[off]
		off++
		if flag == 1 {
			dst[i] = NullDatum
			continue
		}
		if flag == 2 {
			const toastPtrSize = 12
			if off+toastPtrSize > len(data) {
				return fmt.Errorf("DecodeRowProjection: %s: truncated TOAST pointer", c.Name)
			}
			if keep[i] {
				// TOAST pointer: always Buf-backed regardless
				// of arena (mirrors DecodeRowIntoArena's flag==2
				// path). Detoast may run later and needs the
				// pointer to outlive arena Reset.
				dst[i] = NewToastPointerDatum(append([]byte(nil), data[off:off+toastPtrSize]...))
			} else {
				dst[i] = NullDatum
			}
			off += toastPtrSize
			continue
		}
		if keep[i] {
			var (
				v   Datum
				n   int
				err error
			)
			if arena != nil {
				v, n, err = decodeValueArena(c.Type, data[off:], arena)
			} else {
				v, n, err = decodeValue(c.Type, data[off:])
			}
			if err != nil {
				return fmt.Errorf("DecodeRowProjection: %s: %w", c.Name, err)
			}
			dst[i] = v
			off += n
			continue
		}
		// Not kept: advance offset only. Avoids the per-column
		// heap allocations (string copy, big.Int).
		n, err := decodeValueSize(c.Type, data[off:])
		if err != nil {
			return fmt.Errorf("DecodeRowProjection: %s: %w", c.Name, err)
		}
		dst[i] = NullDatum
		off += n
	}
	return nil
}

// decodeValueSize returns just the byte length of an encoded value
// without materialising a Datum. Used by DecodeRowProjection to
// skip columns whose payload the caller does not need.
// (M0054-0005c-followup.)
func decodeValueSize(t catalog.Type, data []byte) (int, error) {
	switch t.Name {
	case "int4", "integer", "int":
		if len(data) < 4 {
			return 0, fmt.Errorf("truncated int4")
		}
		return 4, nil
	case "int8", "bigint":
		if len(data) < 8 {
			return 0, fmt.Errorf("truncated int8")
		}
		return 8, nil
	case "bool", "boolean":
		if len(data) < 1 {
			return 0, fmt.Errorf("truncated bool")
		}
		return 1, nil
	case "timestamp", "timestamptz", "date":
		if len(data) < 8 {
			return 0, fmt.Errorf("truncated %s", t.Name)
		}
		return 8, nil
	default:
		// numeric, varchar, char, text — all use a 4-byte big-endian
		// length prefix followed by `n` bytes of payload.
		if len(data) < 4 {
			return 0, fmt.Errorf("truncated varlen header")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if 4+n > len(data) {
			return 0, fmt.Errorf("truncated varlen body")
		}
		return 4 + n, nil
	}
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
	return decodeRowIntoArena(dst, cols, data, nil)
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
func DecodeRowIntoArena(dst Row, cols []catalog.Column, data []byte, arena *Arena) error {
	return decodeRowIntoArena(dst, cols, data, arena)
}

func decodeRowIntoArena(dst Row, cols []catalog.Column, data []byte, arena *Arena) error {
	off := 0
	for i, c := range cols {
		if off >= len(data) {
			dst[i] = NullDatum
			continue
		}
		flag := data[off]
		off++
		if flag == 1 {
			dst[i] = NullDatum
			continue
		}
		// TOAST pointer: 12 bytes following the 0x02 flag byte.
		// Always Buf-backed regardless of arena binding —
		// detoasted bytes (when DetoastRow runs later) need to
		// outlive the arena's Reset cycle.
		if flag == 2 {
			const toastPtrSize = 12
			if off+toastPtrSize > len(data) {
				return fmt.Errorf("DecodeRow: %s: truncated TOAST pointer", c.Name)
			}
			dst[i] = NewToastPointerDatum(append([]byte(nil), data[off:off+toastPtrSize]...))
			off += toastPtrSize
			continue
		}
		var (
			v   Datum
			n   int
			err error
		)
		if arena != nil {
			v, n, err = decodeValueArena(c.Type, data[off:], arena)
		} else {
			v, n, err = decodeValue(c.Type, data[off:])
		}
		if err != nil {
			return fmt.Errorf("DecodeRow: %s: %w", c.Name, err)
		}
		dst[i] = v
		off += n
	}
	return nil
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
		if d.BoolValue() {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case "timestamp", "timestamptz", "date":
		// DATE shares the timestamp on-disk shape: an 8-byte
		// big-endian nanos-since-epoch. The midnight-UTC
		// coercion happens at literal-parse time (date '...' →
		// KindTime at midnight), so a column-level distinction
		// from timestamp isn't needed for arithmetic and
		// comparison. Wire-format formatting back to the
		// `YYYY-MM-DD` shape would belong in a dedicated KindDate
		// carrier, deferred to the type-system milestone.
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(d.TimeValue().UnixNano()))
		return buf[:], nil
	case "numeric", "decimal":
		// NUMERIC values flow on the wire as decimal text in the
		// same varlen frame VARCHAR uses. KindNumeric formats via
		// formatNumeric so the stored bytes are stable across
		// arithmetic chains. KindInt / KindString round-trip
		// straight through (the analyzer accepts both as
		// assignable to numeric, e.g. HammerDB's loader sends
		// pre-formatted strings). See
		// docs/design/0003-0012-numeric-arithmetic.md.
		switch d.Kind {
		case KindNumeric:
			return encodeVarlen([]byte(numericText(d))), nil
		case KindInt:
			return encodeVarlen([]byte(strconv.FormatInt(d.Int, 10))), nil
		case KindString, KindStringArena:
			return encodeVarlen([]byte(d.StringValue())), nil
		case KindBytes, KindBytesArena:
			return encodeVarlen(d.BytesValue()), nil
		}
		return nil, fmt.Errorf("kind %d cannot encode as %s", d.Kind, t.Name)
	default:
		// Variable-length text-like fallback. NUMERIC datums emit
		// the canonical decimal text via formatNumeric so a column
		// with an unrecognised type name still receives a usable
		// representation (mirrors the dedicated numeric arm above).
		var s string
		switch d.Kind {
		case KindString, KindStringArena:
			s = d.StringValue()
		case KindBytes, KindBytesArena:
			return encodeVarlen(d.BytesValue()), nil
		case KindInt:
			return nil, fmt.Errorf("integer datum cannot encode as %s", t.Name)
		case KindNumeric:
			s = numericText(d)
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
		return NewBoolDatum(data[0] != 0), 1, nil
	case "timestamp", "timestamptz", "date":
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated %s", t.Name)
		}
		ns := int64(binary.BigEndian.Uint64(data[:8]))
		return NewTimeDatum(time.Unix(0, ns).UTC()), 8, nil
	case "numeric", "decimal":
		// Stored as varlen text. Parse into KindNumeric so
		// arithmetic and comparison can run through the
		// scale-aligning helpers without re-parsing on every
		// row. Strings that aren't valid numerics surface as
		// 22P02 — same SQLSTATE upstream reports.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated numeric")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if 4+n > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated numeric body")
		}
		text := string(data[4 : 4+n])
		// M0058-0003: int64 fast path for integer-valued NUMERIC.
		// HammerDB's TPC-H schema stores all integer columns as
		// NUMERIC; the fast path avoids ~400 ns of big.Int allocation
		// per column on every row decoded.
		if v, scale, ok := parseNumericFast(text); ok {
			return Datum{Kind: KindNumeric, Int: v, Scale: scale}, 4 + n, nil
		}
		m, s, err := parseNumeric(text)
		if err != nil {
			return Datum{}, 0, fmt.Errorf("decode numeric %q: %w", text, err)
		}
		return newNumeric(m, int(s)), 4 + n, nil
	default:
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated varlen header")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if 4+n > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated varlen body")
		}
		return NewStringDatum(string(data[4 : 4+n])), 4 + n, nil
	}
}

// decodeValueArena mirrors decodeValue but routes variable-length
// payload (varchar / char / text / bytea — the `default` switch
// arm in decodeValue) through arena.Allocate instead of
// `make([]byte)`. Numeric text remains on the legacy parse path
// (Datum.Int / Big / Scale), as does every fixed-width type.
//
// M0073-0002. The split between decodeValue and decodeValueArena
// keeps the legacy callers (DecodeRow, DecodeRowProjection, toast
// resolution, ANALYZE) byte-for-byte unchanged — only the
// per-page seqScan / per-Rescan indexScan path opts in.
func decodeValueArena(t catalog.Type, data []byte, arena *Arena) (Datum, int, error) {
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
		return NewBoolDatum(data[0] != 0), 1, nil
	case "timestamp", "timestamptz", "date":
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated %s", t.Name)
		}
		ns := int64(binary.BigEndian.Uint64(data[:8]))
		return NewTimeDatum(time.Unix(0, ns).UTC()), 8, nil
	case "numeric", "decimal":
		// Numeric stays on the parse path — Datum.Int / Big /
		// Scale carry the value, no Buf payload to arena.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated numeric")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if 4+n > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated numeric body")
		}
		text := string(data[4 : 4+n])
		if v, scale, ok := parseNumericFast(text); ok {
			return Datum{Kind: KindNumeric, Int: v, Scale: scale}, 4 + n, nil
		}
		m, s, err := parseNumeric(text)
		if err != nil {
			return Datum{}, 0, fmt.Errorf("decode numeric %q: %w", text, err)
		}
		return newNumeric(m, int(s)), 4 + n, nil
	default:
		// varchar / char / text / bytea: arena-backed.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated varlen header")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if 4+n > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated varlen body")
		}
		if n == 0 {
			return Datum{Kind: KindStringArena, arena: arena}, 4, nil
		}
		buf, offset := arena.Allocate(n)
		copy(buf, data[4:4+n])
		return newStringArenaDatum(arena, offset, n), 4 + n, nil
	}
}
