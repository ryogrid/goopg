package btree

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/pgnodes"
	"github.com/goopg/goopg/internal/storage"
)

// ---------------------------------------------------------------------------
// helpers building the on-disk binary image of a datum (little-endian native,
// the x86-64 PG 18.3 layout — see the file header of pgcompare_types.go).
// ---------------------------------------------------------------------------

func i2(v int16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, uint16(v)); return b }
func i4(v int32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, uint32(v)); return b }
func i8(v int64) []byte { b := make([]byte, 8); binary.LittleEndian.PutUint64(b, uint64(v)); return b }
func u4(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}
func f4(v float32) []byte { return u4(math.Float32bits(v)) }
func f8(v float64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, math.Float64bits(v))
	return b
}

// varlena4 builds an uncompressed 4-byte-header varlena around payload.
func varlena4(payload []byte) []byte {
	out := make([]byte, varHdrSz+len(payload))
	binary.LittleEndian.PutUint32(out[0:4], uint32(len(out))<<2)
	copy(out[varHdrSz:], payload)
	return out
}

// varlena1 builds a short (1-byte-header) varlena around payload — the form
// index_form_tuple prefers, so both header forms must compare identically.
func varlena1(payload []byte) []byte {
	out := make([]byte, varHdrSzShort+len(payload))
	out[0] = byte(len(out))<<1 | 0x01
	copy(out[varHdrSzShort:], payload)
	return out
}

// timetz builds the 12-byte TimeTzADT image (int64 usec, int32 zone seconds
// west of GMT).
func timetz(usec int64, zone int32) []byte {
	out := make([]byte, 12)
	binary.LittleEndian.PutUint64(out[0:8], uint64(usec))
	binary.LittleEndian.PutUint32(out[8:12], uint32(zone))
	return out
}

// numeric builds the on-disk NumericData varlena through internal/pgnodes,
// which is byte-validated against real PostgreSQL pg_node_tree output — an
// oracle rather than a second hand-rolled encoder in this test.
func numeric(t *testing.T, text string) []byte {
	t.Helper()
	c, err := pgnodes.NewNumericConst(text, false)
	if err != nil {
		t.Fatalf("NewNumericConst(%q): %v", text, err)
	}
	return c.Datum
}

