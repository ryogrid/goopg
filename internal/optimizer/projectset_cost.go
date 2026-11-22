package optimizer

import (
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// M0149-0001h1a78a: the estimates of a SELECT-list ProjectSet.
//
// PG plans the SRFs of a target list in a ProjectSet and, when an SRF sits
// inside a larger expression (`generate_series(1, 2) + 1`, an explicit cast
// of an unnest), the expression over it in a Result above
// (split_pathtarget_at_srfs, tlist.c). goopg evaluates both in its one
// ProjectSet node, so the costs PG reports for the pair are computed here:
//
//   - ProjectSetOwnCost is create_set_projection_path (pathnode.c): the input's
//     rows times the largest SRF row estimate (expression_returns_set_rows),
//     cpu_tuple_cost plus the target's per-tuple cost per input row, and half
//     of cpu_tuple_cost per added row.
//   - The Result above is create_projection_path: cpu_tuple_cost plus the
//     whole target's per-tuple cost per row. Its target still holds the SRF
//     calls inside the expressions, so cost_qual_eval charges them again.
//
// DeriveLegacyDisplayCost reports the pair's top (the Result when there is
// one), which is what a parent node is priced over.

// ProjectSetNeedsResult reports whether PG plans a Result above this
// ProjectSet: some SRF feeds an enclosing expression — a wrapper, or a cast
// the SQL wrote over an unnest that changes its element type.
func ProjectSetNeedsResult(n *ProjectSet) bool {
	if n == nil {
		return false
	}
	if len(n.Wrappers) > 0 {
		return true
	}
	for _, u := range n.UnnestCols {
		if u.ExplicitCast || u.Wrapped {
			return true
		}
	}
	for _, sc := range n.SrfCols {
		if sc.Wrapped {
			return true
		}
	}
	return false
}

// isSelectListProjectSet reports whether n is the SELECT-list SRF mode the
// estimates here describe (not the older SrfName mode).
func isSelectListProjectSet(n *ProjectSet) bool {
	return len(n.SrfCols)+len(n.UnnestCols)+len(n.RegexpMatchesCols)+len(n.UserSrfCols) > 0
}

// projectSetTlistRows is the largest expression_returns_set_rows over the
// ProjectSet's SRFs: the rows each input row expands to.
func projectSetTlistRows(n *ProjectSet) float64 {
	rows := 1.0
	take := func(r float64) {
		if r > rows {
			rows = r
		}
	}
	for _, sc := range n.SrfCols {
		args := []Expr{sc.Start, sc.Stop}
		if sc.Step != nil {
			args = append(args, sc.Step)
		}
		r := 1000.0 // prorows of every generate_series variant
		if EstimateSeriesRows != nil && seriesArgsEstimable(args) {
			if est, ok := EstimateSeriesRows(args); ok {
				r = clampRowEst(est)
			}
		}
		take(r)
	}
	for _, u := range n.UnnestCols {
		// array_unnest_support: estimate_array_length of the argument.
		take(float64(estimateArrayLength(u.ArrExpr)))
	}
	if len(n.RegexpMatchesCols) > 0 {
		take(10) // regexp_matches' prorows
	}
	for _, u := range n.UserSrfCols {
		take(routineProRows(u.Routine))
	}
	return rows
}

// estimateArrayLength is estimate_array_length (selfuncs.c) for the array
// shapes goopg resolves: an ARRAY[...] constructor (a Const once folded, or an
// ArrayExpr) has one element per argument, an array literal its element
// count; anything else is the default guess of 10.
func estimateArrayLength(e Expr) int {
	switch x := e.(type) {
	case *FuncCall:
		if x.Name == "array_construct" {
			return len(x.Args)
		}
	case *CastExpr:
		if s, ok := x.Operand.(*StringConst); ok {
			if n, ok := arrayLiteralItems(s.Value); ok {
				return n
			}
		}
		// `ARRAY['a','b']::text[]` folds to a Const of the same length.
		if fc, ok := x.Operand.(*FuncCall); ok && fc.Name == "array_construct" {
			return len(fc.Args)
		}
	case *StringConst:
		if n, ok := arrayLiteralItems(x.Value); ok {
			return n
		}
	}
	return defaultArrayLength
}

// arrayLiteralItems counts the elements of an array literal across all its
// dimensions (ArrayGetNItems), or reports false when s is not one.
func arrayLiteralItems(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return 0, false
	}
	items, inQuote, escaped, pending := 0, false, false, false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
			pending = true
		case r == '\\':
			escaped = true
		case r == '"':
			inQuote = !inQuote
			pending = true
		case inQuote:
			pending = true
		case r == '{':
		case r == ',' || r == '}':
			if pending {
				items++
			}
			pending = false
		case r == ' ' || r == '\t' || r == '\n':
		default:
			pending = true
		}
	}
	return items, true
}

