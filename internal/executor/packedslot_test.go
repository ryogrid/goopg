package executor

// packedslot_test.go — D-03 (MD-03) gates for the slot.
//
// 06-verification.md §3 MD-03 requires, in its own words:
//   - a test per type switch (six switches, five of which fail SILENTLY);
//   - watermark invariants, as a property test over random access orders;
//   - the escape check — a partially-deformed slot's `values` must not be
//     observable past nvalid.
// R-8's arena bound (04 §9.9) is asserted here too: "MD-03 may not land
// without answering it, with a test that bounds arena growth across a scan of
// known length."

import (
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/adt/array"
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// packedFixtureCols is deliberately mixed: fixed-width prefix, then a varlena
// (which ends the attcacheoff cache), a NULL-able column, and more fixed-width
// after — so a prefix walk, a bitmap and a resumed offset are all exercised.
func packedFixtureCols() []catalog.Column {
	return []catalog.Column{
		testCol("c0", "int4"),
		testCol("c1", "int8"),
		testCol("c2", "bool"),
		testCol("c3", "text"),
		testCol("c4", "numeric"),
		testCol("c5", "int4"),
		testCol("c6", "text"),
		testCol("c7", "date"),
	}
}

func packedFixtureRow(i int) Row {
	r := Row{
		NewIntDatum(int64(i)),
		NewIntDatum(int64(i) * 1000),
		NewBoolDatum(i%2 == 0),
		NewStringDatum(strings.Repeat("v", 1+i%17)),
		Datum{Kind: KindNumeric, Int: int64(i)*7 + 1, Scale: 2},
		NewIntDatum(int64(-i)),
		NewStringDatum("tail"),
		NewDateDatum(time.Date(1998, 3, 15, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)),
	}
	// Every third row NULLs two columns, one of them the varlena, so the null
	// bitmap and the "data exhausted" tail are both walked.
	if i%3 == 0 {
		r[3] = NullDatum
		r[5] = NullDatum
	}
	return r
}

// newPackedFixture returns a slot over the fixture descriptor plus a
// MaterializedSlot holding the same row decoded eagerly — the oracle every
// assertion below compares against.
func newPackedFixture(t *testing.T, i int, parent *mmgr.Context) (*PackedSlot, *MaterializedSlot) {
	t.Helper()
	desc := NewTupleDescFromColumns(packedFixtureCols())
	row := packedFixtureRow(i)
	pt, err := FormPackedTuple(desc, row, nil)
	if err != nil {
		t.Fatalf("FormPackedTuple: %v", err)
	}
	ps := NewPackedSlot(desc, parent, array.DefaultOutputStyle())
	ps.Load(pt)

	// The oracle is a FULL deform through the same codec, into its own row —
	// so the property test compares "deformed in some order" against
	// "deformed all at once", which is exactly the watermark's contract.
	oracle := make(Row, len(desc.cols))
	if _, err := decodeRowRangeInfo(oracle, desc.cols, desc.info, pt.data(), pt.bitmap(),
		pt.natts(), nil, array.DefaultOutputStyle(), 0, len(desc.cols), 0); err != nil {
		t.Fatalf("oracle decode: %v", err)
	}
	return ps, SlotFromRow(ps.Schema(), oracle)
}

func datumsAgree(t *testing.T, got, want Datum) bool {
	t.Helper()
	if got.IsNull() != want.IsNull() {
		return false
	}
	if got.IsNull() {
		return true
	}
	if got.Kind != want.Kind || got.TimeSub != want.TimeSub {
		return false
	}
	return got.Format() == want.Format()
}

// ---------------------------------------------------------------------------
// Watermark
// ---------------------------------------------------------------------------

// TestPackedSlotWatermarkMatchesAFullDeform is 06 §3 MD-03's watermark
// property test: for a random tuple and a random access sequence,
// PackedSlot.Get(i) equals the full deform's value for every i, in any order,
// including repeats and including a full Row() mid-sequence.
//
// The invariant under test is the one 04 §2.2 states as "a suffix can be
// skipped; a prefix cannot": Get(5) on a fresh slot must deform 0..5, so
// resuming from (nvalid, off) has to be exact. A wrong resume offset does not
// error — it decodes garbage from the middle of a value — which is why this is
// a property test and not three hand-picked orders.
func TestPackedSlotWatermarkMatchesAFullDeform(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5E3779B97F4A7C15))
	width := len(packedFixtureCols())

	for iter := 0; iter < 200; iter++ {
		ps, oracle := newPackedFixture(t, iter, nil)

		// A random access sequence: a random number of reads at random
		// columns, with repeats, occasionally interrupted by a full Row().
		n := 1 + rng.Intn(3*width)
		for k := 0; k < n; k++ {
			if rng.Intn(11) == 0 {
				row := ps.Row()
				for i := 0; i < width; i++ {
					if !datumsAgree(t, row[i], oracle.Get(i)) {
						t.Fatalf("iter %d: Row()[%d] = %v, want %v", iter, i, row[i], oracle.Get(i))
					}
				}
				continue
			}
			col := rng.Intn(width)
			if got, want := ps.Get(col), oracle.Get(col); !datumsAgree(t, got, want) {
				t.Fatalf("iter %d step %d: Get(%d) = %+v, want %+v", iter, k, col, got, want)
			}
			if ps.IsNull(col) != oracle.IsNull(col) {
				t.Fatalf("iter %d: IsNull(%d) = %v, want %v", iter, col, ps.IsNull(col), oracle.IsNull(col))
			}
		}
		if err := ps.Err(); err != nil {
			t.Fatalf("iter %d: latched deform error: %v", iter, err)
		}
	}
}

