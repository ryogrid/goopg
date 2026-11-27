package executor

// applyworker_stream.go — applying streamed transactions (worker.c
// apply_handle_stream_start / _stop / _commit / _abort, streaming = on).
// M0149-0063b.
//
// A publisher streaming a large in-progress transaction sends it in
// segments: stream start (xid), the changes — each carrying the
// (sub)transaction xid after its kind byte — and stream stop; other
// transactions may be sent between segments. The subscriber spools every
// change of a segment under the stream's xid (PG writes them to a file,
// stream_write_change; goopg keeps them in memory) and, at stream commit,
// replays the spool as one transaction (apply_spooled_messages). A stream
// abort discards the spool — the whole of it for the top-level xid, or a
// subtransaction's changes from its first one on (subxact_info_read /
// the file truncation in stream_abort_internal).

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/storage"
)

// spooledChange is one spooled stream change: the (sub)transaction that
// made it and the message without its xid.
type spooledChange struct {
	xid storage.TransactionID
	raw []byte
}

func (w *ApplyWorker) spoolStreamChange(m *xlog.DecodedMessage) error {
	if m.Raw == nil {
		return fmt.Errorf("applyworker: streamed %q message without its raw bytes", m.Kind)
	}
	if w.streams == nil {
		w.streams = map[storage.TransactionID][]spooledChange{}
	}
	w.streams[w.streamXid] = append(w.streams[w.streamXid], spooledChange{xid: m.XID, raw: m.Raw})
	return nil
}

// applyStreamControl handles stream start, stop and abort.
func (w *ApplyWorker) applyStreamControl(m *xlog.DecodedMessage) error {
	switch m.Kind {
	case 'S':
		if w.inStream {
			return fmt.Errorf("applyworker: duplicate STREAM START message")
		}
		if w.inXact {
			return fmt.Errorf("applyworker: STREAM START inside a transaction")
		}
		if m.FirstSegment {
			// The first segment starts the transaction's spool afresh
			// (stream_open_file with first_segment).
			if w.streams == nil {
				w.streams = map[storage.TransactionID][]spooledChange{}
			}
			w.streams[m.XID] = nil
		}
		w.inStream, w.streamXid = true, m.XID
	case 'E':
		if !w.inStream {
			return fmt.Errorf("applyworker: STREAM STOP without STREAM START")
		}
		w.inStream, w.streamXid = false, 0
	case 'A':
		if w.inStream {
			return fmt.Errorf("applyworker: STREAM ABORT message without STREAM STOP")
		}
		spool := w.streams[m.XID]
		if m.SubXID == m.XID || m.SubXID == 0 {
			delete(w.streams, m.XID)
			return nil
		}
		// A subtransaction abort drops that subtransaction's changes —
		// from its first spooled change on, as PG truncates the spool file
		// at the subxact's first offset.
		for i, c := range spool {
			if c.xid == m.SubXID {
				w.streams[m.XID] = spool[:i]
				break
			}
		}
	}
	return nil
}

// applyStreamCommit replays a streamed transaction's spool as one
// transaction (apply_handle_stream_commit → apply_spooled_messages →
// apply_handle_commit_internal).
func (w *ApplyWorker) applyStreamCommit(m *xlog.DecodedMessage) error {
	if w.inStream {
		return fmt.Errorf("applyworker: STREAM COMMIT message without STREAM STOP")
	}
	spool := w.streams[m.XID]
	delete(w.streams, m.XID)
	if err := w.applyBegin(&xlog.DecodedMessage{Kind: 'B', XID: m.XID, CommitLSN: m.CommitLSN, CommitTime: m.CommitTime}); err != nil {
		return err
	}
	for _, c := range spool {
		dm, err := xlog.DecodeMessage(c.raw)
		if err != nil {
			return fmt.Errorf("applyworker: spooled stream change: %w", err)
		}
		if _, err := w.ApplyMessage(dm); err != nil {
			return err
		}
	}
	return w.applyCommit(&xlog.DecodedMessage{Kind: 'C', CommitLSN: m.CommitLSN, EndLSN: m.EndLSN, CommitTime: m.CommitTime})
}

// ResetStreams forgets every partly received streamed transaction: a
// worker that restarts or reconnects receives them again from their first
// segment (PG removes the worker's stream files when it exits).
func (w *ApplyWorker) ResetStreams() {
	w.inStream, w.streamXid, w.streams = false, 0, nil
}
