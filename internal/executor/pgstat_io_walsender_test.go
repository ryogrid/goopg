package executor

import "testing"

// TestWALSenderReceiverIOCounts: the walsender's WAL reads and the
// walreceiver's WAL writes land in pg_stat_io's walsender/wal/normal reads
// and walreceiver/wal/normal writes, one op per call with its bytes, as
// WALRead and XLogWalRcvWrite count them (001_stream_rep waits on
// `sum(reads) > 0 … backend_type = 'walsender' AND object = 'wal'`).
// M0149-0077d.
func TestWALSenderReceiverIOCounts(t *testing.T) {
	r0, rb0 := ioCumulativeGet(ioBktWalSender, ioObjWal, ioCtxNormal, ioOpRead)
	w0, wb0 := ioCumulativeGet(ioBktWalReceiver, ioObjWal, ioCtxNormal, ioOpWrite)
	CountWALSenderRead(8192)
	CountWALSenderRead(0) // nothing read: no op
	CountWALReceiverWrite(100)
	if r, rb := ioCumulativeGet(ioBktWalSender, ioObjWal, ioCtxNormal, ioOpRead); r != r0+1 || rb != rb0+8192 {
		t.Errorf("walsender reads %d/%d, want %d/%d", r, rb, r0+1, rb0+8192)
	}
	if w, wb := ioCumulativeGet(ioBktWalReceiver, ioObjWal, ioCtxNormal, ioOpWrite); w != w0+1 || wb != wb0+100 {
		t.Errorf("walreceiver writes %d/%d, want %d/%d", w, wb, w0+1, wb0+100)
	}
	if !ioTracksOp(ioBktWalSender, ioObjWal, ioCtxNormal, ioOpRead) || !ioTracksOp(ioBktWalReceiver, ioObjWal, ioCtxNormal, ioOpWrite) {
		t.Error("pg_stat_io does not track these cells")
	}
}