// TestPackedSlotDeformsThePrefixNotTheColumn pins the half of the watermark a
// values comparison cannot see: Get(k) must advance nvalid to k+1, not to 1.
// A slot that "deformed only what was asked for" would return the right value
// for k and then serve the PREVIOUS tuple's values for everything below it.
func TestPackedSlotDeformsThePrefixNotTheColumn(t *testing.T) {
	ps, oracle := newPackedFixture(t, 4, nil)
	if ps.nvalid != 0 {
		t.Fatalf("fresh slot has nvalid = %d, want 0", ps.nvalid)
	}
	_ = ps.Get(5)
	if ps.nvalid != 6 {
		t.Fatalf("after Get(5), nvalid = %d, want 6 — a prefix may not be skipped", ps.nvalid)
	}
	for i := 0; i <= 5; i++ {
		if !datumsAgree(t, ps.Get(i), oracle.Get(i)) {
			t.Errorf("column %d below the watermark disagrees with a full deform", i)
		}
	}
	// A repeat below the watermark must not re-decode.
	before := ps.off
	_ = ps.Get(2)
	if ps.off != before || ps.nvalid != 6 {
		t.Error("a read below the watermark advanced the deform state")
	}
}

// ---------------------------------------------------------------------------
// The escape check
// ---------------------------------------------------------------------------

