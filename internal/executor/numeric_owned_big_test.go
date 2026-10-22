package executor

import (
	"math/big"
	"testing"

	"github.com/goopg/goopg/internal/utils/mmgr"
)

// M0122-0016 guards: a big-mantissa numeric's payload is owned by its Datum
// (ArenaID 0, Buf = sign byte + big-endian magnitude, Int = its length), so
// numeric arithmetic no longer writes into the never-reset permanent arena.

func TestBigNumericArithmeticLeavesPermUntouched(t *testing.T) {
	a := newNumeric(new(big.Int).Exp(big.NewInt(10), big.NewInt(25), nil), 16)
	b := Datum{Kind: KindNumeric, Int: 7, Scale: 0}
	before, _ := mmgr.Perm().Usage()
	for i := range 20000 {
		q, err := numericDiv(a, b, 0)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (q.ArenaID != 0 || !isBigNumeric(q)) {
			t.Fatalf("quotient arena %d big=%v, want an owned big numeric", q.ArenaID, isBigNumeric(q))
		}
		if _, err := numericAdd(a, q); err != nil {
			t.Fatal(err)
		}
	}
	if after, _ := mmgr.Perm().Usage(); after != before {
		t.Fatalf("Perm usage grew %d bytes over 20 000 big-lane divisions and additions, want 0", after-before)
	}
}

func TestOwnedBigNumericRoundTrip(t *testing.T) {
	for _, s := range []string{"0", "1", "-1", "18446744073709551616", "-98765432109876543210987654321"} {
		v, _ := new(big.Int).SetString(s, 10)
		d := NewNumericBigDatum(v, 3)
		if d.ArenaID != 0 || int(d.Int) != len(d.Buf) || d.Buf == nil {
			t.Fatalf("%s: arena %d, Int %d, Buf %d bytes; want an owned payload whose length is Int", s, d.ArenaID, d.Int, len(d.Buf))
		}
		if got := d.NumericBigValue(); got.Cmp(v) != 0 {
			t.Fatalf("%s: NumericBigValue = %s", s, got)
		}
		if m := d.MaterializeArena(); m.ArenaID != 0 || m.NumericBigValue().Cmp(v) != 0 {
			t.Fatalf("%s: MaterializeArena changed an owned value", s)
		}
		// An arena-backed copy detaches into an owned one that survives the
		// arena's reset.
		ctx := mmgr.Acquire(nil, mmgr.KindStmt)
		arena := newBigNumericInCtx(ctx, v, 3)
		owned := arena.MaterializeArena()
		ctx.Reset()
		ctx.Release()
		if owned.ArenaID != 0 || owned.NumericBigValue().Cmp(v) != 0 {
			t.Fatalf("%s: detached arena value = %s (arena %d)", s, owned.NumericBigValue(), owned.ArenaID)
		}
	}
}
