package executor

import (
	"testing"
)

// TestPermArenaSlotZero pins that permArena holds slot 0 of
// arenaRegistry. The future packed-Datum flip relies on a
// zero-valued ArenaRef resolving to permArena (matching the
// "uninitialised → permArena" semantic for literal Datums).
// (M0074-0003 forward-compat.)
func TestPermArenaSlotZero(t *testing.T) {
	if permArena == nil {
		t.Fatal("permArena is nil")
	}
	if !permArena.permanent {
		t.Error("permArena.permanent should be true")
	}
	if permArena.registryIdx != permArenaSlot {
		t.Errorf("permArena.registryIdx = %d, want %d", permArena.registryIdx, permArenaSlot)
	}
	if arenaRegistry[permArenaSlot] != permArena {
		t.Error("arenaRegistry[0] should hold permArena")
	}
}

// TestNewArenaRegistersAndDropUnregisters pins the
// register-on-NewArena, unregister-on-Drop lifecycle.
// (M0074-0003 forward-compat.)
func TestNewArenaRegistersAndDropUnregisters(t *testing.T) {
	a := NewArena(0)
	if a.registryIdx <= 0 || int(a.registryIdx) >= arenaRegistrySize {
		t.Fatalf("registryIdx out of range: %d", a.registryIdx)
	}
	if arenaRegistry[a.registryIdx] != a {
		t.Errorf("arenaRegistry[%d] = %v, want %v", a.registryIdx, arenaRegistry[a.registryIdx], a)
	}
	slot := a.registryIdx
	a.Drop()
	if arenaRegistry[slot] != nil {
		t.Errorf("arenaRegistry[%d] should be nil after Drop, got %v", slot, arenaRegistry[slot])
	}
}

// TestPermArenaResetIsNoOp pins that calling Reset on permArena
// does nothing — the never-reset invariant holds. The future
// packed-Datum flip stores literal payloads in permArena; if
// Reset wiped them, those Datums would point at garbage.
// (M0074-0003 forward-compat.)
func TestPermArenaResetIsNoOp(t *testing.T) {
	off, length := permArena.AllocateString("forever-living-string")
	if length == 0 {
		t.Fatal("AllocateString returned zero length for non-empty input")
	}
	// Sanity: read back.
	pre := permArena.Bytes(int(off), int(length))
	if string(pre) != "forever-living-string" {
		t.Fatalf("permArena.Bytes pre-Reset = %q, want %q", pre, "forever-living-string")
	}
	// Reset (should be a no-op for permArena).
	permArena.Reset()
	// Bytes should still be readable.
	post := permArena.Bytes(int(off), int(length))
	if string(post) != "forever-living-string" {
		t.Errorf("permArena.Bytes post-Reset = %q, want %q (Reset broke permArena lifetime)", post, "forever-living-string")
	}
}

// TestArenaAllocateStringEmpty pins (offset, length) = (0, 0)
// for an empty input — no page allocation, clean degenerate
// case for the future packed-Datum flip's empty-string handling.
// (M0074-0003 forward-compat.)
func TestArenaAllocateStringEmpty(t *testing.T) {
	a := NewArena(0)
	defer a.Drop()
	off, length := a.AllocateString("")
	if off != 0 || length != 0 {
		t.Errorf("AllocateString(\"\") = (%d, %d), want (0, 0)", off, length)
	}
}

// TestArenaAllocateBytesRoundTrip pins AllocateBytes
// round-trip via Bytes. (M0074-0003 forward-compat.)
func TestArenaAllocateBytesRoundTrip(t *testing.T) {
	a := NewArena(0)
	defer a.Drop()
	src := []byte{0x01, 0x02, 0x03, 0xff, 0xfe}
	off, length := a.AllocateBytes(src)
	if length != int32(len(src)) {
		t.Errorf("length = %d, want %d", length, len(src))
	}
	got := a.Bytes(int(off), int(length))
	if string(got) != string(src) {
		t.Errorf("AllocateBytes round-trip = %v, want %v", got, src)
	}
}

// TestRegisterArenaRoundRobinSkipsSlotZero pins that
// registerArena never returns slot 0 (which is reserved for
// permArena). (M0074-0003 forward-compat.)
func TestRegisterArenaRoundRobinSkipsSlotZero(t *testing.T) {
	// Allocate enough arenas to wrap the round-robin counter.
	const n = arenaRegistrySize / 2
	arenas := make([]*Arena, n)
	for i := range arenas {
		arenas[i] = NewArena(0)
		if arenas[i].registryIdx == permArenaSlot {
			t.Errorf("arena[%d] got permArena slot %d", i, permArenaSlot)
		}
		if arenas[i].registryIdx <= 0 {
			t.Errorf("arena[%d] got non-positive slot %d", i, arenas[i].registryIdx)
		}
	}
	for _, a := range arenas {
		a.Drop()
	}
}
