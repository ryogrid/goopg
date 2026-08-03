package executor

// M0127-P4.3 — Materialize + the streaming nested loop (design
// leftdeep-joins/07 §4).
//
// Two claims are worth testing separately, because they can each be wrong on
// their own:
//
//  1. Materialize replays its child's output without re-executing the child,
//     across the memory path AND the work_mem overflow path, including the
//     PG `eof_underlying` case where the first pass stopped early.
//  2. The nested loop now consumes its outer side lazily and its inner side
//     exactly once, while emitting exactly what the drain-both implementation
//     emitted.
//
// Claim 2's *output* is also covered from the other direction and much more
// broadly: join_outer_fill_test.go uses the nested loop as the ORACLE for the
// hash path across every outer-join type and both build orientations, so any
// semantic drift here fails there too. What those tests cannot see is HOW the
// rows were produced — that is what the counting operator below is for.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// countingOp wraps a rowsOp and records how many times Open and Next were
// called on it. It is the only way to distinguish "replayed from a cache" from
// "re-executed the subtree", which is the whole point of Materialize.
type countingOp struct {
	inner  *rowsOp
	opens  int
	nexts  int
	closes int
}

func newCountingOp(rows []Row, schema planner.Schema) *countingOp {
	return &countingOp{inner: &rowsOp{rows: rows, schema: schema}}
}

func (c *countingOp) Open(ctx *Context) error { c.opens++; return c.inner.Open(ctx) }
func (c *countingOp) Schema() planner.Schema  { return c.inner.Schema() }
func (c *countingOp) Next() (TupleSlot, error) { //nolint:ireturn
	c.nexts++
	return c.inner.Next()
}
func (c *countingOp) Close() error { c.closes++; return c.inner.Close() }

// seqRows builds n rows of [i, "p<i>"] with a payload long enough that a small
// work_mem really does force the overflow file.
func seqRows(n int, payload string) []Row {
	rows := make([]Row, n)
	for i := range rows {
		rows[i] = Row{NewIntDatum(int64(i)), NewStringDatum(fmt.Sprintf("%s%d-%s", payload, i, strings.Repeat("x", 64)))}
	}
	return rows
}

// readAll drains an operator into rendered strings, preserving emission order.
func readAll(t *testing.T, op Operator) []string {
	t.Helper()
	var out []string
	for {
		slot, err := op.Next()
		if err == EOF {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		parts := make([]string, slot.Width())
		for i := range parts {
			parts[i] = fmt.Sprint(datumToString(slot.Get(i)))
		}
		out = append(out, strings.Join(parts, "|"))
	}
}

// Materialize replays the identical sequence on every pass, and the child is
// read exactly once (n Next calls plus the one that returns EOF), whether the
// cache stayed resident or overflowed to disk.
func TestMaterializeReplaysWithoutReExecutingChild(t *testing.T) {
	const n = 200
	for _, tc := range []struct {
		name    string
		workMem int64
		spills  bool
	}{
		{"resident", 0, false},
		{"overflow", 512, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := newCountingOp(seqRows(n, "m"), batchSchema("c", 2))
			mat := newMaterializeOp(child)
			ctx := &Context{WorkMem: tc.workMem}
			if err := mat.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			defer mat.Close()

			first := readAll(t, mat)
			if len(first) != n {
				t.Fatalf("first pass: %d rows, want %d", len(first), n)
			}
			for pass := 2; pass <= 4; pass++ {
				if err := mat.Rescan(); err != nil {
					t.Fatalf("rescan %d: %v", pass, err)
				}
				got := readAll(t, mat)
				if len(got) != len(first) {
					t.Fatalf("pass %d: %d rows, want %d", pass, len(got), len(first))
				}
				for i := range got {
					if got[i] != first[i] {
						t.Fatalf("pass %d row %d = %q, want %q", pass, i, got[i], first[i])
					}
				}
			}
			// n rows plus the EOF call: the child was read once, not four times.
			if child.nexts != n+1 {
				t.Errorf("child Next called %d times, want %d (the cache should have served passes 2-4)", child.nexts, n+1)
			}
			if child.opens != 1 {
				t.Errorf("child opened %d times, want 1", child.opens)
			}
			if spilled := mat.buf.path != ""; spilled != tc.spills {
				t.Errorf("spilled=%v, want %v (work_mem=%d)", spilled, tc.spills, tc.workMem)
			}
		})
	}
}

