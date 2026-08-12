package executor

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// COPY BINARY format (PostgreSQL wire protocol):
//
//   Header  (19 bytes): 11-byte signature + int32 flags + int32 ext-area-len
//   Per row (variable): int16 fieldCount, then per field int32 len (-1=NULL) + bytes
//   Trailer (2 bytes):  int16 = -1
//
// Upstream references:
//   postgres/src/backend/commands/copyto.c     — writer
//   postgres/src/backend/commands/copyfromparse.c — parser

// copyBinarySignature is the 11-byte magic from the PostgreSQL source.
var copyBinarySignature = []byte("PGCOPY\n\377\r\n\000")

// CopyBinaryHeader returns the 19-byte PGCOPY binary header.
func CopyBinaryHeader() []byte {
	hdr := make([]byte, 19)
	copy(hdr, copyBinarySignature)
	// flags int32 = 0 (no OID)
	// extension area length int32 = 0
	return hdr
}

// AppendCopyBinaryRow encodes one row into binary COPY format and
// appends it to dst. Caller owns dst; returned slice may alias dst.
func AppendCopyBinaryRow(dst []byte, row Row, cols []catalog.Column) ([]byte, error) {
	if len(row) != len(cols) {
		return nil, fmt.Errorf("binary COPY: %d cols vs %d datums", len(cols), len(row))
	}
	dst = appendInt16(dst, int16(len(cols)))
	for i, col := range cols {
		d := row[i]
		if d.IsNull() {
			dst = appendInt32(dst, -1)
			continue
		}
		payload, err := datumToCopyBinary(col.Type, d)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", col.Name, err)
		}
		dst = appendInt32(dst, int32(len(payload)))
		dst = append(dst, payload...)
	}
	return dst, nil
}

// AppendCopyBinaryTrailer appends the two-byte binary COPY trailer
// (int16 = -1) to dst and returns the extended slice.
func AppendCopyBinaryTrailer(dst []byte) []byte {
	return appendInt16(dst, -1)
}

// ParseCopyBinaryHeader checks that data starts with the correct PGCOPY
// signature. Returns the number of bytes consumed (19) or an error.
func ParseCopyBinaryHeader(data []byte) (int, error) {
	if len(data) < 19 {
		return 0, fmt.Errorf("binary COPY header too short: %d bytes", len(data))
	}
	for i, b := range copyBinarySignature {
		if data[i] != b {
			return 0, fmt.Errorf("binary COPY: invalid signature byte %d: got %#02x want %#02x", i, data[i], b)
		}
	}
	// flags and extension area are ignored (always 0 for us).
	return 19, nil
}

// ParseCopyBinaryRows parses all complete binary COPY rows from data
// and returns them. data must start at the first row (after the header).
//
// Returns (rows, trailerFound, bytesConsumed, error). trailerFound is
// true when the int16(-1) trailer was present in data.
func ParseCopyBinaryRows(data []byte, cols []catalog.Column) (rows []Row, trailerFound bool, consumed int, err error) {
	pos := 0
	for pos < len(data) {
		if pos+2 > len(data) {
			break // incomplete int16 — wait for more data
		}
		fieldCount := int16(binary.BigEndian.Uint16(data[pos:]))
		if fieldCount == -1 {
			pos += 2
			return rows, true, pos, nil
		}
		if int(fieldCount) != len(cols) {
			return nil, false, 0, fmt.Errorf("binary COPY: row has %d fields, expected %d", fieldCount, len(cols))
		}
		pos += 2
		row := make(Row, fieldCount)
		for i := range row {
			if pos+4 > len(data) {
				// incomplete row — return what we have and the partial position
				return rows, false, pos - 2, nil
			}
			length := int32(binary.BigEndian.Uint32(data[pos:]))
			pos += 4
			if length == -1 {
				row[i] = NullDatum
				continue
			}
			if int(length) < 0 || pos+int(length) > len(data) {
				// incomplete field data — back up to the start of this row
				return rows, false, pos - 2 - 4, nil
			}
			payload := data[pos : pos+int(length)]
			pos += int(length)
			d, err := copyBinaryToDatum(cols[i].Type, payload)
			if err != nil {
				return nil, false, 0, fmt.Errorf("column %q: %w", cols[i].Name, err)
			}
			row[i] = d
		}
		rows = append(rows, row)
	}
	return rows, false, pos, nil
}

