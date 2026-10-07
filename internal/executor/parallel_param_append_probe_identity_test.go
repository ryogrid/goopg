package executor

// The parallel parameterised-Append nested-loop identity pin (M0146-0049e).
//
// PG's TPC-DS Q54 runs `Gather → Nested Loop (Parallel Seq Scan item,
// Append (probe catalog_sales, probe web_sales))`: the outer is partial and
// every worker re-opens BOTH member probes for its own outer rows. goopg
// builds the Append as a lateral `Join{Algo: NestedLoop, Lateral}` whose
// right side is the UNION ALL of the member probes (createParamAppendNode).
// The planner twin (`paramAppendIsPartialProbe`), the path twin
// (`partialPathDrivingKind`'s PathParamAppend arm) and the executor twin
// (`lateralProbeJoinPartial`'s *setOp arm) admit it together.
//
// This is an IDENTITY test: if the twins disagree, the driving scan stays
// unattached and every participant runs the whole plan, the N-copy
// signature this test fails on.

import (
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// planHasLateralParamAppend reports whether the tree contains a lateral
// nested loop whose right side is a UNION ALL SetOp (the parameterised
// Append), and returns that join.
func planHasLateralParamAppend(n optimizer.Node) *optimizer.Join {
	switch x := n.(type) {
	case *optimizer.Join:
		if x.Algo == optimizer.JoinAlgoNestedLoop && x.Lateral {
			if _, ok := x.Right.(*optimizer.SetOp); ok {
				return x
			}
		}
		if j := planHasLateralParamAppend(x.Left); j != nil {
			return j
		}
		return planHasLateralParamAppend(x.Right)
	case *optimizer.Project:
		return planHasLateralParamAppend(x.Child)
	case *optimizer.Filter:
		return planHasLateralParamAppend(x.Child)
	case *optimizer.Sort:
		return planHasLateralParamAppend(x.Child)
	case *optimizer.Aggregate:
		return planHasLateralParamAppend(x.Child)
	case *optimizer.Gather:
		return planHasLateralParamAppend(x.Child)
	}
	return nil
}

func TestParallelParamAppendProbeIdentity(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE pa_outer (id int, k int)",
		"CREATE TABLE pa_a (k int, v int)",
		"CREATE TABLE pa_b (k int, v int)",
		"CREATE INDEX pa_a_k ON pa_a (k)",
		"CREATE INDEX pa_b_k ON pa_b (k)",
		"INSERT INTO pa_outer SELECT g, g FROM generate_series(0, 99) g",
		// The first 60 outer keys match pa_a (twice each), keys 30..89
		// match pa_b once each; 100,000 filler rows per inner match nothing,
		// so the probe has to earn its election.
		"INSERT INTO pa_a SELECT g % 60, g FROM generate_series(0, 119) g",
		"INSERT INTO pa_b SELECT 30 + g, g FROM generate_series(0, 59) g",
		"INSERT INTO pa_a SELECT 1000 + g, g FROM generate_series(1, 100000) g",
		"INSERT INTO pa_b SELECT 1000 + g, g FROM generate_series(1, 100000) g",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	// The in-process ANALYZE is a no-op; seed what it would record.
	for name, rows := range map[string]int64{"pa_outer": 100, "pa_a": 100120, "pa_b": 100060} {
		if tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: name}); ok {
			tbl.Stats = &catalog.TableStats{RowCount: rows, Columns: []catalog.ColumnStats{
				{NDistinct: rows}, {NDistinct: rows},
			}}
		}
	}

	const sql = `SELECT o.id, u.v FROM pa_outer o,
		(SELECT k, v FROM pa_a UNION ALL SELECT k, v FROM pa_b) u WHERE u.k = o.k`

	for _, mult := range []string{"1", "2"} {
		t.Run("probe-mult="+mult, func(t *testing.T) {
			defer optimizer.SetIndexProbeCostMultiplier(mult)()
			serialRows, err := runQueryWithErr(ctx, sql)
			if err != nil {
				t.Fatalf("serial: %v", err)
			}
			want := len(serialRows)
			if want != 180 {
				t.Fatalf("fixture: serial returned %d rows, want 180 (120 from pa_a + 60 from pa_b)", want)
			}
			stmts, err := parser.Parse(sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			j := planHasLateralParamAppend(plan)
			if j == nil {
				t.Fatalf("planner no longer elects a lateral nested loop over the parameterised Append; "+
					"the identity pin would be vacuous.\nplan: %#v", plan)
			}
			if !optimizer.LateralParamAppendProbe(j) {
				t.Fatalf("the lateral parameterised Append is not partial-capable by the planner twin: %#v", j.Right)
			}
			for _, workers := range []int{1, 2, 4} {
				t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
					stmts, _ := parser.Parse(sql)
					plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
					if err != nil {
						t.Fatalf("plan: %v", err)
					}
					gathered := optimizer.NewGather(0, plan, workers)
					ctx.MaxParallelWorkers = 8
					ctx.ParallelLeaderParticipation = true
					op, err := Build(gathered)
					if err != nil {
						t.Fatalf("build: %v", err)
					}
					if err := op.Open(ctx); err != nil {
						t.Fatalf("open: %v", err)
					}
					n := 0
					for {
						_, err := op.Next()
						if err == EOF {
							break
						}
						if err != nil {
							op.Close()
							t.Fatalf("next: %v", err)
						}
						n++
					}
					op.Close()
					if n != want {
						t.Fatalf("workers=%d: parallel returned %d rows, want %d (serial) — "+
							"the N-copy signature of an unattached driving scan", workers, n, want)
					}
				})
			}
		})
	}
}
