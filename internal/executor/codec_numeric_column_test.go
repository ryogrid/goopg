package executor

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// The numeric-column storage slice of M0119-0006 — the last member of the
// heap-side divergence class the uuid and interval slices worked through.
//
// Until it landed, a `numeric` column was stored through varlenaTextBytes as
// its DECIMAL STRING. pg_type agrees numeric is a varlena (typlen -1, typalign
// 'i', typstorage 'm'), so unlike uuid the descriptor never disagreed with the
// bytes and nothing was misaligned — but the PAYLOAD was goopg's convenience
// form, and every reader that trusts the type hands it to numeric_out as a
// NumericData: a PG 18.3 standby, pg_amcheck's heap tier, a logical
// subscriber. "1234" is read as n_header 0x3231 (NUMERIC_POS, dscale 12849)
// with weight 13363.
//
// Layout reference: postgres/src/backend/utils/adt/numeric.c
// (make_result_opt_error / NumericShort / NumericLong, NBASE 10000,
// DEC_DIGITS 4) — ported in internal/pgnodes/datum.go for pg_node_tree and
// exported for the heap by internal/pgnodes/numeric_storage.go.

func numericType() catalog.Type { return catalog.Type{Name: "numeric"} }

// numericPayload strips the varlena header the heap wraps the NumericData in.
func numericPayload(t *testing.T, enc []byte) []byte {
	t.Helper()
	payload, n, err := decodePhysicalPGVarlena(enc)
	if err != nil {
		t.Fatalf("decodePhysicalPGVarlena: %v", err)
	}
	if n != len(enc) {
		t.Fatalf("varlena consumed %d of %d bytes", n, len(enc))
	}
	return payload
}

// TestNumericColumnStoresNumericData pins the on-disk bytes for a value whose
// text and NumericData forms are both short, so only a byte-level assertion
// can tell them apart. 1234.5 is one NBASE digit of 1234 at weight 0 plus one
// of 5000 at weight -1, dscale 1 → short header 0x8080 | (1<<7) = 0x8080.
func TestNumericColumnStoresNumericData(t *testing.T) {
	enc, err := encodeValuePG(numericType(), NewNumericInt64Datum(12345, 1))
	if err != nil {
		t.Fatalf("encodeValuePG(numeric): %v", err)
	}
	body := numericPayload(t, enc)
	// NUMERIC_SHORT | dscale 1 << 7 | weight 0 == 0x8080, then digits 1234, 5000.
	want := []byte{0x80, 0x80, 0xd2, 0x04, 0x88, 0x13}
	if !bytes.Equal(body, want) {
		t.Fatalf("NumericData body = % x, want % x", body, want)
	}
	if bytes.Contains(body, []byte("1234")) {
		t.Fatalf("body still contains the decimal string: % x", body)
	}
}

// TestNumericColumnRoundTrip walks the shapes numeric_in/numeric_out must
// preserve exactly: sign, weight far from zero, trailing fractional zeros
// (dscale is display state the digit array does not carry), the long-header
// threshold (dscale > 63), and the three specials.
func TestNumericColumnRoundTrip(t *testing.T) {
	longScale := "0." + string(bytes.Repeat([]byte("0"), 70)) + "1" // dscale 71 → long header
	for _, text := range []string{
		"0", "1", "-1", "1234.5", "-1234.5", "0.5", "1.00", "100.00",
		"12345678901234567890123456789", "-0.000000000001",
		"1e100", longScale,
		// NaN / ±Infinity are deliberately absent: the encoder writes their
		// NUMERIC_SPECIAL headers correctly, but goopg's KindNumeric Datum has
		// no representation for them, so `parseNumeric` rejects the decoded
		// text exactly as it rejected the legacy stored text. Pre-existing gap,
		// unchanged by this slice — ledger row 2026-08-10.
	} {
		enc, err := encodeValuePG(numericType(), NewStringDatum(text))
		if err != nil {
			t.Fatalf("encodeValuePG(%q): %v", text, err)
		}
		d, n, err := decodePhysicalPGValueMctx(numericType(), enc, nil)
		if err != nil {
			t.Fatalf("decode(%q): %v", text, err)
		}
		if n != len(enc) {
			t.Errorf("%q: decode consumed %d of %d bytes", text, n, len(enc))
		}
		// numeric_out's spelling of 1e100 is the expanded integer, so compare
		// through a second encode rather than against the input spelling.
		reenc, err := encodeValuePG(numericType(), d)
		if err != nil {
			t.Fatalf("re-encode(%q): %v", text, err)
		}
		if !bytes.Equal(enc, reenc) {
			t.Errorf("%q: round trip changed the image: % x -> % x", text, enc, reenc)
		}
	}
}

