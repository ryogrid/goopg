package executor

import "testing"

// M0127-P0.1 — the merged-key seam (design leftdeep-joins/05 §3, stage E2).
//
// Before this task the lazy hash join called mergedKeySlot once per BUILD row
// and once per PROBE row, and each call allocated five objects: the all-NULL
// Row, its MaterializedSlot wrapper, the []virtualCol, the sources slice and
// the VirtualSlot itself. The composed shape only depends on
// (realWidth, nullWidth, realOnLeft), all three of which the child schemas fix
// at Open, so mergedKeySlotCache builds the slot once and rebinds the real
// source per pull.
//
// These tests pin the two properties that makes that legal: the cached slot
// presents exactly what a freshly-built one would (including after a rebind to
// a different source — a stale source would silently key every row off the
// first row's values), and the steady-state rebind allocates nothing.

func TestMergedKeySlotCacheMatchesFreshSlot(t *testing.T) {
	realRow := Row{NewIntDatum(7), NewStringDatum("x")}
	realSlot := SlotFromRow(nil, realRow)
	const nullWidth = 3

	for _, tc := range []struct {
		name       string
		realOnLeft bool
	}{
		{"real-on-left", true},
		{"real-on-right", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c mergedKeySlotCache
			want := mergedKeySlot(realSlot, len(realRow), nullWidth, tc.realOnLeft)

			got := c.rebind(realSlot, len(realRow), nullWidth, tc.realOnLeft)
			assertSameMergedSlot(t, got, want)

			// An unchanged shape must reuse the same slot object rather
			// than rebuild it — that reuse is the whole point of P0.1.
			again := c.rebind(realSlot, len(realRow), nullWidth, tc.realOnLeft)
			if again != got {
				t.Fatalf("rebind rebuilt the slot for an unchanged shape")
			}
			assertSameMergedSlot(t, again, want)
		})
	}
}

func TestMergedKeySlotCacheRebindsSource(t *testing.T) {
	first := SlotFromRow(nil, Row{NewIntDatum(1), NewStringDatum("a")})
	second := SlotFromRow(nil, Row{NewIntDatum(2), NewStringDatum("b")})
	const nullWidth = 3

	var c mergedKeySlotCache
	// realOnLeft=false → the real columns live at [nullWidth, nullWidth+2).
	c.rebind(first, 2, nullWidth, false)
	s := c.rebind(second, 2, nullWidth, false)

	if got := datumKey(s.Get(nullWidth)); got != datumKey(NewIntDatum(2)) {
		t.Fatalf("column %d = %q after rebind, want the second row's value", nullWidth, got)
	}
	if got := datumKey(s.Get(nullWidth + 1)); got != datumKey(NewStringDatum("b")) {
		t.Fatalf("column %d = %q after rebind, want the second row's value", nullWidth+1, got)
	}
	for i := 0; i < nullWidth; i++ {
		if !s.IsNull(i) {
			t.Fatalf("column %d should be on the NULL side", i)
		}
	}
}

func TestMergedKeySlotCacheRebuildsOnShapeChange(t *testing.T) {
	narrow := SlotFromRow(nil, Row{NewIntDatum(1)})
	wide := SlotFromRow(nil, Row{NewIntDatum(1), NewIntDatum(2)})

	var c mergedKeySlotCache
	first := c.rebind(narrow, 1, 2, false)
	if first.Width() != 3 {
		t.Fatalf("width = %d, want 3", first.Width())
	}
	// The build loops' `width == 0 && len(row) > 0` fallback can widen the
	// real side once (empty child schema); the cache must not keep serving
	// the stale narrow shape.
	second := c.rebind(wide, 2, 2, false)
	if second.Width() != 4 {
		t.Fatalf("width after widening = %d, want 4", second.Width())
	}
	if datumKey(second.Get(3)) != datumKey(NewIntDatum(2)) {
		t.Fatalf("widened slot lost the real side's trailing column")
	}
	// Flipping the orientation must rebuild too.
	flipped := c.rebind(wide, 2, 2, true)
	if !flipped.IsNull(3) {
		t.Fatalf("realOnLeft flip did not rebuild: column 3 should be on the NULL side")
	}
}

// TestMergedKeySlotCacheZeroSteadyStateAllocs is the seam microbench bar from
// IMPLEMENTATION-TODO P0.1 expressed as an assertion: once the shape has
// settled, a per-row rebind must allocate nothing.
func TestMergedKeySlotCacheZeroSteadyStateAllocs(t *testing.T) {
	a := SlotFromRow(nil, Row{NewIntDatum(1), NewStringDatum("a")})
	b := SlotFromRow(nil, Row{NewIntDatum(2), NewStringDatum("b")})

	var c mergedKeySlotCache
	c.rebind(a, 2, 3, false) // settle the shape

	i := 0
	allocs := testing.AllocsPerRun(1000, func() {
		src := a
		if i&1 == 1 {
			src = b
		}
		i++
		s := c.rebind(src, 2, 3, false)
		_ = s.Get(3)
	})
	if allocs != 0 {
		t.Fatalf("steady-state rebind allocated %.1f objects/row, want 0", allocs)
	}
}

// BenchmarkMergedKeySlotSeam / ...Uncached are the seam microbench: the
// cached arm is the P0.1 shape, the uncached arm reproduces the per-row
// mergedKeySlot call it replaced. Run with -benchmem; the cached arm must
// report 0 allocs/op.
func BenchmarkMergedKeySlotSeam(b *testing.B) {
	slots := benchKeySlotSources()
	var c mergedKeySlotCache
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := c.rebind(slots[i&(len(slots)-1)], 2, 3, false)
		_ = s.Get(3)
	}
}

func BenchmarkMergedKeySlotSeamUncached(b *testing.B) {
	slots := benchKeySlotSources()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := mergedKeySlot(slots[i&(len(slots)-1)], 2, 3, false)
		_ = s.Get(3)
	}
}

func benchKeySlotSources() []*MaterializedSlot {
	slots := make([]*MaterializedSlot, 64)
	for i := range slots {
		slots[i] = SlotFromRow(nil, Row{NewIntDatum(int64(i)), NewStringDatum("k")})
	}
	return slots
}

func assertSameMergedSlot(t *testing.T, got, want *VirtualSlot) {
	t.Helper()
	if got.Width() != want.Width() {
		t.Fatalf("width = %d, want %d", got.Width(), want.Width())
	}
	for i := 0; i < want.Width(); i++ {
		if got.IsNull(i) != want.IsNull(i) {
			t.Fatalf("column %d IsNull = %v, want %v", i, got.IsNull(i), want.IsNull(i))
		}
		if want.IsNull(i) {
			continue
		}
		if datumKey(got.Get(i)) != datumKey(want.Get(i)) {
			t.Fatalf("column %d = %q, want %q", i, datumKey(got.Get(i)), datumKey(want.Get(i)))
		}
	}
}
