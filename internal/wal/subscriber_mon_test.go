package wal

import (
	"testing"
	"time"
)

// TestSubscribersRegisterUnregisterRoundTrip pins the basic
// lifecycle: a registered subscriber shows up in Snapshot; an
// unregistered one disappears; double-unregister is a no-op.
func TestSubscribersRegisterUnregisterRoundTrip(t *testing.T) {
	s := NewSubscribers()
	a := s.Register(SubscriberState{SubID: 1, SubName: "sub_alpha", PID: 100})
	b := s.Register(SubscriberState{SubID: 2, SubName: "sub_beta", PID: 200})
	if got := s.Snapshot(); len(got) != 2 {
		t.Fatalf("snapshot len=%d, want 2", len(got))
	}
	s.Unregister(a)
	got := s.Snapshot()
	if len(got) != 1 || got[0].PID != 200 {
		t.Fatalf("after unregister: %+v", got)
	}
	s.Unregister(b)
	if got := s.Snapshot(); len(got) != 0 {
		t.Fatalf("after both unregister: %+v", got)
	}
	s.Unregister(a) // double-unregister is a no-op
}

// TestSubscriberWorkerTypeDefaults: an empty WorkerType registers
// as "leader" so callers don't have to spell it out for the common
// case.
func TestSubscriberWorkerTypeDefaults(t *testing.T) {
	s := NewSubscribers()
	s.Register(SubscriberState{SubName: "sub1", PID: 1})
	got := s.Snapshot()[0]
	if got.WorkerType != SubscriberWorkerLeader {
		t.Errorf("WorkerType=%q want leader", got.WorkerType)
	}
}

// TestSubscriberLSNMonotonic: AdvanceReceivedLSN and the LSN
// half of MarkMessage cannot rewind progress.
func TestSubscriberLSNMonotonic(t *testing.T) {
	s := NewSubscribers()
	sub := s.Register(SubscriberState{SubName: "sub1", PID: 1})
	sub.AdvanceReceivedLSN(100)
	sub.AdvanceReceivedLSN(50) // stale; must not regress
	if got := s.Snapshot()[0].ReceivedLSN; got != 100 {
		t.Errorf("ReceivedLSN=%d want 100", got)
	}
	sub.MarkMessage(time.Now(), 200)
	sub.MarkMessage(time.Now(), 150) // stale endLSN; must not regress
	if got := s.Snapshot()[0].LatestEndLSN; got != 200 {
		t.Errorf("LatestEndLSN=%d want 200", got)
	}
}

// TestSubscribersSnapshotSorted pins the
// (subname, worker_type, relid, pid) sort key. Mirrors the
// stable-order contract pg_stat_subscription readers depend on.
func TestSubscribersSnapshotSorted(t *testing.T) {
	s := NewSubscribers()
	s.Register(SubscriberState{SubName: "sub_b", WorkerType: SubscriberWorkerLeader, PID: 1})
	s.Register(SubscriberState{SubName: "sub_a", WorkerType: SubscriberWorkerTablesync, RelOID: 30, PID: 5})
	s.Register(SubscriberState{SubName: "sub_a", WorkerType: SubscriberWorkerLeader, PID: 2})
	s.Register(SubscriberState{SubName: "sub_a", WorkerType: SubscriberWorkerTablesync, RelOID: 10, PID: 3})
	got := s.Snapshot()
	want := []struct {
		name string
		wt   SubscriberWorkerType
		oid  uint32
		pid  uint32
	}{
		{"sub_a", SubscriberWorkerLeader, 0, 2},
		{"sub_a", SubscriberWorkerTablesync, 10, 3},
		{"sub_a", SubscriberWorkerTablesync, 30, 5},
		{"sub_b", SubscriberWorkerLeader, 0, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].SubName != w.name || got[i].WorkerType != w.wt ||
			got[i].RelOID != w.oid || got[i].PID != w.pid {
			t.Errorf("row %d = (%s, %s, %d, %d), want (%s, %s, %d, %d)",
				i, got[i].SubName, got[i].WorkerType, got[i].RelOID, got[i].PID,
				w.name, w.wt, w.oid, w.pid)
		}
	}
}

// TestSubscriberMarkMessageUpdatesTimestamps: MarkMessage moves
// last_msg_send_time and last_msg_receipt_time forward, and bumps
// latest_end_time alongside latest_end_lsn when a non-zero endLSN
// is supplied.
func TestSubscriberMarkMessageUpdatesTimestamps(t *testing.T) {
	s := NewSubscribers()
	sub := s.Register(SubscriberState{SubName: "sub1", PID: 1})
	t0 := time.Now()
	sub.MarkMessage(t0, 0)
	got := s.Snapshot()[0]
	if !got.LastMsgReceiptTime.Equal(t0) || !got.LastMsgSendTime.Equal(t0) {
		t.Errorf("first MarkMessage didn't stamp timestamps: %+v", got)
	}
	if !got.LatestEndTime.IsZero() {
		t.Errorf("LatestEndTime should stay zero when endLSN==0; got %v", got.LatestEndTime)
	}

	t1 := t0.Add(time.Second)
	sub.MarkMessage(t1, 0xFEED)
	got = s.Snapshot()[0]
	if !got.LastMsgReceiptTime.Equal(t1) {
		t.Errorf("second MarkMessage didn't advance receipt time: %v", got.LastMsgReceiptTime)
	}
	if got.LatestEndLSN != 0xFEED {
		t.Errorf("LatestEndLSN=%x want 0xFEED", got.LatestEndLSN)
	}
	if !got.LatestEndTime.Equal(t1) {
		t.Errorf("LatestEndTime=%v want %v", got.LatestEndTime, t1)
	}
}
