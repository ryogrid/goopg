package optimizer

import (
	"github.com/goopg/goopg/internal/catalog"
)

// planState is one top-level statement's planner dynamic scope (P0-E8).
//
// The planner used to keep this scope in package-level variables — planCTEs
// (the WITH list in scope), planParent (the outer resolveContext a nested
// sub-SELECT inherits) and the analyzer's outerScope — set on entry to a
// nested scope and restored on exit, on the assumption that one statement
// plans on one goroutine at a time. The postmaster runs one goroutine per
// connection and plans with no lock, so two sessions planning at once swapped
// each other's scope: session B's restore of planCTEs (back to nil) landed
// while session A was between registering its WITH list and resolving a
// reference to it, and A's `FROM x` fell through to the catalog — `relation
// "x" does not exist` on a valid `WITH x AS (...) SELECT ... FROM x`. Only a
// cold plan cache plans concurrently, so the failures were the first
// statements of a burst of new connections.
//
// PostgreSQL keeps this scope per query: p_ctenamespace and parentParseState
// on the ParseState (parse_cte.c, parse_node.h), and each backend is a process
// with its own globals. goopg now does the same. The state rides on the
// catalog every planning function already receives, wrapped once per
// top-level statement by withPlanState; re-entrant planning (a view body,
// EXPLAIN's inner statement, COPY (query)) passes the wrapped catalog and so
// shares its statement's state, exactly as it shared the globals.
//
// A global planning mutex was measured first and rejected: it serialises
// planning across sessions (pgbench -S, 32 clients on 16 cores: 58.7k → 16.2k
// tps).
type planState struct {
	// ctes is the WITH list in scope (formerly the package-level planCTEs):
	// nil when no WITH clause is in scope.
	ctes map[string]*plannedCTE
	// parent is the outer resolveContext the next nested sub-SELECT's
	// top-level context is seeded with (formerly planParent).
	parent *resolveContext
	// analyzerOuterScope is the analyzer's lexical-scope slot
	// (analyzer.OuterScopeCarrier) — formerly the analyzer's package-level
	// outerScope.
	analyzerOuterScope any
	// groupedRels keeps each scope's GROUP_AGG rel past its own planning,
	// keyed by the Aggregate createGroupingPaths wrote the winner onto
	// (M0146-0231). PG's set_subquery_pathlist reads the subquery's final
	// rel's whole pathlist; an enclosing join search reads this record to
	// offer a derived leaf the same surviving paths.
	groupedRels map[*Aggregate]groupedRelRecord
}

// groupedRelRecord is one scope's GROUP_AGG rel and the path that won it.
type groupedRelRecord struct {
	rel  *RelOptInfo
	best *Path
}

// recordGroupedRel files agg's GROUP_AGG rel on cat's statement; a no-op
// when cat carries no planState.
func recordGroupedRel(cat catalog.Catalog, agg *Aggregate, rel *RelOptInfo, best *Path) {
	st := planStateOf(cat)
	if st == nil || agg == nil || rel == nil {
		return
	}
	if st.groupedRels == nil {
		st.groupedRels = make(map[*Aggregate]groupedRelRecord)
	}
	st.groupedRels[agg] = groupedRelRecord{rel: rel, best: best}
}

// groupedRelOf is the record recordGroupedRel filed for agg, if any.
func groupedRelOf(cat catalog.Catalog, agg *Aggregate) (groupedRelRecord, bool) {
	st := planStateOf(cat)
	if st == nil || agg == nil {
		return groupedRelRecord{}, false
	}
	rec, ok := st.groupedRels[agg]
	return rec, ok
}

// stmtCatalog is the per-statement catalog wrapper that carries planState.
// Unwrap lets every wrapper-peeling helper (inMemoryCat, scanToggleDisabled)
// see through it, as they see through catalog.SearchPathCatalog.
type stmtCatalog struct {
	catalog.Catalog
	st *planState
}

func (c *stmtCatalog) Unwrap() catalog.Catalog { return c.Catalog }

// AnalyzerOuterScopeSlot implements analyzer.OuterScopeCarrier.
func (c *stmtCatalog) AnalyzerOuterScopeSlot() *any { return &c.st.analyzerOuterScope }

// withPlanState returns cat carrying a planState: cat itself when it (or a
// wrapper it sits under) already carries one — a re-entrant plan shares its
// statement's scope — else a fresh per-statement wrapper.
func withPlanState(cat catalog.Catalog) catalog.Catalog {
	if cat == nil || planStateOf(cat) != nil {
		return cat
	}
	return &stmtCatalog{Catalog: cat, st: &planState{}}
}

// planStateOf finds the planState cat carries, peeling wrapper catalogs;
// nil when none does.
func planStateOf(cat catalog.Catalog) *planState {
	type unwrapper interface {
		Unwrap() catalog.Catalog
	}
	for i := 0; cat != nil && i < 16; i++ {
		if sc, ok := cat.(*stmtCatalog); ok {
			return sc.st
		}
		u, ok := cat.(unwrapper)
		if !ok {
			return nil
		}
		cat = u.Unwrap()
	}
	return nil
}

// plannedCTEsOf is the WITH list in scope for cat's statement (nil when
// none, or when cat carries no planState).
func plannedCTEsOf(cat catalog.Catalog) map[string]*plannedCTE {
	if st := planStateOf(cat); st != nil {
		return st.ctes
	}
	return nil
}

// planParentOf is the outer resolveContext for cat's statement.
func planParentOf(cat catalog.Catalog) *resolveContext {
	if st := planStateOf(cat); st != nil {
		return st.parent
	}
	return nil
}

// mustPlanState is planStateOf for the scope WRITERS (preplanWithClause,
// planRecursiveCTE, planSelectWithParent, planCTEReferenceAsSubquery). The
// exported entry points wrap their catalog (withPlanState), and so do the
// internal planning functions a caller can enter directly
// (planStmtWithSettings, planSelectWithSettings, planFromClause,
// planSelectWithParent) — idempotently, so a production call shares its
// statement's state and a direct one gets a single scope below it. A writer
// still without state is a path that bypassed all of them; a throwaway state
// keeps it planning with an empty scope.
func mustPlanState(cat catalog.Catalog) *planState {
	if st := planStateOf(cat); st != nil {
		return st
	}
	return &planState{}
}
