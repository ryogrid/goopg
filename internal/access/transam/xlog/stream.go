package xlog

// stream.go — streaming of in-progress transactions (ReorderBufferStreamTXN,
// reorderbuffer.c; logical decoding's stream_*_cb). M0149-0063a.
//
// A decoder normally hands a transaction to the output plugin at its
// commit. When the subscriber asked for streaming (pgoutput "streaming"
// on / parallel) and the reorder buffer holds more than
// logical_decoding_work_mem, the largest top-level transaction is streamed
// instead: its queued changes go out between a stream start and a stream
// stop, the buffer forgets them, and the commit later sends whatever was
// queued since plus a stream commit (an abort sends a stream abort). With
// debug_logical_replication_streaming = immediate every change is streamed.
//
// PG spills a transaction to disk when it cannot stream; goopg keeps it in
// memory (ledgered).

import (
	"fmt"

	"github.com/goopg/goopg/internal/storage"
)

// StreamingPlugin is the output plugin's stream API (stream_start_cb,
// stream_stop_cb, stream_commit_cb, stream_abort_cb). The changes of a
// stream segment go through OutputPlugin.Change / MessagePlugin between
// StreamStart and StreamStop.
type StreamingPlugin interface {
	StreamStart(xid storage.TransactionID, firstSegment bool) error
	StreamStop() error
	StreamCommit(xid storage.TransactionID, commitLSN uint64) error
	StreamAbort(xid, subxid storage.TransactionID, abortLSN uint64) error
}

// changeSize is ReorderBufferChangeSize: the change's tuple bytes plus the
// fixed per-change overhead (sizeof(ReorderBufferChange)).
func changeSize(c Change) int64 {
	const reorderBufferChangeOverhead = 80
	return reorderBufferChangeOverhead + int64(len(c.OldTuple)+len(c.NewTuple)+len(c.MsgContent)+len(c.MsgPrefix)+4*len(c.Relids))
}

// largestTop is ReorderBufferLargestStreamableTopTXN: the in-flight
// transaction holding the most bytes, nil when none holds any.
func (rb *ReorderBuffer) largestTop() *txn {
	var best *txn
	for _, t := range rb.txns {
		if t.size > 0 && (best == nil || t.size > best.size || (t.size == best.size && t.xid < best.xid)) {
			best = t
		}
	}
	return best
}

// takeStreamable removes and returns t's queued changes for a stream
// segment, marking t streamed. A trailing DELETE is held back: the next
// change may be the INSERT half of the same UPDATE (foldChanges), which
// must leave in one message.
func (rb *ReorderBuffer) takeStreamable(t *txn) []Change {
	n := len(t.changes)
	if n > 0 && t.changes[n-1].Kind == ChangeDelete {
		n--
	}
	if n == 0 {
		return nil
	}
	out := foldChanges(t.changes[:n])
	var sz int64
	for _, c := range t.changes[:n] {
		sz += changeSize(c)
	}
	rest := append([]Change(nil), t.changes[n:]...)
	t.changes = rest
	t.size -= sz
	rb.size -= sz
	t.streamed = true
	return out
}

// SetStreaming enables streaming of in-progress transactions to a
// StreamingPlugin: limitBytes is logical_decoding_work_mem, immediate is
// debug_logical_replication_streaming = immediate. limitBytes <= 0 turns
// streaming off. M0149-0063a.
func (d *Decoder) SetStreaming(limitBytes int64, immediate bool) {
	d.streamLimit = limitBytes
	d.streamImmediate = immediate
}

