package executor

// join_merge_stream_test.go — M0127-P4.1 guards (design leftdeep-joins/07 §2).
//
// The slice replaces three materialisations (both drained sides, both keyed
// arrays, the whole output) with two work_mem-bounded streams and a state
// machine. Three properties have to hold, and none of them is a row count on
// its own:
//
//   - FORCED-SPILL IDENTITY. Every path the budget opens — sorted runs on
//     both sides, the N-way merge that reads them back, the inner group's
//     overflow file, the matched bitmap indexed across that file — must
//     produce the byte-identical row SEQUENCE the unbounded run produces.
//     This is P3.5's hash-side test applied to merge: the operator's answer
//     may not depend on how much memory it was given. Run over all four join
//     types and both residual regimes, it is also the only test that covers
//     the overflow replay at all.
//   - OUTPUT DOES NOT ACCUMULATE. `o.rows` was the merge join's output
//     buffer; a 40,000-row join must now leave it empty at every point in
//     the drain, or the streaming is cosmetic.
//   - THE SPILL FILES ARE RELEASED. A merge join that spilled must leave the
//     statement's temp-file registry empty at Close, not at statement end
//     (the M0127-P3.3 rule).

import (
	"os"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/pgtemp"
	"github.com/goopg/goopg/internal/planner"
)

func mergeStreamCol(idx int) *planner.ColumnRef {
	return &planner.ColumnRef{Index: idx, Type: catalog.Type{Name: "int4"}}
}

// mergeStreamPlan is a single-key merge join over width-2 rows: column 0 is
// the key, column 1 the payload. residual is the ON-clause remainder (nil =
// the all-equijoin steady state P2.3 produces).
func mergeStreamPlan(jt planner.JoinType, leftWidth int, residual planner.Expr) *planner.Join {
	return &planner.Join{
		Type:      jt,
		Algo:      planner.JoinAlgoMerge,
		LeftKey:   mergeStreamCol(0),
		RightKey:  mergeStreamCol(leftWidth),
		HashKeys:  []planner.JoinKeyPair{{Left: mergeStreamCol(0), Right: mergeStreamCol(leftWidth)}},
		Predicate: residual,
	}
}

func mergeStreamRows(pairs ...[2]int64) []Row {
	out := make([]Row, 0, len(pairs))
	for _, p := range pairs {
		key := NewIntDatum(p[0])
		if p[0] < 0 {
			// Negative key stands for NULL — the componentwise rule's row,
			// which matches nothing and must be emitted LAST for an outer
			// side (the ordering buildMergeSide got from its side list).
			key = NullDatum
		}
		out = append(out, Row{key, NewIntDatum(p[1])})
	}
	return out
}

// The two sides are deliberately unsorted on input and cover every arm of the
// state machine: a 4-row inner group (key 2) for the overflow file, left-only
// keys (1, 4), a right-only key (3), one-to-one matches (5, 7), and a NULL key
// on each side.
func mergeStreamLeft() []Row {
	return mergeStreamRows(
		[2]int64{5, 50}, [2]int64{2, 20}, [2]int64{7, 70}, [2]int64{2, 21},
		[2]int64{1, 10}, [2]int64{-1, 60}, [2]int64{4, 40},
	)
}

func mergeStreamRight() []Row {
	return mergeStreamRows(
		[2]int64{2, 200}, [2]int64{3, 300}, [2]int64{2, 201}, [2]int64{5, 500},
		[2]int64{-1, 600}, [2]int64{7, 700}, [2]int64{2, 202}, [2]int64{2, 203},
	)
}

// mergeStreamResidual rejects the left row payload 21 and the inner row
// payload 201 outright, so inside key 2's group one outer row matches nothing
// (the intra-group MJFillOuter arm) and one inner row is matched by nobody
// (the RIGHT/FULL sweep arm) — the two arms an all-equijoin join never reaches.
func mergeStreamResidual() planner.Expr {
	return &planner.BinaryOp{
		Op: parser.OpAnd,
		Left: &planner.BinaryOp{
			Op: parser.OpNe, Left: mergeStreamCol(1), Right: &planner.IntegerConst{Value: 21},
		},
		Right: &planner.BinaryOp{
			Op: parser.OpNe, Left: mergeStreamCol(3), Right: &planner.IntegerConst{Value: 201},
		},
	}
}

