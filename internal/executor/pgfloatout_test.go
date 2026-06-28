package executor

import (
	"math"
	"testing"
)

// TestPGFloatOut verifies PGFloatOut matches PostgreSQL 18.3 float8out/float4out
// byte-for-byte. Every float8 expected value below was captured from a real
// PG 18.3 instance (postgres/local_install, default extra_float_digits=1):
//
//	SELECT v::float8 FROM (VALUES (2233750), (1234567), ...) t(v);
//
// The crux of the fix is PG's fixed-vs-scientific threshold (display exponent in
// [-4, 15) for float8, [-4, 6) for float4) which differs from Go's strconv 'g'.
func TestPGFloatOut(t *testing.T) {
	f8 := []struct {
		in   float64
		want string
	}{
		{2233750, "2233750"},
		{1234567, "1234567"},
		{0.0001, "0.0001"},
		{0.00001, "1e-05"},
		{1e15, "1e+15"},
		{1e14, "100000000000000"},
		{123456789012345, "123456789012345"},
		{1234567890123456, "1.234567890123456e+15"},
		{1.5, "1.5"},
		{100, "100"},
		{0, "0"},
		{5e-5, "5e-05"},
		{1e20, "1e+20"},
		{1e21, "1e+21"},
		{1e-300, "1e-300"},
		{1e308, "1e+308"},
		{123.375, "123.375"},
		{0.1, "0.1"},
		{0.3, "0.3"},
		{-2233750, "-2233750"},
		{3.14159265358979, "3.14159265358979"},
		{1e100, "1e+100"},
		{1e-100, "1e-100"},
		{2.5e-308, "2.5e-308"},
		{1234.5678, "1234.5678"},
		{1e-310, "1e-310"},
		{5e-324, "5e-324"},
		{1.7976931348623157e308, "1.7976931348623157e+308"},
	}
	for _, tc := range f8 {
		if got := PGFloatOut(tc.in, 64); got != tc.want {
			t.Errorf("PGFloatOut(%v, 64) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Negative zero must render as "-0" (PG: SELECT -1.0::float8 * 0.0::float8).
	if got := PGFloatOut(math.Copysign(0, -1), 64); got != "-0" {
		t.Errorf("PGFloatOut(-0.0, 64) = %q, want %q", got, "-0")
	}

	// Special values.
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
		{math.NaN(), "NaN"},
	} {
		if got := PGFloatOut(tc.in, 64); got != tc.want {
			t.Errorf("PGFloatOut(%v, 64) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// float4 (bitSize 32): threshold is [-4, 6). Captured from PG via ::float4.
	f4 := []struct {
		in   float64
		want string
	}{
		{100000, "100000"},
		{1e6, "1e+06"},
		{123456, "123456"},
		{1234567, "1.234567e+06"},
		{0.0001, "0.0001"},
		{0.00001, "1e-05"},
		{1.5, "1.5"},
		{100, "100"},
		{0, "0"},
		{12345.6, "12345.6"},
		{1e5, "100000"},
		{3.14159, "3.14159"},
		{1e38, "1e+38"},
		{1e-38, "1e-38"},
		{-100000, "-100000"},
		{0.1, "0.1"},
		{1e-44, "1e-44"},
	}
	for _, tc := range f4 {
		// Round through float32 the same way the encode path does.
		in := float64(float32(tc.in))
		if got := PGFloatOut(in, 32); got != tc.want {
			t.Errorf("PGFloatOut(%v, 32) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := PGFloatOut(math.Copysign(0, -1), 32); got != "-0" {
		t.Errorf("PGFloatOut(-0.0, 32) = %q, want %q", got, "-0")
	}
}
