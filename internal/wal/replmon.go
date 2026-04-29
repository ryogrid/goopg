// Replication monitoring registries: process-wide state the
// pg_stat_replication and pg_stat_wal_receiver virtual views render
// into rows.
//
// Upstream backs these views with shared-memory structures
// (WalSnd / WalRcv) populated by the live processes. v0 keeps it
// in-process: a `Senders` registry that walsender goroutines
// register/unregister against, plus a `Receivers` registry holding
// at most one entry (the standby's walreceiver). Views read the
// registries' snapshots; LSN advance hooks let the live workers
// publish progress without taking an external lock.
//
// See docs/design/0005-0003-replication-observability.md.
package wal

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// SenderState is the public, copy-on-read view of one walsender's
// progress. Mirrors upstream's `WalSnd` shape minus fields v0 doesn't
// emit (sync_state — no synchronous replication; reply_time — no
// per-message timestamp tracking yet).
type SenderState struct {
	// PID is the per-connection identifier the goopg server assigns.
	// 0 means "no PID was supplied at registration time" — useful for
	// tests; production wiring always passes the connection's PID.
	PID uint32
	// ApplicationName is the standby's reported application name from
	// its StartupMessage, or "" when the parameter was absent.
	ApplicationName string
	// SlotName is the named slot the walsender acquired, or "" when
	// the connection ran without one (allowed but discouraged).
	SlotName string
	// State is the current walsender lifecycle phase. v0 only has
	// "streaming"; "startup" / "backup" / "stopping" are reserved
	// for upstream parity and future lifecycle wiring.
	State string
	// ClientAddr is the standby's TCP host:port. Empty when the
	// walsender ran against a non-TCP transport (test fixtures).
	ClientAddr string
	// SentLSN is the last byte position the sender wrote into its
	// outbound CopyData stream. Strictly monotonic per walsender.
	SentLSN uint64
	// WriteLSN, FlushLSN, ReplayLSN are the most recent values from
	// the standby's status replies. Zero until the standby has sent
	// at least one update.
	WriteLSN  uint64
	FlushLSN  uint64
	ReplayLSN uint64
	// BackendStart is the wall-clock instant the walsender registered
	// (i.e. began serving the standby).
	BackendStart time.Time
}

// Senders is the process-wide registry of active walsenders.
// Thread-safe for concurrent register/unregister/advance/Snapshot.
type Senders struct {
	mu      sync.RWMutex
	entries map[*Sender]struct{}
}

// NewSenders constructs an empty registry.
func NewSenders() *Senders {
	return &Senders{entries: make(map[*Sender]struct{})}
}

// Register installs a Sender into the registry and returns the
// handle. Walsender goroutines call this on entry; Unregister on
// exit. The returned *Sender exposes the LSN-advance hooks the
// goroutine uses to publish progress.
func (s *Senders) Register(initial SenderState) *Sender {
	if initial.BackendStart.IsZero() {
		initial.BackendStart = time.Now()
	}
	if initial.State == "" {
		initial.State = "streaming"
	}
	se := &Sender{
		pid:             initial.PID,
		applicationName: initial.ApplicationName,
		slotName:        initial.SlotName,
		state:           initial.State,
		clientAddr:      initial.ClientAddr,
		backendStart:    initial.BackendStart,
	}
	se.sentLSN.Store(initial.SentLSN)
	se.writeLSN.Store(initial.WriteLSN)
	se.flushLSN.Store(initial.FlushLSN)
	se.replayLSN.Store(initial.ReplayLSN)
	s.mu.Lock()
	s.entries[se] = struct{}{}
	s.mu.Unlock()
	return se
}

// Unregister removes a previously-registered sender. Safe to call
// twice; subsequent calls are no-ops.
func (s *Senders) Unregister(se *Sender) {
	if se == nil {
		return
	}
	s.mu.Lock()
	delete(s.entries, se)
	s.mu.Unlock()
}

