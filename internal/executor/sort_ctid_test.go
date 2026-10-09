package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestSortByCTID pins M0146-0052 against PG 18.3. A Sort used to evaluate
// its keys on the bare Row, where `ctid` (a slot-carried tid) reads NULL:
// every key was NULL, so `ORDER BY ctid DESC` returned input order. A sort
// that spilled also dropped the tids of the rows it wrote, so a ctid key or
// a ctid projected above the sort read NULL after the merge.
//
// The table holds 7 rows per page (PG places row a at
// ((a-1)/7, (a-1)%7+1); e.g. a=1000 → (142,6), a=3000 → (428,4)). Each case
// runs in memory and spilled (work_mem 64kB), with the plain and the packed
// retention, through both builders: serial, over the forced parallel plan
// the planner picks here (Sort over Gather), and with a Gather Merge spliced
// over the serial Sort so each worker sorts its own share by the key.
func TestSortByCTID(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE so_ctid (a int, b text)")
	runSQL(t, ctx, "INSERT INTO so_ctid SELECT g, repeat('x', 1000) FROM generate_series(1, 3000) g")
	runSQL(t, ctx, "ANALYZE so_ctid")
	tid := func(a int) string { return fmt.Sprintf("(%d,%d)", (a-1)/7, (a-1)%7+1) }

	var descAll, byMod []string
	for a := 3000; a >= 1; a-- {
		descAll = append(descAll, fmt.Sprintf("%d %s", a, tid(a)))
	}
	for m := 0; m < 7; m++ {
		for a := 1; a <= 3000; a++ {
			if a%7 == m {
				byMod = append(byMod, fmt.Sprintf("%d %s", a, tid(a)))
			}
		}
	}
	cases := []struct {
		sql  string
		want []string
	}{
		{"SELECT a, ctid FROM so_ctid ORDER BY ctid DESC", descAll},
		{"SELECT a, ctid FROM so_ctid ORDER BY ctid DESC LIMIT 3", descAll[:3]},
		{"SELECT a, ctid FROM so_ctid ORDER BY a % 7, a", byMod},
	}
	serial := optimizer.DefaultPlannerSettings()
	serial.MaxParallelWorkersPerGather = 0
	parallel := optimizer.DefaultPlannerSettings()
	parallel.ParallelSetupCost, parallel.ParallelTupleCost, parallel.MinParallelTableScanSize = 0, 0, 0
	parallel.MaxParallelWorkersPerGather = 2
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	prevPacked := sortPackedEnabled()
	t.Cleanup(func() { SetSortPackedEnabled(prevPacked) })

	for _, c := range cases {
		stmts, err := parser.Parse(c.sql)
		if err != nil {
			t.Fatalf("parse %q: %v", c.sql, err)
		}
		for _, mode := range []string{"serial", "parallel", "gathermerge"} {
			ps := serial
			if mode == "parallel" {
				ps = parallel
			}
			plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
			if err != nil {
				t.Fatalf("plan %q: %v", c.sql, err)
			}
			if mode == "gathermerge" {
				plan = spliceGatherMergeOverSort(plan)
			}
			text := renderPlanText(plan)
			if mode != "serial" && !strings.Contains(text, "Gather") {
				t.Fatalf("%s: no Gather in the %s plan:\n%s", c.sql, mode, text)
			}
			for _, workMem := range []int64{0, 64 << 10} {
				for _, packed := range []bool{false, true} {
					SetSortPackedEnabled(packed)
					ctx.WorkMem = workMem
					for _, slab := range []bool{false, true} {
						advanceStmtCounter(ctx)
						var op Operator
						if slab {
							op, err = BuildFastIterator(plan)
						} else {
							op, err = Build(plan)
						}
						if err != nil {
							t.Fatalf("build %q: %v", c.sql, err)
						}
						rows, err := Run(op, ctx)
						if err != nil {
							t.Fatalf("run %q: %v", c.sql, err)
						}
						got := make([]string, len(rows))
						for i, r := range rows {
							got[i] = datumTestString(r[0]) + " " + datumTestString(r[1])
						}
						if strings.Join(got, "|") != strings.Join(c.want, "|") {
							first := 0
							for first < len(got) && first < len(c.want) && got[first] == c.want[first] {
								first++
							}
							t.Errorf("%s [%s workmem=%d packed=%v slab=%v]: %d rows, want %d; first difference at row %d: got %q want %q",
								c.sql, mode, workMem, packed, slab, len(got), len(c.want), first, at(got, first), at(c.want, first))
						}
					}
				}
			}
		}
	}
	ctx.WorkMem = 0
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "<none>"
}

// spliceGatherMergeOverSort wraps the plan's Sort in a Gather Merge on the
// same keys, so every participant sorts its own share (the shape the
// planner picks for a big enough table).
func spliceGatherMergeOverSort(n optimizer.Node) optimizer.Node {
	switch x := n.(type) {
	case *optimizer.Sort:
		return optimizer.NewGatherMerge(0, x, 2, x.Keys)
	case *optimizer.Limit:
		c := *x
		c.Child = spliceGatherMergeOverSort(x.Child)
		return &c
	case *optimizer.Project:
		c := *x
		c.Child = spliceGatherMergeOverSort(x.Child)
		return &c
	}
	return n
}
