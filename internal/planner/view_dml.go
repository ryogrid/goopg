package planner

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// viewAutoUpdatableBase inspects a view's defining query and returns the
// single base relation an INSERT/UPDATE/DELETE against the view can be
// rewritten onto, mirroring PostgreSQL's "simply updatable view" rule
// (rewriteHandler.c's view_is_auto_updatable / rewrite_targetlist): exactly
// one base relation in FROM, no joins/aggregation/set-ops/limiting, and a
// target list that is a bare, unrenamed, in-order passthrough of every
// column of that relation (either `SELECT *` or an explicit column list
// naming each base column once, in catalog order).
//
// This is a deliberately narrow subset of PostgreSQL's real rule (which also
// auto-updates views exposing a subset/reordering/renaming of columns, via
// a per-column attribute map). goopg has no INSTEAD OF trigger/rule
// mechanism, so anything outside this subset stays read-only — see
// docs/design/root-0025-updatable-views.md.
//
// ok is false for anything requiring that broader machinery; callers should
// reject the DML with 55000 ("cannot insert/update/delete into/from view").
func viewAutoUpdatableBase(tbl *catalog.Table, cat catalog.Catalog) (base *catalog.Table, ok bool) {
	v := tbl.View
	if v == nil || len(tbl.ViewColumnAliases) > 0 {
		return nil, false
	}
	if v.Distinct || len(v.DistinctOn) > 0 || len(v.GroupBy) > 0 || v.Having != nil ||
		v.Limit != nil || v.Offset != nil || v.SetOp != nil || len(v.Locking) > 0 ||
		len(v.ValuesRows) > 0 || v.With != nil {
		return nil, false
	}
	if len(v.From) != 1 || hasJoinClauses(v.FromExprs) {
		return nil, false
	}
	rv := v.From[0]
	if rv.Subquery != nil || rv.TableFunc != nil {
		return nil, false
	}
	b, found := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
	if !found || b.View != nil || b.Virtual || b.IsMatView {
		return nil, false
	}
	if len(v.Targets) == 1 {
		if star, isStar := v.Targets[0].Expr.(*parser.StarExpr); isStar && star.Table == "" && star.Schema == "" && v.Targets[0].Alias == "" {
			return b, true
		}
	}
	if len(v.Targets) != len(b.Columns) {
		return nil, false
	}
	for i, t := range v.Targets {
		cr, isCol := t.Expr.(*parser.ColumnRef)
		if !isCol || !strings.EqualFold(cr.Column, b.Columns[i].Name) {
			return nil, false
		}
		if cr.Table != "" && !strings.EqualFold(cr.Table, rv.Name) && !strings.EqualFold(cr.Table, rv.Alias) {
			return nil, false
		}
		if t.Alias != "" && !strings.EqualFold(t.Alias, b.Columns[i].Name) {
			return nil, false
		}
	}
	return b, true
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
