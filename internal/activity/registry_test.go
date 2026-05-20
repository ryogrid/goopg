package activity

import (
	"testing"
	"unsafe"
)

// TestActivitySlotIs64Bytes asserts that activitySlot is exactly one
// cache line (64 bytes).  False sharing between slots would negate the
// per-backend isolation benefit of the M0107-0005 refactor.
func TestActivitySlotIs64Bytes(t *testing.T) {
	if got := unsafe.Sizeof(activitySlot{}); got != 64 {
		t.Fatalf("activitySlot size = %d, want 64 (one cache line)", got)
	}
}

// TestWaitEventPackRoundtrip verifies that packWaitStrings / unpackWaitStrings
// are inverses for all registered wait-event constants.
func TestWaitEventPackRoundtrip(t *testing.T) {
	cases := []struct{ typ, evt string }{
		{WaitTypeIO, WaitWALWrite},
		{WaitTypeIO, WaitDataFileRead},
		{WaitTypeClient, WaitClientRead},
		{WaitTypeClient, WaitClientWrite},
		{WaitTypeLock, WaitRelationLock},
		{WaitTypeBufferPin, WaitBufferPin},
		{WaitTypeActivity, WaitWalWriterMain},
	}
	for _, c := range cases {
		packed := packWaitStrings(c.typ, c.evt)
		gotTyp, gotEvt := unpackWaitStrings(packed)
		if gotTyp != c.typ || gotEvt != c.evt {
			t.Errorf("pack(%q, %q) → unpack = (%q, %q)", c.typ, c.evt, gotTyp, gotEvt)
		}
	}
	// Zero = no wait event.
	typ, evt := unpackWaitStrings(0)
	if typ != "" || evt != "" {
		t.Errorf("unpack(0) = (%q, %q), want (\"\", \"\")", typ, evt)
	}
}

// TestActivityRegistryWaitEventAtomic verifies the hot path: WaitEventStart
// stores waitInfo atomically and Snapshot reflects the new value without
// acquiring any global mutex.
func TestActivityRegistryWaitEventAtomic(t *testing.T) {
	r := NewActivityRegistry(16)
	r.Register(&Backend{PID: "1", State: "active"})
	pn := int32(0) // PID "1" → procNum 0
	r.WaitEventStart(pn, WaitTypeIO, WaitWALWrite)
	snap := r.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	if snap[0].WaitEventType != WaitTypeIO {
		t.Errorf("WaitEventType = %q, want %q", snap[0].WaitEventType, WaitTypeIO)
	}
	if snap[0].WaitEvent != WaitWALWrite {
		t.Errorf("WaitEvent = %q, want %q", snap[0].WaitEvent, WaitWALWrite)
	}
	r.WaitEventEnd(pn)
	snap = r.Snapshot()
	if snap[0].WaitEventType != "" || snap[0].WaitEvent != "" {
		t.Errorf("after WaitEventEnd: type=%q event=%q, want both empty",
			snap[0].WaitEventType, snap[0].WaitEvent)
	}
}

// TestActivityRegistryBackgroundWorker verifies RegisterBackground assigns
// a slot beyond the regular range and Release cleans it up.
func TestActivityRegistryBackgroundWorker(t *testing.T) {
	const nReg = 16
	r := NewActivityRegistry(nReg)
	pn := r.RegisterBackground(WalWriterIdx, &Backend{
		PID:         "wal-writer-0",
		BackendType: "walwriter",
		State:       "active",
	})
	if pn != int32(nReg+WalWriterIdx) {
		t.Fatalf("WalWriter procNum = %d, want %d", pn, nReg+WalWriterIdx)
	}
	snap := r.Snapshot()
	if len(snap) != 1 || snap[0].BackendType != "walwriter" {
		t.Fatalf("snapshot = %+v, want walwriter entry", snap)
	}
	r.ReleaseBackground(WalWriterIdx, "wal-writer-0")
	snap = r.Snapshot()
	if len(snap) != 0 {
		t.Fatalf("snapshot len = %d after Release, want 0", len(snap))
	}
}

// TestActivityRegistrySetCurrentGoroutine verifies the goroutine-activity map
// provides (reg, procNum) lookup used by pool/AIO/spill hooks.
func TestActivityRegistrySetCurrentGoroutine(t *testing.T) {
	r := NewActivityRegistry(16)
	r.Register(&Backend{PID: "5", State: "active"})
	pn := int32(4) // PID "5" → (5-1)%16 = 4

	done := make(chan bool, 1)
	go func() {
		SetCurrentGoroutine(r, pn)
		defer ClearCurrentGoroutine()
		got, gotPN, ok := LookupCurrentGoroutine()
		if !ok || got != r || gotPN != pn {
			t.Errorf("LookupCurrentGoroutine = (%v, %d, %v), want (%v, %d, true)",
				got, gotPN, ok, r, pn)
		}
		done <- true
	}()
	<-done

	// After ClearCurrentGoroutine, the goroutine's entry is gone.
	// (We can't easily test this from outside the goroutine, but the
	// goroutine itself calls ClearCurrentGoroutine via defer.)
}
