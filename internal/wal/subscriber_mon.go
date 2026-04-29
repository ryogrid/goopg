// Subscriber-side replication monitoring registry: process-wide
// state the `pg_stat_subscription` virtual view renders into rows.
//
// Mirrors the shape of the publisher-side `Senders` / `Receivers`
// registries in replmon.go: a `Subscribers` collection that the
// LogicalReceiver and tablesync workers register/unregister
// against, with atomic-backed LSN counters so the view's Snapshot
// path is lock-free per entry.
//
// Upstream PG backs `pg_stat_subscription` with the
// `LogicalRepWorker` shared-memory array populated by each apply
// worker (one "leader" per subscription, plus zero or more
// "tablesync" workers, plus parallel-apply workers in newer
// versions). v0 mirrors the same row shape but keeps the registry
// in-process — there is no shared memory in goopg yet.
//
// See docs/design/0008-0005-logical-replication-observability.md.

package wal

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SubscriberWorkerType is the upstream `worker_type` enum surfaced
// in `pg_stat_subscription`. v0 implements two: "leader" (the
// streaming apply worker for one subscription) and "tablesync"
// (one worker per relation in the initial-COPY phase). Parallel-
// apply ("parallel") is reserved for a future loop.
type SubscriberWorkerType string

const (
	SubscriberWorkerLeader    SubscriberWorkerType = "leader"
	SubscriberWorkerTablesync SubscriberWorkerType = "tablesync"
)

// SubscriberState is the public, copy-on-read view of one
// subscriber worker's progress. Mirrors upstream's
// `LogicalRepWorker` columns surfaced through
// `pg_stat_subscription`.
type SubscriberState struct {
	// SubID is the subscription's catalog OID, the upstream
	// `pg_subscription.oid`.
	SubID uint32
	// SubName is the subscription's name. Carried alongside SubID
	// so the view can render `subname` without a join.
	SubName string
	// WorkerType selects the row's role.
	WorkerType SubscriberWorkerType
	// PID is the per-worker identifier; for v0 this is the
	// goroutine's identity (best-effort) or a counter assigned by
	// the registry. Tests pass 0 to leave it unset.
	PID uint32
	// LeaderPID is non-zero only for tablesync workers — points
	// back at the subscription's leader apply worker.
	LeaderPID uint32
	// RelOID is non-zero only for tablesync workers — names the
	// relation being initially copied.
	RelOID uint32
	// ReceivedLSN is the latest LSN the worker has consumed from
	// the publisher's stream (highest commit LSN observed so far
	// for the leader; tablesync end-LSN once known for tablesync
	// workers).
	ReceivedLSN uint64
	// LastMsgSendTime / LastMsgReceiptTime are the wall-clock
	// instants of the last keepalive / WAL-data frame the worker
	// processed.
	LastMsgSendTime    time.Time
	LastMsgReceiptTime time.Time
	// LatestEndLSN / LatestEndTime mirror upstream's
	// `latest_end_lsn` / `latest_end_time`: the last (lsn, time)
	// pair the publisher reported as the end of WAL.
	LatestEndLSN  uint64
	LatestEndTime time.Time
}

// Subscribers is the process-wide registry of active subscriber
// workers. Thread-safe for concurrent register/unregister/advance/
// Snapshot.
type Subscribers struct {
	mu      sync.RWMutex
	entries map[*Subscriber]struct{}
}

// NewSubscribers constructs an empty registry.
func NewSubscribers() *Subscribers {
	return &Subscribers{entries: make(map[*Subscriber]struct{})}
}

