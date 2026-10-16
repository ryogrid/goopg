package executor

import (
	"fmt"
	"math/big"
	"math/rand"
	"strings"
	"testing"
)

// TestAggNumericSumInPlaceMatchesExact pins applyAgg's in-place numeric sum
// (M0122-0012) to exact arithmetic. The fast path covers same-scale int64
// lanes; mixed scales and int64 overflow must still reach numericAdd, so the
// three columns exercise each route:
//   - a numeric(15,2): every value at scale 2 (the in-place path);
//   - b numeric: scales 0..3 mixed within a group (the aligned numericAdd path);
//   - c numeric: values near int64's limit whose running sum overflows into
//     the big-mantissa lane.
//
// Expected sums are computed with math/big over the inserted literals.
func TestAggNumericSumInPlaceMatchesExact(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, `CREATE TABLE ns (g int, a numeric(15,2), b numeric, c numeric)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	rng := rand.New(rand.NewSource(122012))
	type acc struct {
		a, b, c *big.Int
		bScale  int
	}
	groups := map[int]*acc{}
	const scaleB = 3
	for i := 0; i < 400; i++ {
		g := i % 4
		ac := groups[g]
		if ac == nil {
			ac = &acc{a: new(big.Int), b: new(big.Int), c: new(big.Int)}
			groups[g] = ac
		}
		aCents := int64(rng.Intn(2000000) - 1000000)
		aLit := fmt.Sprintf("%.2f", float64(aCents)/100)
		// Re-derive the cents from the literal so the oracle sums what was inserted.
		ac.a.Add(ac.a, mustBigFromDecimal(t, aLit, 2))
		bs := rng.Intn(scaleB + 1)
		bMant := int64(rng.Intn(200000) - 100000)
		bLit := decimalLiteral(bMant, bs)
		if bs > ac.bScale {
			ac.bScale = bs
		}
		ac.b.Add(ac.b, mustBigFromDecimal(t, bLit, scaleB))
		cVal := int64(9_000_000_000_000_000) + int64(rng.Intn(1000))
		cLit := fmt.Sprintf("%d", cVal)
		ac.c.Add(ac.c, big.NewInt(cVal))
		runSQL(t, ctx, fmt.Sprintf("INSERT INTO ns VALUES (%d, %s, %s, %s)", g, aLit, bLit, cLit))
	}
	rows := runSQL(t, ctx, `SELECT g, sum(a), sum(b), sum(c) FROM ns GROUP BY g ORDER BY g`)
	if len(rows) != 4 {
		t.Fatalf("got %d groups, want 4", len(rows))
	}
	for _, r := range rows {
		g := int(r[0].Int)
		ac := groups[g]
		wantA := formatScaled(ac.a, 2, 2)
		wantB := formatScaled(ac.b, scaleB, ac.bScale)
		wantC := ac.c.String()
		if got := r[1].Format(); got != wantA {
			t.Errorf("group %d sum(a) = %s, want %s", g, got, wantA)
		}
		if got := r[2].Format(); got != wantB {
			t.Errorf("group %d sum(b) = %s, want %s", g, got, wantB)
		}
		if got := r[3].Format(); got != wantC {
			t.Errorf("group %d sum(c) = %s, want %s (int64 overflow must reach the big lane)", g, got, wantC)
		}
	}
}

func decimalLiteral(mant int64, scale int) string {
	neg := mant < 0
	if neg {
		mant = -mant
	}
	s := fmt.Sprintf("%0*d", scale+1, mant)
	if scale > 0 {
		s = s[:len(s)-scale] + "." + s[len(s)-scale:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// mustBigFromDecimal parses lit as an exact decimal scaled by 10^scale.
func mustBigFromDecimal(t *testing.T, lit string, scale int) *big.Int {
	t.Helper()
	neg := strings.HasPrefix(lit, "-")
	lit = strings.TrimPrefix(lit, "-")
	intPart, frac := lit, ""
	if i := strings.IndexByte(lit, '.'); i >= 0 {
		intPart, frac = lit[:i], lit[i+1:]
	}
	for len(frac) < scale {
		frac += "0"
	}
	v, ok := new(big.Int).SetString(intPart+frac, 10)
	if !ok {
		t.Fatalf("bad literal %q", lit)
	}
	if neg {
		v.Neg(v)
	}
	return v
}

// formatScaled renders v / 10^scale with exactly outScale decimals
// (outScale <= scale; the dropped digits must be zero).
func formatScaled(v *big.Int, scale, outScale int) string {
	q := new(big.Int).Set(v)
	for i := outScale; i < scale; i++ {
		q.Quo(q, big.NewInt(10))
	}
	return decimalLiteralBig(q, outScale)
}

func decimalLiteralBig(m *big.Int, scale int) string {
	neg := m.Sign() < 0
	s := new(big.Int).Abs(m).String()
	for len(s) < scale+1 {
		s = "0" + s
	}
	if scale > 0 {
		s = s[:len(s)-scale] + "." + s[len(s)-scale:]
	}
	if neg {
		s = "-" + s
	}
	return s
}
