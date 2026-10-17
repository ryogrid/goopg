package optimizer

import "reflect"

// M0146-0129 — a constant-FALSE outer-join qual makes the nullable side dummy.
//
// populate_joinrel_with_paths (joinrels.c), JOIN_LEFT arm:
//
//	if (restriction_is_constant_false(restrictlist, joinrel, false) &&
//	    bms_is_subset(rel2->relids, sjinfo->syn_righthand))
//	    mark_dummy_rel(rel2);
//
// An outer join whose ON clause holds a constant FALSE or NULL conjunct can
// never match, so its nullable input is never read: PG plans that side as a
// dummy rel — a childless `Result  One-Time Filter: false` — and keeps the
// constant as the join's `Join Filter: false`. JOIN_FULL is excluded (both
// sides are preserved). goopg planned the whole nullable subtree beneath the
// join (regress join.sql: `left join ... on false`).

// dummyConstantFalseOuterJoinSides replaces, in place, the nullable input of
// every LEFT / RIGHT join in n whose join predicate has a constant FALSE or
// NULL conjunct by a dummy Result with the same output schema. A kept CTE's
// body is shared between its references and is not entered.
func dummyConstantFalseOuterJoinSides(n Node) Node {
	if n == nil {
		return nil
	}
	if _, cte := n.(*CTEScan); cte {
		return n
	}
	if j, ok := n.(*Join); ok && constantFalseConjunct(j.Predicate) {
		switch j.Type {
		case JoinTypeLeft:
			if !isDummyRelNode(j.Right) {
				j.Right = dummyRelResult(j.Right)
			}
		case JoinTypeRight:
			if !isDummyRelNode(j.Left) {
				j.Left = dummyRelResult(j.Left)
			}
		}
	}
	setPlanChildrenInPlace(n, dummyConstantFalseOuterJoinSides)
	return n
}

// constantFalseConjunct reports whether e has a top-level conjunct that is a
// constant FALSE or NULL (restriction_is_constant_false).
func constantFalseConjunct(e Expr) bool {
	if e == nil {
		return false
	}
	for _, c := range splitAnd(e) {
		if isNullConstExpr(c) {
			return true
		}
		for {
			cast, isCast := c.(*CastExpr)
			if !isCast {
				break
			}
			c = cast.Operand
		}
		if b, ok := c.(*BooleanConst); ok && !b.Value {
			return true
		}
	}
	return false
}

// dummyRelResult is a dummy rel standing in for n: a childless Result whose
// one-time filter is false, emitting n's schema (set_dummy_rel_pathlist).
// The dummy rel keeps its reltarget in PG, so an output n computes from no
// input column — `(SELECT 1 AS a) ss1`'s `1`, a PlaceHolderVar above the
// join — stays that expression and EXPLAIN prints `((1) IS NULL)` over it,
// as PG does; every other output is an identity column reference.
func dummyRelResult(n Node) Node {
	schema := n.Output()
	targets := identityResultTargets(schema)
	if exprs := constantOutputExprs(n); len(exprs) == len(targets) {
		for i, e := range exprs {
			if e != nil && !exprContainsColumnRef(e) && !exprCarriesSublink(e) {
				targets[i] = e
			}
		}
	}
	return &Result{
		pos:           n.Pos(),
		Targets:       targets,
		OneTimeFilter: &BooleanConst{pos: n.Pos(), Value: false},
		schema:        schema,
	}
}

// constantOutputExprs returns the source expression of each output of a
// projecting node, following column references down through Project /
// Result children and trivial SubqueryScan wrappers to the expression that
// computes them (a single-row VALUES list counts as one). An output whose
// source is not a projection stays its own column reference; nil when n is
// not a projection at all.
func constantOutputExprs(n Node) []Expr {
	for depth := 0; depth < 16; depth++ {
		switch x := n.(type) {
		case *SubqueryScan:
			n = x.Child
			continue
		case *Values:
			if len(x.Rows) == 1 {
				return x.Rows[0]
			}
			return nil
		case *Project:
			return resolveThroughChild(x.Targets, x.Child)
		case *Result:
			return resolveThroughChild(x.Targets, x.Child)
		}
		return nil
	}
	return nil
}

// resolveThroughChild replaces each bare column reference in targets by the
// child's source expression for that column, when the child is itself a
// projection.
func resolveThroughChild(targets []Expr, child Node) []Expr {
	if child == nil {
		return targets
	}
	below := constantOutputExprs(child)
	out := make([]Expr, len(targets))
	for i, t := range targets {
		out[i] = t
		if cr, ok := t.(*ColumnRef); ok && cr.Index >= 0 && cr.Index < len(below) && below[cr.Index] != nil {
			out[i] = below[cr.Index]
		}
	}
	return out
}

// setPlanChildrenInPlace replaces every exported child plan node of n by
// fn(child), mutating n — the in-place twin of mapPlanChildren.
func setPlanChildrenInPlace(n Node, fn func(Node) Node) {
	v := reflect.ValueOf(n)
	if v.Kind() != reflect.Ptr || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return
	}
	e := v.Elem()
	for i := 0; i < e.NumField(); i++ {
		f := e.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Type() {
		case nodeIfaceType:
			if !f.IsNil() {
				if out := fn(f.Interface().(Node)); out != nil {
					f.Set(reflect.ValueOf(out))
				}
			}
		case nodeSliceType:
			for j := 0; j < f.Len(); j++ {
				if el := f.Index(j); !el.IsNil() {
					if out := fn(el.Interface().(Node)); out != nil {
						el.Set(reflect.ValueOf(out))
					}
				}
			}
		}
	}
}
