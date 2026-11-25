package xlog

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestSlotDecoderResumesFromConfirmedPosition pins M0149-0040f: a logical
// slot stores PG's 0-based positions (confirmed_flush_lsn = the commit's
// EndRecPtr), and a decoder reopened from the slot — after a subscriber
// confirmed that position, or a later 0-based one such as a keepalive's
// walEnd — starts exactly at the next record instead of one byte into it
// ("invalid record header: padding bytes nonzero" before the fix).
func TestSlotDecoderResumesFromConfirmedPosition(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, err := NewWriter(Config{WALDir: walDir, SegmentSize: 1 << 16})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	slots, err := OpenSlots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slots.CreateLogical("resume", "pgoutput", "appdb", 0); err != nil {
		t.Fatal(err)
	}
	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	appendTxn := func(xid storage.TransactionID, payload string) uint64 {
		t.Helper()
		tup, _ := storage.NewHeapTuple(xid, 0, []byte(payload)).MarshalBinary()
		if _, _, err := w.Append(EncodeHeapInsert(rel, 0, 1, tup)); err != nil {
			t.Fatal(err)
		}
		_, end, err := w.Append(EncodeXactCommit(xid))
		if err != nil {
			t.Fatal(err)
		}
		if err := w.FlushUpTo(end); err != nil {
			t.Fatal(err)
		}
		return end
	}
	runUntilCommits := func(n int) *capturePlugin {
		t.Helper()
		plugin := newCapturePlugin()
		dec, err := NewSlotDecoder(slots, "resume", w, walDir, 1<<16, plugin)
		if err != nil {
			t.Fatal(err)
		}
		defer dec.Close()
		ctx, cancel := context.WithCancel(context.Background())
		runErr := make(chan error, 1)
		go func() { runErr <- dec.Run(ctx) }()
		if err := plugin.waitFor(2*time.Second, func() bool { return plugin.commitCount() >= n }); err != nil {
			cancel()
			t.Fatalf("decoder did not reach %d commit(s): %v (Run err=%v)", n, err, <-runErr)
		}
		cancel()
		if err := <-runErr; err != nil && !isCancelOrClosed(err) {
			t.Fatalf("Run: %v", err)
		}
		return plugin
	}

	end1 := appendTxn(42, "first")
	runUntilCommits(1)
	got, _ := slots.Get("resume")
	if got.ConfirmedFlushLSN != end1 || got.RestartLSN != end1 {
		t.Fatalf("slot after commit 1: confirmed %d restart %d, want both %d (0-based EndRecPtr)", got.ConfirmedFlushLSN, got.RestartLSN, end1)
	}
	// A subscriber confirming the same 0-based position changes nothing.
	if err := slots.AdvanceConfirmedFlushLSN("resume", end1); err != nil {
		t.Fatal(err)
	}

	appendTxn(43, "second")
	plugin := runUntilCommits(1)
	if calls, want := plugin.calls(), []string{"Begin", "Change", "Commit"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("resumed decoder calls %v, want %v (only the second transaction)", calls, want)
	}
}
