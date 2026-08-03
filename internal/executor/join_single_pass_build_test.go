package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// M0127-P0.2 — the single-pass hash build (design leftdeep-joins/05 §4, stage
// E3).
//
// The build used to run in two passes: drainRowsBounded copied every build row
// into an owned []Row, and the build loop then re-iterated that operator (one
// MaterializedSlot allocated per row) to key and insert them. Folding the two
// together removes the re-iteration — but it also moves the owned-copy
// responsibility from drainRowsBounded into the build loop itself. That copy is
// the M0097-0058 / M0073-0004 aliasing class: producers are free to hand out the
// SAME Row buffer on every Next (and arena-backed Datums are invalidated by the
// producer's next Reset), so a build row retained without copying silently
// degrades to "every hash entry holds the last row's values" — a wrong-answer
// bug no plan-shape or row-count check would catch.
//
// These tests drive the build loops over a child that deliberately reuses one
// buffer, in both orientations and on both the INNER (int64-eligible) and the
// SEMI (string-map) lane, and assert the hash table holds the distinct values.

// bufferReuseOp is a child operator that models the reuse producers are allowed
// to perform: it copies each row into ONE shared buffer and returns a slot over
// that buffer, so the previously returned Row is clobbered on every Next.
type bufferReuseOp struct {
	schema planner.Schema
	rows   []Row
	i      int
	buf    Row
}

func (o *bufferReuseOp) Open(*Context) error    { o.i = 0; return nil }
func (o *bufferReuseOp) Schema() planner.Schema { return o.schema }
func (o *bufferReuseOp) Close() error           { return nil }

func (o *bufferReuseOp) Next() (TupleSlot, error) { //nolint:ireturn
	if o.i >= len(o.rows) {
		return nil, EOF
	}
	if o.buf == nil {
		o.buf = make(Row, len(o.rows[o.i]))
	}
	copy(o.buf, o.rows[o.i])
	o.i++
	return SlotFromRow(o.schema, o.buf), nil
}

// buildProbeRows is the build input shared by the tests: the key column is
// distinct per row so an aliased retention shows up as a wrong payload under a
// correct-looking key.
func buildProbeRows() []Row {
	return []Row{
		{NewIntDatum(1), NewStringDatum("one")},
		{NewIntDatum(2), NewStringDatum("two")},
		{NewIntDatum(3), NewStringDatum("three")},
	}
}

// assertBuildTable checks that every build row landed in the string map under
// its own key, with its own payload.
func assertBuildTable(t *testing.T, hash map[string][]Row, payloadCol int) {
	t.Helper()
	want := map[int64]string{1: "one", 2: "two", 3: "three"}
	if len(hash) != len(want) {
		t.Fatalf("hash table has %d keys, want %d", len(hash), len(want))
	}
	for k, payload := range want {
		got := hash[datumKey(NewIntDatum(k))]
		if len(got) != 1 {
			t.Fatalf("key %d: %d rows, want 1", k, len(got))
		}
		if v := got[0][payloadCol].StringValue(); v != payload {
			t.Fatalf("key %d: payload %q, want %q — the build row aliased the child's buffer", k, v, payload)
		}
		if v := got[0][payloadCol-1].Int; v != k {
			t.Fatalf("key %d: key column holds %d — the build row aliased the child's buffer", k, v)
		}
	}
}

func TestBuildLoopRightOwnsBuildRows(t *testing.T) {
	const leftWidth = 4 // the null side of the merged key column space

	for _, tc := range []struct {
		name     string
		joinType planner.JoinType
	}{
		{"inner", planner.JoinTypeInner},
		{"semi", planner.JoinTypeSemi}, // the string-map lane
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := &bufferReuseOp{rows: buildProbeRows()}
			o := &joinOp{
				plan: &planner.Join{
					Type: tc.joinType,
					Algo: planner.JoinAlgoHash,
					// Real columns sit at [leftWidth, leftWidth+2).
					RightKey: &planner.ColumnRef{Index: leftWidth},
				},
				right:  child,
				lazyRW: 2,
			}
			if err := child.Open(nil); err != nil {
				t.Fatalf("open child: %v", err)
			}
			if err := o.buildLoopRight(nil, leftWidth); err != nil {
				t.Fatalf("buildLoopRight: %v", err)
			}
			assertBuildTable(t, o.lazyHash, 1)
		})
	}
}

