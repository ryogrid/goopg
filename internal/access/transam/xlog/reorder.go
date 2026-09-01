// Reorder buffer for the M0008 logical-decoding pipeline.
//
// Upstream's `reorderbuffer.c` accumulates per-transaction change
// events as they're decoded from WAL and only emits them once the
// transaction commits (and never emits them at all if the
// transaction aborts). The output plugin therefore sees changes in
// commit order, never sees uncommitted state, and has a stable
// per-xact frame around each batch.
//
// This file delivers the in-memory data structure that backs that
// behaviour. It's deliberately decoupled from WAL classification
// so the decoder loop in subsequent loops can drive it from any
// source — a real WAL iterator, a test harness, or a replay-only
// path. The contract:
//
//   - `Append(xid, change)` queues `change` under `xid`. First
//     append for an xid records the begin LSN.
//   - `Commit(xid, commitLSN)` returns the queued changes in
//     append order and drops the xact's entry. The caller drives
//     the output plugin with the returned slice.
//   - `Abort(xid)` drops the xact's queue without producing
//     output. Aborted transactions never reach the wire.
//   - `Active()` reports the in-flight xact count for observability.
//
// See docs/design/0008-0001-logical-decoding-pipeline.md.
package xlog

import "github.com/goopg/goopg/internal/storage"

// ChangeKind discriminates one row-level change event. Mirrors the
// `B` / `C` / `R` framing that pgoutput emits — those are
// transaction-frame messages and don't appear here. Only the
// per-row kinds are reorderable.
type ChangeKind int

const (
	// ChangeInsert is a row-level INSERT. NewTuple holds the
	// inserted row's on-disk image (heap-tuple bytes, exactly
	// what `RecordKindHeapInsert` carries). OldTuple is empty.
	ChangeInsert ChangeKind = iota
	// ChangeUpdate is a row-level UPDATE. OldTuple holds the
	// pre-update image (key columns under DEFAULT replica
	// identity, the full row under FULL); NewTuple holds the
	// post-update image. v0's executor emits UPDATE as a paired
	// HeapDelete + HeapInsert, so this variant is only reached
	// once the decoder pairs them; until that lands, callers
	// emit Insert + Delete separately and let the apply worker
	// reconcile.
	ChangeUpdate
	// ChangeDelete is a row-level DELETE. OldTuple holds the
	// deleted row's identifying columns (per replica identity).
	ChangeDelete
)

// Change is one row-level event in flight through the reorder
// buffer. LSN is the WAL byte position the change was decoded at —
// used by the output plugin for order verification and by the
// apply worker to advance `confirmed_flush_lsn`.
type Change struct {
	Kind     ChangeKind
	LSN      uint64
	Rel      storage.RelFileNode
	Block    storage.BlockNumber
	LineSlot uint16
	OldTuple []byte
	NewTuple []byte
}

// txn is the per-xact accumulator inside the reorder buffer.
// Changes are kept in append order — the only ordering rule v0
// honours is "commit-time order matches WAL-arrival order within
// a single xact". Cross-xact ordering is handled by the decoder
// loop, which calls Commit in commit-record order.
type txn struct {
	xid      storage.TransactionID
	beginLSN uint64
	changes  []Change
}

// ReorderBuffer holds in-progress transactions until commit.
// Single-process, single-decoder for v0 — no goroutine safety
// because the decoder loop is sequential. If logical decoding
// ever moves to a goroutine pool, wrap this in a mutex.
type ReorderBuffer struct {
	txns map[storage.TransactionID]*txn
}

// NewReorderBuffer constructs an empty buffer.
func NewReorderBuffer() *ReorderBuffer {
	return &ReorderBuffer{txns: make(map[storage.TransactionID]*txn)}
}