// tzDispLimitSecs mirrors upstream TZDISP_LIMIT
// ((MAX_TZDISP_HOUR + 1) * SECS_PER_HOUR = 16 h, postgres/src/include/
// datatype/timestamp.h:143-144) — the sanity bound timetz_recv applies to a
// received GMT displacement.
const tzDispLimitSecs = int32(16 * 60 * 60)

// --- type-specific binary encoders ---

func datumToCopyBinary(t catalog.Type, d Datum) ([]byte, error) {
	if err := rejectBinaryCopyArray(t); err != nil {
		return nil, err
	}
	switch strings.ToLower(t.Name) {
	case "int2", "smallint":
		// M0119-0006 (52nd slice): before this arm `int2` fell through to the
		// default's KindInt escape and shipped EIGHT big-endian bytes where
		// upstream int2send (postgres/src/backend/utils/adt/int.c:98 —
		// pq_sendint16) ships exactly two, so every binary COPY of a smallint
		// column produced a stream a real PG client rejects with "incorrect
		// binary data format" (CopyReadBinaryAttribute's pq_getmsgend,
		// postgres/src/backend/commands/copyfromparse.c). The range check
		// mirrors the heap int2 encode arm (codec.go) rather than truncating:
		// PG can never hold an out-of-range int2 Datum, so a value that is out
		// of range here is goopg-side corruption and deserves 22003, not a
		// silent wrap.
		if d.Kind != KindInt {
			return nil, fmt.Errorf("expected int, got kind %d", d.Kind)
		}
		if d.Int < -32768 || d.Int > 32767 {
			return nil, &ExecError{Code: "22003",
				Message: fmt.Sprintf("value %q is out of range for type smallint", strings.TrimSpace(d.Format()))}
		}
		b := make([]byte, 2)
		binary.BigEndian.PutUint16(b, uint16(int16(d.Int)))
		return b, nil
	case "int4", "integer", "int":
		if d.Kind != KindInt {
			return nil, fmt.Errorf("expected int, got kind %d", d.Kind)
		}
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(int32(d.Int)))
		return b, nil
	case "int8", "bigint":
		if d.Kind != KindInt {
			return nil, fmt.Errorf("expected int, got kind %d", d.Kind)
		}
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(d.Int))
		return b, nil
	case "float4", "real":
		// M0119-0006 (53rd slice): before this arm a float column fell through to
		// the default, which for goopg's float Datum (KindNumeric/KindString —
		// there is no float Kind, see pgFloatFromDatum) ships the TEXT of the
		// value under FORMAT binary. Upstream float4send
		// (postgres/src/backend/utils/adt/float.c:350) is pq_sendfloat4, i.e. the
		// IEEE-754 bits reinterpreted as a uint32 and sent big-endian
		// (postgres/src/backend/libpq/pqformat.c:252) — 4 bytes, no text.
		f, err := pgFloatFromDatum(d, 32)
		if err != nil {
			return nil, err
		}
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, math.Float32bits(float32(f)))
		return b, nil
	case "float8", "double precision", "double", "float":
		// Sibling of the "float4" arm; float8send is pq_sendfloat8 (float.c:567),
		// the 8-byte big-endian IEEE-754 image. The accepted spellings match the
		// heap "float8" encode arm in codec.go, including the bare `float`
		// (PG's float = float8) that goopg stores verbatim as a declared name.
		f, err := pgFloatFromDatum(d, 64)
		if err != nil {
			return nil, err
		}
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, math.Float64bits(f))
		return b, nil
	case "oid", "regproc":
		// M0119-0006 (54th slice): before this arm an `oid` column fell through
		// to the default's KindInt escape and shipped EIGHT big-endian bytes.
		// Upstream oidsend (postgres/src/backend/utils/adt/oid.c:71) is
		// pq_sendint32 — exactly four — so every binary COPY of an oid column
		// produced a stream a real PG client rejects with "incorrect binary data
		// format" (CopyReadBinaryAttribute's pq_getmsgend). regproc shares the
		// arm because regprocsend IS oidsend upstream ("Exactly the same as
		// oidsend, so share code", regproc.c:208-212) — the same pairing the
		// heap codec already uses.
		v, err := pgUnsignedIDFromDatum(d, "oid", 32)
		if err != nil {
			return nil, err
		}
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(v))
		return b, nil
	case "xid":
		// xidsend (postgres/src/backend/utils/adt/xid.c:67) is pq_sendint32 over
		// the 4-byte TransactionId — same width and same default-arm bug as oid.
		v, err := pgUnsignedIDFromDatum(d, "xid", 32)
		if err != nil {
			return nil, err
		}
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(v))
		return b, nil
	case "xid8":
		// xid8send (xid.c:225) is pq_sendint64 over the FullTransactionId. The
		// default arm happened to ship the right EIGHT bytes for this one type,
		// which is why the divergence hid here and surfaced on the heap side
		// instead (see the "xid8" arm of encodeValuePG). Naming it explicitly is
		// what makes the two twins provably agree rather than coincidentally.
		v, err := pgUnsignedIDFromDatum(d, "xid8", 64)
		if err != nil {
			return nil, err
		}
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, v)
		return b, nil
	case "uuid":
		// uuid_send (postgres/src/backend/utils/adt/uuid.c:192) is
		// pq_sendbytes(uuid->data, UUID_LEN) — the raw 16 bytes, which is
		// byte-for-byte what the heap already stores since the uuid arm of
		// encodeValuePG stopped storing text. Before this arm the default shipped
		// the 36-character canonical TEXT under FORMAT binary; unlike the float
		// case that is not silent corruption (36 != 16, so uuid_recv's
		// pq_getmsgbytes fails) but it is still a stream no PG client can read.
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
	case "bool", "boolean":
		if d.Kind != KindBool {
			return nil, fmt.Errorf("expected bool, got kind %d", d.Kind)
		}
		if d.BoolValue() {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case "timestamp":
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		return encodeTimestampBinary(d.TimeValue().UTC()), nil
	case "timestamptz":
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		return encodeTimestampBinary(d.TimeValue().UTC()), nil
	case "time", "time without time zone":
		// M0119-0006 (51st slice): before this arm `time` fell through to the
		// default and shipped Datum.Format()'s TEXT under FORMAT binary — a
		// stream no real PG client can read. Upstream time_send
		// (postgres/src/backend/utils/adt/date.c) is exactly the 8-byte
		// big-endian TimeADT, i.e. the same microseconds-since-midnight the heap
		// stores; taking them from pgTimeMicros rather than from Hour/Minute/
		// Second is what carries the hour-24 rule (50th slice) into COPY.
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, uint64(pgTimeMicros(d.TimeValue())))
		return b, nil
	case "timetz", "time with time zone":
		// Sibling of the "time" arm. Upstream timetz_send is TimeADT then the
		// int32 zone; the zone's sign convention is PG's (positive = WEST of
		// UTC) while Datum.Scale keeps minutes EAST, so it negates — identical
		// to the heap "timetz" arm in codec.go, which is the twin this must
		// agree with (Hard-won Rule #2).
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		b := make([]byte, 12)
		binary.BigEndian.PutUint64(b[:8], uint64(pgTimeMicros(d.TimeValue())))
		binary.BigEndian.PutUint32(b[8:], uint32(int32(-d.TimeTZOffsetSecs())))
		return b, nil
	case "date":
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		days := int32(d.TimeValue().UTC().Truncate(24*time.Hour).Sub(epoch).Hours() / 24)
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(days))
		return b, nil
	case "interval":
		// M0119-0006 (55th slice): before this arm an `interval` column fell
		// through to the default, whose KindInterval Datum matches no Kind case
		// and so shipped Datum.Format()'s TEXT ('02:00:00') under FORMAT binary —
		// a stream no real PG client can read. Upstream interval_send
		// (postgres/src/backend/utils/adt/timestamp.c:1022) is
		// pq_sendint64(time) + pq_sendint32(day) + pq_sendint32(month): exactly
		// the {time,day,month} the heap already stores at the same offsets
		// (encodeValuePG's "interval" arm), differing only in byte order, so the
		// two twins share pgIntervalFieldsFromDatum rather than re-deriving the
		// fields. The ±infinity sentinels need no case here either: INTERVAL_NOEND
		// / INTERVAL_NOBEGIN *are* all-fields-at-their-extreme.
		months, days, micros, err := pgIntervalFieldsFromDatum(d)
		if err != nil {
			return nil, err
		}
		b := make([]byte, 16)
		binary.BigEndian.PutUint64(b[:8], uint64(micros))
		binary.BigEndian.PutUint32(b[8:12], uint32(days))
		binary.BigEndian.PutUint32(b[12:16], uint32(months))
		return b, nil
	case "numeric", "decimal":
		return encodeNumericBinary(d)
	default:
		// text / varchar / char / bytea — raw bytes
		switch d.Kind {
		case KindString:
			return []byte(d.StringValue()), nil
		case KindBytes:
			return d.BytesValue(), nil
		case KindInt:
			b := make([]byte, 8)
			binary.BigEndian.PutUint64(b, uint64(d.Int))
			return b, nil
		default:
			return []byte(d.Format()), nil
		}
	}
}

