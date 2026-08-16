package xlog

import (
	"sync"
	"testing"
	"time"
)

// TestSendersRegisterUnregisterRoundTrip pins the basic lifecycle:
// a registered sender shows up in Snapshot; an unregistered one
// disappears.
func TestSendersRegisterUnregisterRoundTrip(t *testing.T) {
	s := NewSenders()
	a := s.Register(SenderState{PID: 1, SlotName: "alpha"})
	b := s.Register(SenderState{PID: 2, SlotName: "beta"})
	if got := s.Snapshot(); len(got) != 2 {
		t.Fatalf("snapshot len=%d, want 2", len(got))
	}
	s.Unregister(a)
	got := s.Snapshot()
	if len(got) != 1 || got[0].PID != 2 {
		t.Fatalf("after unregister: %+v", got)
	}
	s.Unregister(b)
	if got := s.Snapshot(); len(got) != 0 {
		t.Fatalf("after both unregister: %+v", got)
	}
	// Double-unregister is a no-op (no panic).
	s.Unregister(a)
}

// TestSenderLSNAdvanceMonotonic confirms SetSentLSN /
// ApplyStandbyStatus only advance forward — a stale standby reply
// can't push a flush_lsn backwards.
func TestSenderLSNAdvanceMonotonic(t *testing.T) {
	s := NewSenders()
	se := s.Register(SenderState{PID: 1})
	se.SetSentLSN(100)
	se.SetSentLSN(50)
	if got := s.Snapshot()[0].SentLSN; got != 100 {
		t.Errorf("sent_lsn = %d, want 100 (stale push must not regress)", got)
	}
	se.ApplyStandbyStatus(80, 70, 60)
	se.ApplyStandbyStatus(50, 50, 50) // stale: must not regress
	snap := s.Snapshot()[0]
	if snap.WriteLSN != 80 || snap.FlushLSN != 70 || snap.ReplayLSN != 60 {
		t.Errorf("standby LSNs regressed under stale push: %+v", snap)
	}
}

// TestSendersSnapshotSorted pins the (slot_name, pid) sort key —
// pg_stat_replication readers expect a stable row order across
// repeated SELECTs.
func TestSendersSnapshotSorted(t *testing.T) {
	s := NewSenders()
	s.Register(SenderState{PID: 5, SlotName: "zeta"})
	s.Register(SenderState{PID: 1, SlotName: "alpha"})
	s.Register(SenderState{PID: 3, SlotName: "alpha"})
	got := s.Snapshot()
	wantOrder := []struct {
		slot string
		pid  uint32
	}{
		{"alpha", 1},
		{"alpha", 3},
		{"zeta", 5},
	}
	for i, w := range wantOrder {
		if got[i].SlotName != w.slot || got[i].PID != w.pid {
			t.Errorf("row %d = (%s, %d), want (%s, %d)",
				i, got[i].SlotName, got[i].PID, w.slot, w.pid)
		}
	}
}

// TestReceiversSingleRow exercises the single-slot semantics: a
// fresh Register replaces the previous entry, an Unregister whose
// handle isn't current is a no-op (so a late goroutine can't drop
// the freshly-registered reconnected receiver).
func TestReceiversSingleRow(t *testing.T) {
	r := NewReceivers()
	if _, ok := r.Snapshot(); ok {
		t.Fatal("empty registry should have no snapshot")
	}
	a := r.Register(ReceiverState{PID: 1, SenderHost: "primary-a:5432"})
	st, ok := r.Snapshot()
	if !ok || st.PID != 1 {
		t.Fatalf("snapshot=%+v ok=%v", st, ok)
	}

	// Reconnect: register a new entry first, THEN the old goroutine
	// unregisters its stale handle. The stale unregister must not
	// blank out the new entry.
	b := r.Register(ReceiverState{PID: 2, SenderHost: "primary-a:5432"})
	r.Unregister(a)
	st, ok = r.Snapshot()
	if !ok || st.PID != 2 {
		t.Fatalf("after reconnect-unregister: snapshot=%+v ok=%v", st, ok)
	}

	r.Unregister(b)
	if _, ok := r.Snapshot(); ok {
		t.Fatal("after unregister current: snapshot should be absent")
	}
}

// TestReceiverProgressAdvances pins SetReceivedLSN +
// MarkMessageReceived: both update the snapshot, both are
// monotonic-forward (LSN) / latest-wins (timestamp).
func TestReceiverProgressAdvances(t *testing.T) {
	r := NewReceivers()
	rec := r.Register(ReceiverState{PID: 1})
	rec.SetReceivedLSN(100)
	rec.SetReceivedLSN(50) // stale
	if got := snapshotOrFail(t, r).ReceivedLSN; got != 100 {
		t.Errorf("received_lsn = %d, want 100", got)
	}
	t1 := time.Now()
	rec.MarkMessageReceived(t1)
	st := snapshotOrFail(t, r)
	if !st.LastMsgReceiptTime.Equal(t1) {
		t.Errorf("last_msg_receipt_time = %v, want %v",
			st.LastMsgReceiptTime, t1)
	}
}

// TestSendersConcurrentRegister is a stress check for the registry
// mutex — repeated Register/Unregister/Snapshot from many
// goroutines must not race or deadlock. Run with `go test -race`
// to catch a missing lock.
func TestSendersConcurrentRegister(t *testing.T) {
	s := NewSenders()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				h := s.Register(SenderState{PID: uint32(i*1000 + j)})
				h.SetSentLSN(uint64(j))
				_ = s.Snapshot()
				s.Unregister(h)
			}
		}(i)
	}
	wg.Wait()
	if got := s.Snapshot(); len(got) != 0 {
		t.Errorf("post-race snapshot has %d entries, want 0", len(got))
	}
}

func snapshotOrFail(t *testing.T, r *Receivers) ReceiverState {
	t.Helper()
	st, ok := r.Snapshot()
	if !ok {
		t.Fatal("expected non-empty receiver snapshot")
	}
	return st
}
