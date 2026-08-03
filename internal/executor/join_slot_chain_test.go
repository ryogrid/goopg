package executor

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// M0127-P1.1 — probe-side slot chaining on the legacy Build path (design
// leftdeep-joins/05 §2, stage E1; the un-deferred 0126-0004).
//
// nextLazy used to flatten the probe child's slot with `r := slotRow(probeSlot)`
// before composing the output. For a *VirtualSlot child that call is a pooled
// acquireRow plus a width-wide 48-byte-Datum copy on every probe row, and the
// pooled row was never released. The child's slot is now bound directly as a
// source of lazyVirtualOut.
//
// The hazard this file exists to pin is 0126-0004's F7: **a child does not
// return a stable slot object.** The same child may hand back its own shared
// VirtualSlot on one pull, a MaterializedSlot over a reused buffer on the next,
// and a freshly allocated slot on the one after that (rowsOp/spillOp do exactly
// this). A seam that caches the child's slot, or that assumes one concrete type,
// emits the PREVIOUS row's values — a wrong-answer bug that no row count would
// catch. Every test below therefore runs the same join over each child shape and
// over a child that rotates shapes per pull, and checks values, not just counts.

// probeSlotShape names the concrete slot form a probe child hands back.
type probeSlotShape int

const (
	// probeShapeReusedBuffer: *MaterializedSlot over ONE buffer the child
	// overwrites on every Next (projectOp's contract).
	probeShapeReusedBuffer probeSlotShape = iota
	// probeShapeSharedVirtual: the child's own shared *VirtualSlot, rebound
	// per pull (what a child joinOp returns — the case P1.1 is about).
	probeShapeSharedVirtual
	// probeShapeFreshSlot: a freshly allocated *MaterializedSlot per call
	// (rowsOp/spillOp, spill.go).
	probeShapeFreshSlot
	// probeShapeRotating: a different one of the three on every call.
	probeShapeRotating
	probeShapeCount = int(probeShapeRotating)
)

func (s probeSlotShape) String() string {
	switch s {
	case probeShapeReusedBuffer:
		return "reused-buffer"
	case probeShapeSharedVirtual:
		return "shared-virtual"
	case probeShapeFreshSlot:
		return "fresh-slot"
	default:
		return "rotating"
	}
}

// shapedProbeOp is a probe child that returns its rows through a chosen
// concrete slot shape.
type shapedProbeOp struct {
	schema planner.Schema
	rows   []Row
	shape  probeSlotShape
	i      int
	calls  int

	buf    Row               // reused-buffer backing store
	inner  *MaterializedSlot // source behind the shared VirtualSlot
	shared *VirtualSlot
}

func (o *shapedProbeOp) Open(*Context) error    { o.i, o.calls = 0, 0; return nil }
func (o *shapedProbeOp) Schema() planner.Schema { return o.schema }
func (o *shapedProbeOp) Close() error           { return nil }

func (o *shapedProbeOp) Next() (TupleSlot, error) { //nolint:ireturn
	if o.i >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.i]
	o.i++
	shape := o.shape
	if shape == probeShapeRotating {
		shape = probeSlotShape(o.calls % probeShapeCount)
	}
	o.calls++
	switch shape {
	case probeShapeSharedVirtual:
		if o.shared == nil {
			cols := make([]virtualCol, len(row))
			for i := range cols {
				cols[i] = virtualCol{sourceIdx: 0, sourceCol: int16(i)}
			}
			o.inner = SlotFromRow(o.schema, make(Row, len(row)))
			o.shared = NewVirtualSlot(o.schema, []TupleSlot{o.inner}, cols)
		}
		copy(o.inner.row, row)
		return o.shared, nil
	case probeShapeFreshSlot:
		fresh := make(Row, len(row))
		copy(fresh, row)
		return SlotFromRow(o.schema, fresh), nil
	default: // probeShapeReusedBuffer
		if o.buf == nil {
			o.buf = make(Row, len(row))
		}
		copy(o.buf, row)
		return SlotFromRow(o.schema, o.buf), nil
	}
}

// chainSchema names lw left columns followed by rw right columns.
func chainSchema(lw, rw int) planner.Schema {
	s := make(planner.Schema, 0, lw+rw)
	for i := 0; i < lw; i++ {
		s = append(s, planner.SchemaColumn{Name: fmt.Sprintf("l%d", i)})
	}
	for i := 0; i < rw; i++ {
		s = append(s, planner.SchemaColumn{Name: fmt.Sprintf("r%d", i)})
	}
	return s
}

