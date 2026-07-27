package executor

import (
	"math/big"
	"testing"
)

// TestExactIntVarianceZeroAndDegenerate pins the guards in exactIntVariance,
// the exact-integer variance/stddev finisher.
//
// Why this guards a real crash: stddev's square root is computed by
// Newton-Raphson on big.Float, seeded with sqrt(variance). When every input in
// a group is equal the variance is exactly 0, the seed is 0, and the first
// iteration used to compute big.Float Quo(0, 0) — which panics with "division
// of zero by zero or infinity by infinity" by design (big.ErrNaN). The panic
// escaped to serveConn, which closes the socket, so the client saw
// "connection lost" and the benchmark harness restarted the server.
//
// TPC-DS Q39 hit this on every run — stddev_samp(inv_quantity_on_hand) over
// (warehouse, item, month) groups where the weekly inventory snapshot never
// changes — and was misdiagnosed for a day as an OOM kill because the crash
// kept landing in unlogged windows (deferral ledger, tpcds-round2 Q39).
//
// The numeric sibling exactNumericVariance already had both guards; the int
// path had neither. Sibling paths must agree.
func TestExactIntVarianceZeroAndDegenerate(t *testing.T) {
	bi := func(v int64) *big.Int { return big.NewInt(v) }

	cases := []struct {
		name     string
		sx, sxx  *big.Int
		n        int64
		isSample bool
		isSqrt   bool
		want     string // expected string datum; "NULL" for NullDatum
	}{
		// Two equal values {5, 5}: Σx=10, Σx²=50 → variance 0.
		// stddev_samp must be "0", not a panic (the Q39 crash shape).
		{"stddev_samp all-equal", bi(10), bi(50), 2, true, true, "0"},
		{"stddev_pop all-equal", bi(10), bi(50), 2, false, true, "0"},
		{"var_samp all-equal", bi(10), bi(50), 2, true, false, "0"},
		// Single row: sample variance denominator n-1 = 0 → NULL (PG semantics).
		{"stddev_samp n=1", bi(5), bi(25), 1, true, true, "NULL"},
		{"var_samp n=1", bi(5), bi(25), 1, true, false, "NULL"},
		// Population variants stay defined at n=1: variance of one value is 0.
		{"stddev_pop n=1", bi(5), bi(25), 1, false, true, "0"},
		// Sanity: a genuinely varying group still computes.
		// {1, 3}: Σx=4, Σx²=10 → var_samp = (2*10-16)/(2*1) = 2.
		{"var_samp {1,3}", bi(4), bi(10), 2, true, false, "2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := exactIntVariance(c.sx, c.sxx, c.n, c.isSample, c.isSqrt)
			if c.want == "NULL" {
				if !got.IsNull() {
					t.Fatalf("want NULL, got %q", got.StringValue())
				}
				return
			}
			if got.IsNull() {
				t.Fatalf("want %q, got NULL", c.want)
			}
			if got.StringValue() != c.want {
				t.Fatalf("want %q, got %q", c.want, got.StringValue())
			}
		})
	}
}

// TestExactNumericVarianceZeroStddev pins the same behaviour on the numeric
// sibling so the two finishers cannot silently diverge again.
func TestExactNumericVarianceZeroStddev(t *testing.T) {
	r := func(v int64) *big.Rat { return new(big.Rat).SetInt64(v) }
	got := exactNumericVariance(r(10), r(50), 2, true, true)
	if got.IsNull() || got.StringValue() != "0" {
		t.Fatalf("numeric stddev_samp of all-equal group: want \"0\", got %v", got)
	}
	got = exactNumericVariance(r(5), r(25), 1, true, true)
	if !got.IsNull() {
		t.Fatalf("numeric stddev_samp of single row: want NULL, got %q", got.StringValue())
	}
}
