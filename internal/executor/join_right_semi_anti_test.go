package executor

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// M0146-0005dj — PG 18's JOIN_RIGHT_SEMI / JOIN_RIGHT_ANTI on the hash join.
//
// The semi join's preserved rows are the BUILD (right, hashed) input. RIGHT
// SEMI emits each build row exactly once, on its first match against a probe
// row (nodeHashjoin.c's HeapTupleHeaderHasMatch / SetMatch under
// JOIN_RIGHT_SEMI); RIGHT ANTI emits, from the post-probe sweep, every build
// row no probe row matched — NULL-keyed build rows included, since equality is
// never true against NULL. Both publish the build side only.
//
// Each case is checked against a reference computed in Go, in memory and under
// a work_mem small enough to force batching (the per-batch matched bitmap and
// the per-batch sweep are where a row could be lost or duplicated).
func TestHashRightSemiAntiJoinEmitsBuildRows(t *testing.T) {
	const lw, rw = 2, 2
	// Duplicate keys on both sides: a build row with several matching probe
	// rows must still come out once under RIGHT SEMI.
	buildRows := intKeyRows(4000, 900, "b")
	probeRows := intKeyRows(3000, 500, "p") // keys 0..499 only
	buildRows = append(buildRows, Row{NullDatum, NewStringDatum("null-key-build")})
	probeRows = append(probeRows, Row{NullDatum, NewStringDatum("null-key-probe")})

	probeKeys := map[string]bool{}
	for _, r := range probeRows {
		if !r[0].IsNull() {
			probeKeys[datumToString(r[0])] = true
		}
	}
	render := func(r Row) string {
		parts := make([]string, len(r))
		for i, d := range r {
			parts[i] = fmt.Sprint(datumToString(d))
		}
		return strings.Join(parts, "|")
	}
	var wantSemi, wantAnti []string
	for _, r := range buildRows {
		if !r[0].IsNull() && probeKeys[datumToString(r[0])] {
			wantSemi = append(wantSemi, render(r))
		} else {
			wantAnti = append(wantAnti, render(r))
		}
	}
	sort.Strings(wantSemi)
	sort.Strings(wantAnti)

	for _, c := range []struct {
		typ  optimizer.JoinType
		want []string
	}{
		{optimizer.JoinTypeRightSemi, wantSemi},
		{optimizer.JoinTypeRightAnti, wantAnti},
	} {
		for _, mem := range []int64{unboundedWorkMem, 256 << 10} {
			t.Run(fmt.Sprintf("type%d/workmem%d", c.typ, mem), func(t *testing.T) {
				plan := batchJoinPlan(lw, len(probeRows), len(buildRows))
				plan.Type = c.typ
				got, bs := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, mem)
				if mem != unboundedWorkMem && (bs == nil || bs.nbatch < 2) {
					t.Fatalf("precondition: the bounded arm did not batch")
				}
				assertSameRows(t, c.want, got)
			})
		}
	}
}

// A residual qual decides the match, as for every hash join: a key hit the
// residual rejects neither emits (RIGHT SEMI) nor suppresses (RIGHT ANTI) the
// build row.
func TestHashRightSemiAntiHonourResidual(t *testing.T) {
	const lw, rw = 2, 2
	buildRows := []Row{
		{NewIntDatum(1), NewIntDatum(10)},
		{NewIntDatum(2), NewIntDatum(20)},
		{NewIntDatum(3), NewIntDatum(30)},
	}
	probeRows := []Row{
		{NewIntDatum(1), NewIntDatum(5)},  // l1 < r1: match
		{NewIntDatum(2), NewIntDatum(50)}, // l1 > r1: residual rejects
		{NewIntDatum(1), NewIntDatum(6)},  // second match for build key 1
	}
	for _, c := range []struct {
		typ  optimizer.JoinType
		want []string
	}{
		{optimizer.JoinTypeRightSemi, []string{"1|10"}},
		{optimizer.JoinTypeRightAnti, []string{"2|20", "3|30"}},
	} {
		plan := batchJoinPlan(lw, len(probeRows), len(buildRows))
		plan.Type = c.typ
		col := func(i int) *optimizer.ColumnRef {
			return &optimizer.ColumnRef{Index: i, Type: catalog.Type{Name: "int4"}}
		}
		plan.Predicate = &optimizer.BinaryOp{Op: parser.OpAnd,
			Left:  &optimizer.BinaryOp{Op: parser.OpEq, Left: col(0), Right: col(lw)},
			Right: &optimizer.BinaryOp{Op: parser.OpLt, Left: col(1), Right: col(lw + 1)}}
		got, _ := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, unboundedWorkMem)
		assertSameRows(t, c.want, got)
	}
}

// EXPLAIN names them as PG does.
func TestRightSemiAntiJoinLabels(t *testing.T) {
	if got := joinLabel("Hash", optimizer.JoinTypeRightSemi); got != "Hash Right Semi Join" {
		t.Errorf("right semi label %q", got)
	}
	if got := joinLabel("Hash", optimizer.JoinTypeRightAnti); got != "Hash Right Anti Join" {
		t.Errorf("right anti label %q", got)
	}
}
