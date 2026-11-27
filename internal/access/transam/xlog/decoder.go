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
package xlog

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
	// dbOid filters logical messages by their database (SetDatabase).
	dbOid uint32
	// skipThrough is start_decoding_at (SetSkipThrough): a transaction
	// whose commit record ends at or before it, and a non-transactional
	// message at or before it, has already been delivered and is not sent
	// again (SnapBuildXactNeedsSkip). M0149-0057c.
	skipThrough uint64
	// writeLocation, when set, is told the LSN each plugin callback's
	// output belongs to (SetWriteLocationHook).
	writeLocation func(lsn uint64)
	// onSent, when set, is told each transaction handed to the plugin: its
	// commit record's end and the restart point that decodes it again.
	onSent func(commitEnd, restart uint64)
	// filterByOrigin, when set, reports whether a transaction from the
	// given replication origin is skipped (SetOriginFilter).
	filterByOrigin func(origin uint16) bool
	// streamLimit / streamImmediate configure streaming of in-progress
	// transactions (SetStreaming); streamErr carries a stream callback's
	// failure out of ApplyChange to Classify. M0149-0063a.
	streamLimit     int64
	streamImmediate bool
	streamErr       error
}

// SetOriginFilter sets the output plugin's filter_by_origin_cb: a committed
// transaction whose commit record names an origin the filter rejects is
// forgotten instead of sent (DecodeCommit → FilterByOrigin →
// ReorderBufferForget). pgoutput's origin = none skips every transaction
// with a non-zero origin. M0149-0062a.
func (d *Decoder) SetOriginFilter(fn func(origin uint16) bool) { d.filterByOrigin = fn }

// SetSentHook sets the callback told, for each transaction sent, its commit
// end and its restart point (where decoding must begin to send it again).
func (d *Decoder) SetSentHook(fn func(commitEnd, restart uint64)) { d.onSent = fn }

// SetWriteLocationHook sets the callback told, before each Begin / Change /
// Commit, the position that output goes out at — logical decoding's
// ctx->write_location: the transaction's first change for Begin
// (begin_cb_wrapper: txn->first_lsn), the change's own for a change, the
// commit record's end for Commit (commit_cb_wrapper: txn->end_lsn). A
// subscriber advances its received position from these, and confirms it when
// it has nothing pending; stamping every message with the commit's end made
// it confirm a transaction it had only begun to apply, and a restart then
// skipped it. M0149-0057c.
func (d *Decoder) SetWriteLocationHook(fn func(lsn uint64)) { d.writeLocation = fn }

func (d *Decoder) at(lsn uint64) {
	if d.writeLocation != nil && lsn != 0 {
		d.writeLocation(lsn)
	}
}

// SetRecordStart tells the decoder the 0-based start of the record about to
// be classified (the end of the previous one).
func (d *Decoder) SetRecordStart(lsn uint64) { d.buf.curStart = lsn }

// SafeRestartLSN is the latest point a decoding restart may begin at after
// the record ending at end: end itself when no transaction is in flight,
// else the first record of the oldest in-flight one — restart_lsn, which
// PG keeps at the oldest running transaction (SnapBuildProcessRunningXacts).
func (d *Decoder) SafeRestartLSN(end uint64) uint64 {
	if lsn, ok := d.buf.OldestRestartLSN(); ok {
		return lsn
	}
	return end
}

// SetSkipThrough makes the decoder drop what ends at or before lsn — the
// later of the client's START_REPLICATION point and the slot's
// confirmed_flush_lsn (CreateDecodingContext's start_decoding_at). A
// reconnecting subscriber must not receive a transaction it already applied.
func (d *Decoder) SetSkipThrough(lsn uint64) { d.skipThrough = lsn }

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
	if d.streamLimit > 0 && d.streamErr == nil {
		d.streamErr = d.checkMemoryLimit()
	}
}

// takeStreamErr returns and clears a stream callback failure.
func (d *Decoder) takeStreamErr() error {
	err := d.streamErr
	d.streamErr = nil
	return err
}

