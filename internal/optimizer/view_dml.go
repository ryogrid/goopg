package optimizer

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// viewAutoUpdatableChain inspects a view's defining query and walks it down
// to the ultimate real base relation an INSERT/UPDATE/DELETE against the
// view can be rewritten onto, mirroring PostgreSQL's "simply updatable view"
// rule (rewriteHandler.c's view_is_auto_updatable / rewrite_targetlist):
// exactly one base relation in FROM, no joins/aggregation/set-ops/limiting,
// and a target list in which every entry is a bare column reference to that
// relation (root-0025 deferred item 1: a subset, reordering, and/or renaming
// of the base relation's columns is fine — PostgreSQL's rule only requires
// each view column to be a plain Var over the base relation, not that every
// base column appear exactly once in order).
//
// When the FROM relation is itself such a simple updatable view, the walk
// recurses into it (view-of-view chaining) — PostgreSQL updates through an
// arbitrarily deep chain of simple views the same way it updates through
// one.
//
// chain holds every view level from outermost (tbl itself) to innermost, in
// that order — the same top-to-bottom order PostgreSQL's CASCADED/LOCAL
// CHECK OPTION propagation walks (see viewChainQuals). colMaps is parallel
// to chain: colMaps[k][i] is the ordinal, within the ultimate base
// relation, that chain[k]'s own i-th output column maps onto — composed
// through every intervening chain level so callers never need to translate
// column ordinals themselves.
//
// This is a deliberately narrow subset of PostgreSQL's real rule (which also
// auto-updates views whose target list mixes plain columns with arbitrary
// expressions, so long as INSERT/UPDATE doesn't target the expression
// columns). goopg has no INSTEAD OF trigger/rule mechanism and requires
// every target-list entry to be a plain column reference; anything outside
// that stays read-only — see docs/design/root-0025-updatable-views.md.
//
// ok is false for anything requiring that broader machinery; callers should
// reject the DML with 55000 ("cannot insert/update/delete into/from view").
func viewAutoUpdatableChain(tbl *catalog.Table, cat catalog.Catalog) (chain []*catalog.Table, base *catalog.Table, colMaps [][]int, ok bool) {
	v := tbl.View
	if v == nil {
		return nil, nil, nil, false
	}
	if v.Distinct || len(v.DistinctOn) > 0 || len(v.GroupBy) > 0 || v.Having != nil ||
		v.Limit != nil || v.Offset != nil || v.SetOp != nil || len(v.Locking) > 0 ||
		len(v.ValuesRows) > 0 || v.With != nil || v.SetOpOperand != nil {
		return nil, nil, nil, false
	}
	if len(v.From) != 1 || hasJoinClauses(v.FromExprs) {
		return nil, nil, nil, false
	}
	rv := v.From[0]
	if rv.Subquery != nil || rv.TableFunc != nil {
		return nil, nil, nil, false
	}
	// CreateView marks EVERY view — including a plain, chainable one — as
	// catalog.Table.Virtual (it has no physical heap storage of its own), so
	// only a virtual relation that is ALSO not a view (a system catalog like
	// pg_class) is disqualified here; a plain view is instead routed into the
	// recursive chain-walk below via its own b.View != nil check.
	b, found := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
	if !found || (b.Virtual && b.View == nil) || b.IsMatView {
		return nil, nil, nil, false
	}
	ownMap, mapOK := viewColumnMap(v.Targets, rv, b, tbl)
	if !mapOK {
		return nil, nil, nil, false
	}
	if b.View != nil {
		innerChain, innerBase, innerColMaps, innerOK := viewAutoUpdatableChain(b, cat)
		if !innerOK {
			return nil, nil, nil, false
		}
		composed := make([]int, len(ownMap))
		for i, bOrd := range ownMap {
			composed[i] = innerColMaps[0][bOrd]
		}
		return append([]*catalog.Table{tbl}, innerChain...), innerBase,
			append([][]int{composed}, innerColMaps...), true
	}
	return []*catalog.Table{tbl}, b, [][]int{ownMap}, true
}

