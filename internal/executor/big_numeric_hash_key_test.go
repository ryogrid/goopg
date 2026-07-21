package executor

// R3-3: numerics whose mantissa exceeds int64 must hash by VALUE.
//
// datumKey is the canonical hash key for equi-join build/probe sides
// (evalHashKey), for the hashed IN-sublink probe, and for grouping. Its
// KindNumeric arm used to call NumericMantissaValue() unconditionally,
// which is only correct on the int64 fast lane: for a flagBigNumeric
// datum that accessor returns d.Int, and in the big lane d.Int holds the
// mctx `(offset<<32 | len)` encoding rather than the value. Two equal big
// numerics stored at different offsets therefore hashed to different keys
// and their pair was silently dropped — a wrong-results bug in ordinary
// hash joins, not merely a missed hashed-probe optimisation.
//
// The fix routes the big lane through canonicalBigNumericKey, which
// applies the same trailing-zero normalisation as the int64 lane and
// hands back values that fit int64 after stripping, so the two lanes
// converge on a single key. These tests pin the value-equality behaviour
// end-to-end (the observable contract) plus the lane convergence at the
// key level (the mechanism that keeps hashFamNumeric one family).

import (
	"math/big"
	"testing"
)

func newBigNumericFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	for _, stmt := range []string{
		"CREATE TABLE bna (k numeric, tag text)",
		"CREATE TABLE bnb (k numeric, tag text)",
		// a1/a2 are past int64 (max ~9.22e18); a3 stays on the fast
		// lane so one fixture covers both lanes and their boundary.
		"INSERT INTO bna VALUES (99999999999999999999, 'a1')",
		"INSERT INTO bna VALUES (12345678901234567890123, 'a2')",
		"INSERT INTO bna VALUES (7, 'a3')",
		"INSERT INTO bnb VALUES (99999999999999999999, 'b1')",
		"INSERT INTO bnb VALUES (12345678901234567890123, 'b2')",
		"INSERT INTO bnb VALUES (7, 'b3')",
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			cleanup()
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return ctx, cleanup
}

// TestBigNumericHashJoinMatchesAllPairs is the wrong-results regression:
// before the fix this returned only the fast-lane pair.
func TestBigNumericHashJoinMatchesAllPairs(t *testing.T) {
	ctx, cleanup := newBigNumericFixture(t)
	defer cleanup()

	rows, err := runQueryWithErr(ctx, "SELECT bna.tag, bnb.tag FROM bna JOIN bnb ON bna.k = bnb.k ORDER BY bna.tag")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	got := renderRows(rows)
	want := []string{"a1|b1", "a2|b2", "a3|b3"}
	if len(got) != len(want) {
		t.Fatalf("big-numeric equi-join dropped pairs: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pair %d: got %q want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// TestBigNumericInProbeMatchesLinearOracle runs the IN sublink on both
// the hashed probe and the linear reference. They must agree, and both
// must find every value — the linear loop compares with compareEq, whose
// numeric arm has always been big-lane correct, so a disagreement would
// localise the bug to the key.
func TestBigNumericInProbeMatchesLinearOracle(t *testing.T) {
	ctx, cleanup := newBigNumericFixture(t)
	defer cleanup()

	const q = "SELECT tag FROM bna WHERE k IN (SELECT k FROM bnb) ORDER BY tag"
	SetHashedSubPlanEnabled(true)
	t.Cleanup(func() { SetHashedSubPlanEnabled(true) })
	hashed, err := runQueryWithErr(ctx, q)
	if err != nil {
		t.Fatalf("hashed: %v", err)
	}
	SetHashedSubPlanEnabled(false)
	linear, err := runQueryWithErr(ctx, q)
	if err != nil {
		t.Fatalf("linear: %v", err)
	}
	h, l := renderRows(hashed), renderRows(linear)
	if len(h) != len(l) {
		t.Fatalf("hashed and linear paths disagree: hashed %v vs linear %v", h, l)
	}
	for i := range h {
		if h[i] != l[i] {
			t.Fatalf("row %d: hashed %q vs linear %q", i, h[i], l[i])
		}
	}
	if len(h) != 3 {
		t.Fatalf("expected all 3 values found, got %v", h)
	}
}

// TestBigNumericKeyLaneConvergence pins the mechanism: a value expressed
// in the big lane and the same value in the int64 lane must produce one
// key, including across scales. Without convergence hashFamNumeric would
// have to split into two families and cross-lane joins would miss.
func TestBigNumericKeyLaneConvergence(t *testing.T) {
	mustBig := func(s string) *big.Int {
		t.Helper()
		v, ok := new(big.Int).SetString(s, 10)
		if !ok {
			t.Fatalf("bad big literal %q", s)
		}
		return v
	}
	cases := []struct {
		name    string
		bigMant string
		bigScl  int
		i64Mant int64
		i64Scl  int
	}{
		// 1.0000000000000000000000 (big) == 1 (fast)
		{"one-across-lanes", "10000000000000000000000", 22, 1, 0},
		// 250.00 expressed hugely vs compactly
		{"scaled-value", "25000000000000000000000", 20, 250, 0},
		// zero is its own case in both helpers
		{"zero", "0", 5, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBig := canonicalBigNumericKey(mustBig(tc.bigMant), tc.bigScl)
			gotI64 := canonicalNumericKey(tc.i64Mant, tc.i64Scl)
			if gotBig != gotI64 {
				t.Fatalf("lanes diverge: big key %q vs int64 key %q", gotBig, gotI64)
			}
		})
	}

	// A genuinely huge value stays in the big form and must be
	// distinguishable from its neighbour (no collapsing).
	a := canonicalBigNumericKey(mustBig("12345678901234567890123"), 0)
	b := canonicalBigNumericKey(mustBig("12345678901234567890124"), 0)
	if a == b {
		t.Fatalf("distinct big numerics collided on key %q", a)
	}
}
