package wal

import (
	"encoding/binary"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/pgnodes"
)

// pgoDecodePhysicalValue is the SECOND decoder of goopg's heap layout (the
// executor's decodePhysicalPGValueMctx is the first), and the two must agree —
// .ralph/PROMPT.md hard-won rule #2. When numeric columns moved from the
// decimal string to PG's base-10000 NumericData (M0119-0006), an unrouted
// numeric here would have kept taking the varlena fall-through, which returns
// the payload verbatim: the subscriber would receive the raw digit array as if
// it were text, WITHOUT any error — the pgoutput protocol declares the value's
// length, not its spelling.
//
// The expected text is numeric_out's (postgres/src/backend/utils/adt/numeric.c
// get_str_from_var).
func TestPgoDecodeNumericMatchesPGNativeLayout(t *testing.T) {
	typ := catalog.Type{Name: "numeric"}
	for _, text := range []string{"0", "1", "-1", "1234.5", "-1234.5", "1.00", "1e100", "-0.000000000001"} {
		t.Run(text, func(t *testing.T) {
			body, err := pgnodes.NumericBodyFromText(text)
			if err != nil {
				t.Fatalf("NumericBodyFromText(%q): %v", text, err)
			}
			raw := shortVarlena(body)
			got, n, err := pgoDecodePhysicalValue(typ, raw, nil)
			if err != nil {
				t.Fatalf("pgoDecodePhysicalValue(numeric): %v", err)
			}
			if n != len(raw) {
				t.Fatalf("consumed %d of %d bytes", n, len(raw))
			}
			want, err := pgnodes.NumericTextFromBody(body)
			if err != nil {
				t.Fatalf("NumericTextFromBody: %v", err)
			}
			if string(got) != want {
				t.Errorf("decoded %q, want %q", got, want)
			}
			if string(got) == string(body) {
				t.Errorf("decoded the raw digit array as text (% x)", body)
			}
		})
	}
}

// A cluster written before the flip replicates too: the same dual-form read the
// executor's heap decoder does (pgnodes.NumericTextFromStoredPayload).
func TestPgoDecodeNumericAcceptsLegacyTextPayload(t *testing.T) {
	typ := catalog.Type{Name: "numeric"}
	for _, text := range []string{"0", "1234.5", "-1234.5", "1.00"} {
		got, _, err := pgoDecodePhysicalValue(typ, shortVarlena([]byte(text)), nil)
		if err != nil {
			t.Fatalf("legacy %q: %v", text, err)
		}
		if string(got) != text {
			t.Errorf("legacy %q decoded to %q", text, got)
		}
	}
}

// shortVarlena wraps a payload the way the heap does (1-byte header when it
// fits, else 4-byte) — the framing executor.varlenaBytes writes.
func shortVarlena(b []byte) []byte {
	if total := len(b) + 1; total <= 127 {
		out := make([]byte, total)
		out[0] = byte(total<<1) | 1
		copy(out[1:], b)
		return out
	}
	out := make([]byte, len(b)+4)
	binary.LittleEndian.PutUint32(out[0:4], uint32(len(b)+4)<<2)
	copy(out[4:], b)
	return out
}
