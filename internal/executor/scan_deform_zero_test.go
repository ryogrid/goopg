package executor

// M0145-0008p: a scan under an Aggregate that consumes no child column
// (count(*)) deforms nothing. PG never calls slot_getsomeattrs for it.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestScanDeformZeroConsumerChains pins the bound algebra around
// deformBoundZero on synthetic chains: a zero-arm Aggregate stamps width 0, a
// folded reference raises it, a Finalize aggregate keeps full width, and a
// join below a zero-arm Aggregate keeps the per-side key bounds.
func TestScanDeformZeroConsumerChains(t *testing.T) {
	scan8 := func() *optimizer.SeqScan { return &optimizer.SeqScan{Table: deformTable(8)} }
	countStar := func(child optimizer.Node) *optimizer.Aggregate {
		return &optimizer.Aggregate{Child: child, Aggs: []optimizer.AggregateCall{{Name: "count", Star: true}}}
	}

	t.Run("count-star", func(t *testing.T) {
		if b, n := seqLeafBound(t, mustBuildDeform(t, countStar(scan8()))); b != 0 || n != 8 {
			t.Fatalf("bound=%d ncols=%d, want 0/8", b, n)
		}
	})
	t.Run("count-star-over-filter", func(t *testing.T) {
		plan := countStar(&optimizer.Filter{Child: scan8(), Predicate: deformLt(deformCol(3), deformInt(1))})
		if b, n := seqLeafBound(t, mustBuildDeform(t, plan)); b != 4 || n != 8 {
			t.Fatalf("bound=%d ncols=%d, want 4/8", b, n)
		}
	})
	t.Run("count-star-over-limit", func(t *testing.T) {
		plan := countStar(&optimizer.Limit{Child: scan8(), Limit: deformInt(2)})
		if b, n := seqLeafBound(t, mustBuildDeform(t, plan)); b != 0 || n != 8 {
			t.Fatalf("bound=%d ncols=%d, want 0/8", b, n)
		}
	})
	t.Run("count-star-over-gather", func(t *testing.T) {
		plan := countStar(&optimizer.Gather{Child: scan8()})
		if b, n := seqLeafBound(t, mustBuildDeform(t, plan)); b != 0 || n != 8 {
			t.Fatalf("bound=%d ncols=%d, want 0/8", b, n)
		}
	})
	t.Run("group-key-raises-zero", func(t *testing.T) {
		plan := &optimizer.Aggregate{Child: scan8(), GroupExprs: []optimizer.Expr{deformCol(2)},
			Aggs: []optimizer.AggregateCall{{Name: "count", Star: true}}}
		if b, n := seqLeafBound(t, mustBuildDeform(t, plan)); b != 3 || n != 8 {
			t.Fatalf("bound=%d ncols=%d, want 3/8", b, n)
		}
	})
	t.Run("finalize-keeps-full", func(t *testing.T) {
		agg := countStar(scan8())
		agg.Mode = optimizer.AggModeFinal
		if got := deformBoundBelow(agg, deformBoundNone); got != deformBoundNone {
			t.Fatalf("finalize bound=%d, want deformBoundNone", got)
		}
	})
	t.Run("union-with-none-widens", func(t *testing.T) {
		if got := deformUnionBound(deformBoundZero, deformBoundNone); got != deformBoundNone {
			t.Fatalf("Zero ∪ None = %d, want None", got)
		}
		if got := deformUnionBound(deformBoundZero, 3); got != 3 {
			t.Fatalf("Zero ∪ 3 = %d, want 3", got)
		}
		if l, r := deformMergedAbove(deformBoundZero, 4, 4); l != deformBoundNone || r != deformBoundNone {
			t.Fatalf("merged above Zero = %d/%d, want None/None", l, r)
		}
	})
}

// TestScanDeformZeroConsumerExecution runs zero-consumer aggregates against a
// real table with the tail poison armed: any read of an undeformed column
// panics. The counts must match the full-deform variants.
func TestScanDeformZeroConsumerExecution(t *testing.T) {
	ctx := deformW8Fixture(t)

	cases := []struct {
		name, sql, full string
		bound           int
		want            string
	}{
		{"count-star", `SELECT count(*) FROM w`, `SELECT count(h) FROM w`, 0, "4"},
		{"count-star-filter", `SELECT count(*) FROM w WHERE b > 21`, `SELECT count(h) FROM w WHERE b > 21`, 2, "2"},
		{"count-star-const", `SELECT count(*), 7 FROM w`, `SELECT count(h), 7 FROM w`, 0, "4|7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := testPlanDeform(t, ctx, tc.sql)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			if b, n := seqLeafBound(t, mustBuildDeform(t, plan)); b != tc.bound || n != 8 {
				t.Fatalf("leaf bound=%d ncols=%d, want %d/8", b, n, tc.bound)
			}
			old := seqScanDeformPoison
			seqScanDeformPoison = true
			rows, err := runQueryWithErr(ctx, tc.sql)
			seqScanDeformPoison = old
			if err != nil {
				t.Fatalf("poisoned run: %v", err)
			}
			fullRows, err := runQueryWithErr(ctx, tc.full)
			if err != nil {
				t.Fatalf("full run: %v", err)
			}
			got, full := renderDeformRows(rows), renderDeformRows(fullRows)
			if fmt.Sprint(got) != fmt.Sprint([]string{tc.want}) || fmt.Sprint(got) != fmt.Sprint(full) {
				t.Fatalf("rows = %v, full = %v, want [%s]", got, full, tc.want)
			}
		})
	}
}
