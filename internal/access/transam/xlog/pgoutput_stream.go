package xlog

// pgoutput_stream.go — pgoutput's streaming callbacks (pgoutput.c
// pgoutput_stream_start / _stop / _commit / _abort; proto.c
// logicalrep_write_stream_*). M0149-0063a.

import (
	"time"

	"github.com/goopg/goopg/internal/storage"
)

const (
	pgoStreamStart  = 'S'
	pgoStreamStop   = 'E'
	pgoStreamCommit = 'c'
	pgoStreamAbort  = 'A'
)

// SetStreaming sets pgoutput's "streaming" option: on streams large
// in-progress transactions, parallel also asks for the abort LSN / time in
// stream aborts (a parallel apply worker needs them).
func (p *PgOutput) SetStreaming(on, parallel bool) {
	p.streaming = on
	p.parallel = on && parallel
}

// writeChange writes a change-class message (R, Y, I, U, D, T, M): inside a
// stream segment the transaction's xid follows the type byte
// (logicalrep_write_* with a valid xid).
func (p *PgOutput) writeChange(buf []byte) (int, error) {
	if !p.inStream || len(buf) == 0 {
		return p.w.Write(buf)
	}
	out := make([]byte, 0, len(buf)+4)
	out = append(out, buf[0])
	out = appendUint32(out, uint32(p.streamXid))
	out = append(out, buf[1:]...)
	return p.w.Write(out)
}

// StreamStart is pgoutput_stream_start: 'S' xid first_segment.
func (p *PgOutput) StreamStart(xid storage.TransactionID, firstSegment bool) error {
	buf := make([]byte, 0, 6)
	buf = append(buf, pgoStreamStart)
	buf = appendUint32(buf, uint32(xid))
	first := byte(0)
	if firstSegment {
		first = 1
	}
	buf = append(buf, first)
	if _, err := p.w.Write(buf); err != nil {
		return err
	}
	if p.streamRel == nil {
		p.streamRel = map[storage.TransactionID]map[uint32]struct{}{}
	}
	set, ok := p.streamRel[xid]
	if !ok {
		set = map[uint32]struct{}{}
		p.streamRel[xid] = set
	}
	p.sessionRel, p.emittedRel = p.emittedRel, set
	p.inStream, p.streamXid = true, xid
	return nil
}

// StreamStop is pgoutput_stream_stop: 'E'.
func (p *PgOutput) StreamStop() error {
	if p.inStream {
		p.emittedRel, p.sessionRel = p.sessionRel, nil
		p.inStream, p.streamXid = false, 0
	}
	_, err := p.w.Write([]byte{pgoStreamStop})
	return err
}

// StreamCommit is pgoutput_stream_commit: 'c' xid flags commit_lsn end_lsn
// commit_time. The relations whose schema the transaction sent now count
// as sent for the session (cleanup_rel_sync_cache(xid, true)).
func (p *PgOutput) StreamCommit(xid storage.TransactionID, commitLSN uint64) error {
	buf := make([]byte, 0, 30)
	buf = append(buf, pgoStreamCommit)
	buf = appendUint32(buf, uint32(xid))
	buf = append(buf, 0) // flags
	buf = appendUint64(buf, p.finalLSN(commitLSN))
	buf = appendUint64(buf, commitLSN) // end_lsn
	buf = appendUint64(buf, pgoTimestamp(time.Now()))
	if _, err := p.w.Write(buf); err != nil {
		return err
	}
	for oid := range p.streamRel[xid] {
		p.emittedRel[oid] = struct{}{}
	}
	delete(p.streamRel, xid)
	return nil
}

// StreamAbort is pgoutput_stream_abort: 'A' xid subxid, plus abort_lsn and
// abort_time under streaming = parallel. An aborted top-level
// transaction's schema sends are forgotten (cleanup_rel_sync_cache(xid,
// false)).
func (p *PgOutput) StreamAbort(xid, subxid storage.TransactionID, abortLSN uint64) error {
	buf := make([]byte, 0, 25)
	buf = append(buf, pgoStreamAbort)
	buf = appendUint32(buf, uint32(xid))
	buf = appendUint32(buf, uint32(subxid))
	if p.parallel {
		buf = appendUint64(buf, abortLSN)
		buf = appendUint64(buf, pgoTimestamp(time.Now()))
	}
	if _, err := p.w.Write(buf); err != nil {
		return err
	}
	if xid == subxid {
		delete(p.streamRel, xid)
	}
	return nil
}
