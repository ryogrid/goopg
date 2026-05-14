package wal

import (
	"errors"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestReorderBufferCommitDrainsInOrder pins the basic contract:
// changes appended under one xid come back at commit time in the
// order they were appended.
func TestReorderBufferCommitDrainsInOrder(t *testing.T) {
	rb := NewReorderBuffer()
	rb.Append(7, Change{Kind: ChangeInsert, LSN: 100, NewTuple: []byte("a")})
	rb.Append(7, Change{Kind: ChangeInsert, LSN: 110, NewTuple: []byte("b")})
	rb.Append(7, Change{Kind: ChangeDelete, LSN: 120, OldTuple: []byte("c")})

	if got := rb.Active(); got != 1 {
		t.Errorf("Active=%d want 1", got)
	}

	got, beginLSN, ok := rb.Commit(7)
	if !ok {
		t.Fatal("Commit ok=false want true")
	}
	if beginLSN != 100 {
		t.Errorf("beginLSN=%d want 100", beginLSN)
	}
	want := []Change{
		{Kind: ChangeInsert, LSN: 100, NewTuple: []byte("a")},
		{Kind: ChangeInsert, LSN: 110, NewTuple: []byte("b")},
		{Kind: ChangeDelete, LSN: 120, OldTuple: []byte("c")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("changes=%+v want %+v", got, want)
	}
	if rb.Active() != 0 {
		t.Errorf("Active after commit=%d want 0", rb.Active())
	}
}

// TestReorderBufferAbortDropsChanges: an aborted xact's queued
// changes never come back through Commit. Mirrors upstream's
// "uncommitted state never reaches the wire".
func TestReorderBufferAbortDropsChanges(t *testing.T) {
	rb := NewReorderBuffer()
	rb.Append(8, Change{Kind: ChangeInsert, LSN: 200})
	if !rb.Abort(8) {
		t.Errorf("Abort returned false")
	}
	if rb.Active() != 0 {
		t.Errorf("Active after abort=%d want 0", rb.Active())
	}
	got, _, ok := rb.Commit(8)
	if ok {
		t.Errorf("Commit after abort returned ok=true with %v", got)
	}
}

// TestReorderBufferIsolatesXacts: two concurrent in-flight xacts
// must not see each other's changes. Commit on xid 1 must drain
// only xid 1's queue.
func TestReorderBufferIsolatesXacts(t *testing.T) {
	rb := NewReorderBuffer()
	rb.Append(1, Change{Kind: ChangeInsert, LSN: 10, NewTuple: []byte("x1.a")})
	rb.Append(2, Change{Kind: ChangeInsert, LSN: 20, NewTuple: []byte("x2.a")})
	rb.Append(1, Change{Kind: ChangeDelete, LSN: 30, OldTuple: []byte("x1.b")})

	if rb.Active() != 2 {
		t.Errorf("Active=%d want 2", rb.Active())
	}

	got1, _, _ := rb.Commit(1)
	if len(got1) != 2 {
		t.Errorf("xid 1 commit produced %d changes want 2", len(got1))
	}
	if rb.Active() != 1 {
		t.Errorf("Active after xid1 commit=%d want 1", rb.Active())
	}
	got2, _, _ := rb.Commit(2)
	if len(got2) != 1 || string(got2[0].NewTuple) != "x2.a" {
		t.Errorf("xid 2 commit produced %+v want one x2.a", got2)
	}
}

// TestReorderBufferOldestBeginLSN tracks the smallest in-flight
// begin LSN — the catalog-xmin tracker uses this to know which
// WAL must remain readable.
func TestReorderBufferOldestBeginLSN(t *testing.T) {
	rb := NewReorderBuffer()
	if got := rb.OldestBeginLSN(); got != 0 {
		t.Errorf("empty OldestBeginLSN=%d want 0", got)
	}
	rb.Append(3, Change{LSN: 500})
	rb.Append(4, Change{LSN: 300})
	rb.Append(3, Change{LSN: 600}) // 3's begin is still 500
	if got := rb.OldestBeginLSN(); got != 300 {
		t.Errorf("OldestBeginLSN=%d want 300", got)
	}
	rb.Commit(4)
	if got := rb.OldestBeginLSN(); got != 500 {
		t.Errorf("after committing 4, OldestBeginLSN=%d want 500", got)
	}
}

// TestReorderBufferRejectsInvalidXID: xid=0 is the
// InvalidTransactionID sentinel; queueing under it would
// conflate distinct xacts. Drop defensively.
func TestReorderBufferRejectsInvalidXID(t *testing.T) {
	rb := NewReorderBuffer()
	rb.Append(storage.InvalidTransactionID, Change{LSN: 1})
	if rb.Active() != 0 {
		t.Errorf("invalid-xid Append created an entry: Active=%d", rb.Active())
	}
}

// recordingPlugin captures the call sequence for decoder tests.
// Tests that need the actual Change bodies (e.g. to assert
// NewTuple/OldTuple after the classifier rewrites a HOT-update
// record) inspect `changes`.
type recordingPlugin struct {
	calls   []string
	changes []Change
	err     error // injected to check error propagation
}

func (p *recordingPlugin) Begin(xid storage.TransactionID, lsn uint64) error {
	p.calls = append(p.calls, "Begin")
	return p.err
}

func (p *recordingPlugin) Change(c Change) error {
	p.calls = append(p.calls, "Change")
	p.changes = append(p.changes, c)
	return p.err
}

func (p *recordingPlugin) Commit(xid storage.TransactionID, lsn uint64) error {
	p.calls = append(p.calls, "Commit")
	return p.err
}

// TestDecoderApplyCommitDrivesPlugin pins the Begin → Change*
// → Commit ordering through the plugin.
func TestDecoderApplyCommitDrivesPlugin(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)
	d.ApplyChange(5, Change{Kind: ChangeInsert, LSN: 1, NewTuple: []byte("a")})
	d.ApplyChange(5, Change{Kind: ChangeInsert, LSN: 2, NewTuple: []byte("b")})
	if err := d.ApplyCommit(5, 99); err != nil {
		t.Fatal(err)
	}
	want := []string{"Begin", "Change", "Change", "Commit"}
	if !reflect.DeepEqual(p.calls, want) {
		t.Errorf("calls=%v want %v", p.calls, want)
	}
}

// TestDecoderAbortSkipsPlugin: an aborted xact's queued changes
// never invoke the plugin.
func TestDecoderAbortSkipsPlugin(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)
	d.ApplyChange(6, Change{LSN: 1})
	d.ApplyAbort(6)
	if err := d.ApplyCommit(6, 100); err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 0 {
		t.Errorf("aborted xact reached plugin: %v", p.calls)
	}
}