// routineProRows is a SETOF routine's prorows: its ROWS clause, else 1000.
func routineProRows(r *catalog.Routine) float64 {
	if r != nil && r.Rows != "" {
		if v, err := strconv.ParseFloat(r.Rows, 64); err == nil && v > 0 {
			return v
		}
	}
	return 1000
}

// routineProCost is a routine's procost: its COST clause, else 100 for the
// SQL and PL/pgSQL routines a ProjectSet calls.
func routineProCost(r *catalog.Routine) float64 {
	if r != nil && r.Cost != "" {
		if v, err := strconv.ParseFloat(r.Cost, 64); err == nil && v >= 0 {
			return v
		}
	}
	return 100
}

// srfCallOps is cost_qual_eval of one SRF call, in cpu_operator_cost units:
// the function's procost plus its arguments.
func srfCallOps(args []Expr, procost float64) (startup, perTuple float64) {
	perTuple = procost
	for _, a := range args {
		s, p := targetExprOps(a)
		startup += s
		perTuple += p
	}
	return startup, perTuple
}

// targetExprOps is qualEvalOps for a target-list expression. An ARRAY[...]
// constructor is an ArrayExpr (or a folded Const) to PG and a subscript a
// SubscriptingRef, neither of which cost_qual_eval charges, while goopg
// resolves them to array_construct and array_subscript calls; those calls are
// taken back out.
func targetExprOps(e Expr) (startup, perTuple float64) {
	if e == nil {
		return 0, 0
	}
	startup, perTuple = qualEvalOps(e)
	walkExprRefs(e, scopeSignal, exprVisitor{Visit: func(x Expr) bool {
		if fc, ok := x.(*FuncCall); ok && (fc.Name == "array_construct" || fc.Name == "array_subscript") {
			perTuple--
		}
		return true
	}})
	return startup, perTuple
}

// projectSetSRFOps is the summed cost of the ProjectSet's SRF calls.
func projectSetSRFOps(n *ProjectSet) (startup, perTuple float64) {
	add := func(s, p float64) { startup += s; perTuple += p }
	for _, sc := range n.SrfCols {
		args := []Expr{sc.Start, sc.Stop}
		if sc.Step != nil {
			args = append(args, sc.Step)
		}
		if sc.Zone != nil {
			args = append(args, sc.Zone)
		}
		add(srfCallOps(args, 1))
	}
	for _, u := range n.UnnestCols {
		add(srfCallOps([]Expr{u.ArrExpr}, 1))
	}
	for _, rc := range n.RegexpMatchesCols {
		args := []Expr{rc.StringExpr, rc.PatternExpr}
		if rc.FlagsExpr != nil {
			args = append(args, rc.FlagsExpr)
		}
		add(srfCallOps(args, 1))
	}
	for _, u := range n.UserSrfCols {
		add(srfCallOps(u.Args, routineProCost(u.Routine)))
	}
	return startup, perTuple
}

// projectSetChildCost is the input's display cost and rows.
func projectSetChildCost(n *ProjectSet) (PlanCost, float64) {
	if n.Child == nil {
		return PlanCost{}, 1
	}
	pc := legacyDisplayCostOf(n.Child)
	rows := pc.PlanRows
	if rows <= 0 {
		rows = float64(EstimateRows(n.Child))
	}
	if rows < 1 {
		rows = 1
	}
	return pc, rows
}

// ProjectSetOwnCost is the ProjectSet line's own estimate
// (create_set_projection_path). Its target is every SRF plus, when no Result
// is planned above, the other target expressions too; with a Result above
// those are the Result's, and the ProjectSet only passes columns through.
func ProjectSetOwnCost(n *ProjectSet) PlanCost {
	return projectSetOwnCost(DefaultPlannerSettings().costParams(), n)
}

