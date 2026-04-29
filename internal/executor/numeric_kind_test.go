package executor

import "testing"

// TestCanonicalNumericKey pins that two numerics that compare equal
// also hash equal. Required for hash-join, count-distinct, and
// group-by hashing (all of which route through datumKey →
// canonicalNumericKey).
//
// Why this test guards a real bug: prior to the 2026-04-29 fix,
// datumKey had no KindNumeric arm and every NUMERIC value collapsed
// to one bucket, turning every TPC-H NUMERIC-keyed hash join into a
// cross product (parity matrix divergent=11 → 4 once fixed).
func TestCanonicalNumericKey(t *testing.T) {
	cases := []struct {
		mantissa int64
		scale    int
		want     string
	}{
		{1, 0, "m:1:0"},
		{10, 1, "m:1:0"},   // 1.0 → 1
		{100, 2, "m:1:0"},  // 1.00 → 1
		{0, 0, "m:0:0"},
		{0, 4, "m:0:0"},    // 0.0000 → 0
		{-1, 0, "m:-1:0"},
		{-10, 1, "m:-1:0"},
		{12345, 2, "m:12345:2"}, // 123.45 stays
		{12300, 2, "m:123:0"},   // 123.00 → 123
	}
	for _, c := range cases {
		if got := canonicalNumericKey(c.mantissa, c.scale); got != c.want {
			t.Errorf("canonicalNumericKey(%d, %d)=%q, want %q",
				c.mantissa, c.scale, got, c.want)
		}
	}
}

// TestDatumKeyNumericInteger pins cross-kind hash compatibility:
// KindInt(1) and KindNumeric(mantissa=1, scale=0) must produce the
// same key so a hash-join key like `aid = $1` matches whether $1
// arrives as KindInt or as scale-0 KindNumeric.
func TestDatumKeyNumericInteger(t *testing.T) {
	intKey := datumKey(Datum{Kind: KindInt, Int: 1})
	numKey := datumKey(Datum{Kind: KindNumeric, NumericMantissa: 1, NumericScale: 0})
	if intKey != numKey {
		t.Fatalf("KindInt(1) hashed as %q, KindNumeric(1, 0) as %q (must match)", intKey, numKey)
	}
	scaledKey := datumKey(Datum{Kind: KindNumeric, NumericMantissa: 1000, NumericScale: 3})
	if scaledKey != numKey {
		t.Fatalf("KindNumeric(1000, 3) hashed as %q, expected %q (1.000 must canonicalise to 1)", scaledKey, numKey)
	}
}

// TestCompareEqNumericIntList pins that `compareEq` between a NUMERIC
// datum and an integer literal returns true when they represent the
// same value. Q16's `p_size in (49, 14, ...)` against the
// `p_size NUMERIC` column was the regression — `compareEq` had no
// NUMERIC arm and silently returned false for every comparison,
// dropping all rows.
func TestCompareEqNumericIntList(t *testing.T) {
	num := Datum{Kind: KindNumeric, NumericMantissa: 49, NumericScale: 0}
	intLit := Datum{Kind: KindInt, Int: 49}
	got, err := compareEq(num, intLit)
	if err != nil {
		t.Fatalf("compareEq(NUMERIC 49, INT 49): %v", err)
	}
	if got.Kind != KindBool || !got.Bool {
		t.Fatalf("compareEq(NUMERIC 49, INT 49)=%+v, want true", got)
	}
	// Negative-case: 49 != 14 across kinds.
	got, err = compareEq(num, Datum{Kind: KindInt, Int: 14})
	if err != nil {
		t.Fatalf("compareEq(NUMERIC 49, INT 14): %v", err)
	}
	if got.Kind != KindBool || got.Bool {
		t.Fatalf("compareEq(NUMERIC 49, INT 14)=%+v, want false", got)
	}
	// Symmetry: INT vs NUMERIC.
	got, err = compareEq(intLit, num)
	if err != nil {
		t.Fatalf("compareEq(INT 49, NUMERIC 49): %v", err)
	}
	if got.Kind != KindBool || !got.Bool {
		t.Fatalf("compareEq(INT 49, NUMERIC 49)=%+v, want true (symmetry)", got)
	}
	// NULL propagation must still apply.
	got, err = compareEq(NullDatum, num)
	if err != nil {
		t.Fatalf("compareEq(NULL, NUMERIC): %v", err)
	}
	if !got.IsNull() {
		t.Fatalf("compareEq(NULL, NUMERIC)=%+v, want NULL", got)
	}
}
