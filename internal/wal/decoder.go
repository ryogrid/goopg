// Decoder loop skeleton for the M0008 logical-decoding pipeline.
//
// The decoder sits between the reorder buffer and the output
// plugin. It receives row-level change events keyed by xid (from
// some upstream classifier), accumulates them into the reorder
// buffer, and on a commit event drives the output plugin with the
// xact's queued changes in append order. Aborts drop the queue.
//
// What this file IS:
//   - The data-flow contract between the WAL classifier (future
//     loop), the reorder buffer (sibling file), and the output
//     plugin (M0008 / 0008-0002).
//   - Pure in-process orchestration — no I/O, no goroutines, no
//     locking.
//
// What this file IS NOT (yet):
//   - A WAL classifier that turns RecordKindHeapInsert /
//     HeapDelete / HeapVacuum / Checkpoint into change events.
//     The current WAL records carry no commit/abort markers, so
//     classification needs RecordKindXactCommit / XactAbort
//     records first — tracked as the next M0008 / 0008-0001
//     follow-up loop.
//   - A wire encoder — that's the OutputPlugin implementer's
//     job. pgoutput lands in 0008-0002.
//
// See docs/design/0008-0001-logical-decoding-pipeline.md.
package wal

import (
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// OutputPlugin is the contract a logical-decoding output plugin
// implements. The decoder calls Begin → Change* → Commit per
// committed transaction, and never calls anything for aborted
// transactions. Errors abort the streaming session — the apply
// worker's restart-from-confirmed-flush-LSN behaviour resumes
// from the last successfully committed xact.
//
// pgoutput will be the first implementation (0008-0002). The
// interface is small enough that a `txt`-flavoured debugger plugin
// (handy for testing) can also implement it without much effort.
type OutputPlugin interface {
	// Begin emits the per-xact prologue — pgoutput's `B`
	// message. xid is the transaction id; commitLSN is the WAL
	// position of the commit record so the subscriber can
	// compute lag.
	Begin(xid storage.TransactionID, commitLSN uint64) error
	// Change emits one row-level event. The decoder calls this
	// in append order within a single xact, after Begin and
	// before Commit.
	Change(c Change) error
	// Commit emits the per-xact epilogue — pgoutput's `C`
	// message. The subscriber's apply path commits its
	// transaction here.
	Commit(xid storage.TransactionID, commitLSN uint64) error
}

// ErrNoPlugin is returned by Decoder methods when no plugin has
// been configured. Defensive: a Decoder without a plugin can't do
// anything useful, and silently dropping output would be worse
// than failing loudly.
var ErrNoPlugin = errors.New("wal: decoder has no output plugin")

// Decoder orchestrates the reorder-buffer → output-plugin flow.
// One Decoder per active logical slot; not goroutine-safe.
type Decoder struct {
	buf    *ReorderBuffer
	plugin OutputPlugin
}

// NewDecoder constructs a decoder backed by `plugin`. The plugin
// may be nil for tests that exercise only the reorder-buffer
// shape; in that case the Apply* methods return ErrNoPlugin.
func NewDecoder(plugin OutputPlugin) *Decoder {
	return &Decoder{
		buf:    NewReorderBuffer(),
		plugin: plugin,
	}
}

// ApplyChange queues a row-level change under `xid`. Doesn't call
// the plugin — output happens at commit time so uncommitted state
// never reaches the wire.
func (d *Decoder) ApplyChange(xid storage.TransactionID, c Change) {
	d.buf.Append(xid, c)
}

// ApplyCommit drains `xid`'s queue through the plugin in
// Begin → Change* → Commit order. commitLSN is propagated to
// Begin and Commit. Returns ErrNoPlugin when no plugin is
// configured. Unknown xids are a no-op (an empty xact at commit
// time means the decoder filtered every change — e.g. catalog-
// only xact); returns nil in that case so the upstream classifier
// loop doesn't need to track which xids made it into the buffer.
func (d *Decoder) ApplyCommit(xid storage.TransactionID, commitLSN uint64) error {
	changes, _, ok := d.buf.Commit(xid)
	if !ok {
		return nil
	}
	if d.plugin == nil {
		return ErrNoPlugin
	}
	if err := d.plugin.Begin(xid, commitLSN); err != nil {
		return fmt.Errorf("wal: decoder Begin(xid=%d): %w", xid, err)
	}
	for _, c := range changes {
		if err := d.plugin.Change(c); err != nil {
			return fmt.Errorf("wal: decoder Change(xid=%d, lsn=%d): %w", xid, c.LSN, err)
		}
	}
	if err := d.plugin.Commit(xid, commitLSN); err != nil {
		return fmt.Errorf("wal: decoder Commit(xid=%d): %w", xid, err)
	}
	return nil
}

// ApplyAbort drops `xid`'s queued changes. The plugin is not
// invoked — aborted xacts never reach the wire.
func (d *Decoder) ApplyAbort(xid storage.TransactionID) {
	d.buf.Abort(xid)
}

// Active reports the number of in-flight transactions in the
// underlying reorder buffer. Surfaced to observability so an
// operator can see uncommitted-on-publisher xacts piling up.
func (d *Decoder) Active() int {
	return d.buf.Active()
}

// OldestBeginLSN exposes the reorder buffer's oldest-begin
// position. The publisher's catalog-xmin tracker uses this to
// know which WAL must remain readable.
func (d *Decoder) OldestBeginLSN() uint64 {
	return d.buf.OldestBeginLSN()
}
