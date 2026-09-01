package nbtree

import (
	"bytes"
	"math/big"
	"testing"
)

// refEncodeNumericKey is the pre-NB-11 trailing-zero strip, kept for the
// differential test: it must produce the same key bytes.
func refStrip(mantissa *big.Int, scale int16) (*big.Int, int32) {
	abs := new(big.Int).Abs(mantissa)
	s := int32(scale)
	ten := big.NewInt(10)
	zero := big.NewInt(0)
	rem := new(big.Int)
	for {
		new(big.Int).QuoRem(abs, ten, rem)
		if rem.Cmp(zero) != 0 {
			break
		}
		abs.Quo(abs, ten)
		s--
		if abs.Sign() == 0 {
			break
		}
	}
	return abs, s
}

// TestEncodeNumericKeyStripMatchesReference pins review/260831 NB-11: reusing
// the scratch big.Ints and swapping the quotient in must strip exactly the
// digits the old two-division loop stripped.
func TestEncodeNumericKeyStripMatchesReference(t *testing.T) {
	values := []string{
		"0", "1", "10", "100000", "-100000", "1234500", "-1234500",
		"999999999999999999999999", "1000000000000000000000000",
		"-1000000000000000000000000", "7", "-7",
	}
	for _, v := range values {
		for _, scale := range []int16{0, 2, 6, -3} {
			m, _ := new(big.Int).SetString(v, 10)
			wantAbs, wantScale := refStrip(m, scale)
			// The encoder embeds both, so comparing keys compares both.
			got := EncodeNumericKey(m, scale)
			wantSign := new(big.Int).Set(wantAbs)
			if m.Sign() < 0 {
				wantSign.Neg(wantSign)
			}
			want := EncodeNumericKey(wantSign, int16(wantScale))
			if !bytes.Equal(got, want) {
				t.Errorf("EncodeNumericKey(%s, %d) = %x, reference strip gives %x", v, scale, got, want)
			}
		}
	}
}

// BenchmarkEncodeNumericKey measures index-key encoding for a NUMERIC value
// with trailing zeros — the shape that makes the strip loop run.
func BenchmarkEncodeNumericKey(b *testing.B) {
	m, _ := new(big.Int).SetString("123450000000000", 10)
	b.ReportAllocs()
	for b.Loop() {
		if len(EncodeNumericKey(m, 4)) == 0 {
			b.Fatal("empty key")
		}
	}
}