// PG's eof_underlying rule: a first pass that stopped short must not truncate
// the cache. A later pass runs past the stored rows and resumes reading the
// child. This is exactly what a keyless semi/anti join does — it breaks out of
// the inner scan on the first qualifying tuple.
func TestMaterializePartialFirstPassResumesChild(t *testing.T) {
	const n, stop = 50, 3
	for _, workMem := range []int64{0, 256} {
		t.Run(fmt.Sprintf("workmem%d", workMem), func(t *testing.T) {
			child := newCountingOp(seqRows(n, "m"), batchSchema("c", 2))
			mat := newMaterializeOp(child)
			if err := mat.Open(&Context{WorkMem: workMem}); err != nil {
				t.Fatalf("open: %v", err)
			}
			defer mat.Close()

			for i := 0; i < stop; i++ {
				if _, err := mat.Next(); err != nil {
					t.Fatalf("short pass Next %d: %v", i, err)
				}
			}
			if err := mat.Rescan(); err != nil {
				t.Fatalf("rescan: %v", err)
			}
			full := readAll(t, mat)
			if len(full) != n {
				t.Fatalf("resumed pass: %d rows, want %d", len(full), n)
			}
			for i, got := range full {
				want := fmt.Sprintf("%d|", i)
				if !strings.HasPrefix(got, want) {
					t.Fatalf("resumed pass row %d = %q, want prefix %q", i, got, want)
				}
			}
			if child.nexts != n+1 {
				t.Errorf("child Next called %d times, want %d", child.nexts, n+1)
			}
		})
	}
}

// withNLInnerWorkMem turns the nested loop's inner-cache work_mem bound ON for
// the duration of a test. The bound ships OFF (see openNestedLoop: TPC-DS Q54
// showed a spilled inner cache is unaffordable until `cost_rescan` prices the
// replay), so the tests that assert the spill path is an identity have to ask
// for it explicitly — otherwise they would silently assert nothing.
func withNLInnerWorkMem(t *testing.T) {
	t.Helper()
	prev := nlInnerWorkMemEnabled
	nlInnerWorkMemEnabled = true
	t.Cleanup(func() { nlInnerWorkMemEnabled = prev })
}

// nlJoinPlan is a nested-loop join of the given type on (l0 = r0). The
// equality lives in Predicate because that is all a nested loop evaluates.
func nlJoinPlan(jt planner.JoinType, leftWidth int) *planner.Join {
	col := func(idx int) *planner.ColumnRef {
		return &planner.ColumnRef{Index: idx, Type: catalog.Type{Name: "int4"}}
	}
	return &planner.Join{
		Type:      jt,
		Algo:      planner.JoinAlgoNestedLoop,
		Predicate: &planner.BinaryOp{Op: parser.OpEq, Left: col(0), Right: col(leftWidth)},
		Left:      valuesNode(0),
		Right:     valuesNode(0),
	}
}