// stubOp stands in for the build-side operator: ensureLazyVirtual reads
// o.left/o.right schemas, but the hash table is pre-loaded here.
type stubOp struct{ schema planner.Schema }

func (o *stubOp) Open(*Context) error      { return nil }
func (o *stubOp) Schema() planner.Schema   { return o.schema }
func (o *stubOp) Close() error             { return nil }
func (o *stubOp) Next() (TupleSlot, error) { return nil, EOF } //nolint:ireturn

// chainFixture wires a lazy hash joinOp: `probe` on the left (the probe side,
// the !BuildLeft orientation the plan-shape contract fixes), and buildRows
// pre-loaded into the string hash table keyed on their column 0.
func chainFixture(jt planner.JoinType, probe *shapedProbeOp, buildRows []Row, lw, rw int) *joinOp {
	schema := chainSchema(lw, rw)
	if jt == planner.JoinTypeSemi || jt == planner.JoinTypeAnti {
		// Join.Output() derives Semi/Anti output from Left alone.
		schema = schema[:lw]
	}
	o := &joinOp{
		plan: &planner.Join{
			Type: jt,
			Algo: planner.JoinAlgoHash,
			// Keys are read in the merged left++right column space:
			// the probe key is left column 0, the build key sits at
			// the first right column.
			LeftKey:  &planner.ColumnRef{Index: 0},
			RightKey: &planner.ColumnRef{Index: lw},
		},
		schema:    schema,
		left:      probe,
		right:     &stubOp{schema: chainSchema(0, rw)},
		lazyProbe: probe,
		lazyLW:    lw,
		lazyRW:    rw,
		lazyHash:  map[string][]Row{},
	}
	for _, br := range buildRows {
		k := datumKey(br[0])
		o.lazyHash[k] = append(o.lazyHash[k], br)
	}
	return o
}

// drainJoin pulls every emitted tuple and copies its columns out immediately —
// the emitted slot is shared and is invalidated by the next pull.
func drainJoin(t *testing.T, o *joinOp) [][]Datum {
	t.Helper()
	var out [][]Datum
	for {
		slot, err := o.nextLazy()
		if err == EOF {
			return out
		}
		if err != nil {
			t.Fatalf("nextLazy: %v", err)
		}
		row := make([]Datum, slot.Width())
		for i := range row {
			row[i] = slot.Get(i)
		}
		out = append(out, row)
	}
}

// formatChainRows renders emitted tuples in a form the expectations can be
// read literally: `1|p1|NULL`. datumKey's canonical form ("m:1:0") is a hash
// key, not a value rendering, and hides which column carries what.
func formatChainRows(rows [][]Datum) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		s := ""
		for j, d := range r {
			if j > 0 {
				s += "|"
			}
			switch {
			case d.IsNull():
				s += "NULL"
			case d.Kind == KindString:
				s += d.StringValue()
			default:
				s += fmt.Sprint(d.Int)
			}
		}
		out[i] = s
	}
	return out
}

func assertRows(t *testing.T, got [][]Datum, want []string) {
	t.Helper()
	gotS := formatChainRows(got)
	if len(gotS) != len(want) {
		t.Fatalf("emitted %d rows %v, want %d %v", len(gotS), gotS, len(want), want)
	}
	for i := range want {
		if gotS[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (all rows: %v)", i, gotS[i], want[i], gotS)
		}
	}
}

// eachShape runs fn for every probe-child slot shape, including the rotating
// one — the F7 case the seam must survive.
func eachShape(t *testing.T, fn func(t *testing.T, shape probeSlotShape)) {
	t.Helper()
	for _, shape := range []probeSlotShape{
		probeShapeReusedBuffer, probeShapeSharedVirtual, probeShapeFreshSlot, probeShapeRotating,
	} {
		t.Run(shape.String(), func(t *testing.T) { fn(t, shape) })
	}
}