// TestNumericColumnDecodesLegacyTextPayload is the reason the flip needs no
// on-disk migration: every cluster written before it — the TPC-H and TPC-DS
// benchmark clusters among them, whose row-count gates read numeric columns in
// their filters — holds the decimal string in its numeric columns. The
// discrimination rule is exact rather than heuristic (see
// pgnodes.NumericTextFromStoredPayload), so both forms decode to the same
// Datum.
func TestNumericColumnDecodesLegacyTextPayload(t *testing.T) {
	for _, text := range []string{"0", "1", "1234.5", "-1234.5", "1.00", "-0.000000000001"} {
		legacy := varlenaTextBytes(text) // exactly what the pre-flip encoder wrote
		got, _, err := decodePhysicalPGValueMctx(numericType(), legacy, nil)
		if err != nil {
			t.Fatalf("decode legacy %q: %v", text, err)
		}
		want, _, err := decodePhysicalPGValueMctx(numericType(),
			mustEncodeNumeric(t, text), nil)
		if err != nil {
			t.Fatalf("decode new-format %q: %v", text, err)
		}
		if got.Format() != want.Format() {
			t.Errorf("%q: legacy payload decoded to %q, new-format payload to %q",
				text, got.Format(), want.Format())
		}
	}
}

// TestNumericStoredFormsAreDisjoint is the proof obligation behind that dual
// read: no NumericData body may be spelled entirely out of the decimal-literal
// charset, or a new-format value would be misread as legacy text. The argument
// is a case analysis on the header (see NumericTextFromStoredPayload); this
// sweeps a wide value set to keep it honest.
func TestNumericStoredFormsAreDisjoint(t *testing.T) {
	inCharset := func(b []byte) bool {
		for _, c := range b {
			switch {
			case c >= '0' && c <= '9':
			case c == '+' || c == '-' || c == '.' || c == 'e' || c == 'E':
			default:
				return false
			}
		}
		return len(b) > 0
	}
	mant := int64(1)
	for i := 0; i < 19; i++ {
		for _, scale := range []int16{0, 1, 2, 5, 20, 63, 64, 100} {
			for _, sign := range []int64{1, -1} {
				enc, err := encodeValuePG(numericType(), NewNumericInt64Datum(sign*mant, scale))
				if err != nil {
					t.Fatalf("encodeValuePG(%d/%d): %v", sign*mant, scale, err)
				}
				if body := numericPayload(t, enc); inCharset(body) {
					t.Fatalf("NumericData body for %d/%d is all-charset (% x) — it would be misread as legacy text",
						sign*mant, scale, body)
				}
			}
		}
		mant = mant*10 + int64(i%9) + 1
	}
	// Zero and the specials take the other header paths.
	for _, text := range []string{"0", "NaN", "Infinity", "-Infinity"} {
		if body := numericPayload(t, mustEncodeNumeric(t, text)); inCharset(body) {
			t.Fatalf("NumericData body for %q is all-charset (% x)", text, body)
		}
	}
}

func mustEncodeNumeric(t *testing.T, text string) []byte {
	t.Helper()
	enc, err := encodeValuePG(numericType(), NewStringDatum(text))
	if err != nil {
		t.Fatalf("encodeValuePG(%q): %v", text, err)
	}
	return enc
}