// checkMemoryLimit is ReorderBufferCheckMemoryLimit for a streaming
// decoder: while the buffer is over the limit (or holds anything, under
// immediate), stream the largest transaction. It streams nothing while a
// transaction could still be one start_decoding_at skips
// (ReorderBufferCanStartStreaming) or while an origin filter is set (goopg
// learns a transaction's origin only at its commit).
func (d *Decoder) checkMemoryLimit() error {
	sp, ok := d.plugin.(StreamingPlugin)
	if !ok || d.streamLimit <= 0 || d.filterByOrigin != nil {
		return nil
	}
	if d.skipThrough != 0 && d.buf.curStart < d.skipThrough {
		return nil
	}
	for d.buf.size >= d.streamLimit || (d.streamImmediate && d.buf.size > 0) {
		t := d.buf.largestTop()
		if t == nil {
			return nil
		}
		changes := d.buf.takeStreamable(t)
		if len(changes) == 0 {
			return nil // only a held-back DELETE is left
		}
		if err := d.streamSegment(sp, t.xid, changes, !t.streamedBefore()); err != nil {
			return err
		}
	}
	return nil
}

// streamedBefore reports whether an earlier segment of t already went out
// (the one takeStreamable just took marks streamed itself).
func (t *txn) streamedBefore() bool { return t.segments > 0 }

// streamSegment sends one stream segment of xid (ReorderBufferStreamTXN):
// stream start, the changes, stream stop.
func (d *Decoder) streamSegment(sp StreamingPlugin, xid storage.TransactionID, changes []Change, first bool) error {
	if len(changes) > 0 {
		d.at(changes[0].LSN)
	}
	if err := sp.StreamStart(xid, first); err != nil {
		return fmt.Errorf("wal: decoder StreamStart(xid=%d): %w", xid, err)
	}
	if t, ok := d.buf.txns[xid]; ok {
		t.segments++
	}
	if err := d.sendChanges(xid, changes); err != nil {
		return err
	}
	if err := sp.StreamStop(); err != nil {
		return fmt.Errorf("wal: decoder StreamStop(xid=%d): %w", xid, err)
	}
	return nil
}

// sendChanges hands changes to the plugin in WAL order, logical messages
// through MessagePlugin (ReorderBufferApplyMessage).
func (d *Decoder) sendChanges(xid storage.TransactionID, changes []Change) error {
	for _, c := range changes {
		d.at(c.LSN)
		if c.Kind == ChangeMessage {
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
	return nil
}

// commitStreamed finishes a transaction part of which was streamed
// (ReorderBufferReplay → stream_commit_cb): the changes queued since its
// last segment go out as a final segment, then the stream commit.
func (d *Decoder) commitStreamed(sp StreamingPlugin, xid storage.TransactionID, changes []Change, commitLSN uint64) error {
	if len(changes) > 0 {
		d.at(changes[0].LSN)
		if err := sp.StreamStart(xid, false); err != nil {
			return fmt.Errorf("wal: decoder StreamStart(xid=%d): %w", xid, err)
		}
		if err := d.sendChanges(xid, changes); err != nil {
			return err
		}
		if err := sp.StreamStop(); err != nil {
			return fmt.Errorf("wal: decoder StreamStop(xid=%d): %w", xid, err)
		}
	}
	d.at(commitLSN)
	if err := sp.StreamCommit(xid, commitLSN); err != nil {
		return fmt.Errorf("wal: decoder StreamCommit(xid=%d): %w", xid, err)
	}
	return nil
}

// ApplyAbortAt drops xid's queued changes; a transaction part of which
// was streamed gets a stream abort (ReorderBufferAbort →
// stream_abort_cb), so the subscriber discards what it received.
func (d *Decoder) ApplyAbortAt(xid storage.TransactionID, abortLSN uint64) error {
	t, ok := d.buf.txns[xid]
	streamed := ok && t.streamed
	d.buf.Abort(xid)
	if !streamed {
		return nil
	}
	sp, ok := d.plugin.(StreamingPlugin)
	if !ok {
		return nil
	}
	d.at(abortLSN)
	if err := sp.StreamAbort(xid, xid, abortLSN); err != nil {
		return fmt.Errorf("wal: decoder StreamAbort(xid=%d): %w", xid, err)
	}
	return nil
}