func projectSetOwnCost(cp costParams, n *ProjectSet) PlanCost {
	child, childRows := projectSetChildCost(n)
	rows := childRows * projectSetTlistRows(n)
	ts, tp := projectSetSRFOps(n)
	needResult := ProjectSetNeedsResult(n)
	if !needResult {
		for _, e := range n.OtherExprs {
			s, p := targetExprOps(e)
			ts += s
			tp += p
		}
	}
	startup := child.StartupCost + ts*cp.cpuOperatorCost
	total := child.TotalCost + ts*cp.cpuOperatorCost +
		(cp.cpuTupleCost+tp*cp.cpuOperatorCost)*childRows +
		(rows-childRows)*cp.cpuTupleCost/2
	width := TupleWidth(n.Output())
	if needResult {
		width = TupleWidth(projectSetOwnSchema(n))
	}
	return PlanCost{StartupCost: startup, TotalCost: total, PlanRows: rows, PlanWidth: width, CostSet: true}
}

// projectSetResultCost is the Result PG plans above the ProjectSet
// (create_projection_path over the ProjectSet path).
func projectSetResultCost(cp costParams, n *ProjectSet) PlanCost {
	own := projectSetOwnCost(cp, n)
	var ts, tp float64
	add := func(s, p float64) { ts += s; tp += p }
	// The Result's target holds every visible column's full expression: the
	// SRF calls (bare or inside a wrapper or a cast) and the other targets.
	add(projectSetSRFOps(n))
	for _, w := range n.Wrappers {
		add(targetExprOps(w.Expr))
	}
	for _, e := range n.OtherExprs {
		add(targetExprOps(e))
	}
	startup := own.StartupCost + ts*cp.cpuOperatorCost
	total := own.TotalCost + ts*cp.cpuOperatorCost + (cp.cpuTupleCost+tp*cp.cpuOperatorCost)*own.PlanRows
	return PlanCost{StartupCost: startup, TotalCost: total, PlanRows: own.PlanRows, PlanWidth: TupleWidth(n.Output()), CostSet: true}
}

// projectSetOwnSchema is the ProjectSet's own output under a Result: one
// column per SRF, then the input columns the Result's expressions read.
func projectSetOwnSchema(n *ProjectSet) Schema {
	var out Schema
	for _, sc := range n.SrfCols {
		out = append(out, SchemaColumn{Type: catalog.Type{Name: sc.Type}})
	}
	for _, u := range n.UnnestCols {
		out = append(out, SchemaColumn{Type: unnestElementType(u.ArrExpr)})
	}
	for range n.RegexpMatchesCols {
		out = append(out, SchemaColumn{Type: catalog.Type{Name: "text[]"}})
	}
	for _, u := range n.UserSrfCols {
		t := catalog.Type{Name: "int4"}
		if u.Routine != nil {
			t = u.Routine.ReturnType
		}
		out = append(out, SchemaColumn{Type: t})
	}
	if n.Child == nil {
		return out
	}
	childOut := n.Child.Output()
	seen := map[int]bool{}
	collect := func(e Expr) {
		walkExprRefs(e, scopeSignal, exprVisitor{Visit: func(x Expr) bool {
			if c, ok := x.(*ColumnRef); ok && c.Index >= 0 && c.Index < len(childOut) && c.Index < n.ChildWidth && !seen[c.Index] {
				seen[c.Index] = true
				out = append(out, childOut[c.Index])
			}
			return true
		}})
	}
	for _, w := range n.Wrappers {
		collect(w.Expr)
	}
	for _, e := range n.OtherExprs {
		collect(e)
	}
	return out
}

// unnestElementType is the element type of an unnest's array argument.
func unnestElementType(arr Expr) catalog.Type {
	t := exprType(arr)
	base := t.Name
	for strings.HasSuffix(base, "[]") {
		base = base[:len(base)-2]
	}
	if base != t.Name && base != "" {
		return catalog.Type{Name: base}
	}
	if t.IsArray {
		return catalog.Type{Name: t.Name}
	}
	return catalog.Type{Name: "text"}
}

// projectSetDisplayCost is the cost a parent sees: the Result above when PG
// plans one, else the ProjectSet itself.
func projectSetDisplayCost(cp costParams, n *ProjectSet) PlanCost {
	if ProjectSetNeedsResult(n) {
		return projectSetResultCost(cp, n)
	}
	return projectSetOwnCost(cp, n)
}