// TestPackedSlotPartialValuesNeverEscape is 06 §3 MD-03's escape check, and
// the invariant is codec.go:1343-1346's: "Callers that stop early MUST NOT let
// the partially-filled row escape: entries at or past `to` still hold whatever
// the previous tuple left there."
//
// Three things are asserted, because there are three ways out of the slot:
//
//  1. Row() deforms fully first, so the escaping slice is never partial;
//  2. Materialize() goes through Row(), so a RETAINED row is never partial;
//  3. with the EX1-01 deform poison armed, the undeformed tail carries the
//     sentinel rather than the previous tuple's values, so a consumer that
//     reaches past the watermark panics at the ColumnRef evaluation sites
//     instead of reading a plausible wrong answer.
func TestPackedSlotPartialValuesNeverEscape(t *testing.T) {
	desc := NewTupleDescFromColumns(packedFixtureCols())
	width := desc.Width()

	first, err := FormPackedTuple(desc, packedFixtureRow(1), nil)
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	second, err := FormPackedTuple(desc, packedFixtureRow(2), nil)
	if err != nil {
		t.Fatalf("form: %v", err)
	}

	ps := NewPackedSlot(desc, nil, array.DefaultOutputStyle())
	ps.Load(first)
	_ = ps.Row() // fully deform tuple 1, so `values` holds tuple 1 everywhere

	ps.Load(second)
	_ = ps.Get(1) // deform only columns 0..1 of tuple 2

	// (1) Row() must not hand out the partial scratch.
	row := ps.Row()
	if len(row) != width {
		t.Fatalf("Row() width = %d, want %d", len(row), width)
	}
	if ps.nvalid != width {
		t.Errorf("Row() returned with nvalid = %d; it must deform fully first", ps.nvalid)
	}
	oracle := make(Row, width)
	if _, err := decodeRowRangeInfo(oracle, desc.cols, desc.info, second.data(), second.bitmap(),
		second.natts(), nil, array.DefaultOutputStyle(), 0, width, 0); err != nil {
		t.Fatalf("oracle: %v", err)
	}
	for i := range oracle {
		if !datumsAgree(t, row[i], oracle[i]) {
			t.Errorf("Row()[%d] is not tuple 2's value — a stale entry escaped", i)
		}
	}

	// (2) Materialize() on a partially deformed slot.
	ps.Load(second)
	_ = ps.Get(0)
	ms := ps.Materialize()
	for i := range oracle {
		if !datumsAgree(t, ms.Get(i), oracle[i]) {
			t.Errorf("Materialize()[%d] is not tuple 2's value — a stale entry was retained", i)
		}
	}
	// …and it must be independent of the slot's scratch and arena.
	ps.Load(first)
	_ = ps.Row()
	for i := range oracle {
		if !datumsAgree(t, ms.Get(i), oracle[i]) {
			t.Errorf("Materialize()[%d] changed when the slot was reloaded — "+
				"the materialised row must own its storage", i)
		}
	}

	// (3) The armed poison: the tail is the sentinel, not tuple 1's values.
	defer func(prev bool) { seqScanDeformPoison = prev }(seqScanDeformPoison)
	seqScanDeformPoison = true

	ps2 := NewPackedSlot(desc, nil, array.DefaultOutputStyle())
	ps2.Load(first)
	_ = ps2.Row()
	ps2.Load(second)
	_ = ps2.Get(1)
	for i := 2; i < width; i++ {
		if !isDeformPoison(ps2.values[i]) {
			t.Errorf("values[%d] past the watermark is not poisoned; with the debug flag "+
				"armed an undeformed entry must be the sentinel, so a consumer that "+
				"reaches past nvalid panics rather than reading tuple 1's value", i)
		}
	}
	// And the sanctioned path still works: Get past the watermark deforms.
	if !datumsAgree(t, ps2.Get(width-1), oracle[width-1]) {
		t.Error("Get past the watermark did not deform")
	}
}

// ---------------------------------------------------------------------------
// R-8 — the arena bound (04 §9.9)
// ---------------------------------------------------------------------------

// TestPackedSlotArenaGrowthIsBoundedAcrossAScan is the test 04 §9.9 names as
// the condition for MD-03 landing at all: "a test that bounds arena growth
// across a scan of known length".
//
// The failure it excludes: if the deform arena is not reset per tuple,
// deforming N rows accumulates N rows of varlena bytes and gives back exactly
// the memory this bundle removes. The reset point is Load (see NewPackedSlot's
// R-8 note), so the arena's high-water mark must be a function of ONE tuple,
// not of N.
func TestPackedSlotArenaGrowthIsBoundedAcrossAScan(t *testing.T) {
	desc := NewTupleDescFromColumns(packedFixtureCols())
	parent := mmgr.Acquire(nil, mmgr.KindExpr)
	defer parent.Release()

	ps := NewPackedSlot(desc, parent, array.DefaultOutputStyle())
	defer ps.Close()

	measure := func(rows int) (allocated, peak int64) {
		for i := 0; i < rows; i++ {
			pt, err := FormPackedTuple(desc, packedFixtureRow(i), nil)
			if err != nil {
				t.Fatalf("form: %v", err)
			}
			ps.Load(pt)
			_ = ps.Row()
		}
		return ps.mctx.Usage()
	}

	_, peakShort := measure(16)
	allocLong, peakLong := measure(4096)

	if peakLong > peakShort*4 {
		t.Errorf("arena peak grew from %d B over 16 rows to %d B over 4096 — the deform "+
			"scratch is accumulating instead of resetting per tuple (R-8, 04 §9.9)",
			peakShort, peakLong)
	}
	// The chunk pool retains capacity across Reset, so lifetime `allocated`
	// must stay a small multiple of one tuple's needs rather than scaling with
	// the row count.
	if allocLong > int64(4096*64) {
		t.Errorf("arena allocated %d B across 4096 rows; Reset must retain chunk "+
			"capacity so steady state allocates nothing", allocLong)
	}
}