// ApplyCommit drains `xid`'s queue through the plugin in
// Begin → Change* → Commit order. commitLSN is propagated to
// Begin and Commit. Returns ErrNoPlugin when no plugin is
// configured. Unknown xids are a no-op (an empty xact at commit
// time means the decoder filtered every change — e.g. catalog-
// only xact); returns nil in that case so the upstream classifier
// loop doesn't need to track which xids made it into the buffer.
func (d *Decoder) ApplyCommit(xid storage.TransactionID, commitLSN uint64) error {
	return d.ApplyCommitAt(xid, 0, commitLSN)
}

// CommitStartAware is implemented by an output plugin that needs the commit
// record's start LSN: pgoutput's Begin final_lsn and Commit commit_lsn are
// the record's start (decode.c DecodeCommit: buf->origptr), its end_lsn the
// record's end. M0149-0061b.
type CommitStartAware interface {
	SetCommitStart(lsn uint64)
}

// ApplyCommitAt is ApplyCommit with the commit record's start LSN (0 when
// unknown: the plugin then uses commitLSN, the record's end, for both).
func (d *Decoder) ApplyCommitAt(xid storage.TransactionID, startLSN, commitLSN uint64) error {
	return d.ApplyCommitOriginAt(xid, startLSN, commitLSN, 0)
}

// ApplyCommitOriginAt is ApplyCommitAt for a commit record naming its
// replication origin (XLogRecGetOrigin). M0149-0062a.
func (d *Decoder) ApplyCommitOriginAt(xid storage.TransactionID, startLSN, commitLSN uint64, origin uint16) error {
	if origin != 0 && d.filterByOrigin != nil && d.filterByOrigin(origin) {
		d.buf.Commit(xid) // ReorderBufferForget
		return nil
	}
	if ca, ok := d.plugin.(CommitStartAware); ok {
		ca.SetCommitStart(startLSN)
	}
	restart, _ := d.buf.RestartLSNOf(xid)
	streamed := false
	if t, tok := d.buf.txns[xid]; tok {
		streamed = t.streamed
	}
	changes, beginLSN, ok := d.buf.Commit(xid)
	if !ok {
		return nil
	}
	if streamed {
		// Part of the transaction already went out in a stream: finish it
		// as one (stream_commit_cb). M0149-0063a.
		if d.onSent != nil {
			d.onSent(commitLSN, restart)
		}
		if sp, sok := d.plugin.(StreamingPlugin); sok {
			return d.commitStreamed(sp, xid, changes, commitLSN)
		}
	}
	if d.skipThrough != 0 && commitLSN <= d.skipThrough {
		return nil
	}
	if d.plugin == nil {
		return ErrNoPlugin
	}
	if d.onSent != nil {
		d.onSent(commitLSN, restart)
	}
	d.at(beginLSN)
	if err := d.plugin.Begin(xid, commitLSN); err != nil {
		return fmt.Errorf("wal: decoder Begin(xid=%d): %w", xid, err)
	}
	for _, c := range changes {
		d.at(c.LSN)
		if c.Kind == ChangeMessage {
			// A transactional logical message, in WAL order with the
			// row changes (ReorderBufferApplyMessage).
			if mp, ok := d.plugin.(MessagePlugin); ok {
				if err := mp.LogicalMessage(xid, c.LSN, true, c.MsgPrefix, c.MsgContent); err != nil {
					return fmt.Errorf("wal: decoder LogicalMessage(xid=%d, lsn=%d): %w", xid, c.LSN, err)
				}
			}
			continue
		}
		if err := d.plugin.Change(c); err != nil {
			return fmt.Errorf("wal: decoder Change(xid=%d, lsn=%d): %w", xid, c.LSN, err)
		}
	}
	d.at(commitLSN)
	if err := d.plugin.Commit(xid, commitLSN); err != nil {
		return fmt.Errorf("wal: decoder Commit(xid=%d): %w", xid, err)
	}
	return nil
}

// ApplyAbort drops `xid`'s queued changes. The plugin is not
// invoked — aborted xacts never reach the wire.
func (d *Decoder) ApplyAbort(xid storage.TransactionID) {
	if err := d.ApplyAbortAt(xid, 0); err != nil && d.streamErr == nil {
		d.streamErr = err
	}
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