// viewColumnMap reports whether targets is a target list in which every
// entry is a bare column reference to a column of b (either `SELECT *` or
// an explicit list of plain column references — no expressions), and if so
// returns the per-entry mapping from target-list ordinal to b's column
// ordinal. view is the view's own catalog.Table; its frozen Columns (fixed
// at CREATE VIEW time) drive the bare-* arm — see below. Unlike PostgreSQL's
// full per-column attribute map, goopg requires EVERY entry to be a plain
// column reference (mixed expression/plain-column views stay entirely
// read-only) — but within that restriction, a subset of
// b's columns, a reordering, and/or a rename (via column alias or an
// explicit `CREATE VIEW v (a, b)` column list — both are already folded
// into the view's own catalog.Table.Columns names by CreateView, so this
// function only needs to match against the SOURCE column referenced, not
// whatever the view calls it) are all fine — root-0025 deferred item 1.
func viewColumnMap(targets []parser.ResTarget, rv parser.RangeVar, b *catalog.Table, view *catalog.Table) ([]int, bool) {
	if len(targets) == 1 {
		if star, isStar := targets[0].Expr.(*parser.StarExpr); isStar && star.Table == "" && star.Schema == "" && targets[0].Alias == "" {
			// Bare `SELECT *`: a star maps POSITIONALLY by definition — the
			// view's i-th frozen output column is the FROM relation's i-th
			// column, whatever the view calls it (an explicit `CREATE VIEW v
			// (a, b)` column list renames the output, never reorders or
			// subsets the frozen expansion; and CREATE OR REPLACE VIEW may
			// only APPEND columns at the end, never reorder or rename the
			// existing prefix — so the frozen prefix's positional identity is
			// stable). goopg stores the star UNEXPANDED
			// (v.View.Targets = [StarExpr]), so the map is an identity over
			// the view's FROZEN column count (view.Columns) — NOT over
			// len(b.Columns), which CREATE OR REPLACE VIEW can have grown past
			// the frozen count, leaving colMap longer than viewColumnNames(tbl)
			// and indexing out of range in viewProxyTable (M0134-0002 slice 1,
			// bug #17811).
			m := make([]int, len(view.Columns))
			for i := range m {
				m[i] = i
			}
			return m, true
		}
	}
	m := make([]int, len(targets))
	for i, t := range targets {
		cr, isCol := t.Expr.(*parser.ColumnRef)
		if !isCol {
			return nil, false
		}
		if cr.Table != "" && !strings.EqualFold(cr.Table, rv.Name) && !strings.EqualFold(cr.Table, rv.Alias) {
			return nil, false
		}
		j := -1
		for k := range b.Columns {
			if strings.EqualFold(b.Columns[k].Name, cr.Column) {
				j = k
				break
			}
		}
		if j == -1 {
			return nil, false
		}
		m[i] = j
	}
	return m, true
}

// viewColumnNames returns tbl's own output column names, in ordinal order —
// the names a DML statement (or an outer chain level's WHERE clause) that
// references tbl uses, regardless of whether they came from the defining
// query's per-target aliases or an explicit `CREATE VIEW v (a, b, ...)`
// column list (CreateView already folds either source into tbl.Columns).
func viewColumnNames(tbl *catalog.Table) []string {
	names := make([]string, len(tbl.Columns))
	for i, c := range tbl.Columns {
		names[i] = c.Name
	}
	return names
}

// viewProxyTable builds a synthetic table with the exact same physical
// column count, order, and ordinals as base — so a row scanned from base is
// still indexed identically — but with the column at base ordinal colMap[i]
// renamed to names[i] for every i. A base column not covered by colMap (the
// view selects a strict subset of base's columns) is left with an empty
// Name, which can never match a real ColumnRef (the parser never produces
// one with an empty column name), so it is unresolvable through the view —
// matching PostgreSQL, which only exposes a view's own target-list columns.
//
// This lets a DML statement (or a chain level's own WHERE clause) written
// against a view that subsets/reorders/renames its base relation's columns
// resolve directly onto base's physical row layout via the normal
// name-based resolveContext machinery, with no separate rewrite pass.
func viewProxyTable(base *catalog.Table, names []string, colMap []int) *catalog.Table {
	proxy := *base
	proxy.Columns = append([]catalog.Column(nil), base.Columns...)
	for i := range proxy.Columns {
		proxy.Columns[i].Name = ""
	}
	for viewOrd, baseOrd := range colMap {
		proxy.Columns[baseOrd].Name = names[viewOrd]
	}
	return &proxy
}