// TestPackedSlotWithoutAnArenaStillDecodes pins the nil-parent form
// NewPackedSlot documents: no arena, owned Go memory per value, GC-bounded.
// It is what the tests above use and what a caller with no session context
// gets, so it must be exactly as correct as the arena form.
func TestPackedSlotWithoutAnArenaStillDecodes(t *testing.T) {
	ps, oracle := newPackedFixture(t, 9, nil)
	if ps.mctx != nil {
		t.Fatal("a nil parent must not acquire an arena")
	}
	for i := 0; i < ps.Width(); i++ {
		if !datumsAgree(t, ps.Get(i), oracle.Get(i)) {
			t.Errorf("column %d differs from the oracle without an arena", i)
		}
	}
}

// ---------------------------------------------------------------------------
// R-0 — a test per type-switch site (04 §9.1, 06 §3 MD-03)
// ---------------------------------------------------------------------------

// The table at 04 §9.1, with what each site does WITHOUT a *PackedSlot arm:
//
//	1 slotToRow                     slot.go       default: return nil          SILENT
//	2 evalFastExpr ColumnRef        exprnode.go   unchecked Get, no bounds     SILENT
//	3 evalExprSlot ColumnRef hoist  expr.go       per-type guards skipped      SILENT
//	4 ctid in fillFromTupleSlot     opnode.go     default: hasCTID = false     SILENT
//	5 ctid in projectOp.Next        operators.go  default: hasCTID = false     SILENT
//	6 VirtualSlot fast path         opnode.go     performance only             LOUD-ish
//
// The bug has been committed once already: slot.go:247-252 records that when
// the *Slot arm was missing from slotToRow, InExpr / CaseExpr / SubqueryExpr /
// ExistsExpr / ExtractExpr / FuncCall fell to `default` and produced spurious
// "nil slot" errors.

// TestPackedSlotArmInSlotToRow — site 1. This is the one with a committed
// precedent, so it asserts the shape of that precedent too: a slot whose
// slotToRow returns nil makes every Row-taking evaluator helper fail.
func TestPackedSlotArmInSlotToRow(t *testing.T) {
	ps, oracle := newPackedFixture(t, 3, nil)
	row := slotToRow(ps)
	if row == nil {
		t.Fatal("slotToRow(*PackedSlot) = nil — the R-0 site-1 arm is missing; " +
			"InExpr/CaseExpr/SubqueryExpr/ExistsExpr/ExtractExpr/FuncCall would all " +
			"raise spurious \"nil slot\" errors (slot.go:247-252)")
	}
	if len(row) != ps.Width() {
		t.Fatalf("slotToRow width = %d, want %d", len(row), ps.Width())
	}
	for i := range row {
		if !datumsAgree(t, row[i], oracle.Get(i)) {
			t.Errorf("slotToRow()[%d] disagrees with a full deform", i)
		}
	}
	// A typed-nil must behave like the other arms.
	var nilSlot *PackedSlot
	if slotToRow(nilSlot) != nil {
		t.Error("slotToRow(typed-nil *PackedSlot) must be nil")
	}
}

// TestPackedSlotArmInEvalFastExpr — site 2, the compiled evaluator. Without an
// arm the switch falls through to an unchecked `slot.Get(colIdx)`, so an
// out-of-range index panics raw. That panic escaped ParallelGroup.Go's recover
// once (TPC-DS Q8) and closed the client socket; PG's contract is that an
// ERROR kills the statement, not the backend.
func TestPackedSlotArmInEvalFastExpr(t *testing.T) {
	ps, oracle := newPackedFixture(t, 5, nil)

	for i := 0; i < ps.Width(); i++ {
		var slab exprTreeSlab
		idx := slab.buildExpr(&optimizer.ColumnRef{Name: "c", Index: i})
		got, err := evalFastExpr(slab, idx, ps, nil)
		if err != nil {
			t.Fatalf("evalFastExpr(col %d): %v", i, err)
		}
		if !datumsAgree(t, got, oracle.Get(i)) {
			t.Errorf("compiled ColumnRef %d = %v, want %v", i, got, oracle.Get(i))
		}
	}

	// Out of range: an error, not a panic, and byte-identical to the
	// interpreted twin's — the compiled arm delegates rather than inventing a
	// second text.
	ref := &optimizer.ColumnRef{Name: "c", Index: 57}
	var slab exprTreeSlab
	idx := slab.buildExpr(ref)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("compiled ColumnRef on a *PackedSlot panicked (%v) — the R-0 "+
					"site-2 arm is missing and the bounds guard was skipped", r)
			}
		}()
		err := func() error { _, e := evalFastExpr(slab, idx, ps, nil); return e }()
		if err == nil {
			t.Fatal("out-of-range ColumnRef on a *PackedSlot returned no error")
		}
		_, wantErr := evalExprSlot(ref, ps, nil)
		if wantErr == nil {
			t.Fatal("the interpreted twin accepted an index the compiled one rejected")
		}
		if err.Error() != wantErr.Error() {
			t.Errorf("compiled error %q != interpreted %q", err, wantErr)
		}
	}()
}