// TestProbeChainFanOut is 0126-0004 §3's mandatory fan-out test: one probe row
// with SEVERAL matches. Every emitted tuple must carry that probe row's own
// columns, so a seam that let the probe source drift between matches (or that
// re-bound it from a stale flattened Row) shows up as duplicated or shifted
// values rather than as a row-count change.
func TestProbeChainFanOut(t *testing.T) {
	eachShape(t, func(t *testing.T, shape probeSlotShape) {
		probe := &shapedProbeOp{
			schema: chainSchema(2, 0),
			shape:  shape,
			rows: []Row{
				{NewIntDatum(1), NewStringDatum("p1")},
				{NewIntDatum(2), NewStringDatum("p2")},
			},
		}
		build := []Row{
			{NewIntDatum(1), NewStringDatum("b1a")},
			{NewIntDatum(1), NewStringDatum("b1b")},
			{NewIntDatum(1), NewStringDatum("b1c")},
			{NewIntDatum(2), NewStringDatum("b2a")},
		}
		o := chainFixture(planner.JoinTypeInner, probe, build, 2, 2)
		if err := probe.Open(nil); err != nil {
			t.Fatalf("open probe: %v", err)
		}
		assertRows(t, drainJoin(t, o), []string{
			"1|p1|1|b1a",
			"1|p1|1|b1b",
			"1|p1|1|b1c",
			"2|p2|2|b2a",
		})
	})
}

// TestProbeChainBindsChildSlot proves the seam actually chains rather than
// silently falling back to the copy — otherwise every other test here would
// still pass against the pre-P1.1 code and prove nothing.
func TestProbeChainBindsChildSlot(t *testing.T) {
	probe := &shapedProbeOp{
		schema: chainSchema(2, 0),
		shape:  probeShapeSharedVirtual,
		rows:   []Row{{NewIntDatum(1), NewStringDatum("p1")}},
	}
	o := chainFixture(planner.JoinTypeInner, probe, []Row{{NewIntDatum(1), NewStringDatum("b1")}}, 2, 2)
	if err := probe.Open(nil); err != nil {
		t.Fatalf("open probe: %v", err)
	}
	if _, err := o.nextLazy(); err != nil {
		t.Fatalf("nextLazy: %v", err)
	}
	if o.lazyProbeSrc != TupleSlot(probe.shared) {
		t.Fatalf("probe source is %T, want the child's own *VirtualSlot — the seam fell back to the copy", o.lazyProbeSrc)
	}
	if o.lazyVirtualOut.sources[o.lazyProbeSrcIdx] != TupleSlot(probe.shared) {
		t.Fatalf("lazyVirtualOut probe source was not rebound to the child's slot")
	}
	if o.lazyProbeSlot.row != nil {
		t.Fatalf("copy fallback ran: lazyProbeSlot still holds a flattened Row")
	}
}

// TestProbeChainKillSwitch pins GOOPG_JOIN_SLOT_CHAIN=off: the copy path must
// still be reachable and must produce identical output, so the switch is a real
// rollback and not a placebo.
func TestProbeChainKillSwitch(t *testing.T) {
	SetJoinSlotChainEnabled(false)
	defer SetJoinSlotChainEnabled(true)

	eachShape(t, func(t *testing.T, shape probeSlotShape) {
		probe := &shapedProbeOp{
			schema: chainSchema(2, 0),
			shape:  shape,
			rows: []Row{
				{NewIntDatum(1), NewStringDatum("p1")},
				{NewIntDatum(2), NewStringDatum("p2")},
			},
		}
		build := []Row{
			{NewIntDatum(1), NewStringDatum("b1a")},
			{NewIntDatum(1), NewStringDatum("b1b")},
			{NewIntDatum(2), NewStringDatum("b2a")},
		}
		o := chainFixture(planner.JoinTypeInner, probe, build, 2, 2)
		if err := probe.Open(nil); err != nil {
			t.Fatalf("open probe: %v", err)
		}
		got := drainJoin(t, o)
		assertRows(t, got, []string{"1|p1|1|b1a", "1|p1|1|b1b", "2|p2|2|b2a"})
		if o.lazyProbeSrc != TupleSlot(o.lazyProbeSlot) {
			t.Fatalf("kill switch off but the probe source is %T — chaining still ran", o.lazyProbeSrc)
		}
	})
}