func runMergeStream(t *testing.T, jt planner.JoinType, residual planner.Expr, workMem int64) []string {
	t.Helper()
	o := &joinOp{
		plan:  mergeStreamPlan(jt, 2, residual),
		left:  &rowsOp{rows: mergeStreamLeft()},
		right: &rowsOp{rows: mergeStreamRight()},
	}
	ctx := NewContext()
	ctx.DataDir = t.TempDir()
	ctx.WorkMem = workMem
	defer ctx.ReleaseSpillFiles()
	out, err := Run(o, ctx)
	if err != nil {
		t.Fatalf("merge join (work_mem=%d): %v", workMem, err)
	}
	return formatRows(out)
}

// TestMergeJoinSpilledRunIsIdenticalToResident is the P4.1 exit property: the
// answer, and its order, may not depend on the memory budget. work_mem=1 puts
// every input row in its own sorted run AND overflows the inner group after
// its first row, so the byte-identical comparison covers the run merge, the
// group replay and the matched bitmap across it in one assertion.
func TestMergeJoinSpilledRunIsIdenticalToResident(t *testing.T) {
	types := []struct {
		name string
		jt   planner.JoinType
	}{
		{"inner", planner.JoinTypeInner},
		{"left", planner.JoinTypeLeft},
		{"right", planner.JoinTypeRight},
		{"full", planner.JoinTypeFull},
	}
	for _, tc := range types {
		for _, withResidual := range []bool{false, true} {
			name := tc.name
			if withResidual {
				name += "/residual"
			}
			t.Run(name, func(t *testing.T) {
				var residual planner.Expr
				if withResidual {
					residual = mergeStreamResidual()
				}
				resident := runMergeStream(t, tc.jt, residual, 0)
				spilled := runMergeStream(t, tc.jt, residual, 1)
				if len(resident) != len(spilled) {
					t.Fatalf("work_mem=1 emitted %d row(s), unbounded emitted %d\n  spilled:  %v\n  resident: %v",
						len(spilled), len(resident), spilled, resident)
				}
				for i := range resident {
					if resident[i] != spilled[i] {
						t.Fatalf("row %d differs: work_mem=1 %q, unbounded %q\n  spilled:  %v\n  resident: %v",
							i, spilled[i], resident[i], spilled, resident)
					}
				}
				if len(resident) == 0 {
					t.Fatalf("%s join emitted nothing — the fixture stopped exercising the join", name)
				}
			})
		}
	}
}

// TestMergeJoinInnerAnswersArePGShaped pins the actual row sets, so the
// identity test above cannot pass by being uniformly wrong. The expected sets
// are derived from the fixture by hand and match what the array
// implementation emitted before P4.1.
func TestMergeJoinInnerAnswersArePGShaped(t *testing.T) {
	cases := []struct {
		name string
		jt   planner.JoinType
		want []string
	}{
		{
			name: "inner",
			jt:   planner.JoinTypeInner,
			want: []string{
				"2,20,2,200", "2,20,2,201", "2,20,2,202", "2,20,2,203",
				"2,21,2,200", "2,21,2,201", "2,21,2,202", "2,21,2,203",
				"5,50,5,500",
				"7,70,7,700",
			},
		},
		{
			name: "left",
			jt:   planner.JoinTypeLeft,
			want: []string{
				"1,10,,",
				"2,20,2,200", "2,20,2,201", "2,20,2,202", "2,20,2,203",
				"2,21,2,200", "2,21,2,201", "2,21,2,202", "2,21,2,203",
				"4,40,,",
				"5,50,5,500",
				"7,70,7,700",
				// The NULL-keyed outer row is emitted last, after every
				// real-key row — buildMergeSide's side-list ordering.
				",60,,",
			},
		},
		{
			// A right-only key and the NULL-keyed inner row are
			// null-extended on the LEFT; the NULL-keyed one comes last.
			name: "right",
			jt:   planner.JoinTypeRight,
			want: []string{
				"2,20,2,200", "2,20,2,201", "2,20,2,202", "2,20,2,203",
				"2,21,2,200", "2,21,2,201", "2,21,2,202", "2,21,2,203",
				",,3,300",
				"5,50,5,500",
				"7,70,7,700",
				",,,600",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runMergeStream(t, tc.jt, nil, 0)
			if len(got) != len(tc.want) {
				t.Fatalf("emitted %d row(s), want %d\n  got:  %v\n  want: %v",
					len(got), len(tc.want), got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("row %d = %q, want %q\n  got:  %v\n  want: %v",
						i, got[i], tc.want[i], got, tc.want)
				}
			}
		})
	}
}