// rejectBinaryCopyArray refuses an array column in BINARY COPY, loudly and by
// name. Both halves of binary COPY dispatch on the element type name (an array
// column is catalog.Type{Name:<ELEMENT>, IsArray:true}), so before this guard an
// int4[] column reported "expected int, got kind 3" while a text[] or bytea[]
// column fell through to the default raw-bytes arm and SILENTLY shipped the
// array's "{a,b}" text where upstream ships array_send's binary shape (ndim,
// hasnull, elemtype, dims/lbounds, then a 4-byte length + the element's own
// send format per element — postgres/src/backend/utils/adt/arrayfuncs.c
// array_send / array_recv). Emitting the text under BINARY produces a stream no
// real PG client can read, so refusing is the PG-compatible answer until
// array_send/array_recv are ported (deferral ledger, M0119-0006). The TEXT and
// CSV formats are unaffected — they legitimately carry array_out's text.
func rejectBinaryCopyArray(t catalog.Type) error {
	if !t.IsArray {
		return nil
	}
	return &ExecError{Code: "0A000",
		Message: fmt.Sprintf("binary COPY of array type %s[] is not supported", t.Name)}
}

func copyBinaryToDatum(t catalog.Type, payload []byte) (Datum, error) {
	if err := rejectBinaryCopyArray(t); err != nil {
		return Datum{}, err
	}
	switch strings.ToLower(t.Name) {
	case "int2", "smallint":
		// Decode twin of the "int2" encode arm. Upstream int2recv (int.c:87) is
		// pq_getmsgint(buf, sizeof(int16)); the length is enforced because the
		// binary COPY parser runs pq_getmsgend after each attribute, so a field
		// that is not exactly two bytes is "incorrect binary data format"
		// upstream and an error here. Before this arm the payload came back
		// from the default as a STRING Datum — a smallint column silently held
		// two raw bytes of text (Hard-won Rule #2: encode and decode are twins).
		if len(payload) != 2 {
			return Datum{}, fmt.Errorf("int2: expected 2 bytes, got %d", len(payload))
		}
		v := int16(binary.BigEndian.Uint16(payload))
		return Datum{Kind: KindInt, Int: int64(v)}, nil
	case "int4", "integer", "int":
		if len(payload) != 4 {
			return Datum{}, fmt.Errorf("int4: expected 4 bytes, got %d", len(payload))
		}
		v := int32(binary.BigEndian.Uint32(payload))
		return Datum{Kind: KindInt, Int: int64(v)}, nil
	case "int8", "bigint":
		if len(payload) != 8 {
			return Datum{}, fmt.Errorf("int8: expected 8 bytes, got %d", len(payload))
		}
		v := int64(binary.BigEndian.Uint64(payload))
		return Datum{Kind: KindInt, Int: v}, nil
	case "float4", "real":
		// Decode twin of the "float4" encode arm: upstream float4recv
		// (float.c:339) is pq_getmsgfloat4, and the binary COPY parser's
		// pq_getmsgend makes any other length "incorrect binary data format".
		// The Datum shape is taken from the heap float4 decode arm
		// (decodePhysicalPGValueMctx, codec.go) — PGFloatOut's shortest round-trip text fed
		// through floatTextDatum — so a value that enters through binary COPY is
		// indistinguishable from the same value entering through INSERT, in
		// Format(), CAST-to-text and every type-agnostic path. Before this arm
		// the default handed back the four raw bytes as a STRING Datum.
		if len(payload) != 4 {
			return Datum{}, fmt.Errorf("float4: expected 4 bytes, got %d", len(payload))
		}
		f4 := float64(math.Float32frombits(binary.BigEndian.Uint32(payload)))
		return floatTextDatum(PGFloatOut(f4, 32)), nil
	case "float8", "double precision", "double", "float":
		if len(payload) != 8 {
			return Datum{}, fmt.Errorf("float8: expected 8 bytes, got %d", len(payload))
		}
		f8 := math.Float64frombits(binary.BigEndian.Uint64(payload))
		return floatTextDatum(PGFloatOut(f8, 64)), nil
	case "oid", "regproc":
		// Decode twin of the "oid"/"regproc" encode arm. Upstream oidrecv
		// (oid.c:60) is pq_getmsgint(buf, sizeof(Oid)) and regprocrecv IS oidrecv
		// (regproc.c:198-202); the binary COPY parser's pq_getmsgend makes any
		// other length "incorrect binary data format". The Datum shape is the
		// heap decode arm's — NewIntDatum over the UNSIGNED 32-bit value, so an
		// oid that entered through binary COPY compares and indexes identically
		// to the same oid entering through INSERT. Before this arm the default
		// handed back the raw bytes as a STRING Datum (Hard-won Rule #2).
		if len(payload) != 4 {
			return Datum{}, fmt.Errorf("oid: expected 4 bytes, got %d", len(payload))
		}
		return NewIntDatum(int64(binary.BigEndian.Uint32(payload))), nil
	case "xid":
		// xidrecv (xid.c:56) — pq_getmsgint over sizeof(TransactionId).
		if len(payload) != 4 {
			return Datum{}, fmt.Errorf("xid: expected 4 bytes, got %d", len(payload))
		}
		return NewIntDatum(int64(binary.BigEndian.Uint32(payload))), nil
	case "xid8":
		// xid8recv (xid.c:215) — pq_getmsgint64 over the FullTransactionId.
		if len(payload) != 8 {
			return Datum{}, fmt.Errorf("xid8: expected 8 bytes, got %d", len(payload))
		}
		return NewIntDatum(int64(binary.BigEndian.Uint64(payload))), nil
	case "uuid":
		// uuid_recv (uuid.c:181) copies exactly UUID_LEN bytes. The Datum is the
		// canonical lowercase-with-dashes KindString the heap decode produces —
		// the engine has no uuid Kind and speaks that text everywhere else.
		if len(payload) != 16 {
			return Datum{}, fmt.Errorf("uuid: expected 16 bytes, got %d", len(payload))
		}
		return NewStringDatum(uuidCanonicalFromBytes(payload)), nil
	case "bool", "boolean":
		if len(payload) != 1 {
			return Datum{}, fmt.Errorf("bool: expected 1 byte, got %d", len(payload))
		}
		return NewBoolDatum(payload[0] != 0), nil
	case "timestamp", "timestamptz":
		if len(payload) != 8 {
			return Datum{}, fmt.Errorf("timestamp: expected 8 bytes, got %d", len(payload))
		}
		usec := int64(binary.BigEndian.Uint64(payload))
		ts := pgEpoch.Add(time.Duration(usec) * time.Microsecond)
		// M0119-0006 (41st slice): the column type is in reach here, so this
		// decoder owes the same subtype tag the heap decode already applies
		// (decodePhysicalPGValueMctx, codec.go) — otherwise a value that entered through
		// binary COPY renders differently from the identical value that entered
		// through INSERT, in every type-agnostic path (Datum.Format(),
		// CAST-to-text, string concat, FK-violation DETAIL). Hard-won Rule #2.
		if isTimestampTZTypeName(t.Name) {
			return NewTimestampTZDatum(ts), nil
		}
		return NewTimeDatum(ts), nil
	case "time", "time without time zone":
		// Decode twin of the "time" arm in datumToCopyBinary. Upstream time_recv
		// range-checks the received TimeADT against [0, USECS_PER_DAY] — the
		// bound is INCLUSIVE, `24:00:00` being a real TimeADT — and raises
		// 22008 outside it, which is what a corrupt or foreign stream deserves
		// instead of a silently out-of-range Datum. (AdjustTimeForTypmod's
		// rounding is NOT applied here: goopg truncates at display, ledgered
		// under M0119-0006.)
		if len(payload) != 8 {
			return Datum{}, fmt.Errorf("time: expected 8 bytes, got %d", len(payload))
		}
		micros := int64(binary.BigEndian.Uint64(payload))
		if micros < 0 || micros > usecsPerDay {
			return Datum{}, &ExecError{Code: "22008", Message: "time out of range"}
		}
		return NewTimeDatum(pgTimeFromMicros(micros)), nil
	case "timetz", "time with time zone":
		// Decode twin of the "timetz" encode arm; upstream timetz_recv reads the
		// TimeADT then the int32 zone and applies the same TZDISP_LIMIT sanity
		// check (±16 h, postgres/src/include/datatype/timestamp.h) with its own
		// 22009 code. Zone sign is flipped back to Datum.Scale's east-of-UTC.
		if len(payload) != 12 {
			return Datum{}, fmt.Errorf("timetz: expected 12 bytes, got %d", len(payload))
		}
		micros := int64(binary.BigEndian.Uint64(payload[:8]))
		if micros < 0 || micros > usecsPerDay {
			return Datum{}, &ExecError{Code: "22008", Message: "time out of range"}
		}
		pgOffset := int32(binary.BigEndian.Uint32(payload[8:12]))
		if pgOffset <= -tzDispLimitSecs || pgOffset >= tzDispLimitSecs {
			return Datum{}, &ExecError{Code: "22009",
				Message: "time zone displacement out of range"}
		}
		return NewTimeTZDatum(pgTimeFromMicros(micros), int(-pgOffset)), nil
	case "date":
		if len(payload) != 4 {
			return Datum{}, fmt.Errorf("date: expected 4 bytes, got %d", len(payload))
		}
		days := int32(binary.BigEndian.Uint32(payload))
		d := pgEpoch.AddDate(0, 0, int(days))
		return NewDateDatum(d), nil
	case "interval":
		// Decode twin of the "interval" encode arm. Upstream interval_recv
		// (timestamp.c:997) is pq_getmsgint64(time) + pq_getmsgint(day) +
		// pq_getmsgint(month), and the binary COPY parser's pq_getmsgend makes
		// any other length "incorrect binary data format". The Datum shape is the
		// heap decode arm's (decodePhysicalPGValueMctx, codec.go) —
		// NewIntervalDatumFull — so an interval that entered through binary COPY
		// sorts, compares and prints identically to the same interval entering
		// through INSERT. Before this arm the default handed the 16 raw bytes back
		// as a STRING Datum, which then made every runtime interval operation
		// lexicographic (Hard-won Rule #2). interval_recv's trailing
		// AdjustIntervalForTypmod is NOT applied here — goopg truncates at display,
		// ledgered under M0119-0006 alongside AdjustTimeForTypmod.
		if len(payload) != 16 {
			return Datum{}, fmt.Errorf("interval: expected 16 bytes, got %d", len(payload))
		}
		micros := int64(binary.BigEndian.Uint64(payload[:8]))
		days := int32(binary.BigEndian.Uint32(payload[8:12]))
		months := int32(binary.BigEndian.Uint32(payload[12:16]))
		return NewIntervalDatumFull(months, days, micros), nil
	case "numeric", "decimal":
		return decodeNumericBinary(payload)
	default:
		return NewStringDatum(string(payload)), nil
	}
}

