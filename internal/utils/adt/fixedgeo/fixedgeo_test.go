package fixedgeo

import (
	"encoding/binary"
	"math"
	"testing"
)

// TestRoundTrip pins that every canonical text goopg keeps in memory for the
// six types survives Encode → Decode byte-for-byte, at PG's typlen.
func TestRoundTrip(t *testing.T) {
	for _, tc := range []struct{ typ, text string }{
		{"point", "(1.5,2)"},
		{"point", "(-1e+300,0.1)"},
		{"point", "(Infinity,NaN)"},
		{"lseg", "[(0,0),(1,1)]"},
		{"box", "(5,5),(0,0)"},
		{"line", "{1,-1,0}"},
		{"circle", "<(1,2),3.25>"},
		{"tid", "(3,4)"},
		{"tid", "(4294967295,65535)"},
	} {
		b, err := Encode(tc.typ, tc.text)
		if err != nil {
			t.Fatalf("%s %q: %v", tc.typ, tc.text, err)
		}
		size, _, _ := Width(tc.typ)
		if len(b) != size {
			t.Errorf("%s: %d bytes, want %d", tc.typ, len(b), size)
		}
		got, n, err := Decode(tc.typ, b)
		if err != nil || n != size || got != tc.text {
			t.Errorf("%s: Decode = %q, %d, %v; want %q", tc.typ, got, n, err, tc.text)
		}
	}
}

// TestPGLayout pins the byte layout against PG's structs: Point is x then y,
// BOX is high then low, and ItemPointerData is bi_hi, bi_lo, ip_posid.
func TestPGLayout(t *testing.T) {
	b, _ := Encode("point", "(1.5,2)")
	if math.Float64frombits(binary.LittleEndian.Uint64(b[0:])) != 1.5 || math.Float64frombits(binary.LittleEndian.Uint64(b[8:])) != 2 {
		t.Errorf("point layout: % x", b)
	}
	b, _ = Encode("tid", "(65537,7)")
	if binary.LittleEndian.Uint16(b[0:]) != 1 || binary.LittleEndian.Uint16(b[2:]) != 1 || binary.LittleEndian.Uint16(b[4:]) != 7 {
		t.Errorf("tid layout: % x", b)
	}
	if _, err := Encode("point", "(1,2,3)"); err == nil {
		t.Error("point with three coordinates must fail")
	}
}

// TestSendRecv pins the binary COPY formats: point_send is two big-endian
// float8, tidsend a big-endian uint32 block then uint16 offset; Recv
// inverts Send, reorders box corners and rejects a negative circle radius.
func TestSendRecv(t *testing.T) {
	b, _ := Send("point", "(1.5,2)")
	if math.Float64frombits(binary.BigEndian.Uint64(b[0:])) != 1.5 || math.Float64frombits(binary.BigEndian.Uint64(b[8:])) != 2 {
		t.Errorf("point_send: % x", b)
	}
	b, _ = Send("tid", "(65537,7)")
	if binary.BigEndian.Uint32(b[0:]) != 65537 || binary.BigEndian.Uint16(b[4:]) != 7 || len(b) != 6 {
		t.Errorf("tidsend: % x", b)
	}
	for _, tc := range []struct{ typ, text string }{
		{"point", "(1.5,2)"}, {"lseg", "[(0,0),(1,1)]"}, {"box", "(5,5),(0,0)"},
		{"line", "{1,-1,0}"}, {"circle", "<(1,2),3.25>"}, {"tid", "(4294967295,65535)"},
	} {
		b, err := Send(tc.typ, tc.text)
		if err != nil {
			t.Fatalf("%s: %v", tc.typ, err)
		}
		if got, err := Recv(tc.typ, b); err != nil || got != tc.text {
			t.Errorf("%s: Recv = %q, %v; want %q", tc.typ, got, err, tc.text)
		}
	}
	lowFirst, _ := Send("lseg", "[(0,0),(5,5)]") // same 4 floats as a box written low→high
	if got, _ := Recv("box", lowFirst); got != "(5,5),(0,0)" {
		t.Errorf("box_recv corner order: %q", got)
	}
	neg, _ := Send("point", "(1,2)")
	neg = append(neg, make([]byte, 8)...)
	binary.BigEndian.PutUint64(neg[16:], math.Float64bits(-1))
	if _, err := Recv("circle", neg); err == nil {
		t.Error("circle_recv must reject a negative radius")
	}
}