// sign normalizes a comparator result so tests assert an ORDER, not a
// magnitude (upstream only promises the sign).
func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// checkOrder asserts that cmp is a strict total order over the given values,
// listed smallest first: every earlier value < every later one, and each value
// equal to itself. That catches an asymmetric or non-transitive comparator,
// which a handful of pairwise assertions would not.
func checkOrder(t *testing.T, name string, cmp PGAttrComparator, vals [][]byte) {
	t.Helper()
	for i := range vals {
		if got := sign(cmp(vals[i], vals[i])); got != 0 {
			t.Errorf("%s: value %d not equal to itself: got %d", name, i, got)
		}
		for j := i + 1; j < len(vals); j++ {
			if got := sign(cmp(vals[i], vals[j])); got != -1 {
				t.Errorf("%s: cmp(v%d, v%d) = %d, want -1", name, i, j, got)
			}
			if got := sign(cmp(vals[j], vals[i])); got != 1 {
				t.Errorf("%s: cmp(v%d, v%d) = %d, want 1", name, j, i, got)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// integers
// ---------------------------------------------------------------------------

func TestPGCompareIntegersAreSignedAndNative(t *testing.T) {
	checkOrder(t, "int2", PGCompareInt2, [][]byte{
		i2(math.MinInt16), i2(-300), i2(-1), i2(0), i2(1), i2(255), i2(256), i2(math.MaxInt16),
	})
	checkOrder(t, "int4", PGCompareInt4, [][]byte{
		i4(math.MinInt32), i4(-70000), i4(-1), i4(0), i4(1), i4(255), i4(256), i4(math.MaxInt32),
	})
	checkOrder(t, "int8", PGCompareInt8, [][]byte{
		i8(math.MinInt64), i8(-1), i8(0), i8(1), i8(1 << 40), i8(math.MaxInt64),
	})
}

// TestPGCompareInt4DisagreesWithBytewise is the whole reason this file exists:
// on the little-endian native image a bytewise comparison is not merely
// imprecise, it is wrong in both directions. If this test ever passes with
// bytes.Compare substituted for PGCompareInt4, the file has stopped doing its
// job.
func TestPGCompareInt4DisagreesWithBytewise(t *testing.T) {
	cases := []struct {
		name string
		a, b int32
	}{
		{"byte order: 1 < 256", 1, 256},
		{"sign: -1 < 0", -1, 0},
	}
	for _, tc := range cases {
		av, bv := i4(tc.a), i4(tc.b)
		if got := sign(PGCompareInt4(av, bv)); got != -1 {
			t.Errorf("%s: PGCompareInt4 = %d, want -1", tc.name, got)
		}
		if got := sign(bytes.Compare(av, bv)); got == -1 {
			t.Errorf("%s: bytes.Compare agreed (%d) — the premise of this file is broken", tc.name, got)
		}
	}
}

func TestPGCompareOIDIsUnsigned(t *testing.T) {
	// 0xFFFFFFFF is -1 as int4 but the LARGEST oid. btoidcmp must put it last.
	checkOrder(t, "oid", PGCompareOID, [][]byte{u4(0), u4(1), u4(1 << 31), u4(0xFFFFFFFF)})
	if got := sign(PGCompareInt4(u4(0xFFFFFFFF), u4(0))); got != -1 {
		t.Fatalf("signed int4 view of 0xFFFFFFFF = %d, want -1 (guards the oid/int4 distinction)", got)
	}
}

func TestPGCompareBoolAndChar(t *testing.T) {
	checkOrder(t, "bool", PGCompareBool, [][]byte{{0}, {1}})
	// "char" is compared as UNSIGNED: 0x80 is above 0x01, not below it.
	checkOrder(t, "char", PGCompareCharType, [][]byte{{0x00}, {0x01}, {0x7F}, {0x80}, {0xFF}})
}

// ---------------------------------------------------------------------------
// floats
// ---------------------------------------------------------------------------

func TestPGCompareFloat8NaNIsLargestAndEqualToItself(t *testing.T) {
	checkOrder(t, "float8", PGCompareFloat8, [][]byte{
		f8(math.Inf(-1)), f8(-1e300), f8(-1), f8(0), f8(1), f8(1e300), f8(math.Inf(1)), f8(math.NaN()),
	})
	if got := sign(PGCompareFloat8(f8(math.NaN()), f8(math.NaN()))); got != 0 {
		t.Errorf("NaN vs NaN = %d, want 0", got)
	}
	// Negative zero equals positive zero even though the bit patterns differ —
	// the case a bitwise comparison gets wrong.
	if got := sign(PGCompareFloat8(f8(math.Copysign(0, -1)), f8(0))); got != 0 {
		t.Errorf("-0.0 vs 0.0 = %d, want 0", got)
	}
	if bytes.Equal(f8(math.Copysign(0, -1)), f8(0)) {
		t.Fatal("-0.0 and 0.0 have the same image — the -0 case is not being tested")
	}
}

func TestPGCompareFloat4(t *testing.T) {
	checkOrder(t, "float4", PGCompareFloat4, [][]byte{
		f4(float32(math.Inf(-1))), f4(-1), f4(0), f4(1), f4(float32(math.Inf(1))), f4(float32(math.NaN())),
	})
}

// ---------------------------------------------------------------------------
// varlena string/binary types
// ---------------------------------------------------------------------------

func TestPGCompareTextCAndBytea(t *testing.T) {
	checkOrder(t, "text", PGCompareTextC, [][]byte{
		varlena4([]byte("")), varlena4([]byte("A")), varlena4([]byte("AA")),
		varlena4([]byte("a")), varlena4([]byte("ab")), varlena4([]byte("b")),
	})
	// Both header forms describe the same value and must compare equal: the
	// header is a length, not part of the datum.
	if got := sign(PGCompareTextC(varlena1([]byte("abc")), varlena4([]byte("abc")))); got != 0 {
		t.Errorf("short vs long header for the same value = %d, want 0", got)
	}
	if got := sign(PGCompareBytea(varlena1([]byte{0x00, 0xFF}), varlena4([]byte{0x00, 0xFF, 0x01}))); got != -1 {
		t.Errorf("bytea prefix ordering = %d, want -1", got)
	}
}

func TestPGCompareBpcharIgnoresTrailingSpaces(t *testing.T) {
	if got := sign(PGCompareBpcharC(varlena4([]byte("ab")), varlena4([]byte("ab   ")))); got != 0 {
		t.Errorf("'ab' vs 'ab   ' = %d, want 0 (bpchar is blank-padded)", got)
	}
	// The same pair under text semantics is NOT equal — the distinction this
	// function exists for.
	if got := sign(PGCompareTextC(varlena4([]byte("ab")), varlena4([]byte("ab   ")))); got == 0 {
		t.Error("text compared 'ab' equal to 'ab   ' — bpchar/text are not distinguished")
	}
	// Leading and interior spaces are NOT stripped.
	checkOrder(t, "bpchar", PGCompareBpcharC, [][]byte{
		varlena4([]byte(" a")), varlena4([]byte("a b")), varlena4([]byte("ab ")), varlena4([]byte("abc")),
	})
}

func TestPGCompareNameStopsAtNUL(t *testing.T) {
	name := func(s string) []byte {
		b := make([]byte, 64)
		copy(b, s)
		return b
	}
	checkOrder(t, "name", PGCompareName, [][]byte{name("a"), name("ab"), name("b")})
	// Garbage after the terminator must not affect the answer.
	dirty := name("ab")
	dirty[10] = 0xFF
	if got := sign(PGCompareName(dirty, name("ab"))); got != 0 {
		t.Errorf("bytes past the NUL changed the answer: got %d, want 0", got)
	}
}

func TestPGCompareUUID(t *testing.T) {
	mk := func(first, last byte) []byte {
		b := make([]byte, 16)
		b[0], b[15] = first, last
		return b
	}
	checkOrder(t, "uuid", PGCompareUUID, [][]byte{mk(0, 0), mk(0, 1), mk(1, 0), mk(0xFF, 0)})
}

func TestPGCompareTimeTZOrdersByGMT(t *testing.T) {
	const hour = int64(3600) * 1000000
	// 12:00+01 (zone -3600 s west) is 11:00 GMT; 12:00+00 is 12:00 GMT.
	plus1 := timetz(12*hour, -3600)
	utc := timetz(12*hour, 0)
	if got := sign(PGCompareTimeTZ(plus1, utc)); got != -1 {
		t.Errorf("12:00+01 vs 12:00+00 = %d, want -1 (GMT-equivalent ordering)", got)
	}
	// Same instant, two spellings: 12:00+01 and 11:00+00 are both 11:00 GMT.
	// Upstream breaks the tie on the local time, so they are NOT equal.
	same := timetz(11*hour, 0)
	if got := sign(PGCompareTimeTZ(plus1, same)); got != 1 {
		t.Errorf("12:00+01 vs 11:00+00 = %d, want 1 (local-time tiebreak)", got)
	}
	if got := sign(PGCompareTimeTZ(plus1, plus1)); got != 0 {
		t.Errorf("timetz not equal to itself: %d", got)
	}
}

// ---------------------------------------------------------------------------
// numeric
// ---------------------------------------------------------------------------

func TestPGCompareNumericTotalOrder(t *testing.T) {
	checkOrder(t, "numeric", PGCompareNumeric, [][]byte{
		numeric(t, "-Infinity"),
		numeric(t, "-12345678901234567890"),
		numeric(t, "-1"),
		numeric(t, "-0.5"),
		numeric(t, "0"),
		numeric(t, "0.5"),
		numeric(t, "1"),
		numeric(t, "2"),
		numeric(t, "10"),
		numeric(t, "9999"),
		numeric(t, "10000"), // an NBASE digit boundary
		numeric(t, "12345678901234567890"),
		numeric(t, "Infinity"),
		numeric(t, "NaN"), // cmp_numerics puts NaN ABOVE +Infinity
	})
}

func TestPGCompareNumericIgnoresDisplayScale(t *testing.T) {
	// dscale is presentation only: 1, 1.0 and 1.000 are the same value, and
	// their on-disk images differ.
	one, oneP0, oneP000 := numeric(t, "1"), numeric(t, "1.0"), numeric(t, "1.000")
	if bytes.Equal(one, oneP000) {
		t.Fatal("1 and 1.000 have identical images — the dscale case is not being tested")
	}
	for _, v := range [][]byte{oneP0, oneP000} {
		if got := sign(PGCompareNumeric(one, v)); got != 0 {
			t.Errorf("1 vs a differently-scaled 1 = %d, want 0", got)
		}
	}
	if got := sign(PGCompareNumeric(numeric(t, "0"), numeric(t, "0.00"))); got != 0 {
		t.Errorf("0 vs 0.00 = %d, want 0", got)
	}
}

// TestPGCompareNumericShortWeightIsSignExtended guards the one bit of the
// decoder a plain mask gets wrong: in the short header the weight is a
// SIGN-EXTENDED 7-bit field, so a value below 1 (negative weight) would
// otherwise decode as a large positive weight and sort above everything.
func TestPGCompareNumericShortWeightIsSignExtended(t *testing.T) {
	small := numeric(t, "0.00000001") // weight -2 in NBASE digits
	big := numeric(t, "10000")        // weight 1
	if got := sign(PGCompareNumeric(small, big)); got != -1 {
		t.Fatalf("0.00000001 vs 10000 = %d, want -1", got)
	}
	parts, ok := decodeNumericParts(small)
	if !ok {
		t.Fatal("decodeNumericParts failed on 0.00000001")
	}
	if parts.weight >= 0 {
		t.Errorf("weight decoded as %d, want negative (sign extension lost)", parts.weight)
	}
}

// ---------------------------------------------------------------------------
// corrupt operands
// ---------------------------------------------------------------------------

// TestPGCompareCorruptOperandsFallBack pins the documented contract: a datum
// whose length does not match its attlen (or a varlena whose header is
// compressed/external/truncated) must NOT panic — a page split cannot afford
// to — and must still yield a deterministic answer.
func TestPGCompareCorruptOperandsFallBack(t *testing.T) {
	cmps := map[string]PGAttrComparator{
		"int2": PGCompareInt2, "int4": PGCompareInt4, "int8": PGCompareInt8,
		"oid": PGCompareOID, "bool": PGCompareBool, "char": PGCompareCharType,
		"float4": PGCompareFloat4, "float8": PGCompareFloat8,
		"bytea": PGCompareBytea, "text": PGCompareTextC, "bpchar": PGCompareBpcharC,
		"name": PGCompareName, "uuid": PGCompareUUID, "timetz": PGCompareTimeTZ,
		"numeric": PGCompareNumeric,
	}
	bad := [][]byte{
		nil, {}, {0x01}, {0x01, 0x02, 0x03}, {0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		{0x01, 0x00, 0x00, 0x00}, // VARATT_IS_1B_E: an external TOAST pointer
		{0x02, 0x00, 0x00, 0x00}, // a 4-byte COMPRESSED header
	}
	for name, cmp := range cmps {
		for i := range bad {
			for j := range bad {
				a, b := bad[i], bad[j]
				got := sign(cmp(a, b))
				// Antisymmetry must survive even on garbage, or a sort over a
				// corrupt page could fail to terminate.
				if rev := sign(cmp(b, a)); rev != -got {
					t.Errorf("%s: cmp(%v,%v)=%d but cmp(%v,%v)=%d — not antisymmetric", name, a, b, got, b, a, rev)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// end to end through ComparePGIndexTuples
// ---------------------------------------------------------------------------

// TestComparePGIndexTuplesWithTypedComparator is the proof that these
// comparators plug into slice 3b-1's descriptor and change the answer on real
// IndexTupleData: two tuples whose int4 keys are 1 and 256 sort correctly with
// PGCompareInt4 installed and BACKWARDS with the nil (bytewise) default.
func TestComparePGIndexTuplesWithTypedComparator(t *testing.T) {
	physical := PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'}
	mk := func(v int32, off uint16) []byte {
		raw, err := FormPGIndexTuple([]PGIndexAttr{physical}, [][]byte{i4(v)}, []bool{false},
			storage.ItemPointer{Block: 1, Offset: off})
		if err != nil {
			t.Fatalf("FormPGIndexTuple(%d): %v", v, err)
		}
		return raw
	}
	one, twoFiftySix := mk(1, 1), mk(256, 2)

	typed := &PGIndexKeyDesc{Attrs: []PGKeyAttr{{PGIndexAttr: physical, Compare: PGCompareInt4}}}
	got, err := ComparePGIndexTuples(typed, one, twoFiftySix)
	if err != nil {
		t.Fatalf("ComparePGIndexTuples: %v", err)
	}
	if sign(got) != -1 {
		t.Errorf("typed comparator: 1 vs 256 = %d, want -1", sign(got))
	}

	bytewise := &PGIndexKeyDesc{Attrs: []PGKeyAttr{{PGIndexAttr: physical}}}
	got, err = ComparePGIndexTuples(bytewise, one, twoFiftySix)
	if err != nil {
		t.Fatalf("ComparePGIndexTuples (bytewise): %v", err)
	}
	if sign(got) != 1 {
		t.Errorf("bytewise default: 1 vs 256 = %d, want 1 — the default is no longer wrong, so this test's premise died", sign(got))
	}
}

// TestPGCompareDescInvertsTypedComparator checks the typed comparator composes
// with the descriptor's DESC flag rather than fighting it (SK_BT_DESC is one
// negation in compareAttr, applied on top of whatever the opclass said).
func TestPGCompareDescInvertsTypedComparator(t *testing.T) {
	attr := PGKeyAttr{
		PGIndexAttr: PGIndexAttr{Len: 4, ByVal: true, AlignBy: 4, Storage: 'p'},
		Compare:     PGCompareInt4,
		Desc:        true,
	}
	if got := sign(attr.compareAttr(i4(1), false, i4(256), false)); got != 1 {
		t.Errorf("DESC int4: 1 vs 256 = %d, want 1", got)
	}
}