func encodeTimestampBinary(t time.Time) []byte {
	usec := t.Sub(pgEpoch).Microseconds()
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(usec))
	return b
}

// encodeNumericBinary encodes a Datum (KindNumeric or KindInt) into
// PostgreSQL's binary NUMERIC format:
//
//	int16 ndigits  — number of base-10000 digits
//	int16 weight   — weight of first digit (power of 10000)
//	int16 sign     — 0=positive, 0x4000=negative
//	int16 dscale   — declared decimal precision (digits after point)
//	ndigits × int16 — base-10000 digits, most significant first
func encodeNumericBinary(d Datum) ([]byte, error) {
	var text string
	switch d.Kind {
	case KindNumeric:
		text = d.Format()
	case KindInt:
		text = fmt.Sprintf("%d", d.Int)
	default:
		return nil, fmt.Errorf("cannot encode kind %d as numeric", d.Kind)
	}

	// Parse the text representation into integer + fractional parts.
	negative := false
	if strings.HasPrefix(text, "-") {
		negative = true
		text = text[1:]
	}
	dotIdx := strings.IndexByte(text, '.')
	var intPart, fracPart string
	var dscale int16
	if dotIdx >= 0 {
		intPart = text[:dotIdx]
		fracPart = text[dotIdx+1:]
		dscale = int16(len(fracPart))
	} else {
		intPart = text
	}

	// Pad fracPart to a multiple of 4.
	for len(fracPart)%4 != 0 {
		fracPart += "0"
	}

	// Convert intPart to base-10000 groups (from right to left).
	// pad to multiple of 4
	for len(intPart)%4 != 0 {
		intPart = "0" + intPart
	}
	var intGroups []uint16
	for i := 0; i < len(intPart); i += 4 {
		v := 0
		for _, c := range intPart[i : i+4] {
			v = v*10 + int(c-'0')
		}
		intGroups = append(intGroups, uint16(v))
	}

	// Convert fracPart to base-10000 groups (left to right).
	var fracGroups []uint16
	for i := 0; i < len(fracPart); i += 4 {
		v := 0
		for _, c := range fracPart[i : i+4] {
			v = v*10 + int(c-'0')
		}
		fracGroups = append(fracGroups, uint16(v))
	}

	// Trim leading zero groups from intGroups and trailing zero groups from fracGroups.
	for len(intGroups) > 0 && intGroups[0] == 0 {
		intGroups = intGroups[1:]
	}
	for len(fracGroups) > 0 && fracGroups[len(fracGroups)-1] == 0 {
		fracGroups = fracGroups[:len(fracGroups)-1]
	}

	digits := append(intGroups, fracGroups...)
	ndigits := int16(len(digits))
	weight := int16(len(intGroups)) - 1

	var sign int16
	if negative {
		sign = 0x4000
	}

	buf := make([]byte, 8+int(ndigits)*2)
	binary.BigEndian.PutUint16(buf[0:], uint16(ndigits))
	binary.BigEndian.PutUint16(buf[2:], uint16(weight))
	binary.BigEndian.PutUint16(buf[4:], uint16(sign))
	binary.BigEndian.PutUint16(buf[6:], uint16(dscale))
	for i, d := range digits {
		binary.BigEndian.PutUint16(buf[8+i*2:], d)
	}
	return buf, nil
}

