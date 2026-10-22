package executor

import (
	"math/big"
	"testing"
)

// TestExactIntVarianceZeroAndDegenerate pins the guards in exactIntVariance,
// the exact-integer variance/stddev finisher.
//
// Why this guards a real crash: stddev's square root was computed by
// Newton-Raphson on big.Float, seeded with sqrt(variance) (replaced by PG's
// exact sqrt_var in M0122-0012 slice 46; the guards stay pinned). When every input in
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
		want     string // expected text (Format), PG 18.3's; "NULL" for NullDatum
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
		// {1, 3}: Σx=4, Σx²=10 → var_samp = (2*10-16)/(2*1) = 2, printed at
		// select_div_scale's 16 decimals as PG prints it.
		{"var_samp {1,3}", bi(4), bi(10), 2, true, false, "2.0000000000000000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := exactIntVariance(c.sx, c.sxx, c.n, c.isSample, c.isSqrt)
			if c.want == "NULL" {
				if !got.IsNull() {
					t.Fatalf("want NULL, got %q", got.Format())
				}
				return
			}
			if got.IsNull() {
				t.Fatalf("want %q, got NULL", c.want)
			}
			if got.Format() != c.want {
				t.Fatalf("want %q, got %q", c.want, got.Format())
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

// TestExactIntVarianceMatchesPGDigits pins exactIntVariance to PostgreSQL
// 18.3's numeric_stddev_internal text (M0122-0012 slice 46). Every want was
// read back from the PG oracle: the display scale is select_div_scale's
// rscale (16 significant digits of the quotient), the variance is the
// division rounded half away from zero, and the stddev is sqrt_var of that
// rounded variance at the same scale — including a quotient past int64 and
// a scale that rounds the root to an integer.
func TestExactIntVarianceMatchesPGDigits(t *testing.T) {
	sums := func(vals ...int64) (*big.Int, *big.Int, int64) {
		sx, sxx := new(big.Int), new(big.Int)
		for _, v := range vals {
			b := big.NewInt(v)
			sx.Add(sx, b)
			sxx.Add(sxx, new(big.Int).Mul(b, b))
		}
		return sx, sxx, int64(len(vals))
	}
	cases := []struct {
		vals             []int64
		isSample, isSqrt bool
		want             string
	}{
		{[]int64{1, 2, 4}, true, true, "1.5275252316519467"},
		{[]int64{1, 2, 4}, false, true, "1.2472191289246471"},
		{[]int64{1, 2, 4}, true, false, "2.3333333333333333"},
		{[]int64{100000, 250000, 999999, 7}, true, true, "453456.70504140"},
		{[]int64{100000, 250000, 999999, 7}, true, false, "205622983347.00000000"},
		{[]int64{-3, 2, 2, 9}, true, true, "4.9328828623162473"},
		{[]int64{-3, 2, 2, 9}, false, false, "18.2500000000000000"},
		{[]int64{600, 470, 170, 430, 300}, false, false, "21704.000000000000"},
		{[]int64{430, 300}, true, true, "91.9238815542511782"},
		{[]int64{9223372036854775807, -9223372036854775807, 12}, true, true, "9223372036854775807"},
		{[]int64{9223372036854775807, -9223372036854775807, 12}, true, false, "85070591730234615847396907784232501297"},
	}
	for _, c := range cases {
		sx, sxx, n := sums(c.vals...)
		if got := exactIntVariance(sx, sxx, n, c.isSample, c.isSqrt).Format(); got != c.want {
			t.Errorf("%v sample=%v sqrt=%v: got %s, PG %s", c.vals, c.isSample, c.isSqrt, got, c.want)
		}
	}
}