// TestMergeJoinDoesNotAccumulateOutput is the structural half of the slice.
// One key, 200 rows a side: the join emits 40,000 rows, which the array
// implementation held in o.rows all at once before Next returned its first
// tuple. The streaming operator must leave that buffer empty for the whole
// drain.
func TestMergeJoinDoesNotAccumulateOutput(t *testing.T) {
	const n = 200
	left := make([]Row, 0, n)
	right := make([]Row, 0, n)
	for i := 0; i < n; i++ {
		left = append(left, Row{NewIntDatum(1), NewIntDatum(int64(i))})
		right = append(right, Row{NewIntDatum(1), NewIntDatum(int64(i))})
	}
	o := &joinOp{
		plan:  mergeStreamPlan(planner.JoinTypeInner, 2, nil),
		left:  &rowsOp{rows: left},
		right: &rowsOp{rows: right},
	}
	ctx := NewContext()
	ctx.DataDir = t.TempDir()
	defer ctx.ReleaseSpillFiles()
	if err := o.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(o.rows) != 0 {
		t.Fatalf("Open pre-computed %d output row(s); the join must not join before Next asks", len(o.rows))
	}
	emitted := 0
	for {
		slot, err := o.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		_ = slot
		emitted++
		if len(o.rows) != 0 {
			t.Fatalf("after %d row(s) the join had accumulated %d row(s) in o.rows", emitted, len(o.rows))
		}
	}
	if emitted != n*n {
		t.Fatalf("emitted %d row(s), want %d", emitted, n*n)
	}
	if err := o.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestMergeJoinReleasesSpillFilesOnClose — every file the budget forced to
// disk (sorted runs on both sides, the inner group's overflow) is unlinked and
// deregistered by Close, not left for the statement-end sweep (M0127-P3.3).
func TestMergeJoinReleasesSpillFilesOnClose(t *testing.T) {
	o := &joinOp{
		plan:  mergeStreamPlan(planner.JoinTypeFull, 2, mergeStreamResidual()),
		left:  &rowsOp{rows: mergeStreamLeft()},
		right: &rowsOp{rows: mergeStreamRight()},
	}
	ctx := NewContext()
	ctx.DataDir = t.TempDir()
	ctx.WorkMem = 1
	if _, err := Run(o, ctx); err != nil {
		t.Fatalf("merge join: %v", err)
	}
	dir := pgtemp.Dir(ctx.DataDir)
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("Close left %d spill file(s) on disk: %v", len(entries), names)
	}
	if n := ctx.ReleaseSpillFiles(); n != 0 {
		t.Errorf("ReleaseSpillFiles removed %d file(s) after Close, want 0 — Close did not deregister", n)
	}
}

// TestMergeJoinSpillsInnerGroupPastWorkMem proves the overflow file is really
// reached rather than the whole group quietly fitting: with a one-byte budget
// a 4-row inner group keeps exactly one row resident and writes the other
// three out.
func TestMergeJoinSpillsInnerGroupPastWorkMem(t *testing.T) {
	o := &joinOp{
		plan:  mergeStreamPlan(planner.JoinTypeInner, 2, nil),
		left:  &rowsOp{rows: mergeStreamLeft()},
		right: &rowsOp{rows: mergeStreamRight()},
	}
	ctx := NewContext()
	ctx.DataDir = t.TempDir()
	ctx.WorkMem = 1
	defer ctx.ReleaseSpillFiles()
	if err := o.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer o.Close()
	sawOverflow := false
	for {
		slot, err := o.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		_ = slot
		m := o.mergeStream
		if m.groupCount > len(m.groupMem) {
			sawOverflow = true
			if len(m.groupMem) != 1 {
				t.Fatalf("group kept %d row(s) resident under a 1-byte budget, want exactly 1", len(m.groupMem))
			}
		}
	}
	if !sawOverflow {
		t.Fatal("no inner group overflowed to disk — the budget is not reaching bufferGroup")
	}
}