func decodeNumericBinary(payload []byte) (Datum, error) {
	if len(payload) < 8 {
		return Datum{}, fmt.Errorf("numeric: payload too short (%d bytes)", len(payload))
	}
	ndigits := int(binary.BigEndian.Uint16(payload[0:]))
	weight := int(int16(binary.BigEndian.Uint16(payload[2:])))
	sign := binary.BigEndian.Uint16(payload[4:])
	dscale := int(binary.BigEndian.Uint16(payload[6:]))

	if len(payload) < 8+ndigits*2 {
		return Datum{}, fmt.Errorf("numeric: payload has %d bytes, need %d", len(payload), 8+ndigits*2)
	}

	digits := make([]int64, ndigits)
	for i := range digits {
		digits[i] = int64(binary.BigEndian.Uint16(payload[8+i*2:]))
	}

	// Reconstruct the numeric value as mantissa × 10^-dscale.
	// weight is the power of 10000 of the first digit.
	// Total integer value = sum(digits[i] * 10000^(weight-i)).
	// Mantissa (at scale dscale) = value * 10^dscale.
	var mantissa int64
	overflow := false
	for i, d := range digits {
		exp := weight - i
		if exp >= 0 {
			// Integer part
			for j := 0; j < exp; j++ {
				if mantissa > math.MaxInt64/10000 {
					overflow = true
					break
				}
				mantissa *= 10000
			}
			if overflow {
				break
			}
			mantissa += d
		} else {
			// Fractional part: digit d contributes at position -exp*4 digits after decimal
			// This is already handled by the dscale reconstruction below.
		}
	}

	if overflow || ndigits > 4 {
		// Fall back to big.Int path for large numerics.
		// Reconstruct via string.
		return decodeNumericBinaryViaBig(digits, weight, sign, dscale)
	}

	// Multiply mantissa by 10^dscale to get the integer representation.
	// Then store as KindNumeric with NumericMantissa and NumericScale.
	fullMantissa := int64(0)
	// Compute the value as: digits[0]*10000^weight + ... + digits[n-1]*10000^(weight-n+1)
	// then multiply by 10^dscale to get an integer mantissa.
	for i, d := range digits {
		exp := weight - i
		p := int64(1)
		for j := 0; j < exp; j++ {
			p *= 10000
		}
		for j := 0; j < (-exp); j++ {
			// Fractional part — we need dscale post-decimal digits
			_ = j
		}
		fullMantissa += d * p
	}
	// Now fullMantissa is the integer part; we need to add fractional.
	// Simpler: rebuild from string representation.
	return decodeNumericBinaryViaBig(digits, weight, sign, dscale)
}

