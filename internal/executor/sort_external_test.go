package executor

import (
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)
// TestM0068SortExternalSpills constructs a sortOp with a tiny chunk
// limit so a small input forces multiple spill rounds. The test
// asserts that (a) at least one spill file was created during Open,
// (b) the merged output is fully sorted in ascending key order, and
// (c) the row count is preserved end-to-end. This pins the M0068-0006
// external-sort contract: peak memory is bounded by chunkLimitBytes
// regardless of input size.
func TestM0068SortExternalSpills(t *testing.T) {
	const N = 4096
	rng := rand.New(rand.NewSource(0xC0FFEE))
	rows := make([]Row, 0, N)
	for i := 0; i < N; i++ {
		rows = append(rows, Row{NewIntDatum(rng.Int63())})
	}
	src := &fakeBorrowSource{rows: rows}
	s := &sortOp{
		child: src,
		keys: []optimizer.SortKey{
			{Expr: &optimizer.ColumnRef{Index: 0}, Desc: false},
		},
		chunkLimitBytes: 1024, // tiny, forces many spills
	}
	if err := s.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.spillFiles) == 0 {
		t.Fatalf("expected ≥ 1 spill file with chunkLimitBytes=1024 over %d rows; got 0", N)
	}
	emitted := 0
	var prev int64
	for {
		slot, err := s.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		row := slot.Row()
		if emitted > 0 && row[0].Int < prev {
			t.Fatalf("merge order violated at %d: %d < %d", emitted, row[0].Int, prev)
		}
		prev = row[0].Int
		emitted++
	}
	if emitted != N {
		t.Fatalf("emitted = %d, want %d", emitted, N)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestM0068SortNoSpillBelowChunk confirms that a small input that
// fits within the default chunk limit does NOT spill — preserving
// the in-memory fast path for typical TPC-H sorts.
func TestM0068SortNoSpillBelowChunk(t *testing.T) {
	rows := make([]Row, 0, 64)
	for i := int64(63); i >= 0; i-- {
		rows = append(rows, Row{NewIntDatum(i)})
	}
	src := &fakeBorrowSource{rows: rows}
	s := &sortOp{
		child: src,
		keys: []optimizer.SortKey{
			{Expr: &optimizer.ColumnRef{Index: 0}, Desc: false},
		},
	}
	if err := s.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.spillFiles) != 0 {
		t.Fatalf("unexpected spill: %d files", len(s.spillFiles))
	}
	emitted := 0
	for {
		slot, err := s.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		row := slot.Row()
		if got, want := row[0].Int, int64(emitted); got != want {
			t.Fatalf("row %d: got %d, want %d", emitted, got, want)
		}
		emitted++
	}
	if emitted != 64 {
		t.Fatalf("emitted = %d, want 64", emitted)
	}
	_ = s.Close()
}

// ctidRowSource is a minimal Operator stub emitting rows with a caller-chosen
// TID side-channel, so the EX3-05 Cut A gate tests can verify TID tracking
// without a heap.
type ctidRowSource struct {
	rows  []Row
	ctids []sortCTID
	idx   int
}

func (o *ctidRowSource) Open(*Context) error      { o.idx = 0; return nil }
func (o *ctidRowSource) Schema() optimizer.Schema { return nil }
func (o *ctidRowSource) Close() error             { return nil }
func (o *ctidRowSource) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	ms := SlotFromRow(nil, o.rows[o.idx])
	if o.idx < len(o.ctids) {
		c := o.ctids[o.idx]
		ms.hasCTID = c.has
		ms.ctidBlock = c.block
		ms.ctidOff = c.off
	}
	o.idx++
	return ms, nil
}

// ctidGateFixture returns a 3-row out-of-order input whose keys sort to
// [1,2,3] while the TIDs identify the original positions: key k rode in on
// (0,k), so after sorting row i must carry (0,i+1).
func ctidGateFixture() ([]Row, []sortCTID) {
	rows := []Row{{NewIntDatum(3)}, {NewIntDatum(1)}, {NewIntDatum(2)}}
	ctids := []sortCTID{
		{block: 0, off: 3, has: true},
		{block: 0, off: 1, has: true},
		{block: 0, off: 2, has: true},
	}
	return rows, ctids
}

