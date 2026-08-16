package xlog

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// TestSlotDecoderRunDrivesPluginThroughCommit pins the
// end-to-end M0008 contract: a logical slot decoder consumes
// records as they're appended to the WAL stream, dispatches
// them through Classify into the OutputPlugin, and advances the
// slot's ConfirmedFlushLSN at every commit so a restart resumes
// from the right LSN.
func TestSlotDecoderRunDrivesPluginThroughCommit(t *testing.T) {
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
	if _, err := slots.CreateLogical("logical1", "pgoutput", "appdb", 0); err != nil {
		t.Fatal(err)
	}

	plugin := newCapturePlugin()
	dec, err := NewSlotDecoder(slots, "logical1", w, walDir, 1<<16, plugin)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runErr := make(chan error, 1)
	go func() { runErr <- dec.Run(ctx) }()

	// Append: insert (xid 42), insert (xid 42), commit (xid 42).
	rel := storage.RelFileNode{DBOid: 1, RelOid: 16400}
	t1, _ := storage.NewHeapTuple(42, 0, []byte("alpha")).MarshalBinary()
	t2, _ := storage.NewHeapTuple(42, 0, []byte("beta")).MarshalBinary()
	if _, _, err := w.Append(EncodeHeapInsert(rel, 0, 1, t1)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Append(EncodeHeapInsert(rel, 0, 2, t2)); err != nil {
		t.Fatal(err)
	}
	_, commitEnd, err := w.Append(EncodeXactCommit(42))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(commitEnd); err != nil {
		t.Fatal(err)
	}

	// Wait for the plugin to observe the commit.
	if err := plugin.waitFor(2*time.Second, func() bool { return plugin.commitCount() >= 1 }); err != nil {
		select {
		case runErr := <-runErr:
			t.Fatalf("plugin commit not observed: %v (Run err=%v)", err, runErr)
		default:
			t.Fatalf("plugin commit not observed: %v", err)
		}
	}

	// Cancel the loop and confirm clean shutdown.
	cancel()
	select {
	case err := <-runErr:
		if err != nil && !isCancelOrClosed(err) {
			t.Errorf("Run returned %v want context.Canceled / io.EOF / ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run didn't return after cancel")
	}

	// Plugin saw the right xact frame.
	gotCalls := plugin.calls()
	wantCalls := []string{"Begin", "Change", "Change", "Commit"}
	if !reflect.DeepEqual(gotCalls, wantCalls) {
		t.Errorf("plugin calls=%v want %v", gotCalls, wantCalls)
	}

	// Slot's ConfirmedFlushLSN advanced to the commit record's
	// EndLSN — the M0008 restart anchor.
	got, err := slots.Get("logical1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfirmedFlushLSN != commitEnd {
		t.Errorf("ConfirmedFlushLSN=%d want %d (commit EndLSN)", got.ConfirmedFlushLSN, commitEnd)
	}
}

// TestSlotDecoderRejectsPhysicalSlot pins the type-check at
// construction: only logical slots are valid consumers of the
// decoder loop. Physical slots feed the streaming-replication
// path, not pgoutput.
func TestSlotDecoderRejectsPhysicalSlot(t *testing.T) {
	dir := t.TempDir()
	walDir := filepath.Join(dir, "pg_wal")
	w, _ := NewWriter(Config{WALDir: walDir, SegmentSize: 1 << 16})
	defer w.Close()
	slots, _ := OpenSlots(dir)
	if _, err := slots.Create("phys1", SlotPhysical, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSlotDecoder(slots, "phys1", w, walDir, 1<<16, newCapturePlugin()); err == nil {
		t.Errorf("NewSlotDecoder against a physical slot should fail")
	}
}

// capturePlugin is a thread-safe OutputPlugin that records the
// call sequence. Used by the slot-decoder test to observe the
// async loop without races on the recordingPlugin from
// reorder_test.go (which is sequential-test-only).
type capturePlugin struct {
	mu     sync.Mutex
	cs     []string
	commit int
}

func newCapturePlugin() *capturePlugin { return &capturePlugin{} }

func (p *capturePlugin) Begin(_ storage.TransactionID, _ uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cs = append(p.cs, "Begin")
	return nil
}
func (p *capturePlugin) Change(_ Change) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cs = append(p.cs, "Change")
	return nil
}
func (p *capturePlugin) Commit(_ storage.TransactionID, _ uint64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cs = append(p.cs, "Commit")
	p.commit++
	return nil
}

func (p *capturePlugin) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.cs...)
}

func (p *capturePlugin) commitCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.commit
}

func (p *capturePlugin) waitFor(timeout time.Duration, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return nil
		}
		if time.Now().After(deadline) {
			return context.DeadlineExceeded
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func isCancelOrClosed(err error) bool {
	return err == nil ||
		err == context.Canceled ||
		err == context.DeadlineExceeded ||
		err == ErrClosed ||
		err.Error() == "EOF"
}