func decodeNumericBinaryViaBig(digits []int64, weight int, sign uint16, dscale int) (Datum, error) {
	if len(digits) == 0 {
		return newNumeric(big.NewInt(0), 0), nil
	}
	// Build the value = sum(digits[i] * 10000^(weight-i))
	val := new(big.Int)
	ten000 := big.NewInt(10000)
	for i, d := range digits {
		exp := weight - i
		coeff := new(big.Int).SetInt64(d)
		if exp > 0 {
			pow := new(big.Int).Exp(ten000, big.NewInt(int64(exp)), nil)
			coeff.Mul(coeff, pow)
		} else if exp < 0 {
			// Divide by 10000^|exp| — handled by dscale tracking
			_ = exp
		}
		val.Add(val, coeff)
	}
	// Multiply by 10^dscale to get integer mantissa at the right scale.
	scale := big.NewInt(1)
	for i := 0; i < dscale; i++ {
		scale.Mul(scale, big.NewInt(10))
	}
	// Add fractional digits: digits after weight 0 contribute.
	// Actually rebuild via string text for correctness.
	text := numericBinaryToString(digits, weight, dscale)
	if sign == 0x4000 {
		text = "-" + text
	}
	m, s, err := parseNumeric(text)
	if err != nil {
		return Datum{}, fmt.Errorf("numeric decode: %w", err)
	}
	_ = val
	_ = scale
	return newNumeric(m, int(s)), nil
}

