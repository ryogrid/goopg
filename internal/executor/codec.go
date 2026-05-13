package executor

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
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
	case "int2", "smallint":
		if len(data) < 2 {
			return 0, fmt.Errorf("truncated int2")
		}
		return 2, nil
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
	case "timestamp", "timestamptz", "date", "time", "timetz":
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

func encodeValue(t catalog.Type, d Datum) ([]byte, error) {
	switch t.Name {
	case "int4", "integer", "int", "serial":
		var v int64
		switch d.Kind {
		case KindInt:
			v = d.Int
		case KindString, KindStringArena:
			var err error
			v, err = coerceStringToInt64(d.StringValue(), "integer")
			if err != nil {
				return nil, err
			}
			if v < -2147483648 || v > 2147483647 {
				return nil, &ExecError{Code: "22003",
					Message: fmt.Sprintf("value %q is out of range for type integer", strings.TrimSpace(d.StringValue()))}
			}
		default:
			return nil, fmt.Errorf("expected int for %s, got kind %d", t.Name, d.Kind)
		}
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], uint32(int32(v)))
		return buf[:], nil
	case "int8", "bigint", "bigserial":
		var v int64
		switch d.Kind {
		case KindInt:
			v = d.Int
		case KindString, KindStringArena:
			var err error
			v, err = coerceStringToInt64(d.StringValue(), "bigint")
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("expected int for %s, got kind %d", t.Name, d.Kind)
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(v))
		return buf[:], nil
	case "int2", "smallint":
		// int2 (smallint): stored as 2-byte big-endian int16. M0097-0003.
		var v int64
		switch d.Kind {
		case KindInt:
			v = d.Int
		case KindString, KindStringArena:
			var err error
			v, err = coerceStringToInt64(d.StringValue(), "smallint")
			if err != nil {
				return nil, err
			}
		case KindNumeric:
			v = d.NumericMantissaValue()
		default:
			return nil, fmt.Errorf("kind %d cannot encode as smallint", d.Kind)
		}
		if v < -32768 || v > 32767 {
			return nil, &ExecError{Code: "22003",
				Message: fmt.Sprintf("value \"%d\" is out of range for type smallint", v)}
		}
		var buf [2]byte
		binary.BigEndian.PutUint16(buf[:], uint16(int16(v)))
		return buf[:], nil

	case "float4", "real":
		// float4 stored as varlen text for v0 compatibility. M0097-0003.
		var s string
		switch d.Kind {
		case KindInt:
			s = strconv.FormatInt(d.Int, 10)
		case KindString, KindStringArena:
			raw := strings.TrimSpace(d.StringValue())
			if _, err := strconv.ParseFloat(raw, 32); err != nil {
				return nil, &ExecError{Code: "22P02",
					Message: fmt.Sprintf("invalid input syntax for type real: %q", d.StringValue())}
			}
			s = raw
		case KindNumeric:
			s = numericText(d)
		default:
			return nil, fmt.Errorf("kind %d cannot encode as real", d.Kind)
		}
		return encodeVarlen([]byte(s)), nil

	case "float8", "double precision", "double":
		// float8 stored as varlen text for v0 compatibility. M0097-0003.
		var s string
		switch d.Kind {
		case KindInt:
			s = strconv.FormatInt(d.Int, 10)
		case KindString, KindStringArena:
			raw := strings.TrimSpace(d.StringValue())
			if _, err := strconv.ParseFloat(raw, 64); err != nil {
				return nil, &ExecError{Code: "22P02",
					Message: fmt.Sprintf("invalid input syntax for type double precision: %q", d.StringValue())}
			}
			s = raw
		case KindNumeric:
			s = numericText(d)
		default:
			return nil, fmt.Errorf("kind %d cannot encode as double precision", d.Kind)
		}
		return encodeVarlen([]byte(s)), nil

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
		if d.Kind == KindString || d.Kind == KindStringArena {
			ts, err := parseCopyTimestamp(d.StringValue())
			if err != nil {
				return nil, &ExecError{Code: "22007",
					Message: fmt.Sprintf("invalid input syntax for type %s: %q", t.Name, d.StringValue())}
			}
			d = NewTimeDatum(ts)
		}
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(d.TimeValue().UnixNano()))
		return buf[:], nil
	case "time", "timetz":
		// TIME stores only time-of-day as 8-byte big-endian nanos anchored at epoch.
		if d.Kind == KindString || d.Kind == KindStringArena {
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
		binary.BigEndian.PutUint64(buf[:], uint64(d.TimeValue().UnixNano()))
		return buf[:], nil
	case "oid":
		// Oid: uint32 (0..4294967295). Negative input is treated as a uint32
		// wrap-around per PostgreSQL behavior (e.g., -1040 → 4294966256). M0097-0003.
		var n int64
		switch d.Kind {
		case KindInt:
			n = d.Int
		case KindString, KindStringArena:
			var err error
			origStr := d.StringValue() // preserve original for error messages
			rawStr := strings.TrimSpace(origStr)
			n, err = strconv.ParseInt(rawStr, 10, 64)
			if err != nil {
				numErr, isNumErr := err.(*strconv.NumError)
				if isNumErr && numErr.Err == strconv.ErrRange {
					// Value is syntactically valid but overflows int64 → out of range.
					return nil, &ExecError{Code: "22003",
						Message: fmt.Sprintf("value %q is out of range for type oid", rawStr)}
				}
				return nil, &ExecError{Code: "22P02",
					Message: fmt.Sprintf("invalid input syntax for type oid: %q", origStr)}
			}
		default:
			return nil, fmt.Errorf("kind %d cannot encode as oid", d.Kind)
		}
		// Wrap negative values: -N → 2^32 + (-N) for -2^32 < -N < 0.
		if n < 0 {
			n += 4294967296
		}
		if n < 0 || n > 4294967295 {
			return nil, &ExecError{Code: "22003",
				Message: fmt.Sprintf("value %d is out of range for type oid", n)}
		}
		// Store as 4-byte big-endian uint32 (same as int4). M0097-0003.
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], uint32(n))
		return buf[:], nil

	case "uuid":
		// Uuid accepts standard/brace/no-hyphen formats; normalizes to lowercase
		// canonical xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx for storage. M0097-0003.
		var uuidStr string
		switch d.Kind {
		case KindString, KindStringArena:
			uuidStr = strings.TrimSpace(d.StringValue())
			if !isValidUUIDStr(uuidStr) {
				return nil, &ExecError{Code: "22P02",
					Message: fmt.Sprintf("invalid input syntax for type uuid: %q", d.StringValue())}
			}
			uuidStr = normalizeUUIDStr(uuidStr)
		default:
			return nil, fmt.Errorf("kind %d cannot encode as uuid", d.Kind)
		}
		return encodeVarlen([]byte(uuidStr)), nil

	case "name":
		// The "name" type silently truncates input to NAMEDATALEN-1 = 63 bytes,
		// matching PostgreSQL's behaviour (postgres/src/include/pg_config_manual.h).
		// M0097-0003.
		var s string
		switch d.Kind {
		case KindString, KindStringArena:
			s = d.StringValue()
		case KindBytes, KindBytesArena:
			s = string(d.BytesValue())
		case KindInt:
			s = fmt.Sprintf("%d", d.Int)
		default:
			return nil, fmt.Errorf("kind %d cannot encode as name", d.Kind)
		}
		if len(s) > 63 {
			s = s[:63]
		}
		return encodeVarlen([]byte(s)), nil

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
			// Coerce integer to text-form when the target column is text-like.
			// This occurs for CTAS `SELECT generate_series(1,10)` where the
			// column is given a fallback "text" type. M0096-0008.
			s = fmt.Sprintf("%d", d.Int)
		case KindNumeric:
			s = numericText(d)
		default:
			return nil, fmt.Errorf("kind %d cannot encode as %s", d.Kind, t.Name)
		}
		// varchar(N) and char(N) length enforcement. M0097-0003.
		tname := strings.ToLower(t.Name)
		if tname == "varchar" || tname == "character varying" {
			if len(t.Args) > 0 {
				n := int(t.Args[0])
				// Strip trailing spaces: PostgreSQL accepts trailing spaces if the
				// stripped value fits within N (e.g., 'c     ' → 'c' in varchar(1)).
				stripped := strings.TrimRight(s, " ")
				if len(stripped) > n {
					return nil, &ExecError{Code: "22001",
						Message: fmt.Sprintf("value too long for type character varying(%d)", n)}
				}
				s = stripped
			}
		} else if tname == "char" || tname == "bpchar" || tname == "character" {
			// Bare `char` with no length argument defaults to char(1) in PostgreSQL.
			n := 1
			if len(t.Args) > 0 {
				n = int(t.Args[0])
			}

			stripped := strings.TrimRight(s, " ")
			if len(stripped) > n {
				return nil, &ExecError{Code: "22001",
					Message: fmt.Sprintf("value too long for type character(%d)", n)}
			}
			// Store the stripped value; the DataRow output path re-pads to N
			// for wire protocol display so psql sees N-width char columns.
			// We do NOT pad in storage to avoid breaking comparison semantics
			// (compareDatum uses strings.Compare which is padding-sensitive). M0097-0003.
			s = stripped
		}
		return encodeVarlen([]byte(s)), nil
	}
}