func TestBuildLoopLeftOwnsBuildRows(t *testing.T) {
	const rightWidth = 3 // the null side of the merged key column space

	child := &bufferReuseOp{rows: buildProbeRows()}
	o := &joinOp{
		plan: &planner.Join{
			Type: planner.JoinTypeInner,
			Algo: planner.JoinAlgoHash,
			// Real columns sit at [0, 2) for the BuildLeft orientation.
			LeftKey:   &planner.ColumnRef{Index: 0},
			BuildLeft: true,
		},
		left:   child,
		lazyLW: 2,
	}
	if err := child.Open(nil); err != nil {
		t.Fatalf("open child: %v", err)
	}
	if err := o.buildLoopLeft(nil, rightWidth); err != nil {
		t.Fatalf("buildLoopLeft: %v", err)
	}
	assertBuildTable(t, o.lazyHash, 1)
}

// TestBuildLoopRightNullKeyBookkeeping pins the NullAware counters the
// re-iteration used to maintain: a NULL key must be skipped (never inserted)
// while still counting towards antiBuildRows, and must raise antiBuildHasNull.
func TestBuildLoopRightNullKeyBookkeeping(t *testing.T) {
	const leftWidth = 1

	child := &bufferReuseOp{rows: []Row{
		{NewIntDatum(1), NewStringDatum("one")},
		{NullDatum, NewStringDatum("null-key")},
		{NewIntDatum(3), NewStringDatum("three")},
	}}
	o := &joinOp{
		plan: &planner.Join{
			Type:      planner.JoinTypeAnti,
			Algo:      planner.JoinAlgoHash,
			RightKey:  &planner.ColumnRef{Index: leftWidth},
			NullAware: true,
		},
		right:  child,
		lazyRW: 2,
	}
	if err := child.Open(nil); err != nil {
		t.Fatalf("open child: %v", err)
	}
	if err := o.buildLoopRight(nil, leftWidth); err != nil {
		t.Fatalf("buildLoopRight: %v", err)
	}
	if o.antiBuildRows != 3 {
		t.Fatalf("antiBuildRows = %d, want 3", o.antiBuildRows)
	}
	if !o.antiBuildHasNull {
		t.Fatalf("antiBuildHasNull = false, want true")
	}
	if len(o.lazyHash) != 2 {
		t.Fatalf("hash table has %d keys, want 2 (the NULL key must not be inserted)", len(o.lazyHash))
	}
}

// BenchmarkSinglePassBuild measures the build loop over a 4k-row child. The
// pre-P0.2 shape allocated, per row, one owned Row inside drainRowsBounded AND
// one MaterializedSlot when rowsOp re-emitted it; the single-pass shape keeps
// only the owned Row.
func BenchmarkSinglePassBuild(b *testing.B) {
	rows := make([]Row, 4096)
	for i := range rows {
		rows[i] = Row{NewIntDatum(int64(i)), NewStringDatum("payload")}
	}
	child := &bufferReuseOp{rows: rows}
	for b.Loop() {
		o := &joinOp{
			plan: &planner.Join{
				Type:     planner.JoinTypeInner,
				Algo:     planner.JoinAlgoHash,
				RightKey: &planner.ColumnRef{Index: 0},
			},
			right:  child,
			lazyRW: 2,
		}
		if err := child.Open(nil); err != nil {
			b.Fatal(err)
		}
		if err := o.buildLoopRight(nil, 0); err != nil {
			b.Fatal(err)
		}
	}
}