// viewChainQuals resolves every level of a view chain's own WHERE clause
// (outermost tbl first, as returned by viewAutoUpdatableChain) against the
// ultimate base relation and combines them per PostgreSQL's rules:
//
//   - all is the AND of every level's qual, unconditionally: a chained view
//     only ever exposes the rows visible through every level's own SELECT,
//     independent of CHECK OPTION — this is the row-visibility restriction
//     UPDATE/DELETE must apply so they can't touch a row the view itself (at
//     any level) would filter out.
//   - checked is the AND of only the quals PostgreSQL's CASCADED/LOCAL CHECK
//     OPTION propagation actually enforces (rewriteHandler.c ~3791-3843): a
//     CASCADED check option (the default for an unqualified WITH CHECK
//     OPTION) forces every inner level to be checked too, regardless of that
//     inner level's own CheckOption setting; a LOCAL check option checks
//     only levels that themselves specify CHECK OPTION. checked is nil when
//     no level in the chain triggers a check.
func viewChainQuals(pos int, chain []*catalog.Table, colMaps [][]int, base *catalog.Table, cat catalog.Catalog) (all, checked Expr, err error) {
	forceCascade := false
	for i, lvl := range chain {
		var innerNames []string
		var innerColMap []int
		if i+1 < len(chain) {
			innerNames = viewColumnNames(chain[i+1])
			innerColMap = colMaps[i+1]
		}
		q, qerr := viewQualOnBase(lvl, innerNames, innerColMap, base, cat)
		if qerr != nil {
			return nil, nil, qerr
		}
		all = andExpr(pos, all, q)
		if lvl.CheckOption != "" || forceCascade {
			checked = andExpr(pos, checked, q)
		}
		if lvl.CheckOption == "cascaded" || forceCascade {
			forceCascade = true
		}
	}
	return all, checked, nil
}

// viewQualOnBase resolves a view's own WHERE clause — written using the
// column names of its immediate FROM relation, and the alias the view's
// defining query itself bound (not whatever alias the outer DML statement
// used) — against the ultimate base relation. When the immediate FROM
// relation is itself a view further down the chain (innerNames/innerColMap
// non-nil), a viewProxyTable translates that level's exposed column names
// onto base's physical layout first; when the immediate FROM is base
// itself, its names already are base's own names and no translation is
// needed. Returns nil when the view has no WHERE clause.
func viewQualOnBase(tbl *catalog.Table, innerNames []string, innerColMap []int, base *catalog.Table, cat catalog.Catalog) (Expr, error) {
	if tbl.View.Where == nil {
		return nil, nil
	}
	rv := tbl.View.From[0]
	alias := rv.Alias
	if alias == "" {
		alias = rv.Name
	}
	resolveTbl := base
	if innerNames != nil {
		resolveTbl = viewProxyTable(base, innerNames, innerColMap)
	}
	ctx := singleBindingContext(resolveTbl, alias)
	ctx.cat = cat
	return resolveExpr(tbl.View.Where, ctx)
}

// viewResolveAlias picks the alias a DML statement's resolveContext binding
// should use when substituting resolveTbl (viewProxyTable) in place of a
// view target: the statement's own explicit alias when given, otherwise the
// view's own name. resolveTbl's Name field is always the physical base
// relation's name (ordinal-based lookups elsewhere — e.g. partition
// routing — key off it), never the view's, so without this an unaliased
// qualified reference to the view itself (`UPDATE v SET ... WHERE v.col =
// ...`) would fail to resolve against the substituted binding.
func viewResolveAlias(explicit, viewName string) string {
	if explicit != "" {
		return explicit
	}
	return viewName
}

// andExpr combines two possibly-nil boolean Exprs with AND, returning
// whichever side is non-nil when the other is absent.
func andExpr(pos int, left, right Expr) Expr {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	return &BinaryOp{pos: pos, Op: parser.OpAnd, Left: left, Right: right}
}

// viewCmd enumerates the three DML commands that can target a view, so
// viewNotUpdatableError can render PostgreSQL's per-command wording.
type viewCmd int

const (
	viewCmdInsert viewCmd = iota
	viewCmdUpdate
	viewCmdDelete
)

// viewNotUpdatableError mirrors PostgreSQL's error_view_not_updatable
// (rewriteHandler.c): ERRCODE_OBJECT_NOT_IN_PREREQUISITE_STATE (55000),
// with a command-specific message/hint.
func viewNotUpdatableError(pos int, viewName string, cmd viewCmd) *PlanError {
	var verb, ing, trig string
	switch cmd {
	case viewCmdInsert:
		verb, ing, trig = "insert into", "inserting into", "INSERT"
	case viewCmdUpdate:
		verb, ing, trig = "update", "updating", "UPDATE"
	case viewCmdDelete:
		verb, ing, trig = "delete from", "deleting from", "DELETE"
	}
	return &PlanError{
		Pos:     pos,
		Code:    "55000",
		Message: fmt.Sprintf("cannot %s view %q", verb, viewName),
		Hint: fmt.Sprintf("To enable %s the view, provide an INSTEAD OF %s trigger or an unconditional ON %s DO INSTEAD rule.",
			ing, trig, trig),
	}
}