// TestDecoderUnknownCommitIsNoop: committing an xid the buffer
// never saw is a no-op (the upstream classifier may filter every
// change for some xacts; the commit record still arrives).
func TestDecoderUnknownCommitIsNoop(t *testing.T) {
	p := &recordingPlugin{}
	d := NewDecoder(p)
	if err := d.ApplyCommit(99, 200); err != nil {
		t.Errorf("unknown commit err=%v want nil", err)
	}
	if len(p.calls) != 0 {
		t.Errorf("unknown commit reached plugin: %v", p.calls)
	}
}

// TestDecoderRequiresPlugin: a decoder configured with nil
// plugin returns ErrNoPlugin on commit-with-changes.
func TestDecoderRequiresPlugin(t *testing.T) {
	d := NewDecoder(nil)
	d.ApplyChange(7, Change{LSN: 1})
	if err := d.ApplyCommit(7, 50); !errors.Is(err, ErrNoPlugin) {
		t.Errorf("err=%v want ErrNoPlugin", err)
	}
}

// TestReorderFoldDeleteInsertToUpdate verifies that consecutive
// (ChangeDelete, ChangeInsert) pairs for the same relation in one
// committed xact are folded into a single ChangeUpdate by Commit().
func TestReorderFoldDeleteInsertToUpdate(t *testing.T) {
	rel := storage.RelFileNode{DBOid: 1, RelOid: 42}
	rb := NewReorderBuffer()
	const xid = storage.TransactionID(100)

	rb.Append(xid, Change{Kind: ChangeDelete, LSN: 10, Rel: rel, OldTuple: []byte("old")})
	rb.Append(xid, Change{Kind: ChangeInsert, LSN: 20, Rel: rel, NewTuple: []byte("new")})

	changes, _, ok := rb.Commit(xid)
	if !ok {
		t.Fatal("Commit ok=false")
	}
	if len(changes) != 1 {
		t.Fatalf("got %d changes after fold, want 1", len(changes))
	}
	c := changes[0]
	if c.Kind != ChangeUpdate {
		t.Errorf("kind=%v want ChangeUpdate", c.Kind)
	}
	if !reflect.DeepEqual(c.OldTuple, []byte("old")) {
		t.Errorf("OldTuple=%q want 'old'", c.OldTuple)
	}
	if !reflect.DeepEqual(c.NewTuple, []byte("new")) {
		t.Errorf("NewTuple=%q want 'new'", c.NewTuple)
	}
}

// TestReorderFoldDoesNotFoldDifferentRels verifies that
// (Delete, Insert) pairs on different relations are NOT folded.
func TestReorderFoldDoesNotFoldDifferentRels(t *testing.T) {
	relA := storage.RelFileNode{DBOid: 1, RelOid: 10}
	relB := storage.RelFileNode{DBOid: 1, RelOid: 20}
	rb := NewReorderBuffer()
	const xid = storage.TransactionID(101)

	rb.Append(xid, Change{Kind: ChangeDelete, LSN: 10, Rel: relA, OldTuple: []byte("a")})
	rb.Append(xid, Change{Kind: ChangeInsert, LSN: 20, Rel: relB, NewTuple: []byte("b")})

	changes, _, _ := rb.Commit(xid)
	if len(changes) != 2 {
		t.Fatalf("got %d changes, want 2 (cross-rel pairs must not be folded)", len(changes))
	}
}
