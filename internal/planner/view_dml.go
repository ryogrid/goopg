package planner

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
// and a target list that is a bare, unrenamed, in-order passthrough of every
// column of that relation (either `SELECT *` or an explicit column list
// naming each base column once, in catalog order).
//
// When the FROM relation is itself such a simple updatable view, the walk
// recurses into it (view-of-view chaining) — PostgreSQL updates through an
// arbitrarily deep chain of simple views the same way it updates through
// one. Because every level's target list is required to be an unrenamed,
// in-order passthrough, column names and ordinals are identical at every
// level all the way down to base, so a chain level's own WHERE clause can
// always be resolved directly against base (see viewQualOnBase/
// viewChainQuals) without any column-mapping translation.
//
// chain holds every view level from outermost (tbl itself) to innermost, in
// that order — the same top-to-bottom order PostgreSQL's CASCADED/LOCAL
// CHECK OPTION propagation walks (see viewChainQuals).
//
// This is a deliberately narrow subset of PostgreSQL's real rule (which also
// auto-updates views exposing a subset/reordering/renaming of columns, via
// a per-column attribute map). goopg has no INSTEAD OF trigger/rule
// mechanism, so anything outside this subset stays read-only — see
// docs/design/root-0025-updatable-views.md.
//
// ok is false for anything requiring that broader machinery; callers should
// reject the DML with 55000 ("cannot insert/update/delete into/from view").
func viewAutoUpdatableChain(tbl *catalog.Table, cat catalog.Catalog) (chain []*catalog.Table, base *catalog.Table, ok bool) {
	v := tbl.View
	if v == nil || len(tbl.ViewColumnAliases) > 0 {
		return nil, nil, false
	}
	if v.Distinct || len(v.DistinctOn) > 0 || len(v.GroupBy) > 0 || v.Having != nil ||
		v.Limit != nil || v.Offset != nil || v.SetOp != nil || len(v.Locking) > 0 ||
		len(v.ValuesRows) > 0 || v.With != nil {
		return nil, nil, false
	}
	if len(v.From) != 1 || hasJoinClauses(v.FromExprs) {
		return nil, nil, false
	}
	rv := v.From[0]
	if rv.Subquery != nil || rv.TableFunc != nil {
		return nil, nil, false
	}
	// CreateView marks EVERY view — including a plain, chainable one — as
	// catalog.Table.Virtual (it has no physical heap storage of its own), so
	// only a virtual relation that is ALSO not a view (a system catalog like
	// pg_class) is disqualified here; a plain view is instead routed into the
	// recursive chain-walk below via its own b.View != nil check.
	b, found := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
	if !found || (b.Virtual && b.View == nil) || b.IsMatView {
		return nil, nil, false
	}
	if !viewTargetsPassthrough(v.Targets, rv, b) {
		return nil, nil, false
	}
	if b.View != nil {
		innerChain, innerBase, innerOK := viewAutoUpdatableChain(b, cat)
		if !innerOK {
			return nil, nil, false
		}
		return append([]*catalog.Table{tbl}, innerChain...), innerBase, true
	}
	return []*catalog.Table{tbl}, b, true
}

// viewTargetsPassthrough reports whether targets is a bare, unrenamed,
// in-order passthrough of every column of b (either `SELECT *` or an
// explicit column list naming each column of b once, in catalog order) —
// the target-list half of viewAutoUpdatableChain's "simply updatable view"
// rule, shared by every level of a view chain.
func viewTargetsPassthrough(targets []parser.ResTarget, rv parser.RangeVar, b *catalog.Table) bool {
	if len(targets) == 1 {
		if star, isStar := targets[0].Expr.(*parser.StarExpr); isStar && star.Table == "" && star.Schema == "" && targets[0].Alias == "" {
			return true
		}
	}
	if len(targets) != len(b.Columns) {
		return false
	}
	for i, t := range targets {
		cr, isCol := t.Expr.(*parser.ColumnRef)
		if !isCol || !strings.EqualFold(cr.Column, b.Columns[i].Name) {
			return false
		}
		if cr.Table != "" && !strings.EqualFold(cr.Table, rv.Name) && !strings.EqualFold(cr.Table, rv.Alias) {
			return false
		}
		if t.Alias != "" && !strings.EqualFold(t.Alias, b.Columns[i].Name) {
			return false
		}
	}
	return true
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
func viewChainQuals(pos int, chain []*catalog.Table, base *catalog.Table, cat catalog.Catalog) (all, checked Expr, err error) {
	forceCascade := false
	for _, lvl := range chain {
		q, qerr := viewQualOnBase(lvl, base, cat)
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

// viewQualOnBase resolves a view's own WHERE clause against its base
// relation, using the alias the view's defining query itself bound (not
// whatever alias the outer DML statement used) so unqualified and
// qualified column references inside the stored qual resolve correctly.
// Returns nil when the view has no WHERE clause.
func viewQualOnBase(tbl *catalog.Table, base *catalog.Table, cat catalog.Catalog) (Expr, error) {
	if tbl.View.Where == nil {
		return nil, nil
	}
	rv := tbl.View.From[0]
	alias := rv.Alias
	if alias == "" {
		alias = rv.Name
	}
	ctx := singleBindingContext(base, alias)
	ctx.cat = cat
	return resolveExpr(tbl.View.Where, ctx)
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