// The shape claim: after Open the outer side has not been touched, and over
// the whole join the inner side is read exactly once no matter how many outer
// tuples drive it.
func TestNestedLoopStreamsOuterAndReadsInnerOnce(t *testing.T) {
	const outerN, innerN = 40, 60
	outer := newCountingOp(seqRows(outerN, "l"), batchSchema("l", 2))
	inner := newCountingOp(seqRows(innerN, "r"), batchSchema("r", 2))
	o := newJoinOp(nlJoinPlan(planner.JoinTypeInner, 2), outer, inner)
	if err := o.Open(&Context{}); err != nil {
		t.Fatalf("open join: %v", err)
	}
	// The pre-P4.3 implementation ran the entire join inside Open, so both
	// children were at EOF by this line.
	if outer.nexts != 0 {
		t.Errorf("outer read %d tuples during Open; the outer side must stream", outer.nexts)
	}
	if inner.nexts != 0 {
		t.Errorf("inner read %d tuples during Open; nothing should run before Next", inner.nexts)
	}
	got := readAll(t, o)
	if len(got) != outerN {
		t.Fatalf("%d joined rows, want %d", len(got), outerN)
	}
	if inner.nexts != innerN+1 {
		t.Errorf("inner child Next called %d times, want %d — the Materialize should have replayed it for outer tuples 2..%d",
			inner.nexts, innerN+1, outerN)
	}
	if err := o.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// The overflow path has to produce the identical join. A work_mem that cannot
// hold the inner side forces the cache to disk and every outer tuple past the
// first replays it from the file.
func TestNestedLoopIdenticalWhenInnerCacheSpills(t *testing.T) {
	withNLInnerWorkMem(t)
	const outerN, innerN, lw, rw = 30, 80, 2, 2
	left := seqRows(outerN, "l")
	right := seqRows(innerN, "r")
	for _, jt := range []planner.JoinType{
		planner.JoinTypeInner, planner.JoinTypeLeft, planner.JoinTypeRight, planner.JoinTypeFull,
	} {
		t.Run(fmt.Sprint(jt), func(t *testing.T) {
			want, _ := runBatchJoin(t, nlJoinPlan(jt, lw), left, right, lw, rw, 0)
			got, _ := runBatchJoin(t, nlJoinPlan(jt, lw), left, right, lw, rw, 512)
			assertSameMultiset(t, "spilled inner cache", got, want)
		})
	}
}

// A RIGHT join whose outer side is empty still owes every inner tuple. The
// streaming loop never enters its inner phase there, so the sweep has to fill
// the cache itself — the one path where the join drains the Materialize
// directly.
func TestNestedLoopRightJoinOverEmptyOuter(t *testing.T) {
	withNLInnerWorkMem(t)
	const innerN, lw, rw = 12, 2, 2
	right := seqRows(innerN, "r")
	for _, workMem := range []int64{0, 256} {
		t.Run(fmt.Sprintf("workmem%d", workMem), func(t *testing.T) {
			got, _ := runBatchJoin(t, nlJoinPlan(planner.JoinTypeRight, lw), nil, right, lw, rw, workMem)
			if len(got) != innerN {
				t.Fatalf("%d rows, want %d (every right tuple, null-extended)", len(got), innerN)
			}
		})
	}
}

// Keyless semi/anti break out of the inner scan on the first qualifying tuple.
// The join must still be correct for the outer tuples that follow, which is
// the Materialize resume rule exercised through the join rather than directly.
func TestNestedLoopKeylessSemiAntiEarlyOut(t *testing.T) {
	withNLInnerWorkMem(t)
	const lw, rw = 2, 2
	// Left keys 0..9; right keys 0..4 only, so half the outer tuples match.
	left := seqRows(10, "l")
	right := seqRows(5, "r")
	for _, tc := range []struct {
		jt   planner.JoinType
		want int
	}{
		{planner.JoinTypeSemi, 5},
		{planner.JoinTypeAnti, 5},
	} {
		t.Run(fmt.Sprint(tc.jt), func(t *testing.T) {
			for _, workMem := range []int64{0, 128} {
				got, _ := runBatchJoin(t, nlJoinPlan(tc.jt, lw), left, right, lw, rw, workMem)
				if len(got) != tc.want {
					t.Fatalf("work_mem=%d: %d rows, want %d", workMem, len(got), tc.want)
				}
				for _, r := range got {
					// Semi/anti emit the OUTER tuple only, so the rendering
					// has exactly lw columns.
					if n := strings.Count(r, "|") + 1; n != lw {
						t.Fatalf("work_mem=%d: row %q has %d columns, want %d (outer-only schema)", workMem, r, n, lw)
					}
				}
			}
		})
	}
}