// isValidUUIDStr reports whether s is a valid UUID string in any of
// PostgreSQL's accepted formats:
//   - Standard: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx (36 chars)
//   - Braces:   {xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx} (38 chars)
//   - No-hyphen: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx (32 hex chars)
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

func decodeValue(t catalog.Type, data []byte) (Datum, int, error) {
	switch t.Name {
	case "int2", "smallint":
		if len(data) < 2 {
			return Datum{}, 0, fmt.Errorf("truncated int2")
		}
		v := int16(binary.BigEndian.Uint16(data[:2]))
		return Datum{Kind: KindInt, Int: int64(v)}, 2, nil
	case "int4", "integer", "int", "serial":
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated int4/serial")
		}
		v := int32(binary.BigEndian.Uint32(data[:4]))
		return Datum{Kind: KindInt, Int: int64(v)}, 4, nil
	case "oid":
		// oid is uint32 stored as 4-byte big-endian. M0097-0003.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated oid")
		}
		v := binary.BigEndian.Uint32(data[:4])
		return Datum{Kind: KindInt, Int: int64(v)}, 4, nil
	case "int8", "bigint", "bigserial":
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated int8/bigserial")
		}
		v := int64(binary.BigEndian.Uint64(data[:8]))
		return Datum{Kind: KindInt, Int: v}, 8, nil
	case "bool", "boolean":
		if len(data) < 1 {
			return Datum{}, 0, fmt.Errorf("truncated bool")
		}
		return NewBoolDatum(data[0] != 0), 1, nil
	case "timestamp", "timestamptz", "date", "time", "timetz":
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated %s", t.Name)
		}
		ns := int64(binary.BigEndian.Uint64(data[:8]))
		return NewTimeDatum(time.Unix(0, ns).UTC()), 8, nil
	case "float8", "double precision", "double", "float4", "real":
		// Stored as varlen text. Decode as KindNumeric for proper numeric sort/comparison.
		// M0097-0003.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated float")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if 4+n > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated float body")
		}
		text := string(data[4 : 4+n])
		if v, scale, ok := parseNumericFast(text); ok {
			return Datum{Kind: KindNumeric, Int: v, Scale: scale}, 4 + n, nil
		}
		m, s, err := parseNumeric(text)
		if err == nil {
			return newNumeric(m, int(s)), 4 + n, nil
		}
		// NaN / infinity fall back to string (handled by comparison/display).
		return NewStringDatum(text), 4 + n, nil
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
	case "int2", "smallint":
		if len(data) < 2 {
			return Datum{}, 0, fmt.Errorf("truncated int2")
		}
		v := int16(binary.BigEndian.Uint16(data[:2]))
		return Datum{Kind: KindInt, Int: int64(v)}, 2, nil
	case "int4", "integer", "int", "serial":
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated int4/serial")
		}
		v := int32(binary.BigEndian.Uint32(data[:4]))
		return Datum{Kind: KindInt, Int: int64(v)}, 4, nil
	case "oid":
		// oid is uint32 stored as 4-byte big-endian. M0097-0003.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated oid")
		}
		v := binary.BigEndian.Uint32(data[:4])
		return Datum{Kind: KindInt, Int: int64(v)}, 4, nil
	case "int8", "bigint", "bigserial":
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated int8/bigserial")
		}
		v := int64(binary.BigEndian.Uint64(data[:8]))
		return Datum{Kind: KindInt, Int: v}, 8, nil
	case "bool", "boolean":
		if len(data) < 1 {
			return Datum{}, 0, fmt.Errorf("truncated bool")
		}
		return NewBoolDatum(data[0] != 0), 1, nil
	case "timestamp", "timestamptz", "date", "time", "timetz":
		if len(data) < 8 {
			return Datum{}, 0, fmt.Errorf("truncated %s", t.Name)
		}
		ns := int64(binary.BigEndian.Uint64(data[:8]))
		return NewTimeDatum(time.Unix(0, ns).UTC()), 8, nil
	case "float8", "double precision", "double", "float4", "real":
		// Same as decodeValue: decode as KindNumeric for proper sort. M0097-0003.
		if len(data) < 4 {
			return Datum{}, 0, fmt.Errorf("truncated float")
		}
		n := int(binary.BigEndian.Uint32(data[:4]))
		if 4+n > len(data) {
			return Datum{}, 0, fmt.Errorf("truncated float body")
		}
		text := string(data[4 : 4+n])
		if v, scale, ok := parseNumericFast(text); ok {
			return Datum{Kind: KindNumeric, Int: v, Scale: scale}, 4 + n, nil
		}
		m, s, err := parseNumeric(text)
		if err == nil {
			return newNumeric(m, int(s)), 4 + n, nil
		}
		return NewStringDatum(text), 4 + n, nil
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
