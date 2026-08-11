package pgnodes

import (
	"fmt"
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
func numericPayloadIsLegacyText(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(string(payload))) {
	case "nan", "infinity", "-infinity", "+infinity", "inf", "-inf", "+inf":
		return true
	}
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