// Snapshot returns a sorted copy of every active sender's state.
// Sort key is (slot_name, pid) so the row order is stable across
// repeated SELECTs for the same set of senders. Atomic loads only —
// the snapshot is a coherent point-in-time view per sender, but two
// senders may be observed at slightly different instants.
func (s *Senders) Snapshot() []SenderState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SenderState, 0, len(s.entries))
	for se := range s.entries {
		out = append(out, se.snapshot())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SlotName != out[j].SlotName {
			return out[i].SlotName < out[j].SlotName
		}
		return out[i].PID < out[j].PID
	})
	return out
}

// Sender is a per-walsender progress handle. Its fields update via
// atomic stores so the Snapshot path doesn't need to acquire the
// registry mutex per entry.
type Sender struct {
	pid             uint32
	applicationName string
	slotName        string
	state           string
	clientAddr      string
	backendStart    time.Time

	sentLSN   atomic.Uint64
	writeLSN  atomic.Uint64
	flushLSN  atomic.Uint64
	replayLSN atomic.Uint64
}

// SetSentLSN advances the "bytes shipped" counter. Walsender calls
// this after a successful WriteCopyData. Idempotent: out-of-order
// calls are clamped to monotonic-forward via max().
func (se *Sender) SetSentLSN(lsn uint64) {
	for {
		cur := se.sentLSN.Load()
		if lsn <= cur {
			return
		}
		if se.sentLSN.CompareAndSwap(cur, lsn) {
			return
		}
	}
}

// ApplyStandbyStatus updates the three standby-reported LSNs from a
// single standby-status reply. Each is monotonic-clamped
// independently.
func (se *Sender) ApplyStandbyStatus(write, flush, replay uint64) {
	advanceLSN(&se.writeLSN, write)
	advanceLSN(&se.flushLSN, flush)
	advanceLSN(&se.replayLSN, replay)
}

func advanceLSN(p *atomic.Uint64, lsn uint64) {
	for {
		cur := p.Load()
		if lsn <= cur {
			return
		}
		if p.CompareAndSwap(cur, lsn) {
			return
		}
	}
}

func (se *Sender) snapshot() SenderState {
	return SenderState{
		PID:             se.pid,
		ApplicationName: se.applicationName,
		SlotName:        se.slotName,
		State:           se.state,
		ClientAddr:      se.clientAddr,
		BackendStart:    se.backendStart,
		SentLSN:         se.sentLSN.Load(),
		WriteLSN:        se.writeLSN.Load(),
		FlushLSN:        se.flushLSN.Load(),
		ReplayLSN:       se.replayLSN.Load(),
	}
}

// ReceiverState is the public, copy-on-read view of the standby's
// walreceiver state. Single-row (a process is at most one standby).
// Mirrors upstream's WalRcv shape minus fields v0 doesn't track
// (slot_name on receiver side requires the upstream slot-name being
// echoed back, which our handshake doesn't yet do; sender_host is
// taken from the configured PrimaryAddr).
type ReceiverState struct {
	// Status is one of "streaming", "stopped", "starting". v0 emits
	// "streaming" while a receiver is registered and "stopped"
	// otherwise (the row is then absent rather than reporting
	// "stopped" — the view is empty when no receiver runs).
	Status string
	// PID is the goroutine identity the receiver assigned (best-
	// effort; v0 uses os.Getpid since walreceivers are not
	// cross-process workers).
	PID uint32
	// ReceiveStartLSN is the LSN the receiver asked the primary to
	// resume from at handshake time.
	ReceiveStartLSN uint64
	// ReceivedLSN is the latest end-LSN of a record successfully
	// Append'd into the local WAL writer.
	ReceivedLSN uint64
	// LastMsgSendTime / LastMsgReceiptTime are the wall-clock
	// instants of the last keepalive / WAL-data frame the standby
	// observed. v0 currently sets both to LastMsgReceiptTime.
	LastMsgSendTime    time.Time
	LastMsgReceiptTime time.Time
	// SenderHost is the upstream address the receiver dialed
	// (from PrimaryAddr).
	SenderHost string
	// SlotName is the replication slot the receiver requested, or
	// "" when running slot-less (allowed for one-shot transports).
	SlotName string
	// Conninfo is the libpq-style connection string used. Stored
	// verbatim (no password masking yet) — v0 conninfo is host+port
	// only so this is just informational.
	Conninfo string
}

