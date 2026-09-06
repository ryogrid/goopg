package executor

// E-10 (EX5-03) executor consumer check: a Gather Merge over a partial INDEX
// path.
//
// C-19f reported this as a latent wrong-answer: gatherMergeOp attached only
// attachParallelScan, never the index or bitmap claim sets, so every worker's
// index scan walked the WHOLE index and the merge returned N copies of every
// row — correctly ordered, which is what makes it silent. The planner worked
// around it by admitting seq-scan-driven subpaths only, so the shape was
// unreachable in production and the bug could not be observed from SQL.
//
// These tests force the shape directly, and assert VALUES (each id exactly
// once, ascending), not a row count: the count catches the N-copies form, the
// value check also catches a partition that drops or garbles rows.

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// spliceGatherMergeOverIndexScan replaces the plan's IndexScan with
// GatherMerge(IndexScan) ordered by the scan's leading key column, leaving the
// spine above it in place. This is the shape C-19e wants to cost and that the
// producer refuses today.
func spliceGatherMergeOverIndexScan(n optimizer.Node, workers int) (optimizer.Node, *optimizer.IndexScan) {
	switch x := n.(type) {
	case *optimizer.IndexScan:
		keys := []optimizer.SortKey{{
			Expr: &optimizer.ColumnRef{Index: 0, Name: "id", Type: catalog.Type{Name: "int4"}},
		}}
		return optimizer.NewGatherMerge(0, x, workers, keys), x
	case *optimizer.Project:
		child, scan := spliceGatherMergeOverIndexScan(x.Child, workers)
		if scan == nil {
			return n, nil
		}
		c := *x
		c.Child = child
		return &c, scan
	case *optimizer.Filter:
		child, scan := spliceGatherMergeOverIndexScan(x.Child, workers)
		if scan == nil {
			return n, nil
		}
		c := *x
		c.Child = child
		return &c, scan
	}
	return n, nil
}

// TestGatherMergeOverParallelIndexScanIdentity is the E-10 gate: each row
// exactly once, in ascending key order, over a Gather Merge whose partial
// subtree is driven by a plain index scan.
//
// Before the claim-set fix this failed with (workers+1)x the rows — the
// N-copies defect C-19f predicted.
func TestGatherMergeOverParallelIndexScanIdentity(t *testing.T) {
	ctx, cleanup := parallelIndexScanFixture(t)
	defer cleanup()

	const sql = "SELECT id, grp FROM pq_pidx WHERE id >= 100"
	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := make([]string, 0, len(serialRows))
	for _, r := range serialRows {
		want = append(want, datumTestString(r[0]))
	}
	if len(want) != 2901 {
		t.Fatalf("fixture: got %d rows, want 2901", len(want))
	}

	for _, workers := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			advanceStmtCounter(ctx)
			node := planForTest(t, ctx, sql)
			gathered, scan := spliceGatherMergeOverIndexScan(node, workers)
			if scan == nil {
				t.Fatalf("no IndexScan in the plan for %q; the test would prove nothing", sql)
			}
			if scan.LowKey == nil || scan.Key != nil {
				t.Fatalf("fixture must plan a plain RANGE index scan, got %+v", scan)
			}

			ctx.MaxParallelWorkers = 8
			ctx.ParallelLeaderParticipation = true
			op, err := Build(gathered)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			gm, ok := findGatherMergeOp(op)
			if !ok {
				t.Fatalf("Build did not produce a *gatherMergeOp (got %T)", op)
			}
			if err := op.Open(ctx); err != nil {
				t.Fatalf("open: %v", err)
			}
			var got []string
			for {
				slot, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					t.Fatalf("next: %v", err)
				}
				got = append(got, datumTestString(slot.Row()[0]))
			}
			claimed := gm.pidx.claimedBlocks()
			if err := op.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("got %d rows, want %d — a %.2gx multiple means every worker walked "+
					"the whole index (the C-19f N-copies defect)",
					len(got), len(want), float64(len(got))/float64(len(want)))
			}
			if !reflect.DeepEqual(got, want) {
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("row %d: got %s, want %s — the merge lost the ordering "+
							"or the partition dropped rows", i, got[i], want[i])
					}
				}
			}
			if claimed < 2 {
				t.Errorf("leaf claim set handed out %d blocks; the fixture must span several "+
					"leaves for the partition to be exercised", claimed)
			}
		})
	}
}