// TestPackedSlotArmInEvalExprSlot — site 3, the interpreted evaluator's
// ColumnRef hoist. Its bounds guards are written per concrete type, so a slot
// kind without one is simply not checked.
func TestPackedSlotArmInEvalExprSlot(t *testing.T) {
	ps, oracle := newPackedFixture(t, 6, nil)

	for i := 0; i < ps.Width(); i++ {
		got, err := evalExprSlot(&optimizer.ColumnRef{Name: "c", Index: i}, ps, nil)
		if err != nil {
			t.Fatalf("evalExprSlot(col %d): %v", i, err)
		}
		if !datumsAgree(t, got, oracle.Get(i)) {
			t.Errorf("interpreted ColumnRef %d = %v, want %v", i, got, oracle.Get(i))
		}
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("interpreted ColumnRef on a *PackedSlot panicked (%v) — the "+
					"R-0 site-3 arm is missing", r)
			}
		}()
		_, err := evalExprSlot(&optimizer.ColumnRef{Name: "c", Index: 57}, ps, nil)
		if err == nil {
			t.Fatal("out-of-range ColumnRef on a *PackedSlot returned no error")
		}
		if ee, ok := err.(*ExecError); !ok || ee.Code != "XX000" {
			t.Errorf("error = %v, want an XX000 ExecError", err)
		}
		if !strings.Contains(err.Error(), "PackedSlot") {
			t.Errorf("error %q does not name the slot kind; the per-type guard is what "+
				"makes the two evaluators' texts identical", err)
		}
	}()
}

// TestPackedSlotArmsInFillFromTupleSlot — sites 4 and 6, both in
// opnode.go's fillFromTupleSlot.
//
// Site 4 is the ctid switch: without an arm a PackedSlot's carried tid falls
// to `default: hasCTID = false`, silently, and only CTIDExpr and WHERE CURRENT
// OF would ever notice (04 §7).
//
// Site 6 is the *VirtualSlot fast path. A PackedSlot deliberately takes the
// generic path (see the comment there): PackedSlot.Row() returns its own
// reused scratch, so the generic path is already allocation-free, and a
// VirtualSlot-shaped early return would skip site 4 and drop the tid. This
// test is what pins that decision — it is the one that fails if someone adds
// the "obvious" fast path.
func TestPackedSlotArmsInFillFromTupleSlot(t *testing.T) {
	desc := NewTupleDescFromColumns(packedFixtureCols())
	pt, err := FormPackedTuple(desc, packedFixtureRow(7), nil)
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	ps := NewPackedSlot(desc, nil, array.DefaultOutputStyle())
	ps.LoadWithTID(pt, 42, 9)

	oracle := make(Row, desc.Width())
	if _, err := decodeRowRangeInfo(oracle, desc.cols, desc.info, pt.data(), pt.bitmap(),
		pt.natts(), nil, array.DefaultOutputStyle(), 0, desc.Width(), 0); err != nil {
		t.Fatalf("oracle: %v", err)
	}

	var s Slot
	s.fillFromTupleSlot(ps)

	if !s.HasRow {
		t.Fatal("fillFromTupleSlot did not mark the slot as carrying a row")
	}
	if len(s.Cells) != desc.Width() {
		t.Fatalf("Cells width = %d, want %d — site 6: the generic path must copy the "+
			"whole deformed row", len(s.Cells), desc.Width())
	}
	for i := range oracle {
		if !datumsAgree(t, s.Cells[i], oracle[i]) {
			t.Errorf("Cells[%d] disagrees with a full deform", i)
		}
	}
	block, off, ok := s.TID()
	if !ok || block != 42 || off != 9 {
		t.Errorf("ctid = (%d,%d,%v), want (42,9,true) — the R-0 site-4 arm is missing "+
			"and a PackedSlot's tid fell to `default: hasCTID = false`, which breaks "+
			"CTIDExpr and WHERE CURRENT OF silently", block, off, ok)
	}

	// A PackedSlot with no tid must report none, exactly like a synthesized row.
	ps2 := NewPackedSlot(desc, nil, array.DefaultOutputStyle())
	ps2.Load(pt)
	var s2 Slot
	s2.fillFromTupleSlot(ps2)
	if _, _, ok := s2.TID(); ok {
		t.Error("a PackedSlot without a loaded tid reported one")
	}
}

