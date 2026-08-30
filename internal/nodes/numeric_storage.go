package nodes

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"strings"
)

// PostgreSQL's on-disk NumericData, exported for the HEAP.
//
// M0119-0006 (the numeric-column storage slice). `parseNumericVar`/`varlena`
// and `decodeNumericVar`/`text` above were written for pg_node_tree — a numeric
// Const's constvalue, which outfuncs.c dumps byte-for-byte. They are, however,
// a complete port of numeric_in/numeric_out's serialization, and the heap needs
// exactly the same bytes: `executor.encodeValuePG` used to store a numeric
// column as the DECIMAL STRING behind a varlena header, which any reader that
// trusts pg_type (a PG 18.3 standby, pg_amcheck's heap tier, a logical
// subscriber) feeds straight to numeric_out as a NumericData and misreads.
//
// The three functions below are the only supported way in for a caller outside
// this package; they take and return the varlena PAYLOAD (the NumericData body
// with no length header) because the heap's own varlena framing — short 1-byte
// header when it fits, 4-byte otherwise — belongs to the caller, and PG's
// heap_fill_tuple makes the same choice independently of make_result.

// NumericBodyFromText encodes a decimal / scientific literal, or one of the
// NaN / ±Infinity spellings numeric_in accepts, as PostgreSQL's NumericData
// body: the uint16 n_header (short form) or n_sign_dscale + int16 n_weight
// (long form) followed by the base-10000 digits, little-endian.
//
// Leading and trailing whitespace is trimmed first, as numeric_in does.
func NumericBodyFromText(text string) ([]byte, error) {
	v, err := parseNumericVar(strings.TrimSpace(text), false)
	if err != nil {
		return nil, err
	}
	full := v.varlena() // 4-byte varlena header + body
	return full[4:], nil
}

// NumericTextFromBody inverts NumericBodyFromText, rendering the canonical
// numeric_out text (sign included; a NUMERIC_SPECIAL renders as NaN /
// Infinity / -Infinity).
func NumericTextFromBody(body []byte) (string, error) {
	// decodeNumericVar reads a full varlena (it skips 4 bytes of header), so
	// re-frame the body rather than duplicating its bit-picking here.
	buf := make([]byte, 4+len(body))
	copy(buf[4:], body)
	v, err := decodeNumericVar(buf)
	if err != nil {
		return "", err
	}
	if v.special != 0 {
		return v.specialText(), nil
	}
	s := v.text()
	if v.negative {
		return "-" + s, nil
	}
	return s, nil
}

// NumericTextFromStoredPayload renders the text of a numeric varlena payload
// read off disk, accepting BOTH the PG-faithful NumericData body and the
// pre-M0119-0006 legacy form, which was the decimal string itself.
//
// The flip has no on-disk migration (ledger, 2026-08-10), so every cluster
// created before it holds text payloads in its numeric columns — including the
// TPC-H and TPC-DS benchmark clusters, whose row-count gates read them. This
// function is what lets those rows keep decoding, and it is shared by the two
// readers of the heap layout (executor's decodePhysicalPGValueMctx and
// internal/wal's pgoutput) so the discrimination rule cannot drift between
// them.
//
// The rule is a charset test, and it is exact rather than heuristic: a payload
// whose every byte lies in the decimal-literal set is ALWAYS legacy text,
// because no NumericData body can be spelled entirely from that set.
//
//   - short form: n_header has NUMERIC_SHORT (0x8000) set, so its high byte —
//     body[1], little-endian — is >= 0x80, above every byte in the set (max
//     'e' = 0x65).
//   - special (NaN/±Inf): n_header is 0xC000/0xD000/0xF000; same argument.
//   - long form with at least one digit: every NBASE digit is 0..9999, so its
//     high byte is <= 0x27, below every byte in the set (min '+' = 0x2B).
//   - long form with no digits: that is the value zero (strip_var empties the
//     digit array only for zero), whose n_sign_dscale is NUMERIC_POS | dscale
//     — high byte dscale>>8, and the long form is only chosen for dscale > 63,
//     so the byte is 0x00 for every dscale below 11008, far past
//     NUMERIC_MAX_DISPLAY_SCALE.
//
// So the two forms are disjoint under this test in both directions, and a
// legacy payload is never mistaken for NumericData nor the reverse.
func NumericTextFromStoredPayload(payload []byte) (string, error) {
	if numericPayloadIsLegacyText(payload) {
		return string(payload), nil
	}
	s, err := NumericTextFromBody(payload)
	if err != nil {
		return "", fmt.Errorf("numeric: %w", err)
	}
	return s, nil
}

