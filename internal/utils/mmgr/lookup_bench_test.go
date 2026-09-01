package mmgr

import "testing"

// BenchmarkLookup measures resolving a ContextID to its Context
// (review/260831 UT-9). Every arena-backed Datum does this to reach its
// payload, and it used to take a process-global mutex to read one pointer —
// so the parallel case is the one that matters: with the mutex, backends
// serialised against each other on a pure read.
func BenchmarkLookup(b *testing.B) {
	c := Acquire(nil, KindSession)
	defer c.Release()
	id := c.ID()

	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if Lookup(id) == nil {
				b.Fatal("Lookup returned nil")
			}
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if Lookup(id) == nil {
					b.Fatal("Lookup returned nil")
				}
			}
		})
	})
}