// packedChildOp is a test-only Operator that yields PackedSlots. It exists to
// drive site 5 and lives here, in a _test file, precisely because D-03 lands
// with NO PRODUCER: nothing in the pipeline may construct a PackedSlot.
type packedChildOp struct {
	slot *PackedSlot
	done bool
}

func (o *packedChildOp) Open(*Context) error      { return nil }
func (o *packedChildOp) Close() error             { return nil }
func (o *packedChildOp) Schema() optimizer.Schema { return o.slot.Schema() }
func (o *packedChildOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, nil
	}
	o.done = true
	return o.slot, nil
}

// TestPackedSlotArmInProjectOpCtid — site 5, projectOp.Next's ctid
// propagation. It is opnode.go's sibling and the two must agree; a divergence
// here is the pattern_sibling_paths_must_agree class.
func TestPackedSlotArmInProjectOpCtid(t *testing.T) {
	desc := NewTupleDescFromColumns(packedFixtureCols())
	pt, err := FormPackedTuple(desc, packedFixtureRow(8), nil)
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	ps := NewPackedSlot(desc, nil, array.DefaultOutputStyle())
	ps.LoadWithTID(pt, 1234, 56)

	child := &packedChildOp{slot: ps}
	o := &projectOp{
		child:   child,
		targets: []optimizer.Expr{&optimizer.ColumnRef{Name: "c0", Index: 0}},
		schema:  optimizer.Schema{{Name: "c0", Type: catalog.Type{Name: "int4"}}},
		out:     make(Row, 1),
	}
	out, err := o.Next()
	if err != nil {
		t.Fatalf("projectOp.Next: %v", err)
	}
	if out == nil {
		t.Fatal("projectOp.Next returned no slot")
	}
	block, off, ok := out.TID()
	if !ok || block != 1234 || off != 56 {
		t.Errorf("projected ctid = (%d,%d,%v), want (1234,56,true) — the R-0 site-5 arm "+
			"is missing and `SELECT ctid …` over a packed input would return no tid",
			block, off, ok)
	}
	if got := out.Get(0); !datumsAgree(t, got, NewIntDatum(8)) {
		t.Errorf("projected column = %v, want 8", got)
	}
}

// TestPackedSlotArmExistsAtEveryTypeSwitchSite is the exhaustiveness guard.
// The five tests above each drive one site through real code; this one catches
// the case they cannot — an arm DELETED from a site whose behaviour without it
// is silently wrong rather than observably wrong, and a sixth site whose
// decision was written as a comment rather than as code.
//
// It is a source check on purpose. 04 §9.1 forbids both alternatives to the
// concrete switches: an opaque wrapper (rowshape_assert.go:39-48 — capability
// discovery is by type assertion across ~26 sites, and a wrapper changed TPC-H
// to 7 VALUE-DIFF / 4 ROWS-DIFF with zero assertion failures) and a `Width()
// int` capability interface (exprnode.go — the itab lookup cost ~1.4 ns/eval
// and made the compiled path slower than the interpreter it replaced). "Add a
// fifth arm; do not widen the interface." So there is no type-system mechanism
// left that could enforce this, and a grep is what remains.
func TestPackedSlotArmExistsAtEveryTypeSwitchSite(t *testing.T) {
	// Counts of the ACTUAL construct, not a mention of the word. The first
	// version of this test asserted strings.Contains(file, "PackedSlot"),
	// which every one of these files satisfies from a COMMENT — deleting a
	// `case *PackedSlot:` left it green. Review called it decorative, and it
	// was: a guard whose failure mode is "cannot fail" is worse than no
	// guard, because it is cited as coverage.
	sites := []struct {
		file   string
		needle string
		want   int
		reason string
	}{
		{"slot.go", "case *PackedSlot:", 1,
			"site 1 — slotToRow: `default: return nil` makes the row silently nil"},
		{"exprnode.go", "case *PackedSlot:", 1,
			"site 2 — evalFastExpr ColumnRef: falls through to an unchecked Get"},
		// expr.go arms TWO sites in two different SHAPES, so it needs two
		// rows: site 3 is a type ASSERTION in the ColumnRef bounds hoist,
		// site 7 is a `case` in the CTIDExpr switch. Counting only `case`
		// here is what made the first version of this test pass while site 7
		// was missing entirely.
		{"expr.go", "slot.(*PackedSlot)", 1,
			"site 3 — evalExprSlot ColumnRef hoist: without it the per-type " +
				"bounds guards are skipped and a stale index panics raw"},
		{"expr.go", "case *PackedSlot:", 1,
			"site 7 — the CTIDExpr switch, which review found missing from " +
				"both the implementation AND 04 §9.1's table: without it `ctid` " +
				"reads NULL silently, after sites 4 and 5 propagated the tid there"},
		{"opnode.go", "case *PackedSlot:", 1,
			"site 4 — fillFromTupleSlot: `default: hasCTID = false` drops the tid"},
		{"operators.go", "case *PackedSlot:", 1,
			"site 5 — projectOp.Next: `default: hasCTID = false`"},
	}
	for _, s := range sites {
		b, err := os.ReadFile(s.file)
		if err != nil {
			t.Fatalf("read %s: %v", s.file, err)
		}
		if got := strings.Count(string(b), s.needle); got != s.want {
			t.Errorf("%s has %d occurrences of %q, want %d: %s. R-0 (04 §9.1) is the "+
				"risk register's first entry because these sites fail SILENTLY, and "+
				"the bug has been committed once already",
				s.file, got, s.needle, s.want, s.reason)
		}
	}
}

