package executor

import "github.com/goopg/goopg/internal/optimizer"

// prepareWorkerInitPlans evaluates, in the leader, every initplan the
// workers of one parallel launch could reach, and returns the values to hand
// to their Contexts (M0146-0192).
//
// This is ExecInitParallelPlan's initParam step (execParallel.c): PG
// evaluates a Gather's initplans before launching workers and serialises the
// Params to them. That is why is_parallel_safe (clauses.c) may treat an
// initplan's output as parallel-safe. The planner admits such a qual below
// a Gather through optimizer.InitPlanEvaluatedByLeader. This walk evaluates
// exactly the sublinks that predicate admits, at any depth of the subtree,
// including inside a correlated SubPlan's body that runs in the workers.
// An admitted initplan therefore never executes inside a worker, where its
// own Gather would nest.
//
// The returned map is built fresh for each launch and never mutated after,
// so the workers read it without locking. Entries the leader already
// carries (none today, since only workers hold a map) are kept.
func prepareWorkerInitPlans(ctx *Context, plan optimizer.Node) (map[*optimizer.SubqueryExpr]Datum, error) {
	var found []*optimizer.SubqueryExpr
	seen := map[*optimizer.SubqueryExpr]bool{}
	optimizer.WalkPlanExprsDeep(plan, func(e optimizer.Expr) {
		sq, ok := e.(*optimizer.SubqueryExpr)
		if !ok || seen[sq] || !optimizer.InitPlanEvaluatedByLeader(sq) {
			return
		}
		seen[sq] = true
		found = append(found, sq)
	})
	if len(found) == 0 {
		return ctx.InitPlanValues, nil
	}
	vals := make(map[*optimizer.SubqueryExpr]Datum, len(found)+len(ctx.InitPlanValues))
	for k, v := range ctx.InitPlanValues {
		vals[k] = v
	}
	for _, sq := range found {
		if _, ok := vals[sq]; ok {
			continue
		}
		v, err := evalSubquery(sq, nil, ctx)
		if err != nil {
			return nil, err
		}
		vals[sq] = v
	}
	return vals, nil
}