// TestProbeChainSemiAnti covers the outer-only emit slot: Semi/Anti return the
// probe row alone, which P1.1 composes through lazyOuterOnlyOut instead of
// flattening. Output width must stay len(o.schema) = the left width.
func TestProbeChainSemiAnti(t *testing.T) {
	build := []Row{{NewIntDatum(1), NewStringDatum("b1")}, {NewIntDatum(1), NewStringDatum("b1-dup")}}
	rows := []Row{
		{NewIntDatum(1), NewStringDatum("p1")},
		{NewIntDatum(2), NewStringDatum("p2")},
		{NewIntDatum(1), NewStringDatum("p3")},
	}
	for _, tc := range []struct {
		name string
		jt   planner.JoinType
		want []string
	}{
		{"semi", planner.JoinTypeSemi, []string{"1|p1", "1|p3"}},
		{"anti", planner.JoinTypeAnti, []string{"2|p2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eachShape(t, func(t *testing.T, shape probeSlotShape) {
				probe := &shapedProbeOp{schema: chainSchema(2, 0), shape: shape, rows: rows}
				o := chainFixture(tc.jt, probe, build, 2, 2)
				if err := probe.Open(nil); err != nil {
					t.Fatalf("open probe: %v", err)
				}
				assertRows(t, drainJoin(t, o), tc.want)
			})
		})
	}
}

// TestProbeChainOuterOnlyWidthFallback covers the one branch where the width
// test changes an OBSERVABLE result rather than merely guarding: the
// outer-only slot IS the whole emitted tuple, so its width is the tuple's
// width. Pre-P1.1, a probe child wider than o.schema emitted all of its
// columns (slotRow handed the full Row to lazyOuterOnlySlot); the chained
// slot's column map would silently narrow it to len(o.schema). P1.1 is a seam
// rewrite, not a semantics change, so the wider case falls back to the copy
// and keeps the old width. (For the composed INNER output the column map
// already fixed the width before and after, so a wider probe changes nothing
// there.)
func TestProbeChainOuterOnlyWidthFallback(t *testing.T) {
	probe := &shapedProbeOp{
		schema: chainSchema(3, 0),
		shape:  probeShapeSharedVirtual,
		rows: []Row{
			{NewIntDatum(1), NewStringDatum("p1"), NewStringDatum("extra")},
			{NewIntDatum(2), NewStringDatum("p2"), NewStringDatum("extra")},
		},
	}
	o := chainFixture(planner.JoinTypeSemi, probe, []Row{{NewIntDatum(1)}}, 2, 1)
	if err := probe.Open(nil); err != nil {
		t.Fatalf("open probe: %v", err)
	}
	got := drainJoin(t, o)
	assertRows(t, got, []string{"1|p1|extra"})
	if o.lazyOuterOnlySlot.row == nil {
		t.Fatalf("expected the copy fallback to run for the over-wide probe slot")
	}
}

// TestProbeChainLeftJoinNullPad covers both LEFT null-padding exits: the
// hash-level miss and the predicate-level one (every candidate filtered out).
// Both compose [probe, NULL] from the still-bound probe source, so a seam that
// dropped the binding when it stopped flattening would emit NULLs on the probe
// side too.
func TestProbeChainLeftJoinNullPad(t *testing.T) {
	eachShape(t, func(t *testing.T, shape probeSlotShape) {
		probe := &shapedProbeOp{
			schema: chainSchema(2, 0),
			shape:  shape,
			rows: []Row{
				{NewIntDatum(1), NewStringDatum("p1")}, // matches
				{NewIntDatum(9), NewStringDatum("p9")}, // hash-level miss
			},
		}
		o := chainFixture(planner.JoinTypeLeft, probe, []Row{{NewIntDatum(1), NewStringDatum("b1")}}, 2, 2)
		if err := probe.Open(nil); err != nil {
			t.Fatalf("open probe: %v", err)
		}
		assertRows(t, drainJoin(t, o), []string{"1|p1|1|b1", "9|p9|NULL|NULL"})
	})

	// Predicate-level miss: the hash key matches but the residual conjunct
	// (probe col 1 = build col 1) rejects every candidate.
	probe := &shapedProbeOp{
		schema: chainSchema(2, 0),
		shape:  probeShapeSharedVirtual,
		rows:   []Row{{NewIntDatum(1), NewStringDatum("p1")}},
	}
	o := chainFixture(planner.JoinTypeLeft, probe, []Row{{NewIntDatum(1), NewStringDatum("other")}}, 2, 2)
	o.plan.Predicate = &planner.BinaryOp{
		Op:    parser.OpEq,
		Left:  &planner.ColumnRef{Index: 1},
		Right: &planner.ColumnRef{Index: 3},
	}
	if err := probe.Open(nil); err != nil {
		t.Fatalf("open probe: %v", err)
	}
	assertRows(t, drainJoin(t, o), []string{"1|p1|NULL|NULL"})
}