// TestPackedSlotHasNoProducer pins D-03's central constraint: the slice lands
// the type UNREACHABLE, so that the R-0 arms and their tests exist before any
// operator can emit one. A constructor call from non-test code means a
// producer arrived without the gates the conversion slices owe (06 §3 MD-04
// onward).
func TestPackedSlotHasNoProducer(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	allowed := map[string]bool{"packedslot.go": true, "packedtuple.go": true}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if allowed[name] {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, ctor := range []string{"NewPackedSlot(", "NewPackedSlotForSchema(", "FormPackedTuple(", "FormPackedTupleHashed("} {
			if strings.Contains(string(b), ctor) {
				t.Errorf("%s calls %s — D-03 lands PackedTuple/PackedSlot with NO PRODUCER. "+
					"A conversion slice that adds one owes the gates in 06 §3 (alloc arm, "+
					"plan-shape pin, sibling-parity test) in the same commit", name, ctor)
			}
		}
	}
}

// TestPackedSlotSatisfiesTupleSlot is the compile-time claim, written out so
// that a future change to the interface names this file as a breakage site.
func TestPackedSlotSatisfiesTupleSlot(t *testing.T) {
	var _ TupleSlot = (*PackedSlot)(nil)
	var _ SlotView = (*PackedSlot)(nil)
	ps, _ := newPackedFixture(t, 2, nil)
	var ts TupleSlot = ps
	if ts.Width() != len(packedFixtureCols()) {
		t.Errorf("Width() = %d, want the DESCRIPTOR width, not the watermark", ts.Width())
	}
	if len(ts.Schema()) != ts.Width() {
		t.Error("Schema() and Width() disagree")
	}
	// Release clears; the slot is reusable afterwards, which is what pooling
	// needs and what the interface's "returns the slot to the pool" means.
	ts.Release()
	if ps.nvalid != 0 || ps.tup.Valid() {
		t.Error("Release did not clear the slot")
	}
}

// TestPackedSlotLatchesADeformError pins R-2 (04 §9.3): Get has no error
// return, so a deform failure is latched and surfaced at the next
// error-returning boundary — and NEVER served as a NullDatum, which "is the
// shape of a silent wrong-answer bug".
func TestPackedSlotLatchesADeformError(t *testing.T) {
	// A descriptor whose type the decoder cannot name, over bytes formed for a
	// different one: encode-side validation is what normally makes this
	// unreachable, so the tuple is built by hand.
	good := NewTupleDescFromColumns([]catalog.Column{testCol("a", "text")})
	pt, err := FormPackedTuple(good, Row{NewStringDatum("x")}, nil)
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	bad := NewTupleDescFromColumns([]catalog.Column{testCol("a", "interval")})
	ps := NewPackedSlot(bad, nil, array.DefaultOutputStyle())
	ps.Load(pt)
	_ = ps.Get(0)
	if ps.Err() == nil {
		t.Skip("this fixture no longer produces a decode error; the codec grew an arm " +
			"that accepts it. The invariant under test (a latched error, never a " +
			"NullDatum fallback) still holds — see deformTo.")
	}
	// The error is latched, not re-raised per column: a second read does not
	// clear it, and every column reads as poisoned/undeformed rather than as a
	// plausible NULL.
	_ = ps.Get(0)
	if ps.Err() == nil {
		t.Error("the latched deform error was cleared by a second read")
	}
}