func numericBinaryToString(digits []int64, weight int, dscale int) string {
	// Convert base-10000 digits + weight to a decimal string.
	// weight=0 means digits[0] is the ones place (in base 10000).
	var intDigits []string
	var fracDigits []string

	for i, d := range digits {
		s := fmt.Sprintf("%04d", d)
		exp := weight - i
		if exp >= 0 {
			intDigits = append(intDigits, s)
		} else {
			fracDigits = append(fracDigits, s)
		}
	}

	intStr := strings.Join(intDigits, "")
	if intStr == "" {
		intStr = "0"
	}
	// Trim leading zeros but keep at least one digit.
	for len(intStr) > 1 && intStr[0] == '0' {
		intStr = intStr[1:]
	}

	if dscale == 0 {
		return intStr
	}

	fracStr := strings.Join(fracDigits, "")
	// Pad/trim fracStr to exactly dscale digits.
	for len(fracStr) < dscale {
		fracStr += "0"
	}
	if len(fracStr) > dscale {
		fracStr = fracStr[:dscale]
	}
	return intStr + "." + fracStr
}

func appendInt16(dst []byte, v int16) []byte {
	return append(dst, byte(v>>8), byte(v))
}

func appendInt32(dst []byte, v int32) []byte {
	return append(dst, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