// TestProbeChainRebindAssertion pins the lifetime invariant the aliasing rests
// on: a probe row is pulled only after every match of the previous one has been
// drained. Nothing in the type system enforces it, so bindProbe checks it —
// this test drives the violation directly.
func TestProbeChainRebindAssertion(t *testing.T) {
	probe := &shapedProbeOp{
		schema: chainSchema(2, 0),
		shape:  probeShapeSharedVirtual,
		rows:   []Row{{NewIntDatum(1), NewStringDatum("p1")}},
	}
	o := chainFixture(planner.JoinTypeInner, probe, []Row{{NewIntDatum(1), NewStringDatum("b1")}}, 2, 2)
	if err := probe.Open(nil); err != nil {
		t.Fatalf("open probe: %v", err)
	}
	o.ensureLazyVirtual()
	o.lazyActive = true
	if _, err := o.bindProbe(SlotFromRow(o.schema, Row{NewIntDatum(7), NewStringDatum("x")})); err == nil {
		t.Fatalf("bindProbe accepted a rebind while matches were draining")
	}
}

// seamProbeRows is the benchmark/alloc-guard workload: one match per probe row
// so every pull crosses the seam exactly once.
const seamProbeRows = 1024

// newSeamFixture builds the INNER join the seam measurements run over, on the
// P0.3 int64 key lane. The lane matters for the measurement, not the seam: the
// string lane allocates a datumKey per probe row, which would sit on top of the
// seam's own cost and make "0 allocs at the seam" unreadable.
func newSeamFixture() (*shapedProbeOp, *joinOp) {
	rows := make([]Row, seamProbeRows)
	for i := range rows {
		rows[i] = Row{NewIntDatum(int64(i % 8)), NewStringDatum("payload"), NewIntDatum(int64(i))}
	}
	build := make([]Row, 8)
	for i := range build {
		build[i] = Row{NewIntDatum(int64(i)), NewStringDatum("b")}
	}
	probe := &shapedProbeOp{schema: chainSchema(3, 0), shape: probeShapeSharedVirtual, rows: rows}
	o := chainFixture(planner.JoinTypeInner, probe, build, 3, 2)
	o.lazyHash = nil
	o.lazyHashIsInt = true
	o.lazyIntHash = map[int64][]Row{}
	for _, br := range build {
		o.lazyIntHash[br[0].Int] = append(o.lazyIntHash[br[0].Int], br)
	}
	return probe, o
}

// drainSeam runs one full probe pass over the fixture.
func drainSeam(probe *shapedProbeOp, o *joinOp) error {
	if err := probe.Open(nil); err != nil {
		return err
	}
	o.lazyActive = false
	o.lazyMatches = nil
	o.lazyMatchIdx = 0
	for {
		_, err := o.nextLazy()
		if err == EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// TestProbeSeamZeroAllocs is the stage gate from IMPLEMENTATION-TODO P1.1:
// the seam must allocate NOTHING per probe row in steady state. Pre-P1.1 each
// pull paid VirtualSlot.Row()'s pooled acquireRow plus a width-wide 48-byte
// Datum copy, and the pooled row was never released.
func TestProbeSeamZeroAllocs(t *testing.T) {
	probe, o := newSeamFixture()
	// Warm the per-Open buffers (null padding rows, the composed slot) so
	// the measured passes see steady state.
	if err := drainSeam(probe, o); err != nil {
		t.Fatalf("warmup: %v", err)
	}
	var failed error
	got := testing.AllocsPerRun(4, func() {
		if err := drainSeam(probe, o); err != nil {
			failed = err
		}
	})
	if failed != nil {
		t.Fatalf("drain: %v", failed)
	}
	if got != 0 {
		t.Fatalf("%.1f allocs per pass over %d probe rows, want 0", got, seamProbeRows)
	}
}

// BenchmarkProbeSeam measures the seam itself: one probe row per pull against a
// *VirtualSlot child — the shape every interior seam of a left-deep chain has
// under the 02 plan-shape contract. The `off` arm is the pre-P1.1 seam, kept
// runnable by the kill switch so the delta stays reproducible.
func BenchmarkProbeSeam(b *testing.B) {
	run := func(b *testing.B, chained bool) {
		SetJoinSlotChainEnabled(chained)
		defer SetJoinSlotChainEnabled(true)
		probe, o := newSeamFixture()
		b.ReportAllocs()
		for b.Loop() {
			if err := drainSeam(probe, o); err != nil {
				b.Fatalf("drain: %v", err)
			}
		}
	}
	b.Run("chained", func(b *testing.B) { run(b, true) })
	b.Run("off", func(b *testing.B) { run(b, false) })
}
