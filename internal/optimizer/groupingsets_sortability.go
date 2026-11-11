package optimizer

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// Grouping-column sortability and hashability (M0148-0001j4 item 4).
//
// PG records, per GROUP BY item, whether it has an ordering operator
// (SortGroupClause.sortop, from the type's default btree opclass) and whether
// its equality operator can hash (SortGroupClause.hashable, from the default
// hash opclass); get_sort_group_operators fills both at parse analysis.
// preprocess_grouping_sets (planner.c) then splits the grouping sets: a set
// that contains an unsortable column can only be hashed, and must therefore
// contain no unhashable column; create_grouping_paths can only hash when no
// aggregate has ORDER BY / DISTINCT (numOrderedAggs == 0), so unsortable
// sets beside an ordered aggregate leave grouped_rel->pathlist empty. Both
// end in 0A000 "could not implement GROUP BY".
//
// Among PG 18.3's built-in types (pg_opclass.dat, default opclasses) the
// only one-sided ones are below; every other type with either opclass has
// both. An array type follows its element type (array_ops dispatches to the
// element's operators). Types not listed keep goopg's previous behaviour
// (treated as both sortable and hashable).
var (
	groupingUnhashableTypes = map[string]bool{
		"bit": true, "varbit": true, "bit varying": true,
		"money": true, "tsquery": true, "tsvector": true,
	}
	groupingUnsortableTypes = map[string]bool{
		"xid": true, "cid": true, "aclitem": true,
	}
)

// errCouldNotImplementGroupBy is PG's message for a grouping no path can
// implement (planner.c preprocess_grouping_sets and create_grouping_paths);
// PG reports it with no error position.
func errCouldNotImplementGroupBy() error {
	return &PlanError{Code: "0A000", Message: "could not implement GROUP BY",
		Detail: "Some of the datatypes only support hashing, while others only support sorting."}
}

func groupingTypeName(t catalog.Type) string {
	return strings.ToLower(strings.TrimSpace(t.Name))
}

// groupingExprSortable / groupingExprHashable classify one grouping column
// by its type.
func groupingExprSortable(e Expr) bool {
	return !groupingUnsortableTypes[groupingTypeName(exprType(e))]
}

func groupingExprHashable(e Expr) bool {
	return !groupingUnhashableTypes[groupingTypeName(exprType(e))]
}

// checkGroupingSetsImplementable applies preprocess_grouping_sets' and
// create_grouping_paths' implementability errors to a grouping-sets
// aggregate: a set holding both an unsortable and an unhashable column, or
// any unsortable set while an aggregate needs ordered input.
func checkGroupingSetsImplementable(a *Aggregate) error {
	if a == nil || a.GroupingSets == nil {
		return nil
	}
	unsortable := make([]bool, len(a.GroupExprs))
	unhashable := make([]bool, len(a.GroupExprs))
	oneSided := false
	for i, e := range a.GroupExprs {
		unsortable[i] = !groupingExprSortable(e)
		unhashable[i] = !groupingExprHashable(e)
		oneSided = oneSided || unsortable[i] || unhashable[i]
	}
	if !oneSided {
		return nil
	}
	hasUnsortableSet := false
	for _, set := range a.GroupingSets {
		setUnsortable, setUnhashable := false, false
		for _, gi := range set {
			if gi < 0 || gi >= len(a.GroupExprs) {
				continue
			}
			setUnsortable = setUnsortable || unsortable[gi]
			setUnhashable = setUnhashable || unhashable[gi]
		}
		if setUnsortable {
			if setUnhashable {
				return errCouldNotImplementGroupBy()
			}
			hasUnsortableSet = true
		}
	}
	if hasUnsortableSet && groupingHasSpecialAgg(a) {
		return errCouldNotImplementGroupBy()
	}
	return nil
}