// planForTest parses and plans sql against the fixture's catalog.
func planForTest(t *testing.T, ctx *Context, sql string) optimizer.Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return node
}

// findGatherMergeOp digs the gatherMergeOp out from under the spine Build
// wrapped around it.
func findGatherMergeOp(op Operator) (*gatherMergeOp, bool) {
	switch x := op.(type) {
	case *gatherMergeOp:
		return x, true
	case *filterOp:
		return findGatherMergeOp(x.child)
	case *projectOp:
		return findGatherMergeOp(x.child)
	case *instrumentedOp:
		return findGatherMergeOp(x.inner)
	}
	return nil, false
}

// TestParallelClaimSetAttachesEveryKind is the anti-drift guard. It fails when
// a claim kind is added to parallelClaimSet without an arm in attachAll — the
// mechanical form of the bug this file exists for.
//
// The NumField assertion is the load-bearing half: without it a new field would
// simply not be covered and every existing case would still pass.
func TestParallelClaimSetAttachesEveryKind(t *testing.T) {
	cases := []struct {
		field  string
		leaf   Operator
		wanted func(Operator, *parallelClaimSet) bool
	}{
		{"pscan", &seqScanOp{}, func(op Operator, cs *parallelClaimSet) bool {
			return op.(*seqScanOp).pscan == cs.pscan
		}},
		{"pbm", &bitmapHeapScanOp{}, func(op Operator, cs *parallelClaimSet) bool {
			return op.(*bitmapHeapScanOp).pbm == cs.pbm
		}},
		{"pidx", &indexScanOp{}, func(op Operator, cs *parallelClaimSet) bool {
			return op.(*indexScanOp).pidx == cs.pidx
		}},
		{"pidx", &indexOnlyScanOp{}, func(op Operator, cs *parallelClaimSet) bool {
			return op.(*indexOnlyScanOp).pidx == cs.pidx
		}},
	}

	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.field] = true
		cs := newParallelClaimSet()
		// pbm is nil until prebuildBitmap runs; give it a value so the arm is
		// actually exercised rather than short-circuiting on a nil state.
		cs.pbm = newParallelBitmapState()
		if !cs.attachAll(c.leaf) {
			t.Errorf("%s: attachAll reported nothing attached for %T", c.field, c.leaf)
			continue
		}
		if !c.wanted(c.leaf, cs) {
			t.Errorf("%s: attachAll left %T unwired", c.field, c.leaf)
		}
	}

	n := reflect.TypeOf(parallelClaimSet{}).NumField()
	if len(covered) != n {
		t.Fatalf("parallelClaimSet has %d claim kinds but this test covers %d (%v) — "+
			"a new kind must be wired into attachAll AND covered here, or a Gather "+
			"and a Gather Merge over the same plan will disagree", n, len(covered), covered)
	}
}

// TestGatherSiblingsShareOneClaimSet pins the structural reason the two
// operators cannot drift: neither owns claim state of its own, both embed the
// single set. A future kind added to parallelClaimSet therefore reaches both
// consumers by construction.
func TestGatherSiblingsShareOneClaimSet(t *testing.T) {
	claimFields := map[string]bool{}
	cst := reflect.TypeOf(parallelClaimSet{})
	for i := 0; i < cst.NumField(); i++ {
		claimFields[cst.Field(i).Name] = true
	}

	for _, tp := range []reflect.Type{
		reflect.TypeOf(gatherOp{}),
		reflect.TypeOf(gatherMergeOp{}),
	} {
		embedded := false
		for i := 0; i < tp.NumField(); i++ {
			f := tp.Field(i)
			if f.Anonymous && f.Type == reflect.PointerTo(cst) {
				embedded = true
				continue
			}
			if claimFields[f.Name] {
				t.Errorf("%s declares its own %q instead of using the shared "+
					"parallelClaimSet; that is how the two siblings drifted apart",
					tp.Name(), f.Name)
			}
		}
		if !embedded {
			t.Errorf("%s does not embed *parallelClaimSet", tp.Name())
		}
	}
}
