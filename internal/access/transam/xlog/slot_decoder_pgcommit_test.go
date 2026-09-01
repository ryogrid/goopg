package xlog

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestSlotDecoderAdvancesOnPGFormatCommit is the review/260831-2 XL-4 guard.
// SlotDecoder.Run advanced ConfirmedFlushLSN only when the record's NATIVE
// payload started with RecordKindXactCommit. Every commit the server writes is
// PG-format (initdb/open.go's xact-marker hook calls EncodeXactCommitPG), and
// those dispatch through classifyDecodedXLog → Decoder.ApplyCommit instead, so
// the slot's restart anchor never moved: after a restart the slot replayed
// every transaction since it was created, re-delivering already-acked commits
// to the subscriber.
func TestSlotDecoderAdvancesOnPGFormatCommit(t *testing.T) {
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
	if _, err := slots.CreateLogical("logical_pg", "pgoutput", "appdb", 0); err != nil {
		t.Fatal(err)
	}

	plugin := newCapturePlugin()
	dec, err := NewSlotDecoder(slots, "logical_pg", w, walDir, 1<<16, plugin)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- dec.Run(ctx) }()

	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	tup, _ := storage.NewHeapTuple(77, 0, []byte("gamma")).MarshalBinary()
	if _, _, err := w.Append(EncodeHeapInsert(rel, 0, 1, tup)); err != nil {
		t.Fatal(err)
	}
	commitPayload, err := EncodeXactCommitPG(77, false)
	if err != nil {
		t.Fatal(err)
	}
	_, commitEnd, err := w.Append(commitPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(commitEnd); err != nil {
		t.Fatal(err)
	}

	if err := plugin.waitFor(2*time.Second, func() bool { return plugin.commitCount() >= 1 }); err != nil {
		select {
		case rerr := <-runErr:
			t.Fatalf("plugin commit not observed: %v (Run err=%v)", err, rerr)
		default:
			t.Fatalf("plugin commit not observed: %v", err)
		}
	}
	cancel()
	select {
	case err := <-runErr:
		if err != nil && !isCancelOrClosed(err) {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run didn't return after cancel")
	}

	got, err := slots.Get("logical_pg")
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfirmedFlushLSN != commitEnd {
		t.Errorf("ConfirmedFlushLSN=%d want %d (PG-format commit EndLSN); pre-fix it stayed at the slot's creation LSN",
			got.ConfirmedFlushLSN, commitEnd)
	}
}
