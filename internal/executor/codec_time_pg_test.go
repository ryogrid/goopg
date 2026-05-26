package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

func TestEncodeDecodeValuePGTimeRoundTrip(t *testing.T) {
	typ := catalog.Type{Name: "time", Args: []int64{2}}
	enc, err := encodeValuePG(typ, NewStringDatum("11:59 EDT"))
	if err != nil {
		t.Fatalf("encodeValuePG(time): %v", err)
	}
	got, n, err := decodePhysicalPGValueMctx(typ, enc, nil)
	if err != nil {
		t.Fatalf("decodePhysicalPGValueMctx(time): %v", err)
	}
	if n != 8 {
		t.Fatalf("decode consumed %d bytes, want 8", n)
	}
	if got.Kind != KindTime {
		t.Fatalf("decoded kind=%v, want KindTime", got.Kind)
	}
	u := got.TimeValue().UTC()
	if u.Hour() != 11 || u.Minute() != 59 || u.Second() != 0 {
		t.Fatalf("decoded time=%s, want 11:59:00", u.Format("15:04:05"))
	}
}

func TestEncodeValuePGTimeRejectsNamedZoneWithoutDate(t *testing.T) {
	typ := catalog.Type{Name: "time", Args: []int64{2}}
	if _, err := encodeValuePG(typ, NewStringDatum("15:36:39 America/New_York")); err == nil {
		t.Fatal("expected time parse error for bare named timezone")
	}
}

func TestDecodeRowFallsBackToLegacyTimeEncoding(t *testing.T) {
	cols := []catalog.Column{{Name: "f1", Type: catalog.Type{Name: "time", Args: []int64{2}}}}
	body, err := EncodeRow(cols, Row{NewStringDatum("02:03 PST")})
	if err != nil {
		t.Fatalf("EncodeRow(time): %v", err)
	}
	var got Row = make([]Datum, 1)
	if err := DecodeRowInto(got, cols, body); err != nil {
		t.Fatalf("DecodeRowInto(time): %v", err)
	}
	if got[0].Kind != KindTime {
		t.Fatalf("decoded kind=%v, want KindTime", got[0].Kind)
	}
	u := got[0].TimeValue().UTC()
	if u.Hour() != 2 || u.Minute() != 3 || u.Second() != 0 {
		t.Fatalf("decoded time=%s, want 02:03:00", u.Format("15:04:05"))
	}
}
