package optimizer

import (
	"sync"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0148-0001c3 — a partial index's predicate, whichever form the catalog
// still holds.
//
// CREATE INDEX stores both the parsed predicate (Index.Predicate) and its SQL
// text (Index.PredicateString, pg_index.indpred's source). An index loaded at
// server start carries only the text: Predicate is nil while HasPredicate is
// true, so every consumer that read Predicate treated a restarted partial
// index as unusable (the planner) or as having no predicate at all (the
// executor's row maintenance). IndexPredicateExpr re-parses the text once per
// distinct string; the parsed tree is shared read-only.

var indexPredicateParseCache sync.Map // string -> parser.Expr

// IndexPredicateExpr returns idx's predicate as a parser expression: the
// stored tree, else PredicateString parsed. nil for a plain index or an
// unparsable string.
func IndexPredicateExpr(idx *catalog.Index) parser.Expr {
	if idx == nil || !idx.HasPredicate {
		return nil
	}
	if idx.Predicate != nil {
		return idx.Predicate
	}
	if idx.PredicateString == "" {
		return nil
	}
	if e, ok := indexPredicateParseCache.Load(idx.PredicateString); ok {
		return e.(parser.Expr)
	}
	e, err := parser.ParseExpr(idx.PredicateString)
	if err != nil || e == nil {
		return nil
	}
	indexPredicateParseCache.Store(idx.PredicateString, e)
	return e
}

// ResolveIndexPredicateOf is ResolveIndexPredicate over IndexPredicateExpr.
func ResolveIndexPredicateOf(idx *catalog.Index, tbl *catalog.Table) (Expr, error) {
	return ResolveIndexPredicate(IndexPredicateExpr(idx), tbl)
}
