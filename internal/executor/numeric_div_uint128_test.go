package executor

import (
	"math"
	"math/big"
	"math/rand"
	"testing"
)

// M0122-0012 slice 68 guard: numericDivUint128 answers exactly what the
// big.Int path does — same digits, same select_div_scale rscale, same
// half-away-from-zero rounding — for operands of either lane whose magnitudes
// fit a uint64, and declines everything else.

func TestNumericDivUint128MatchesBigPath(t *testing.T) {
	r := rand.New(rand.NewSource(68))
	mag := func() *big.Int {
		switch r.Intn(4) {
		case 0:
			return big.NewInt(1 + r.Int63n(1000))
		case 1:
			return new(big.Int).SetUint64(r.Uint64() >> uint(r.Intn(64)))
		case 2:
			// Just past int64: the big lane with a uint64 magnitude, as
			// Q39's 16-decimal means are.
			return new(big.Int).Add(new(big.Int).SetUint64(math.MaxInt64), big.NewInt(1+r.Int63n(1<<40)))
		default:
			return new(big.Int).SetUint64(r.Uint64())
		}
	}
	mk := func(m *big.Int, neg bool, scale int16, bigLane bool) Datum {
		v := new(big.Int).Set(m)
		if neg {
			v.Neg(v)
		}
		if bigLane || !v.IsInt64() {
			return NewNumericBigDatum(v, scale)
		}
		return Datum{Kind: KindNumeric, Int: v.Int64(), Scale: scale}
	}
	took := 0
	for range 200000 {
		ma, mb := mag(), mag()
		if mb.Sign() == 0 {
			continue
		}
		a := mk(ma, r.Intn(2) == 0, int16(r.Intn(21)), r.Intn(3) == 0)
		b := mk(mb, r.Intn(2) == 0, int16(r.Intn(21)), r.Intn(3) == 0)
		got, ok := numericDivUint128(a, b)
		if !ok {
			continue
		}
		took++
		want, err := numericDivBig(a, b, 0)
		if err != nil {
			t.Fatalf("big path error for %s / %s: %v", formatDatumDateStyle(a, nil), formatDatumDateStyle(b, nil), err)
		}
		if got.Scale != want.Scale || formatDatumDateStyle(got, nil) != formatDatumDateStyle(want, nil) {
			t.Fatalf("%s / %s = %s (scale %d), want %s (scale %d)",
				formatDatumDateStyle(a, nil), formatDatumDateStyle(b, nil),
				formatDatumDateStyle(got, nil), got.Scale, formatDatumDateStyle(want, nil), want.Scale)
		}
	}
	if took < 50000 {
		t.Fatalf("the uint128 path answered only %d of the cases; the fuzz no longer exercises it", took)
	}
}

func TestNumericDivUint128RoundingAndDeclines(t *testing.T) {
	n := func(m int64, s int16) Datum { return Datum{Kind: KindNumeric, Int: m, Scale: s} }
	// 20/3 and -20/3 at rscale 16 round the trailing 6 up, away from zero;
	// 10/-4 is exact. (A quotient below 1 takes rscale 20, whose 20 digits
	// pass a uint64: the big path's case.)
	for _, c := range []struct {
		a, b Datum
		want string
	}{
		{n(20, 0), n(3, 0), "6.6666666666666667"},
		{n(-20, 0), n(3, 0), "-6.6666666666666667"},
		{n(10, 0), n(-4, 0), "-2.5000000000000000"},
		{n(10, 0), n(3, 0), "3.3333333333333333"},
	} {
		want, err := numericDivBig(c.a, c.b, 0)
		if err != nil {
			t.Fatal(err)
		}
		if formatDatumDateStyle(want, nil) != c.want {
			t.Fatalf("oracle %s, want %s", formatDatumDateStyle(want, nil), c.want)
		}
		got, ok := numericDivUint128(c.a, c.b)
		if !ok || formatDatumDateStyle(got, nil) != c.want {
			t.Fatalf("%v / %v = %s (ok=%v), want %s", c.a, c.b, formatDatumDateStyle(got, nil), ok, c.want)
		}
	}
	// Declines: a zero divisor (numericDiv raises 22012 on the big path), a
	// zero numerator, a magnitude past uint64, a quotient past int64, and a
	// 20-digit quotient (2/3 at rscale 20).
	huge := NewNumericBigDatum(new(big.Int).Lsh(big.NewInt(1), 70), 0)
	for name, c := range map[string][2]Datum{
		"zero divisor":   {n(5, 0), n(0, 0)},
		"zero numerator": {n(0, 0), n(5, 0)},
		"wide operand":   {huge, n(3, 0)},
		"wide quotient":  {n(math.MaxInt64, 0), n(1, 18)},
		"wide shift":     {n(2, 0), n(3, 0)},
	} {
		if _, ok := numericDivUint128(c[0], c[1]); ok {
			t.Fatalf("%s: answered, want a decline", name)
		}
	}
	if _, err := numericDiv(n(5, 0), n(0, 0), 0); err == nil {
		t.Fatal("division by zero did not raise")
	}
}