// numericPayloadIsLegacyText reports whether payload is the pre-M0119-0006
// stored form — the decimal string, or one of the special spellings, which
// `coerceTextLikeDatum` could pass through verbatim.
//
// The charset loop runs FIRST and the special-spelling switch second, which is
// the reverse of the original order and is exactly equivalent to it: the two
// tests are disjoint, because every accepted spelling contains a letter
// (n/a/i/f/t/y) that the decimal-literal charset excludes, so no payload can
// satisfy both. Order mattered for cost, not for the answer — a NumericData
// body is arbitrary binary, and leading with `strings.ToLower` sent every
// non-ASCII payload through `strings.Map`'s allocating Unicode path just to
// reach a switch that could never match. On the TPC-H Q6 scan that was 16.7 %
// of the whole query's CPU spent lowercasing bytes that were never text
// (docs/design/not_ralph/tpch-q6-numeric-decode/DESIGN.md §5.2).
func numericPayloadIsLegacyText(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	if numericPayloadIsDecimalCharset(payload) {
		return true
	}
	return numericPayloadIsSpecialSpelling(payload)
}

// numericSpecialSpellings are the forms numeric_in accepts for the non-finite
// values, as a legacy text payload could have spelled them.
var numericSpecialSpellings = [][]byte{
	[]byte("nan"), []byte("infinity"), []byte("-infinity"),
	[]byte("+infinity"), []byte("inf"), []byte("-inf"), []byte("+inf"),
}

// numericPayloadIsSpecialSpelling is the allocation-free equivalent of
// `strings.ToLower(strings.TrimSpace(string(payload)))` matched against
// numericSpecialSpellings. bytes.TrimSpace applies the same Unicode
// whitespace rule as strings.TrimSpace but returns a sub-slice, and
// bytes.EqualFold does the case-insensitive compare in place — so neither
// step copies the payload, which is what made the original expensive on
// NumericData bodies (arbitrary binary sent through strings.Map).
func numericPayloadIsSpecialSpelling(payload []byte) bool {
	t := bytes.TrimSpace(payload)
	// "inf" is the shortest spelling, "-infinity" the longest; anything
	// outside that band cannot match and skips the compares entirely.
	if len(t) < 3 || len(t) > 9 {
		return false
	}
	// Every spelling begins with one of these six bytes, so one comparison
	// rejects almost all NumericData bodies before the EqualFold loop. Worth
	// having: a short-form header's low byte is the weight, typically 0x00 or
	// 0x01, and the length band alone does NOT exclude it — a 4-6 byte body
	// sits squarely inside it. Without this gate the compares cost 7.8 % of
	// the Q6 scan.
	switch t[0] {
	case 'n', 'N', 'i', 'I', '+', '-':
	default:
		return false
	}
	for _, sp := range numericSpecialSpellings {
		if bytes.EqualFold(t, sp) {
			return true
		}
	}
	return false
}

// numericPayloadIsDecimalCharset reports whether every byte of payload lies in
// the decimal-literal set. It is the allocation-free half of the storage-form
// discrimination described on NumericTextFromStoredPayload.
func numericPayloadIsDecimalCharset(payload []byte) bool {
	for _, b := range payload {
		switch {
		case b >= '0' && b <= '9':
		case b == '+' || b == '-' || b == '.' || b == 'e' || b == 'E':
		default:
			return false
		}
	}
	return true
}

