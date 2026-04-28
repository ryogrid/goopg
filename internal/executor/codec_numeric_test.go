package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestEncodeDecodeNumericRoundTrip pins the M0003 NUMERIC codec
// behaviour: integer literals (e.g. `INSERT INTO t (n) VALUES (1)`)
// flow through the encoder as their decimal-string form, and
// SELECT * round-trips them as KindString. Without this case TPC-H
// loaders fail with "integer datum cannot encode as numeric".
func TestEncodeDecodeNumericRoundTrip(t *testing.T) {
	cols := []catalog.Column{
		{Name: "n", Type: catalog.Type{Name: "numeric"}},
	}
	row := Row{{Kind: KindInt, Int: 42}}
	bytes, err := EncodeRow(cols, row)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRow(cols, bytes)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Kind != KindString || got[0].String != "42" {
		t.Errorf("decoded=%+v; want KindString '42'", got[0])
	}

	// String values with decimals also round-trip verbatim.
	rowStr := Row{{Kind: KindString, String: "1234567890.12345"}}
	bytes2, err := EncodeRow(cols, rowStr)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := DecodeRow(cols, bytes2)
	if err != nil {
		t.Fatal(err)
	}
	if got2[0].String != "1234567890.12345" {
		t.Errorf("decoded=%q; want '1234567890.12345'", got2[0].String)
	}
}