func ctidGateSortOp(rows []Row, ctids []sortCTID) *sortOp {
	return &sortOp{
		child: &ctidRowSource{rows: rows, ctids: ctids},
		keys: []optimizer.SortKey{
			{Expr: &optimizer.ColumnRef{Index: 0}, Desc: false},
		},
	}
}

// TestSortCTIDSkippedWithoutConsumer pins EX3-05 Cut A's default: a sort with
// no consumer above (wantCTIDs false) must not maintain the TID side-channel
// even when every input row carries one — no per-row append at Open, no
// re-attach at Next — while ordering stays bit-identical (stable ascending).
func TestSortCTIDSkippedWithoutConsumer(t *testing.T) {
	rows, ctids := ctidGateFixture()
	s := ctidGateSortOp(rows, ctids)
	if err := s.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.ctids) != 0 {
		t.Fatalf("len(ctids) = %d, want 0 (no consumer marked the sort)", len(s.ctids))
	}
	for i := int64(1); i <= 3; i++ {
		slot, err := s.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got := slot.Row()[0].Int; got != i {
			t.Fatalf("row %d: key = %d, want %d (order regressed)", i, got, i)
		}
		if _, _, ok := slot.TID(); ok {
			t.Fatalf("row %d: TID attached with no consumer (wantCTIDs=false)", i)
		}
	}
	if _, err := s.Next(); err != EOF {
		t.Fatalf("trailing Next = %v, want EOF", err)
	}
	_ = s.Close()
}

// TestSortCTIDMaintainedWhenMarked pins the enabled path: once
// markSortWantCTIDs runs (as lockRowsOp.Open does for ORDER BY ... FOR
// UPDATE), the sort records TIDs at Open, carries them through the sort
// permutation, and re-attaches each row's own TID at Next.
func TestSortCTIDMaintainedWhenMarked(t *testing.T) {
	rows, ctids := ctidGateFixture()
	s := ctidGateSortOp(rows, ctids)
	markSortWantCTIDs(s)
	if err := s.Open(&Context{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.ctids) != 3 {
		t.Fatalf("len(ctids) = %d, want 3 (marked sort must record TIDs)", len(s.ctids))
	}
	for i := int64(1); i <= 3; i++ {
		slot, err := s.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got := slot.Row()[0].Int; got != i {
			t.Fatalf("row %d: key = %d, want %d", i, got, i)
		}
		blk, off, ok := slot.TID()
		if !ok {
			t.Fatalf("row %d: TID missing on marked sort", i)
		}
		if blk != 0 || off != uint16(i) {
			t.Fatalf("row %d: TID = (%d,%d), want (0,%d) (TID did not follow its row)", i, blk, off, i)
		}
	}
	if _, err := s.Next(); err != EOF {
		t.Fatalf("trailing Next = %v, want EOF", err)
	}
	_ = s.Close()
}

// TestMarkSortWantCTIDsSpine pins the consumer-detection walk: marking from
// above a project→filter→limit→sort spine enables the sort; a LockRows node
// is a consumer rather than a conduit, so a sort below one stays disabled.
func TestMarkSortWantCTIDsSpine(t *testing.T) {
	rows, ctids := ctidGateFixture()
	s := ctidGateSortOp(rows, ctids)
	top := &projectOp{child: &filterOp{child: &limitOp{child: s}}}
	markSortWantCTIDs(top)
	if !s.wantCTIDs {
		t.Fatal("sort on a project→filter→limit spine not enabled")
	}

	below := ctidGateSortOp(rows, ctids)
	markSortWantCTIDs(&lockRowsOp{child: below})
	if below.wantCTIDs {
		t.Fatal("sort below a LockRows enabled (LockRows consumes, it does not forward the mark)")
	}

	// The EXPLAIN ANALYZE wrapper must stay transparent to the walk.
	wrapped := ctidGateSortOp(rows, ctids)
	markSortWantCTIDs(&instrumentedOp{inner: wrapped})
	if !wrapped.wantCTIDs {
		t.Fatal("sort under instrumentedOp not enabled")
	}
}