// Append queues `c` under `xid`. The first append for an xid
// records `c.LSN` as the xact's begin LSN — the LSN the output
// plugin uses for the `B` (begin) message.
//
// xid==InvalidTransactionID is rejected: every change must belong
// to a transaction. v0's executor always wraps DML in a
// per-statement transaction, so this is enforceable.
func (rb *ReorderBuffer) Append(xid storage.TransactionID, c Change) {
	if xid == storage.InvalidTransactionID {
		// Defensive: drop changes with no xact rather than
		// queueing under xid=0 and conflating distinct xacts.
		return
	}
	t, ok := rb.txns[xid]
	if !ok {
		t = &txn{xid: xid, beginLSN: c.LSN}
		rb.txns[xid] = t
	}
	t.changes = append(t.changes, c)
}

// Commit returns the changes queued under `xid` (in append order)
// and removes the xact's entry. The second return value is the
// xact's begin LSN, useful for the output plugin's `B` message.
// `commitLSN` is recorded by the caller's output plugin path; the
// reorder buffer doesn't store it.
//
// Consecutive (ChangeDelete, ChangeInsert) pairs for the same
// relation are folded into a single ChangeUpdate so the output
// plugin emits a `U` message instead of separate `D` + `I` —
// matching the upstream pgoutput behaviour for UPDATE (M0094-0002).
//
// Commit on an xid the buffer has never seen returns nil/0/false —
// distinguishes "empty xact" (which v0 doesn't model, since the
// first Append sets up the entry) from "unknown xid".
func (rb *ReorderBuffer) Commit(xid storage.TransactionID) ([]Change, uint64, bool) {
	t, ok := rb.txns[xid]
	if !ok {
		return nil, 0, false
	}
	delete(rb.txns, xid)
	return foldChanges(t.changes), t.beginLSN, true
}

// foldChanges collapses consecutive (Delete, Insert) pairs on the
// same relation into a single Update change. This converts the
// executor's "DELETE old + INSERT new" UPDATE representation into
// the unified ChangeUpdate that pgoutput's `U` message requires.
func foldChanges(in []Change) []Change {
	if len(in) < 2 {
		return in
	}
	// review/260831 XL-38: the common case is that nothing folds — a
	// transaction of plain INSERTs or UPDATEs — and the copy was paid anyway.
	// Find the first foldable pair before allocating; when there is none, the
	// input is already the answer.
	first := -1
	for i := 0; i+1 < len(in); i++ {
		if in[i].Kind == ChangeDelete && in[i+1].Kind == ChangeInsert && in[i].Rel == in[i+1].Rel {
			first = i
			break
		}
	}
	if first < 0 {
		return in
	}
	out := make([]Change, 0, len(in))
	out = append(out, in[:first]...)
	for i := first; i < len(in); i++ {
		if i+1 < len(in) &&
			in[i].Kind == ChangeDelete &&
			in[i+1].Kind == ChangeInsert &&
			in[i].Rel == in[i+1].Rel {
			out = append(out, Change{
				Kind:     ChangeUpdate,
				LSN:      in[i+1].LSN,
				Rel:      in[i].Rel,
				Block:    in[i+1].Block,
				LineSlot: in[i+1].LineSlot,
				OldTuple: in[i].OldTuple,
				NewTuple: in[i+1].NewTuple,
			})
			i++ // consume the Insert too
			continue
		}
		out = append(out, in[i])
	}
	return out
}

// Abort drops the queued changes for `xid` without emitting them.
// Returns true when the xact was tracked, false when `xid` was
// unknown.
func (rb *ReorderBuffer) Abort(xid storage.TransactionID) bool {
	if _, ok := rb.txns[xid]; !ok {
		return false
	}
	delete(rb.txns, xid)
	return true
}

// Active reports the number of in-flight transactions. Used by
// observability to surface long-running uncommitted state on the
// publisher.
func (rb *ReorderBuffer) Active() int {
	return len(rb.txns)
}

// OldestBeginLSN returns the smallest begin LSN across all
// in-flight xacts, or zero when the buffer is empty. The
// publisher's catalog-xmin tracker uses this to know which WAL
// position must remain readable for the slot — releasing WAL
// past `OldestBeginLSN` would lose data the decoder still needs.
func (rb *ReorderBuffer) OldestBeginLSN() uint64 {
	var min uint64
	first := true
	for _, t := range rb.txns {
		if first || t.beginLSN < min {
			min = t.beginLSN
			first = false
		}
	}
	return min
}