// TestPackedSlotFailsClosedAfterADeformError is the escape-rule test for the
// ERROR path, which review found open.
//
// The original code set `nvalid = len(values)` on a latched error, reasoning
// that no later deform would run anyway. But `Row()` calls
// `deformTo(width)`, which then early-returns on `n <= nvalid` and hands back
// a slice whose entries past the failing column belong to the PREVIOUS tuple
// on a reused slot. `poisonDeformTail` cannot catch it: it is gated on
// `seqScanDeformPoison`, which is false in production — so this test runs
// with the flag OFF deliberately, because that is the shipping configuration.
func TestPackedSlotFailsClosedAfterADeformError(t *testing.T) {
	if seqScanDeformPoison {
		t.Skip("run with the poison flag off: that is the shipping configuration " +
			"and the one where the escape was reachable")
	}
	// Tuple 1 loads cleanly and leaves real values in the scratch.
	good := NewTupleDescFromColumns([]catalog.Column{
		testCol("a", "text"), testCol("b", "text"),
	})
	first, err := FormPackedTuple(good, Row{NewStringDatum("keep-me"), NewStringDatum("and-me")}, nil)
	if err != nil {
		t.Fatalf("form first: %v", err)
	}
	bad := NewTupleDescFromColumns([]catalog.Column{
		testCol("a", "text"), testCol("b", "interval"),
	})
	ps := NewPackedSlot(bad, nil, array.DefaultOutputStyle())
	ps.Load(first)
	if got := ps.Get(0); got.StringValue() != "keep-me" {
		t.Fatalf("first tuple col 0 = %v, want keep-me", got)
	}
	_ = ps.Row() // fill the whole scratch with tuple 1's values

	// Tuple 2 fails mid-deform on column 1.
	second, err := FormPackedTuple(good, Row{NewStringDatum("new"), NewStringDatum("boom")}, nil)
	if err != nil {
		t.Fatalf("form second: %v", err)
	}
	ps.Load(second)
	_ = ps.Get(1)
	if ps.Err() == nil {
		t.Skip("fixture no longer errors; the codec grew an arm accepting it")
	}

	// THE ASSERTION: neither escape path may publish tuple 1's tail as though
	// it were tuple 2's.
	if r := ps.Row(); r != nil {
		t.Errorf("Row() returned %v after a latched deform error; it must fail "+
			"closed (nil), or a caller reads the previous tuple's values as this "+
			"row's", r)
	}
	if ms := ps.Materialize(); ms.row != nil {
		t.Errorf("Materialize() cloned %v after a latched deform error — worse "+
			"than returning it live, since the mixed row is then RETAINED", ms.row)
	}
}

// TestPackedSlotArmInCTIDExpr covers R-0 site 7, which review found missing
// from both the implementation and 04 §9.1's table. It is the only consumer
// of a slot's carried tid for `ctid` in an expression: evalFastExpr does not
// fold CTIDExpr, and WHERE CURRENT OF uses the TID() interface method.
func TestPackedSlotArmInCTIDExpr(t *testing.T) {
	desc := NewTupleDescFromColumns([]catalog.Column{testCol("a", "int4")})
	pt, err := FormPackedTuple(desc, Row{NewIntDatum(7)}, nil)
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	ps := NewPackedSlot(desc, nil, array.DefaultOutputStyle())
	ps.LoadWithTID(pt, 42, 3)

	got, err := evalExprSlot(&optimizer.CTIDExpr{}, ps, &Context{})
	if err != nil {
		t.Fatalf("evalExprSlot(CTIDExpr): %v", err)
	}
	if want := "(42,3)"; got.StringValue() != want {
		t.Fatalf("ctid = %q, want %q — without the *PackedSlot arm this reads "+
			"NULL silently, after sites 4 and 5 propagated the tid here", got.StringValue(), want)
	}
}