// Receivers is a single-slot registry holding at most one walreceiver
// entry. Modelled as a slice for symmetry with Senders so the
// catalog wiring is uniform; v0 enforces len <= 1.
type Receivers struct {
	mu      sync.RWMutex
	current *Receiver
}

// NewReceivers constructs an empty registry.
func NewReceivers() *Receivers {
	return &Receivers{}
}

// Register installs the receiver entry, replacing any previous one
// (a reconnect cycle drops the old entry on Unregister and installs
// a fresh one here). Returns a handle the live walreceiver uses to
// publish LSN progress.
func (r *Receivers) Register(initial ReceiverState) *Receiver {
	if initial.Status == "" {
		initial.Status = "streaming"
	}
	if initial.LastMsgReceiptTime.IsZero() {
		initial.LastMsgReceiptTime = time.Now()
	}
	if initial.LastMsgSendTime.IsZero() {
		initial.LastMsgSendTime = initial.LastMsgReceiptTime
	}
	rec := &Receiver{
		status:          initial.Status,
		pid:             initial.PID,
		receiveStartLSN: initial.ReceiveStartLSN,
		senderHost:      initial.SenderHost,
		slotName:        initial.SlotName,
		conninfo:        initial.Conninfo,
	}
	rec.receivedLSN.Store(initial.ReceivedLSN)
	rec.lastMsgSendUnixNano.Store(initial.LastMsgSendTime.UnixNano())
	rec.lastMsgReceiptUnixNano.Store(initial.LastMsgReceiptTime.UnixNano())
	r.mu.Lock()
	r.current = rec
	r.mu.Unlock()
	return rec
}

// Unregister clears the registry IFF the supplied entry is still the
// current one. A reconnect that registered a fresh entry first won't
// be clobbered by the old goroutine's late Unregister.
func (r *Receivers) Unregister(rec *Receiver) {
	if rec == nil {
		return
	}
	r.mu.Lock()
	if r.current == rec {
		r.current = nil
	}
	r.mu.Unlock()
}

// Snapshot returns the current receiver state, or false when no
// receiver is registered.
func (r *Receivers) Snapshot() (ReceiverState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.current == nil {
		return ReceiverState{}, false
	}
	return r.current.snapshot(), true
}

// Receiver is a per-walreceiver progress handle. Atomic-backed for
// the same lock-free Snapshot read pattern as Sender.
type Receiver struct {
	status          string
	pid             uint32
	receiveStartLSN uint64
	senderHost      string
	slotName        string
	conninfo        string

	receivedLSN            atomic.Uint64
	lastMsgSendUnixNano    atomic.Int64
	lastMsgReceiptUnixNano atomic.Int64
}

// SetReceivedLSN advances the "bytes durably appended into local
// WAL" counter. Idempotent monotonic-forward.
func (rec *Receiver) SetReceivedLSN(lsn uint64) {
	advanceLSN(&rec.receivedLSN, lsn)
}

// MarkMessageReceived records that a WAL-data or keepalive frame
// just arrived. Touches both timestamp fields for now (v0 doesn't
// distinguish send vs. receipt because the protocol layer stamps
// both at the same point).
func (rec *Receiver) MarkMessageReceived(now time.Time) {
	ns := now.UnixNano()
	rec.lastMsgReceiptUnixNano.Store(ns)
	rec.lastMsgSendUnixNano.Store(ns)
}

func (rec *Receiver) snapshot() ReceiverState {
	return ReceiverState{
		Status:             rec.status,
		PID:                rec.pid,
		ReceiveStartLSN:    rec.receiveStartLSN,
		ReceivedLSN:        rec.receivedLSN.Load(),
		LastMsgSendTime:    time.Unix(0, rec.lastMsgSendUnixNano.Load()),
		LastMsgReceiptTime: time.Unix(0, rec.lastMsgReceiptUnixNano.Load()),
		SenderHost:         rec.senderHost,
		SlotName:           rec.slotName,
		Conninfo:           rec.conninfo,
	}
}