// Register installs a Subscriber into the registry and returns
// the handle. Apply / tablesync goroutines call this on entry;
// Unregister on exit. Defaults: WorkerType ""→"leader",
// LastMsgReceiptTime zero → time.Now (and SendTime mirrors it).
func (s *Subscribers) Register(initial SubscriberState) *Subscriber {
	if initial.WorkerType == "" {
		initial.WorkerType = SubscriberWorkerLeader
	}
	if initial.LastMsgReceiptTime.IsZero() {
		initial.LastMsgReceiptTime = time.Now()
	}
	if initial.LastMsgSendTime.IsZero() {
		initial.LastMsgSendTime = initial.LastMsgReceiptTime
	}
	sub := &Subscriber{
		subID:      initial.SubID,
		subName:    initial.SubName,
		workerType: initial.WorkerType,
		pid:        initial.PID,
		leaderPID:  initial.LeaderPID,
		relOID:     initial.RelOID,
	}
	sub.receivedLSN.Store(initial.ReceivedLSN)
	sub.latestEndLSN.Store(initial.LatestEndLSN)
	sub.lastMsgSendUnixNano.Store(initial.LastMsgSendTime.UnixNano())
	sub.lastMsgReceiptUnixNano.Store(initial.LastMsgReceiptTime.UnixNano())
	if !initial.LatestEndTime.IsZero() {
		sub.latestEndUnixNano.Store(initial.LatestEndTime.UnixNano())
	}
	s.mu.Lock()
	s.entries[sub] = struct{}{}
	s.mu.Unlock()
	return sub
}

// Unregister removes a previously-registered worker. Safe to call
// twice; subsequent calls are no-ops.
func (s *Subscribers) Unregister(sub *Subscriber) {
	if sub == nil {
		return
	}
	s.mu.Lock()
	delete(s.entries, sub)
	s.mu.Unlock()
}

// Snapshot returns a sorted copy of every active subscriber's
// state. Sort key is (subname, workerType, relOID, pid) so the
// row order is stable across repeated SELECTs even when multiple
// tablesync workers race to register against one subscription.
func (s *Subscribers) Snapshot() []SubscriberState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SubscriberState, 0, len(s.entries))
	for sub := range s.entries {
		out = append(out, sub.snapshot())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubName != out[j].SubName {
			return out[i].SubName < out[j].SubName
		}
		if out[i].WorkerType != out[j].WorkerType {
			return out[i].WorkerType < out[j].WorkerType
		}
		if out[i].RelOID != out[j].RelOID {
			return out[i].RelOID < out[j].RelOID
		}
		return out[i].PID < out[j].PID
	})
	return out
}

// Subscriber is a per-worker progress handle. Identity fields are
// set once at Register; counters update via atomic stores so the
// Snapshot path stays lock-free per entry.
type Subscriber struct {
	subID      uint32
	subName    string
	workerType SubscriberWorkerType
	pid        uint32
	leaderPID  uint32
	relOID     uint32

	receivedLSN  atomic.Uint64
	latestEndLSN atomic.Uint64

	lastMsgSendUnixNano    atomic.Int64
	lastMsgReceiptUnixNano atomic.Int64
	latestEndUnixNano      atomic.Int64
}

// AdvanceReceivedLSN bumps the per-worker received-LSN counter.
// Monotonic-clamped so an out-of-order call cannot rewind
// progress.
func (sub *Subscriber) AdvanceReceivedLSN(lsn uint64) {
	advanceLSN(&sub.receivedLSN, lsn)
}

// MarkMessage records that a frame from the publisher arrived.
// `endLSN` reports the publisher's claimed end-of-WAL at that
// moment (zero leaves the field untouched). Idempotent.
func (sub *Subscriber) MarkMessage(now time.Time, endLSN uint64) {
	ns := now.UnixNano()
	sub.lastMsgReceiptUnixNano.Store(ns)
	sub.lastMsgSendUnixNano.Store(ns)
	if endLSN != 0 {
		advanceLSN(&sub.latestEndLSN, endLSN)
		sub.latestEndUnixNano.Store(ns)
	}
}

func (sub *Subscriber) snapshot() SubscriberState {
	state := SubscriberState{
		SubID:              sub.subID,
		SubName:            sub.subName,
		WorkerType:         sub.workerType,
		PID:                sub.pid,
		LeaderPID:          sub.leaderPID,
		RelOID:             sub.relOID,
		ReceivedLSN:        sub.receivedLSN.Load(),
		LastMsgSendTime:    time.Unix(0, sub.lastMsgSendUnixNano.Load()),
		LastMsgReceiptTime: time.Unix(0, sub.lastMsgReceiptUnixNano.Load()),
		LatestEndLSN:       sub.latestEndLSN.Load(),
	}
	// LatestEndTime stays zero (Go's zero time, not Unix epoch)
	// until a MarkMessage with a non-zero endLSN actually fires —
	// the view renders it as blank in that case.
	if ns := sub.latestEndUnixNano.Load(); ns != 0 {
		state.LatestEndTime = time.Unix(0, ns)
	}
	return state
}
