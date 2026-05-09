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

// --- type-specific binary encoders ---

func datumToCopyBinary(t catalog.Type, d Datum) ([]byte, error) {
	switch strings.ToLower(t.Name) {
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
	case "date":
		if d.Kind != KindTime {
			return nil, fmt.Errorf("expected time, got kind %d", d.Kind)
		}
		epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		days := int32(d.TimeValue().UTC().Truncate(24*time.Hour).Sub(epoch).Hours() / 24)
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(days))
		return b, nil
	case "numeric", "decimal":
		return encodeNumericBinary(d)
	default:
		// text / varchar / char / bytea — raw bytes
		switch d.Kind {
		case KindString, KindStringArena:
			return []byte(d.StringValue()), nil
		case KindBytes, KindBytesArena:
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

func copyBinaryToDatum(t catalog.Type, payload []byte) (Datum, error) {
	switch strings.ToLower(t.Name) {
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
		return NewTimeDatum(pgEpoch.Add(time.Duration(usec) * time.Microsecond)), nil
	case "date":
		if len(payload) != 4 {
			return Datum{}, fmt.Errorf("date: expected 4 bytes, got %d", len(payload))
		}
		days := int32(binary.BigEndian.Uint32(payload))
		t := pgEpoch.AddDate(0, 0, int(days))
		return NewTimeDatum(t), nil
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
