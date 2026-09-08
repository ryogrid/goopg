package executor

// EX2-02c first cut: ownership transfer across the gather worker queue.
//
// transferRowForQueue passes the fresh *VirtualSlot.Row() buffer directly
// when it is arena-free, and keeps the unconditional MaterializeForTransfer
// copy for every other slot kind (producer-reused buffers) and for
// arena-backed virtual rows. These tests pin the safety contract, not the
// allocation win (the alloc arm belongs to the bench suites):
//
//   - transferred rows satisfy AssertTransferable (arena rules);
//   - transferred rows survive producer-buffer reuse AND arena reset;
//   - the non-virtual path still copies (anti-regression against a future
//     "optimisation" that would transfer an aliased buffer);
//   - a serial control query is pinned value-identical (serial path is
//     untouched by the cut, but the gate demands the arm be shown green).

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// arenaStringDatum builds a KindString Datum addressing a live arena,
// mirroring TestAssertTransferableRejectsArenaBackedRow.
func arenaStringDatum(arena *mmgr.Context, s string) Datum {
	off, length := arena.AllocString(s)
	return Datum{
		Kind:    KindString,
		ArenaID: arena.ID(),
		Int:     int64(off)<<32 | int64(length),
	}
}

func virtualSlotOver(t *testing.T, srcs ...TupleSlot) *VirtualSlot {
	t.Helper()
	schema := optimizer.Schema{}
	cols := []virtualCol{}
	for si, s := range srcs {
		for ci := 0; ci < s.Width(); ci++ {
			schema = append(schema, optimizer.SchemaColumn{Name: "c"})
			cols = append(cols, virtualCol{sourceIdx: int16(si), sourceCol: int16(ci)})
		}
	}
	return NewVirtualSlot(schema, srcs, cols)
}

// TestTransferRowForQueueVirtualNoArena pins the fast path: an arena-free
// virtual row transfers with values intact, satisfies the transfer contract,
// and is immune to the producer reusing its source buffers afterwards.
func TestTransferRowForQueueVirtualNoArena(t *testing.T) {
	src := SlotFromRow(nil, Row{NewIntDatum(7), NewStringDatum("seven")})
	out := transferRowForQueue(virtualSlotOver(t, src))
	if err := AssertTransferable(out); err != nil {
		t.Fatalf("transferred row must satisfy the transfer contract: %v", err)
	}
	if out[0].Int != 7 || out[1].StringValue() != "seven" {
		t.Fatalf("values lost in transfer: %v", out)
	}
	// Producer reuses its buffer underneath; the transferred row must not
	// follow. (For the virtual fast path this holds because Row() already
	// copied the Datum structs into a fresh buffer.)
	src.row[0] = NewIntDatum(99)
	src.row[1] = NewStringDatum("ninety-nine")
	if out[0].Int != 7 || out[1].StringValue() != "seven" {
		t.Errorf("transferred row aliases the producer buffer: %v", out)
	}
}

// TestTransferRowForQueueVirtualArenaSurvivesReset pins the gated path: an
// arena-backed virtual row is still promoted, so it survives the producer
// arena reset that fires at exactly the worker-takes-new-work cadence
// (parallel_runtime.go:31-35).
func TestTransferRowForQueueVirtualArenaSurvivesReset(t *testing.T) {
	arena := mmgr.Acquire(nil, mmgr.KindStmt)
	defer arena.Release()

	src := SlotFromRow(nil, Row{arenaStringDatum(arena, "hello"), NewIntDatum(3)})
	out := transferRowForQueue(virtualSlotOver(t, src))
	arena.Reset() // the producer recycles the bytes underneath the queue
	if err := AssertTransferable(out); err != nil {
		t.Fatalf("arena-backed virtual row must be promoted before transfer: %v", err)
	}
	if got := out[0].StringValue(); got != "hello" {
		t.Errorf("value lost across arena reset: %q", got)
	}
	if out[1].Int != 3 {
		t.Errorf("second column lost across arena reset: %v", out)
	}
}

// TestTransferRowForQueueMaterializedSlotCopies pins the default branch:
// a materialised slot aliases a producer-reused buffer, so the transfer
// MUST copy even when no arena is involved. Overwriting the source after
// the transfer must leave the queued row unchanged.
func TestTransferRowForQueueMaterializedSlotCopies(t *testing.T) {
	src := SlotFromRow(nil, Row{NewIntDatum(1), NewStringDatum("one")})
	out := transferRowForQueue(src)
	if err := AssertTransferable(out); err != nil {
		t.Fatalf("transferred row must satisfy the transfer contract: %v", err)
	}
	src.row[0] = NewIntDatum(2)
	src.row[1] = NewStringDatum("two")
	if out[0].Int != 1 || out[1].StringValue() != "one" {
		t.Errorf("default-branch transfer aliases the producer buffer: %v", out)
	}
}

// TestTransferRowForQueueSerialControlArm pins the mandatory serial control:
// a serial aggregate-over-filter shape returns identical values with the cut
// in place (the serial path shares no code with the worker queue, but the
// gate requires the arm be shown, not assumed).
func TestTransferRowForQueueSerialControlArm(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, v text)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1, 'a'), (2, 'b'), (3, 'c')"); err != nil {
		t.Fatal(err)
	}
	rows := runQuery(t, ctx, "SELECT count(*), min(id), max(id) FROM t WHERE id > 1")
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0][0].Int != 2 || rows[0][1].Int != 2 || rows[0][2].Int != 3 {
		t.Errorf("serial control values wrong: %v", rows[0])
	}
}
