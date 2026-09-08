package executor

import "testing"

// TestAcquireRowContract pins the three properties callers rely on, which the
// pooled implementation documented and the make-based one must still provide.
func TestAcquireRowContract(t *testing.T) {
	for _, w := range []int{0, 1, 7, 16, 64, 65, 200} {
		r := acquireRow(w)
		if len(r) != w {
			t.Errorf("acquireRow(%d): len=%d, want %d", w, len(r), w)
		}
		if cap(r) != w {
			t.Errorf("acquireRow(%d): cap=%d, want %d (callers reslice to cap)", w, cap(r), w)
		}
		for i := range r {
			// Datum contains a []byte so it is not comparable; check the
			// fields that carry stale state.
			if r[i].Kind != 0 || r[i].Int != 0 || r[i].Buf != nil || r[i].ArenaID != 0 || r[i].Hi != 0 {
				t.Fatalf("acquireRow(%d): index %d not zero (%+v) — callers that "+
					"assign individual columns would observe stale data", w, i, r[i])
			}
		}
	}
	if r := acquireRow(-1); r != nil {
		t.Errorf("acquireRow(-1) = %v, want nil", r)
	}
}

// TestAcquireRowReturnsDistinctBacking is the anti-recycling pin.
//
// The pooled implementation could hand the SAME backing array to two live
// callers if a release were ever wrong. Nothing may depend on recycling now,
// and nothing may quietly reintroduce it: two rows acquired without an
// intervening release must not alias, or a downstream consumer holding the
// first row would see the second row's values appear underneath it.
func TestAcquireRowReturnsDistinctBacking(t *testing.T) {
	const w = 8
	a := acquireRow(w)
	b := acquireRow(w)
	a[0] = NewIntDatum(111)
	b[0] = NewIntDatum(222)
	if a[0].Int != 111 {
		t.Fatalf("second acquireRow aliased the first: a[0]=%d, want 111", a[0].Int)
	}
	if &a[0] == &b[0] {
		t.Fatal("two live rows share backing storage")
	}
}

// TestReleaseRowIsInertAndSafe pins that releaseRow neither mutates its
// argument nor panics on the shapes its ~12 call sites can pass. The pooled
// version zeroed the row; a caller that still reads a row after releasing it
// would have silently seen zeros, and must now see its data.
func TestReleaseRowIsInertAndSafe(t *testing.T) {
	r := acquireRow(4)
	r[0] = NewIntDatum(42)
	r[3] = NewIntDatum(99)
	releaseRow(r)
	if r[0].Int != 42 || r[3].Int != 99 {
		t.Errorf("releaseRow mutated its argument: got %d,%d want 42,99", r[0].Int, r[3].Int)
	}
	releaseRow(nil)
	releaseRow(Row{})
	releaseRow(acquireRow(200))
}
