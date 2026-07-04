package activity

import (
	"testing"
	"time"
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


// TestActivityRegistryTrackIOTimingFastPath verifies M0122-0003's
// runtime-SET follow-up: LookupTrackedGoroutine reports ok=false by
// default (fast path off, matching track_io_timing's "off" bootval),
// starts reporting ok=true only after UpdateTrackIOTiming(procNum, true)
// latches the fast path AND the calling goroutine's own procNum has it
// on — a second backend with the fast path latched but its own flag
// still off must not be reported as tracked.
func TestActivityRegistryTrackIOTimingFastPath(t *testing.T) {
	r := NewActivityRegistry(16)
	r.Register(&Backend{PID: "1", State: "active"}) // procNum 0
	r.Register(&Backend{PID: "2", State: "active"}) // procNum 1
	const onPN, offPN = int32(0), int32(1)

	SetCurrentGoroutine(r, onPN)
	defer ClearCurrentGoroutine()

	if _, _, ok := r.LookupTrackedGoroutine(); ok {
		t.Fatalf("LookupTrackedGoroutine before any SET = ok, want false")
	}
	if r.TrackIOTiming(onPN) {
		t.Fatalf("TrackIOTiming(onPN) before SET = true, want false")
	}

	r.UpdateTrackIOTiming(onPN, true)
	if !r.TrackIOTiming(onPN) {
		t.Errorf("TrackIOTiming(onPN) after enabling = false, want true")
	}
	if r.TrackIOTiming(offPN) {
		t.Errorf("TrackIOTiming(offPN) = true, want false (never enabled for this backend)")
	}
	reg, pn, ok := r.LookupTrackedGoroutine()
	if !ok || reg != r || pn != onPN {
		t.Fatalf("LookupTrackedGoroutine on the enabled backend = (%v, %d, %v), want (%v, %d, true)",
			reg, pn, ok, r, onPN)
	}

	// Disabling this backend's own flag (independent of the latched
	// fast path) must stop it from being reported as tracked.
	r.UpdateTrackIOTiming(onPN, false)
	if _, _, ok := r.LookupTrackedGoroutine(); ok {
		t.Fatalf("LookupTrackedGoroutine after disabling this backend = ok, want false")
	}
}

// TestActivityRegistryTrackIOTimingFastPathBoot verifies
// EnableTrackIOTimingFastPath primes the process-wide flag (the
// boot-time-config path in initdb.Open) without needing any specific
// backend's UpdateTrackIOTiming call — the fast path alone is not
// sufficient for a given backend to be reported as tracked, since
// per-backend TrackIOTimingOn still defaults false.
func TestActivityRegistryTrackIOTimingFastPathBoot(t *testing.T) {
	r := NewActivityRegistry(16)
	r.Register(&Backend{PID: "1", State: "active"})
	pn := int32(0)
	SetCurrentGoroutine(r, pn)
	defer ClearCurrentGoroutine()

	r.EnableTrackIOTimingFastPath()
	if _, _, ok := r.LookupTrackedGoroutine(); ok {
		t.Fatalf("LookupTrackedGoroutine after only priming the fast path = ok, want false")
	}
	r.UpdateTrackIOTiming(pn, true)
	if _, _, ok := r.LookupTrackedGoroutine(); !ok {
		t.Fatalf("LookupTrackedGoroutine after UpdateTrackIOTiming = not ok, want ok")
	}
}

// TestActivityRegistryStateChangeIsWallClock verifies that the monotonic
// nanos written by hot-path WaitEvent / Update paths are converted back
// into a wall-clock RFC3339Nano timestamp by Snapshot — runtimeshim.Nanotime
// returns monotonic-since-runtime-start, so a naive formatNanos would
// emit a year like 1970/2001 in the wire output of pg_stat_activity.
//
// Regression for M0107-0008 loop 5 (activity-registry Nanotime wiring).
func TestActivityRegistryStateChangeIsWallClock(t *testing.T) {
	r := NewActivityRegistry(16)
	r.Register(&Backend{PID: "1", State: "active"})
	pn := int32(0)

	before := time.Now().UTC()
	r.WaitEventStart(pn, WaitTypeIO, WaitWALWrite)
	snap := r.Snapshot()
	after := time.Now().UTC()

	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	sc := snap[0].StateChange
	if sc == "" {
		t.Fatalf("StateChange is empty; mono→wall conversion failed")
	}
	got, err := time.Parse(time.RFC3339Nano, sc)
	if err != nil {
		t.Fatalf("StateChange %q not RFC3339Nano: %v", sc, err)
	}
	// Allow ±2s of slack (mono/wall epochs are captured separately).
	lo, hi := before.Add(-2*time.Second), after.Add(2*time.Second)
	if got.Before(lo) || got.After(hi) {
		t.Errorf("StateChange %v outside [%v, %v]", got, lo, hi)
	}

	// XactStart should round-trip through the same conversion.
	r.BeginTransaction("1")
	snap = r.Snapshot()
	if snap[0].XactStart == "" {
		t.Fatalf("XactStart empty after BeginTransaction")
	}
	xs, err := time.Parse(time.RFC3339Nano, snap[0].XactStart)
	if err != nil {
		t.Fatalf("XactStart %q not RFC3339Nano: %v", snap[0].XactStart, err)
	}
	if xs.Before(lo) || xs.After(time.Now().UTC().Add(2*time.Second)) {
		t.Errorf("XactStart %v out of plausible range", xs)
	}
}
