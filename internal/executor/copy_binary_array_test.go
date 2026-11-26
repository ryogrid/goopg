package executor

import (
	"encoding/hex"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCopyBinaryArrayMatchesPG pins array_send's layout for a 1-D int4[]
// with a NULL (bytes captured from PG 18.3's COPY … WITH (FORMAT binary)
// field): ndim 1, has-nulls 1, elemtype 23, dim 3, lbound 1, then
// length-prefixed int4sends and -1 for the NULL. The decode is the inverse.
func TestCopyBinaryArrayMatchesPG(t *testing.T) {
	col := catalog.Type{Name: "int4", IsArray: true}
	const want = "00000001" + "00000001" + "00000017" + "00000003" + "00000001" +
		"00000004" + "00000001" + "ffffffff" + "00000004" + "00000003"
	got, err := datumToCopyBinary(col, NewStringDatum("{1,NULL,3}"))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != want {
		t.Fatalf("array_send = %x, want %s", got, want)
	}
	d, err := copyBinaryToDatum(col, got)
	if err != nil {
		t.Fatal(err)
	}
	if d.Format() != "{1,NULL,3}" {
		t.Fatalf("array_recv = %q", d.Format())
	}
	empty, err := datumToCopyBinary(col, NewStringDatum("{}"))
	if err != nil || hex.EncodeToString(empty) != "000000000000000000000017" {
		t.Fatalf("empty array_send = %x, %v", empty, err)
	}
	wrong, _ := datumToCopyBinary(catalog.Type{Name: "int8", IsArray: true}, NewStringDatum("{1}"))
	if _, err := copyBinaryToDatum(col, wrong); err == nil {
		t.Fatal("an int8[] payload decoded into an int4[] column")
	}
}
