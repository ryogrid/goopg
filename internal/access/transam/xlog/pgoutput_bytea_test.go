package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestPgoutputByteaTextIsHex pins M0149-0063a against PG 18.3's byteaout
// (bytea_output = hex): a bytea column's text value in a pgoutput tuple is
// "\x" plus two hex digits per byte. goopg sent the raw bytes as the
// "text", which a subscriber's byteain rejected whenever they held a
// backslash (015_stream.pl's sha256 values).
func TestPgoutputByteaTextIsHex(t *testing.T) {
	raw := []byte{0x5c, 0x00, 0xff, 'a'}
	varlena := append([]byte{byte((1+len(raw))<<1 | 1)}, raw...) // short 1-byte header
	got, n, err := pgoDecodePhysicalValue(catalog.Type{Name: "bytea"}, varlena, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `\x5c00ff61` || n != len(varlena) {
		t.Errorf("bytea text = %q (%d bytes read), want \\x5c00ff61 (%d)", got, n, len(varlena))
	}
	txt, _, err := pgoDecodePhysicalValue(catalog.Type{Name: "text"}, varlena, nil)
	if err != nil || string(txt) != string(raw) {
		t.Errorf("text value = %q, %v; want the payload unchanged", txt, err)
	}
}
