package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestEncodeDecodeNumericRoundTrip pins the M0003 NUMERIC codec
// behaviour: integer / string / KindNumeric datums all encode as
// decimal text in the varlen frame, and decode parses back into
// KindNumeric so arithmetic and comparison can run through the
// scale-aligning helpers without re-parsing on every row.
func TestEncodeDecodeNumericRoundTrip(t *testing.T) {
	cols := []catalog.Column{
		{Name: "n", Type: catalog.Type{Name: "numeric"}},
	}
	// Integer datum (`INSERT INTO t (n) VALUES (42)`).
	row := Row{{Kind: KindInt, Int: 42}}
	bytes, err := EncodeRowPG(cols, row)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRow(cols, bytes)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Kind != KindNumeric || got[0].NumericMantissaValue() != 42 || got[0].Scale != 0 {
		t.Errorf("decoded=%+v; want KindNumeric{42,0}", got[0])
	}

	// String values with decimals (e.g. HammerDB loader).
	rowStr := Row{NewStringDatum("1234567890.12345")}
	bytes2, err := EncodeRowPG(cols, rowStr)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := DecodeRow(cols, bytes2)
	if err != nil {
		t.Fatal(err)
	}
	if got2[0].Format() != "1234567890.12345" {
		t.Errorf("decoded=%q; want '1234567890.12345'", got2[0].Format())
	}

	// KindNumeric datums (output of NUMERIC arithmetic) format
	// back to their canonical decimal-text form.
	rowNum := Row{{Kind: KindNumeric, Int: -12345, Scale: 3}}
	bytes3, err := EncodeRowPG(cols, rowNum)
	if err != nil {
		t.Fatal(err)
	}
	got3, err := DecodeRow(cols, bytes3)
	if err != nil {
		t.Fatal(err)
	}
	if got3[0].Format() != "-12.345" {
		t.Errorf("decoded=%q; want '-12.345'", got3[0].Format())
	}
}
