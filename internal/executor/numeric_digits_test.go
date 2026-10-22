package executor

import (
	"math/big"
	"math/rand"
	"testing"
)

// M0122-0012 slice 67 guards: the NBASE weight / first-digit helpers count
// decimal digits and divide instead of formatting the value, and powers of
// ten come from a table. The oracles below are the string-based versions
// they replaced, which produced PG's select_div_scale and sqrt_var inputs.

func nbaseWeightAndFirstDigitString(absStr string, dscale int) (weight, firstdigit int) {
	if len(absStr) == 1 && absStr[0] == '0' {
		return 0, 0
	}
	nd := len(absStr)
	msbDecPos := nd - 1 - dscale
	weight = msbDecPos / 4
	if msbDecPos < 0 && msbDecPos%4 != 0 {
		weight--
	}
	posInNBASE := msbDecPos - 4*weight
	for i := 0; i <= posInNBASE; i++ {
		d := byte(0)
		if i < nd {
			d = absStr[i] - '0'
		}
		firstdigit = firstdigit*10 + int(d)
	}
	return weight, firstdigit
}

func pgNBaseWeightFirstString(d *big.Int, scale int) (int, int64) {
	s4 := (scale + 3) / 4 * 4
	v := new(big.Int).Mul(d, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(s4-scale)), nil))
	nd := (len(v.String()) + 3) / 4
	lead := new(big.Int).Quo(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(4*(nd-1))), nil))
	return nd - 1 - s4/4, lead.Int64()
}

// digitValues spans one-digit values, every power-of-ten boundary (10^k - 1,
// 10^k, 10^k + 1) up to 10^60 — the uint64 / big.Int seam at 10^19..10^20
// included — and random values of up to 200 bits.
func digitValues(r *rand.Rand) []*big.Int {
	var out []*big.Int
	for v := int64(1); v < 10; v++ {
		out = append(out, big.NewInt(v))
	}
	for k := 1; k <= 60; k++ {
		p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(k)), nil)
		out = append(out, new(big.Int).Sub(p, big.NewInt(1)), p, new(big.Int).Add(p, big.NewInt(1)))
	}
	out = append(out, new(big.Int).SetUint64(^uint64(0)), new(big.Int).Add(new(big.Int).SetUint64(^uint64(0)), big.NewInt(1)))
	for range 2000 {
		out = append(out, new(big.Int).Add(new(big.Int).Rand(r, new(big.Int).Lsh(big.NewInt(1), uint(1+r.Intn(200)))), big.NewInt(1)))
	}
	return out
}

func TestDecimalDigitsMatchesString(t *testing.T) {
	for _, v := range digitValues(rand.New(rand.NewSource(67))) {
		want := len(v.String())
		if got := decimalDigitsBig(v); got != want {
			t.Fatalf("decimalDigitsBig(%s) = %d, want %d", v, got, want)
		}
		if v.IsUint64() {
			if got := decimalDigitsUint64(v.Uint64()); got != want {
				t.Fatalf("decimalDigitsUint64(%s) = %d, want %d", v, got, want)
			}
		}
	}
	if got := decimalDigitsUint64(0); got != 1 {
		t.Fatalf("decimalDigitsUint64(0) = %d, want 1", got)
	}
}

func TestPow10BigTableAndFallback(t *testing.T) {
	for _, k := range []int{0, 1, 19, 20, 159, 160, 300} {
		want := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(k)), nil)
		if got := pow10Big(k); got.Cmp(want) != 0 {
			t.Fatalf("pow10Big(%d) = %s, want %s", k, got, want)
		}
	}
	if got := pow10Big(-3); got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("pow10Big(-3) = %s, want 1 (big.Int.Exp's answer)", got)
	}
}

func TestNBaseWeightHelpersMatchStringOracle(t *testing.T) {
	r := rand.New(rand.NewSource(1967))
	for _, v := range digitValues(r) {
		for dscale := -9; dscale <= 40; dscale++ {
			for _, m := range []*big.Int{v, new(big.Int).Neg(v)} {
				w, f := nbaseWeightAndFirstDigit(m, dscale)
				ww, wf := nbaseWeightAndFirstDigitString(v.String(), dscale)
				if w != ww || f != wf {
					t.Fatalf("nbaseWeightAndFirstDigit(%s, %d) = (%d, %d), want (%d, %d)", m, dscale, w, f, ww, wf)
				}
			}
			if dscale < 0 {
				continue
			}
			w, f := pgNBaseWeightFirst(v, dscale)
			ww, wf := pgNBaseWeightFirstString(v, dscale)
			if w != ww || f != wf {
				t.Fatalf("pgNBaseWeightFirst(%s, %d) = (%d, %d), want (%d, %d)", v, dscale, w, f, ww, wf)
			}
		}
	}
	if w, f := nbaseWeightAndFirstDigit(new(big.Int), 7); w != 0 || f != 0 {
		t.Fatalf("zero = (%d, %d), want (0, 0)", w, f)
	}
}
