package wal

import "testing"

// TestReplayStartAtUsesControlFileRedo is the M0131-S20.2 guard.
//
// goopg used to derive its replay start by SCANNING the stream for the last
// record its own isCheckpointRecord recognised, then decoding the redo
// pointer out of that record's 88-byte CheckPoint struct. Upstream never
// searches: InitWalRecovery reads checkPoint.redo out of pg_control's
// checkPointCopy and hands it straight to PerformWalRecovery
// (postgres/src/backend/access/transam/xlogrecovery.c:597-707).
//
// The difference is invisible on WAL goopg wrote itself and decisive on WAL
// it did not: a PG-authored tail whose checkpoint record the scan fails to
// classify collapses to "replay everything from the first retained segment",
// which on a real cluster is both enormous and — once retention has removed
// the segments before it — not actually a consistent starting point.
func TestReplayStartAtUsesControlFileRedo(t *testing.T) {
	// Four records ending at 100/200/300/400, none of them a checkpoint the
	// scan can recognise — the PG-authored-WAL shape.
	recs := []Record{
		{StartLSN: 1, EndLSN: 100},
		{StartLSN: 100, EndLSN: 200},
		{StartLSN: 200, EndLSN: 300},
		{StartLSN: 300, EndLSN: 400},
	}

	if idx, _ := replayStart(recs); idx != 0 {
		t.Fatalf("precondition: the scan finds no checkpoint here, so it must "+
			"return 0 (replay everything); got %d — this test no longer "+
			"distinguishes the pointer from the scan", idx)
	}

	// redo lands inside record 2 (EndLSN 200 > 150): that record must be
	// REPLAYED, not skipped. Record LSNs are 1-based positions and redo is a
	// 0-based XLogRecPtr, so the ">" bias errs toward replaying one extra
	// record — idempotent, where skipping one is data loss.
	if idx, _ := replayStartAt(recs, 150); idx != 1 {
		t.Errorf("redo=150: start index %d, want 1 (the record redo points into)", idx)
	}
	// redo exactly at a record boundary starts at the next record.
	if idx, _ := replayStartAt(recs, 200); idx != 2 {
		t.Errorf("redo=200: start index %d, want 2", idx)
	}
	// redo past every record: nothing to replay, and no panic slicing.
	if idx, _ := replayStartAt(recs, 400); idx != len(recs) {
		t.Errorf("redo=400: start index %d, want %d (nothing past the redo point)", idx, len(recs))
	}
	// redo == 0 is the "no control file / fresh cluster" sentinel and must
	// leave the goopg-authored scan in charge, not mean "start at 0" by
	// accident of the comparison.
	if idx, _ := replayStartAt(recs, 0); idx != 0 {
		t.Errorf("redo=0: start index %d, want the scan's 0", idx)
	}
}

// TestReplayStartAtOverridesTheScan pins the precedence: with a control-file
// redo pointer the scan does NOT get to move the anchor, because the scan is
// a reconstruction of exactly the value the pointer already carries. Only the
// reported CheckpointLSN still comes from the scan — it is bookkeeping
// (ReplayStats, the startup xact-stamp pass), and zeroing it would make a
// recovered cluster look checkpoint-less to every reader of the stat.
func TestReplayStartAtOverridesTheScan(t *testing.T) {
	// Record 1 is a legacy 1-byte checkpoint marker, so the scan anchors
	// there; the control file says redo is later.
	recs := []Record{
		{StartLSN: 1, EndLSN: 100},
		{StartLSN: 100, EndLSN: 200, Payload: []byte{RecordKindCheckpoint}},
		{StartLSN: 200, EndLSN: 300},
		{StartLSN: 300, EndLSN: 400},
	}
	scanIdx, scanCkpt := replayStart(recs)
	if scanIdx != 1 || scanCkpt != 200 {
		t.Fatalf("precondition: scan anchor = (%d, %d), want (1, 200)", scanIdx, scanCkpt)
	}

	idx, ckpt := replayStartAt(recs, 250)
	if idx != 2 {
		t.Errorf("start index %d, want 2 — the control-file redo pointer wins over the scan", idx)
	}
	if ckpt != scanCkpt {
		t.Errorf("CheckpointLSN %d, want %d — the reported checkpoint LSN still "+
			"comes from the scan even when the anchor does not", ckpt, scanCkpt)
	}
}
