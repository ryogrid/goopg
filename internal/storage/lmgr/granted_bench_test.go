package lmgr

import "testing"

// TestGrantedExceptMatchesWalk pins review/260831 ST-14: the cached-mask fast
// path must answer exactly what the holder walk answered.
func TestGrantedExceptMatchesWalk(t *testing.T) {
	walk := func(s *lockState, b BackendID) Mask {
		var g Mask
		for h, m := range s.holders {
			if h == b {
				continue
			}
			g |= m
		}
		return g
	}
	for _, holders := range []map[BackendID]Mask{
		{},
		{1: bit(AccessShareLock)},
		{1: bit(AccessShareLock), 2: bit(RowExclusiveLock)},
		{1: bit(AccessShareLock) | bit(ExclusiveLock), 2: bit(RowShareLock), 3: bit(AccessExclusiveLock)},
	} {
		s := &lockState{holders: holders}
		s.recomputeGranted()
		for _, b := range []BackendID{0, 1, 2, 3, 99} {
			if got, want := s.grantedExcept(b), walk(s, b); got != want {
				t.Errorf("holders=%v b=%d: grantedExcept = %b, walk = %b", holders, b, got, want)
			}
		}
	}
}

// BenchmarkGrantedExcept measures the conflict-check helper for a backend that
// does not hold the tag — the common case (review/260831 ST-14).
func BenchmarkGrantedExcept(b *testing.B) {
	s := &lockState{holders: map[BackendID]Mask{}}
	for i := 1; i <= 16; i++ {
		s.holders[BackendID(i)] = bit(AccessShareLock)
	}
	s.recomputeGranted()
	b.ReportAllocs()
	for b.Loop() {
		if s.grantedExcept(BackendID(999)) == 0 {
			b.Fatal("empty mask")
		}
	}
}
