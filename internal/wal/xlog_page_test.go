package wal

import (
	"errors"
	"testing"
)

// TestXLogFileNameRoundTrip pins the upstream-compatible filename
// encoding. Expected values are computed against PostgreSQL's
// XLogFileName macro at the default 16 MiB segment size:
//
//	(tli=1, segno=0)   -> "000000010000000000000000"
//	(tli=1, segno=1)   -> "000000010000000000000001"
//	(tli=1, segno=255) -> "0000000100000000000000FF"  (last seg in log 0)
//	(tli=1, segno=256) -> "000000010000000100000000"  (first seg in log 1)
//	(tli=2, segno=257) -> "000000020000000100000001"
//
// pg_waldump matches against this exact format, so any drift will
// break the M0014-0003 acceptance gate. ParseXLogFileName is the
// inverse — round-trip every case.
func TestXLogFileNameRoundTrip(t *testing.T) {
	const seg16M = 16 << 20
	cases := []struct {
		tli   uint32
		segno uint64
		want  string
	}{
		{1, 0, "000000010000000000000000"},
		{1, 1, "000000010000000000000001"},
		{1, 255, "0000000100000000000000FF"},
		{1, 256, "000000010000000100000000"},
		{2, 257, "000000020000000100000001"},
	}
	for _, c := range cases {
		got := XLogFileName(c.tli, c.segno, seg16M)
		if got != c.want {
			t.Errorf("XLogFileName(tli=%d, segno=%d) = %q, want %q", c.tli, c.segno, got, c.want)
			continue
		}
		tli, segno, ok := ParseXLogFileName(got, seg16M)
		if !ok {
			t.Errorf("ParseXLogFileName(%q) returned ok=false", got)
			continue
		}
		if tli != c.tli || segno != c.segno {
			t.Errorf("ParseXLogFileName(%q) = (tli=%d, segno=%d), want (%d, %d)", got, tli, segno, c.tli, c.segno)
		}
	}
}

// TestParseXLogFileNameRejectsGarbage: anything not 24 hex chars
// returns ok=false so the writer's segment-listing loop can skip
// unrecognised names without erroring.
func TestParseXLogFileNameRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "00", "not-a-hex-name-at-all--", "0000000100000000000000FG"} {
		if _, _, ok := ParseXLogFileName(s, 16<<20); ok {
			t.Errorf("ParseXLogFileName(%q) returned ok=true unexpectedly", s)
		}
	}
}

// TestXLogPageHeaderRoundTrip pins the short-form encode→decode
// is lossless for every field, so a future refactor that
// re-orders bytes or drops a field shows up as a test failure.
func TestXLogPageHeaderRoundTrip(t *testing.T) {
	want := XLogPageHeader{
		Magic:    XLOGPageMagic,
		Info:     XLPFirstIsContRecord,
		TLI:      7,
		PageAddr: 0x123456789ABCDEF0,
		RemLen:   42,
	}
	buf := make([]byte, SizeOfXLogShortPHD)
	if err := EncodeXLogPageHeader(buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeXLogPageHeader(buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != want {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", got, want)
	}
}

// TestXLogLongPageHeaderRoundTrip pins the long form. The
// EncodeXLogLongPageHeader helper forces XLPLongHeader on so the
// decoder's "long bit must be set" guard succeeds; the test
// verifies that automatic-set behaviour by passing Std.Info=0.
func TestXLogLongPageHeaderRoundTrip(t *testing.T) {
	want := XLogLongPageHeader{
		Std: XLogPageHeader{
			Magic:    XLOGPageMagic,
			Info:     0, // long-bit gets forced on by Encode
			TLI:      1,
			PageAddr: 0,
			RemLen:   0,
		},
		SysID:      0xCAFEBABEDEADBEEF,
		SegSize:    16 << 20,
		XLogBlcksz: XLOGBlockSize,
	}
	buf := make([]byte, SizeOfXLogLongPHD)
	if err := EncodeXLogLongPageHeader(buf, want); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeXLogLongPageHeader(buf)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	// Encode forces XLPLongHeader on; reflect that in the
	// expected snapshot before comparing.
	want.Std.Info |= XLPLongHeader
	if got != want {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", got, want)
	}
}

// TestDecodeXLogPageHeaderRejectsBadMagic pins the typed sentinel
// the M0014-0004 legacy-format detector branches on. A page header
// with the wrong magic must return ErrInvalidPageHeader (wrapped),
// distinguishable from a generic truncation/length error.
func TestDecodeXLogPageHeaderRejectsBadMagic(t *testing.T) {
	buf := make([]byte, SizeOfXLogShortPHD)
	// Plant a deliberately wrong magic; everything else is zero.
	buf[0] = 0xAB
	buf[1] = 0xCD
	_, err := DecodeXLogPageHeader(buf)
	if !errors.Is(err, ErrInvalidPageHeader) {
		t.Errorf("Decode bad-magic err = %v, want ErrInvalidPageHeader wrap", err)
	}
}

// TestEncodeXLogPageHeaderRejectsUndefinedFlags pins the
// XLPAllFlags contract: a future flag addition must update
// XLPAllFlags or this guard catches the silent drift.
func TestEncodeXLogPageHeaderRejectsUndefinedFlags(t *testing.T) {
	buf := make([]byte, SizeOfXLogShortPHD)
	h := XLogPageHeader{Magic: XLOGPageMagic, Info: 0x0080} // unused bit
	if err := EncodeXLogPageHeader(buf, h); err == nil {
		t.Error("EncodeXLogPageHeader accepted undefined flag bit")
	}
}

// TestDecodeXLogLongPageHeaderRequiresLongBit pins the inverse:
// a long-form decode against a short-form page (XLPLongHeader
// not set) is a caller bug — surface it rather than silently
// reading garbage from bytes 24..39.
func TestDecodeXLogLongPageHeaderRequiresLongBit(t *testing.T) {
	buf := make([]byte, SizeOfXLogLongPHD)
	short := XLogPageHeader{Magic: XLOGPageMagic, Info: 0} // no long bit
	if err := EncodeXLogPageHeader(buf[:SizeOfXLogShortPHD], short); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeXLogLongPageHeader(buf); !errors.Is(err, ErrInvalidPageHeader) {
		t.Errorf("Decode without long bit err = %v, want ErrInvalidPageHeader wrap", err)
	}
}
