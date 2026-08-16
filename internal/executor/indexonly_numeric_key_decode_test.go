package executor

import (
	"math/big"
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
)

// TestNumericIndexKeyDecodeSiblingParity pins the two B-tree key decoders
// against each other for NUMERIC. decodeBTreeKeyToDatum serves the
// single-column index-only scan and decodeIndexKeyColumn the composite one
// (and, since M0119-0006, amcheck's operator-class comparator walk). They are
// the sibling paths Hard-won Rule #2 covers: before this slice NUMERIC hit the
// shared `default:` branch in BOTH, which reads 8 bytes as a float8 and hands
// back an enum Datum — silently wrong values out of an IOS.
func TestNumericIndexKeyDecodeSiblingParity(t *testing.T) {
	col := catalog.Column{Name: "n", Type: catalog.Type{Name: "numeric"}}

	cases := []struct {
		mant  int64
		scale int16
		want  string
	}{
		{0, 0, "0"},
		{1, 0, "1"},
		{15, 1, "1.5"},
		{150, 2, "1.5"}, // trailing zeros normalise away in the key encoding
		{-250, 2, "-2.5"},
		{100025, 2, "1000.25"},
		{-1, 0, "-1"},
		{125, 3, "0.125"},
	}
	for _, c := range cases {
		key := nbtree.EncodeNumericKey(big.NewInt(c.mant), c.scale)

		single, err := decodeBTreeKeyToDatum(key, col)
		if err != nil {
			t.Fatalf("decodeBTreeKeyToDatum(%d,%d): %v", c.mant, c.scale, err)
		}
		multi, n, err := decodeIndexKeyColumn(key, col)
		if err != nil {
			t.Fatalf("decodeIndexKeyColumn(%d,%d): %v", c.mant, c.scale, err)
		}
		if n != len(key) {
			t.Fatalf("decodeIndexKeyColumn consumed %d of %d bytes", n, len(key))
		}
		if single.Kind != KindNumeric || multi.Kind != KindNumeric {
			t.Fatalf("kinds: single=%v multi=%v, want KindNumeric", single.Kind, multi.Kind)
		}
		if got := single.Format(); got != c.want {
			t.Errorf("decodeBTreeKeyToDatum(%d,%d) = %q, want %q", c.mant, c.scale, got, c.want)
		}
		if got := multi.Format(); got != c.want {
			t.Errorf("decodeIndexKeyColumn(%d,%d) = %q, want %q", c.mant, c.scale, got, c.want)
		}
	}
}

// TestNumericIndexKeyDecodeCompositeWalk pins the self-delimiting property the
// composite walk needs: a numeric key column followed by another column must
// report exactly its own byte count so the next column starts at the right
// offset.
func TestNumericIndexKeyDecodeCompositeWalk(t *testing.T) {
	numCol := catalog.Column{Name: "n", Type: catalog.Type{Name: "numeric"}}
	intCol := catalog.Column{Name: "i", Type: catalog.Type{Name: "int4"}}

	numKey := nbtree.EncodeNumericKey(big.NewInt(-12345), 3)
	intKey := nbtree.EncodeInt4(77)
	composite := append(append([]byte{}, numKey...), intKey...)

	d, n, err := decodeIndexKeyColumn(composite, numCol)
	if err != nil {
		t.Fatalf("numeric column: %v", err)
	}
	if n != len(numKey) {
		t.Fatalf("numeric column consumed %d bytes, want %d", n, len(numKey))
	}
	if got := d.Format(); got != "-12.345" {
		t.Fatalf("numeric column = %q, want %q", got, "-12.345")
	}
	d2, n2, err := decodeIndexKeyColumn(composite[n:], intCol)
	if err != nil {
		t.Fatalf("int4 column: %v", err)
	}
	if n2 != len(intKey) || d2.Int != 77 {
		t.Fatalf("int4 column = (%d, %d bytes), want (77, %d bytes)", d2.Int, n2, len(intKey))
	}
}
