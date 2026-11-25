package executor

import (
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// M0149-0001h1a78d: VERBOSE Output lists for a SELECT-list ProjectSet and the
// Result PG plans above it.
//
// split_pathtarget_at_srfs (tlist.c) gives the ProjectSet a targetlist of
// the SRF calls, followed by the input columns the expressions above still
// need, and gives the Result the SELECT list. In the Result each reference to
// a ProjectSet column is an OUTER_VAR whose referent is not a Var, so ruleutils
// parenthesises it: `((generate_series(1, 2)) + 1)`. Without a Result (every
// SRF is a bare target) the ProjectSet's targetlist is the SELECT list itself.
// goopg evaluates both levels in one ProjectSet node; the two lists are
// rebuilt from its SRF columns, wrappers and other expressions.

// projectSetSelectList reports whether ps is the SELECT-list SRF mode.
func projectSetSelectList(ps *optimizer.ProjectSet) bool {
	return len(ps.SrfCols)+len(ps.UnnestCols)+len(ps.RegexpMatchesCols)+len(ps.UserSrfCols) > 0
}

// projectSetSRFCalls returns each SRF's call, deparsed, keyed by its column:
// visible output columns for the bare SRFs, eval-row slots for the wrapped.
func projectSetSRFCalls(ps *optimizer.ProjectSet, reg *subPlanReg, qualify bool) (bare, slot map[int]string) {
	bare, slot = map[int]string{}, map[int]string{}
	put := func(col int, wrapped bool, fc *optimizer.FuncCall) {
		txt := formatExprQual(fc, reg, qualify)
		if wrapped {
			slot[col] = txt
		} else {
			bare[col] = txt
		}
	}
	for _, sc := range ps.SrfCols {
		args := []optimizer.Expr{sc.Start, sc.Stop}
		if sc.Step != nil {
			args = append(args, sc.Step)
		}
		if sc.Zone != nil {
			args = append(args, sc.Zone)
		}
		put(sc.ColIdx, sc.Wrapped, &optimizer.FuncCall{Name: "generate_series", Args: args})
	}
	for _, u := range ps.UnnestCols {
		if fc, ok := u.ArrExpr.(*optimizer.FuncCall); ok && u.FuncName != "" {
			put(u.ColIdx, u.Wrapped, &optimizer.FuncCall{Name: u.FuncName, Args: fc.Args})
			continue
		}
		put(u.ColIdx, u.Wrapped, &optimizer.FuncCall{Name: "unnest", Args: []optimizer.Expr{u.ArrExpr}})
	}
	for _, rc := range ps.RegexpMatchesCols {
		args := []optimizer.Expr{rc.StringExpr, rc.PatternExpr}
		if rc.FlagsExpr != nil {
			args = append(args, rc.FlagsExpr)
		}
		put(rc.ColIdx, rc.Wrapped, &optimizer.FuncCall{Name: "regexp_matches", Args: args})
	}
	for _, u := range ps.UserSrfCols {
		name := "srf"
		if u.Routine != nil {
			name = u.Routine.Name
		}
		put(u.ColIdx, u.Wrapped, &optimizer.FuncCall{Name: name, Args: u.Args})
	}
	return bare, slot
}

// projectSetVerboseLists returns the ProjectSet's Output list and, when PG
// plans a Result above it, the Result's (nil otherwise).
func projectSetVerboseLists(ps *optimizer.ProjectSet, reg *subPlanReg, qualify bool) (psOut, resultOut []string) {
	bare, slot := projectSetSRFCalls(ps, reg, qualify)
	out := ps.Output()
	wrapperOf := map[int]optimizer.Expr{}
	for _, w := range ps.Wrappers {
		wrapperOf[w.OutCol] = w.Expr
	}
	// An unnest whose elements the SQL cast is an expression over the SRF as
	// well (`unnest(a)::text`): its Result entry is the cast of the reference.
	castOf := map[int]optimizer.UnnestCol{}
	for _, u := range ps.UnnestCols {
		if !u.Wrapped && u.ExplicitCast {
			castOf[u.ColIdx] = u
		}
	}
	if !optimizer.ProjectSetNeedsResult(ps) {
		for i := range out {
			if txt, ok := bare[i]; ok {
				psOut = append(psOut, txt)
				continue
			}
			psOut = append(psOut, formatExprQual(otherExprAt(ps, i), reg, qualify))
		}
		return psOut, nil
	}
	// The Result references slots and bare SRF columns as `(call)`.
	ref := func(e optimizer.Expr) string {
		cloned, ok := optimizer.CloneExprReplacingColumnRefs(e, func(c *optimizer.ColumnRef) optimizer.Expr {
			if c.Index >= ps.ChildWidth {
				if txt, hit := slot[c.Index]; hit {
					return displayColumn(c, "("+txt+")", reg)
				}
			}
			return nil
		})
		if !ok {
			cloned = e
		}
		return formatExprQual(cloned, reg, qualify)
	}
	var needed []int
	seen := map[int]bool{}
	noteVars := func(e optimizer.Expr) {
		optimizer.WalkExprTree(e, func(x optimizer.Expr) {
			if c, ok := x.(*optimizer.ColumnRef); ok && c.Index >= 0 && c.Index < ps.ChildWidth && !seen[c.Index] {
				seen[c.Index] = true
				needed = append(needed, c.Index)
			}
		})
	}
	for i := range out {
		switch {
		case wrapperOf[i] != nil:
			resultOut = append(resultOut, ref(wrapperOf[i]))
			noteVars(wrapperOf[i])
		case bare[i] != "":
			txt := "(" + bare[i] + ")"
			if u, ok := castOf[i]; ok {
				if typ, tok := castTypeName(u.CastType, 0); tok {
					txt = "(" + txt + ")::" + typ
				}
			}
			resultOut = append(resultOut, txt)
		default:
			e := otherExprAt(ps, i)
			resultOut = append(resultOut, formatExprQual(e, reg, qualify))
			noteVars(e)
		}
	}
	// The ProjectSet's SRF entries are in the order the SELECT list reaches
	// them; a wrapper's slots come at its column's position.
	emitted := map[int]bool{}
	for i := range out {
		if txt, ok := bare[i]; ok {
			psOut = append(psOut, txt)
			continue
		}
		if wrapperOf[i] == nil {
			continue
		}
		var slots []int
		optimizer.WalkExprTree(wrapperOf[i], func(x optimizer.Expr) {
			if c, ok := x.(*optimizer.ColumnRef); ok && c.Index >= ps.ChildWidth && !emitted[c.Index] {
				if _, hit := slot[c.Index]; hit {
					emitted[c.Index] = true
					slots = append(slots, c.Index)
				}
			}
		})
		sort.Ints(slots)
		for _, s := range slots {
			psOut = append(psOut, slot[s])
		}
	}
	// Then the input columns the Result still reads.
	for _, idx := range needed {
		col := ps.Child.Output()[idx]
		psOut = append(psOut, formatExprQual(&optimizer.ColumnRef{Index: idx, Name: col.Name, Type: col.Type, SourceTableIdx: col.SourceTableIdx}, reg, qualify))
	}
	return psOut, resultOut
}

func otherExprAt(ps *optimizer.ProjectSet, i int) optimizer.Expr {
	if i < len(ps.OtherExprs) {
		return ps.OtherExprs[i]
	}
	return nil
}

// emitSRFResultOutput prints the VERBOSE Output of the Result synthesised
// above a ProjectSet, at the Result's detail indent.
func emitSRFResultOutput(rows *[]Row, c optimizer.Node, detailIndent int, opts parser.ExplainOptions, reg *subPlanReg) {
	if !opts.Verbose || reg == nil {
		return
	}
	ps, ok := skipProjectsAndFilters(c).(*optimizer.ProjectSet)
	if !ok || !projectSetSelectList(ps) {
		return
	}
	_, res := projectSetVerboseLists(ps, reg, reg.names().qualifyTlist())
	if len(res) == 0 {
		return
	}
	*rows = append(*rows, Row{NewStringDatum(strings.Repeat(" ", detailIndent*2) + "Output: " + strings.Join(res, ", "))})
}

func skipProjectsAndFilters(n optimizer.Node) optimizer.Node {
	for {
		switch w := n.(type) {
		case *optimizer.Project:
			n = w.Child
			continue
		case *optimizer.Filter:
			n = w.Child
			continue
		}
		return n
	}
}

// projectSetPassThroughNames is the Output of a pass-through node (Sort,
// Limit) over a SELECT-list ProjectSet: each column references the top of the
// ProjectSet/Result pair, so a computed entry prints parenthesised and a plain
// column as itself.
func projectSetPassThroughNames(ps *optimizer.ProjectSet, reg *subPlanReg, qualify bool) []string {
	psOut, res := projectSetVerboseLists(ps, reg, qualify)
	top := res
	if top == nil {
		top = psOut
	}
	out := make([]string, len(top))
	for i, txt := range top {
		// setrefs keeps a Const in the upper tlist as a Const, so it prints
		// bare; any other non-Var referent is parenthesised
		// (get_special_variable).
		if e := otherExprAt(ps, i); exprIsConstLike(e) {
			out[i] = txt
			continue
		} else if _, isVar := e.(*optimizer.ColumnRef); isVar {
			out[i] = txt
			continue
		}
		out[i] = "(" + txt + ")"
	}
	return out
}