// NumericInt64FromStoredPayload decodes a stored numeric varlena payload
// straight into the (mantissa, dscale) pair goopg's KindNumeric Datum carries
// — the value is mantissa × 10^-dscale — without ever materialising
// numeric_out text.
//
// It is a fast path in front of NumericTextFromStoredPayload, not a
// replacement: ok=false means "fall back to the text path", and the caller
// must do so. That happens for
//
//   - legacy-text payloads (the pre-M0119-0006 stored form),
//   - NUMERIC_SPECIAL — NaN, +Infinity, -Infinity,
//   - mantissas that do not fit int64 (the *big.Int lane),
//   - a value carrying significant digits below its own dscale, which
//     PostgreSQL does not produce but which is refused rather than rounded,
//   - anything malformed.
//
// Why this exists: reading a numeric column used to cost a full round trip —
// NumericData → numeric_out text → math/big.Int — which measured 47 % of all
// CPU and ~7.8 heap allocations per value on a TPC-H Q6 scan. Both endpoints
// are exact integers with an explicit decimal scale, so the conversion is
// integer arithmetic (docs/design/not_ralph/tpch-q6-numeric-decode/DESIGN.md).
//
// The arithmetic, mirroring numeric.c's NBASE=10000 / DEC_DIGITS=4 layout:
// with digits d[0..n-1] read as one base-10000 integer D and a weight giving
// the power of 10000 that d[0] carries,
//
//	value    = D · 10000^(weight-n+1)
//	mantissa = value · 10^dscale = D · 10^(4·(weight-n+1) + dscale)
//
// so with e = 4·(weight-n+1) + dscale the result is D·10^e for e ≥ 0 and
// D/10^-e for e < 0, the latter required to divide exactly.
func NumericInt64FromStoredPayload(payload []byte) (int64, int16, bool) {
	// The storage-form discrimination stays in one place: a legacy text
	// payload must not be read as a NumericData header.
	if len(payload) < 2 || numericPayloadIsLegacyText(payload) {
		return 0, 0, false
	}
	header := binary.LittleEndian.Uint16(payload)
	var (
		negative bool
		dscale   int
		weight   int
		off      int
	)
	switch header & numericSignMask {
	case numericShort:
		negative = header&numericShortSignMask != 0
		dscale = int((header & numericShortDscaleMask) >> numericShortDscaleShift)
		w := int(header & numericShortWeightMask)
		if header&numericShortWeightSignMask != 0 {
			w -= numericShortWeightMask + 1 // sign-extend the 7-bit weight
		}
		weight = w
		off = 2
	case numericPos, numericNeg:
		if len(payload) < 4 {
			return 0, 0, false
		}
		negative = header&numericSignMask == numericNeg
		dscale = int(header & numericDscaleMask)
		weight = int(int16(binary.LittleEndian.Uint16(payload[2:])))
		off = 4
	default:
		// NUMERIC_SPECIAL (NaN / ±Infinity) has no int64 mantissa.
		return 0, 0, false
	}
	if dscale < 0 || dscale > numericMaxInt16Scale {
		return 0, 0, false
	}

	rest := payload[off:]
	n := len(rest) / 2
	if len(rest)%2 != 0 {
		return 0, 0, false
	}
	// Zero is stored digitless (strip_var empties the array only for zero),
	// and no exponent can change it.
	if n == 0 {
		return 0, int16(dscale), true
	}

	// D is accumulated in 128 bits. It has to be: the intermediate routinely
	// exceeds int64 on values whose FINAL mantissa fits, because the trailing
	// base-10000 digit carries zero-padding that the e<0 division removes.
	// 999999999999.999999 is the worked example — D = 99999999999999999900,
	// past int64, while the mantissa 999999999999999999 fits comfortably.
	var hi, lo uint64
	for i := 0; i < n; i++ {
		dig := int16(binary.LittleEndian.Uint16(rest[2*i:]))
		if dig < 0 || dig >= numericNBase {
			return 0, 0, false
		}
		if hi > math.MaxUint64/numericNBase {
			return 0, 0, false
		}
		carryHi, low := bits.Mul64(lo, numericNBase)
		newHi := hi*numericNBase + carryHi
		if newHi < carryHi {
			return 0, 0, false // 128-bit overflow
		}
		var c uint64
		lo, c = bits.Add64(low, uint64(dig), 0)
		newHi += c
		hi = newHi
	}

	e := 4*(weight-n+1) + dscale
	switch {
	case e > 0:
		if e > numericMaxPow10 || hi != 0 {
			return 0, 0, false
		}
		h, l := bits.Mul64(lo, uint64(pow10Int64[e]))
		if h != 0 {
			return 0, 0, false
		}
		lo = l
	case e < 0:
		if -e > numericMaxPow10 {
			// 10^-e exceeds uint64, so a non-zero D cannot divide exactly.
			return 0, 0, false
		}
		p := uint64(pow10Int64[-e])
		if hi >= p {
			return 0, 0, false // quotient would not fit 64 bits (Div64 panics)
		}
		q, r := bits.Div64(hi, lo, p)
		if r != 0 {
			// Significant digits below dscale: refuse rather than round.
			return 0, 0, false
		}
		hi, lo = 0, q
	}
	if hi != 0 {
		return 0, 0, false
	}

	if negative {
		// The negative range reaches one further than the positive one, so
		// -9223372036854775808 is representable and must not be refused.
		if lo > 1<<63 {
			return 0, 0, false
		}
		if lo == 1<<63 {
			return math.MinInt64, int16(dscale), true
		}
		return -int64(lo), int16(dscale), true
	}
	if lo > math.MaxInt64 {
		return 0, 0, false
	}
	return int64(lo), int16(dscale), true
}

const (
	// numericNBase is numeric.c's NBASE — digits are base 10000.
	numericNBase = 10000
	// numericMaxPow10 is the largest exponent with 10^e inside int64.
	numericMaxPow10 = 18
	// numericMaxInt16Scale bounds dscale to what an int16 Datum scale holds;
	// PG's own NUMERIC_MAX_DISPLAY_SCALE is far below it.
	numericMaxInt16Scale = math.MaxInt16
)

// pow10Int64[i] is 10^i for i in 0..numericMaxPow10.
var pow10Int64 = [numericMaxPow10 + 1]int64{
	1, 10, 100, 1000, 10000, 100000, 1000000, 10000000, 100000000,
	1000000000, 10000000000, 100000000000, 1000000000000, 10000000000000,
	100000000000000, 1000000000000000, 10000000000000000, 100000000000000000,
	1000000000000000000,
}
