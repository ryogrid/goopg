package optimizer

import (
	"reflect"

	"github.com/goopg/goopg/internal/parser"
)

// prefetchFuncDepPassthroughs resolves, BEFORE the grouping election, every
// bare column reference the statement reads above its aggregate — the target
// list, ORDER BY and DISTINCT ON — so that each column the GROUP BY key only
// functionally determines is already on the Aggregate's Passthrough list when
// `createGroupingPaths` prices its candidates (M0145-0008m).
//
// The functionally-dependent passthrough used to be appended lazily, during
// target resolution, AFTER the election. A candidate that narrows the
// aggregate's input — `indexOrderedAggInput`'s Index Only Scan on the primary
// key, whose coverage check reads `aggNode.Passthrough` — could therefore win
// without the column the passthrough reads: `SELECT sum(c1), c2 ... GROUP BY
// c1` (c1 the primary key) elected `GroupAggregate > Index Only Scan` emitting
// only c1, and the executor's swallowed evaluation error read c2 as NULL.
// PG builds grouping paths only after the query's final targets are known
// (`grouping_planner` computes `final_target` / `grouping_target` before
// `create_grouping_paths`), so no candidate can omit a column an upper node
// needs.
//
// The pass calls the same `resolveExprAfterAggregate` the later resolution
// runs, whose only side effect is that lazy append, so the later pass finds
// each column already added (`agg.funcDepCols`). Results and errors are
// discarded here: the real resolution reports them in its own order. It does
// not descend into aggregate-call arguments (those read the aggregate's INPUT,
// not a passthrough) or into nested sub-selects (planned in their own scope,
// and re-planning them here would allocate a second set of scan identities).
func prefetchFuncDepPassthroughs(s *parser.SelectStmt, agg *aggregateSurface) {
	if s == nil || agg == nil || agg.node == nil {
		return
	}
	visit := func(cr *parser.ColumnRef) {
		_, _ = resolveExprAfterAggregate(cr, agg)
	}
	// A sub-expression that IS a grouping expression resolves as a whole to
	// its group-key column (`resolveExprAfterAggregate` tries that match
	// first), so the columns inside it are never read above the aggregate and
	// must not become passthroughs: TPC-DS Q23's `substr(i_item_desc, 1, 30)`
	// is a GROUP BY expression, and descending into it would add
	// `i_item_desc` (determined by the item primary key) for nothing.
	isGroupKey := func(e parser.Expr) bool {
		if _, ok := agg.groupByExpr[parserExprKey(e)]; ok {
			return true
		}
		_, ok := agg.groupByExprQual[qualifiedGroupKey(e)]
		return ok
	}
	for _, t := range s.Targets {
		if _, isStar := t.Expr.(*parser.StarExpr); isStar {
			// A star expands against the aggregate input and adds every
			// functionally-dependent column it meets; the target pass's own
			// star arm is that expansion.
			_, _, _ = resolveTargetsAfterAggregate([]parser.ResTarget{t}, agg)
			continue
		}
		walkParserColumnRefs(reflect.ValueOf(t.Expr), visit, isGroupKey, 0)
	}
	for _, o := range s.OrderBy {
		walkParserColumnRefs(reflect.ValueOf(o.Expr), visit, isGroupKey, 0)
	}
	for _, e := range s.DistinctOn {
		walkParserColumnRefs(reflect.ValueOf(e), visit, isGroupKey, 0)
	}
}

// walkParserColumnRefs visits every *parser.ColumnRef reachable from v through
// exported fields, stopping at aggregate calls, nested SELECTs and any
// expression `stop` claims (a grouping expression).
func walkParserColumnRefs(v reflect.Value, visit func(*parser.ColumnRef), stop func(parser.Expr) bool, depth int) {
	if depth > 64 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() {
			walkParserColumnRefs(v.Elem(), visit, stop, depth+1)
		}
		return
	case reflect.Ptr:
		if v.IsNil() || !v.CanInterface() {
			return
		}
		iface := v.Interface()
		if e, isExpr := iface.(parser.Expr); isExpr {
			if _, isCol := e.(*parser.ColumnRef); !isCol && stop(e) {
				return
			}
		}
		switch x := iface.(type) {
		case *parser.ColumnRef:
			visit(x)
			return
		case *parser.SelectStmt:
			return
		case *parser.FuncCall:
			if isAggregateFunc(x) {
				return
			}
		}
		walkParserColumnRefs(v.Elem(), visit, stop, depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if f := v.Field(i); f.CanInterface() {
				walkParserColumnRefs(f, visit, stop, depth+1)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkParserColumnRefs(v.Index(i), visit, stop, depth+1)
		}
	}
}
