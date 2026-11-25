package xlog

import (
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestEvaluateRecoveryStopLSNAndImmediate pins the shared
// recoveryStopsBefore/After verdict a standby's replay and archive recovery
// both use: an LSN target stops at the first record whose ReadRecPtr reaches
// it, after it when inclusive and before it otherwise; 'immediate' stops
// after the record that makes recovery consistent and never before.
// M0149-0023b.
func TestEvaluateRecoveryStopLSNAndImmediate(t *testing.T) {
	framed, err := EncodeXactCommitPGWithRelsAt(1000, storage.TransactionID(900), nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{StartLSN: 0x3000101, EndLSN: 0x3000140}
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(framed); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	recs, err := ReadSegmentRecords(dir, 0, 1<<20)
	if err != nil || len(recs) == 0 {
		t.Fatalf("read back: %v (%d records)", err, len(recs))
	}
	rec.XLog = recs[len(recs)-1].XLog

	if ev, hit := EvaluateRecoveryStop(RecoveryStop{Set: true, LSN: 0x3000100, Inclusive: true}, rec, false); !hit || ev.Kind != StopAtLSN || !ev.After || ev.LSN != 0x3000100 {
		t.Errorf("inclusive LSN: %+v hit=%v", ev, hit)
	}
	if ev, hit := EvaluateRecoveryStop(RecoveryStop{Set: true, LSN: 0x3000100}, rec, false); !hit || ev.After {
		t.Errorf("exclusive LSN: %+v hit=%v; want stop before", ev, hit)
	}
	if _, hit := EvaluateRecoveryStop(RecoveryStop{Set: true, LSN: 0x3000200, Inclusive: true}, rec, false); hit {
		t.Error("LSN past the record: hit")
	}
	if _, hit := EvaluateRecoveryStop(RecoveryStop{Immediate: true}, rec, false); hit {
		t.Error("immediate before consistency: hit")
	}
	if ev, hit := EvaluateRecoveryStop(RecoveryStop{Immediate: true}, rec, true); !hit || !ev.After || ev.Kind != StopAtImmediate {
		t.Errorf("immediate at consistency: %+v hit=%v", ev, hit)
	}
	if ev, hit := EvaluateRecoveryStop(RecoveryStop{XidSet: true, Xid: 900, Inclusive: true}, rec, false); !hit || ev.Xid != 900 || !ev.IsCommit ||
		ev.Result().StopMessage() == "" {
		t.Errorf("xid: %+v hit=%v", ev, hit)
	}
}
