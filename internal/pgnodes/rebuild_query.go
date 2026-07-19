package pgnodes

// rebuild_query.go — M0123-S3 (sub-slice 2, part b): the inverse of
// resolver_query.go. On startup the per-database view reload reads a stored
// pg_rewrite.ev_action (a canonical query tree) with ReadRuleAction, then
// RebuildViewQuery turns the IR Query back into a goopg parser.SelectStmt so
// goopg can re-register/plan the view the same way it did before the restart.
// It is the query-tree analogue of rebuild.go's scalar Rebuild, and pairs with
// the ResolveViewQuery forward resolver:
//
//	ResolveViewQuery -> OutRuleAction (stored)
//	... ReadRuleAction -> RebuildViewQuery
//
// is a fixed point — re-resolving the rebuilt AST against the same relation
// metadata reproduces the input Query byte-for-byte.
//
// Scope matches ResolveViewQuery exactly: a single RTE_RELATION range-table
// entry, an optional WHERE qual, and a flat target list of Vars / scalar
// expressions (Const / OpExpr / FuncExpr over the base relation's columns and
// literals). Any node outside that subset is a reader/producer mismatch and
// returns an error so the reload surfaces it rather than silently dropping the
// view. (SQL-text ev_action values, if any, are discriminated in the reload,
// not here.)
//
// The Query IR is self-describing for the rebuild: the FROM item's name is the
// single RTE eref's aliasname and every column name is that eref's colnames
// list (populated by the forward resolver with the relation's full column list
// in attribute-number order), so a Var's attno maps straight back to a column
// reference without a live catalog lookup.
//
// Design: docs/design/0123-0004-pgnodes-query-serializer.md.

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// viewRebuildScope carries the range-table metadata RebuildViewQuery needs to
// turn a Var back into a column reference: the base relation name (the FROM
// item) and the full column-name list (indexed by attno-1), both taken from
// the Query's single RTE eref.
type viewRebuildScope struct {
	relname  string
	colnames []string
}

// RebuildViewQuery converts a canonical query-tree IR (as produced by
// ResolveViewQuery and read back by ReadRuleAction) into a goopg view-definition
// AST. It is the reload-time inverse of ResolveViewQuery: resolving the returned
// *SelectStmt against the same relation metadata reproduces the input Query.
func RebuildViewQuery(q *Query) (*parser.SelectStmt, error) {
	if q.CommandType != 1 {
		return nil, fmt.Errorf("pgnodes: RebuildViewQuery: commandType %d (want 1=CMD_SELECT)", q.CommandType)
	}
	if len(q.Rtable) != 1 {
		return nil, fmt.Errorf("pgnodes: RebuildViewQuery: %d range-table entries (want 1)", len(q.Rtable))
	}
	rte, ok := q.Rtable[0].(*RangeTblEntry)
	if !ok {
		return nil, fmt.Errorf("pgnodes: RebuildViewQuery: rtable[0] is %T (want *RangeTblEntry)", q.Rtable[0])
	}
	eref, ok := rte.Eref.(*Alias)
	if !ok {
		return nil, fmt.Errorf("pgnodes: RebuildViewQuery: RTE eref is %T (want *Alias)", rte.Eref)
	}
	scope := &viewRebuildScope{relname: eref.Aliasname, colnames: eref.Colnames}

	if len(q.TargetList) == 0 {
		return nil, fmt.Errorf("pgnodes: RebuildViewQuery: empty target list")
	}
	targets := make([]parser.ResTarget, 0, len(q.TargetList))
	for _, n := range q.TargetList {
		te, ok := n.(*TargetEntry)
		if !ok {
			return nil, fmt.Errorf("pgnodes: RebuildViewQuery: target is %T (want *TargetEntry)", n)
		}
		rt, err := scope.rebuildTarget(te)
		if err != nil {
			return nil, err
		}
		targets = append(targets, rt)
	}

	// The optional WHERE qual lives in the jointree's FromExpr.quals.
	var where parser.Expr
	if q.Jointree != nil {
		fe, ok := q.Jointree.(*FromExpr)
		if !ok {
			return nil, fmt.Errorf("pgnodes: RebuildViewQuery: jointree is %T (want *FromExpr)", q.Jointree)
		}
		if fe.Quals != nil {
			w, err := scope.rebuildExpr(fe.Quals)
			if err != nil {
				return nil, err
			}
			where = w
		}
	}

	// A plain FROM populates both the flattened From list (the check
	// ResolveViewQuery keys on) and one FromExpr with an empty Joins list,
	// exactly as goopg's parser produces for `FROM <one table>`.
	rv := parser.RangeVar{Name: scope.relname}
	return &parser.SelectStmt{
		Targets:   targets,
		From:      []parser.RangeVar{rv},
		FromExprs: []parser.FromExpr{{Base: rv}},
		Where:     where,
	}, nil
}

// rebuildTarget reconstructs one ResTarget. It emits an explicit AS alias only
// when the target's stored resname differs from the name goopg's forward
// resolver would auto-derive (a column reference's own column name, a function
// call's name) — the inverse of queryScope.targetName. That keeps
// resolve→Rebuild→re-resolve a fixed point without decorating plain column
// targets with redundant aliases.
func (s *viewRebuildScope) rebuildTarget(te *TargetEntry) (parser.ResTarget, error) {
	expr, err := s.rebuildExpr(te.Expr)
	if err != nil {
		return parser.ResTarget{}, err
	}
	alias := te.Resname
	if natural, ok := s.naturalName(te.Expr); ok && natural == te.Resname {
		alias = ""
	}
	return parser.ResTarget{Alias: alias, Expr: expr}, nil
}

// naturalName returns the resname goopg's forward resolver would auto-derive for
// this target expression (queryScope.targetName): a column reference's column
// name or a function call's name. It returns ok=false for shapes with no
// auto-name (a bare operator expression), which therefore always require an
// explicit alias to round-trip.
func (s *viewRebuildScope) naturalName(n Node) (string, bool) {
	switch v := n.(type) {
	case *Var:
		name, err := s.columnName(v.Varattno)
		if err != nil {
			return "", false
		}
		return name, true
	case *FuncExpr:
		if name, ok := catalog.RegprocName(v.Funcid); ok {
			return name, true
		}
	}
	return "", false
}

// rebuildExpr is the query-scoped recursion: it turns a Var into a column
// reference over the single base relation and delegates every scalar shape to
// the shared rebuild helpers (so a Const, OpExpr, or FuncExpr rebuilds
// identically to the S2 DEFAULT-expression path), threading itself as the
// recursion so operands of an OpExpr / FuncExpr may themselves be column Vars.
func (s *viewRebuildScope) rebuildExpr(n Node) (parser.Expr, error) {
	switch v := n.(type) {
	case *Var:
		name, err := s.columnName(v.Varattno)
		if err != nil {
			return nil, err
		}
		return &parser.ColumnRef{Column: name}, nil
	case *Const:
		return rebuildConst(v)
	case *OpExpr:
		return rebuildOpExprWith(v, s.rebuildExpr)
	case *FuncExpr:
		return rebuildFuncExprWith(v, s.rebuildExpr)
	case *BoolExpr:
		return rebuildBoolExprWith(v, s.rebuildExpr)
	case *NullTest:
		return rebuildNullTestWith(v, s.rebuildExpr)
	case *BooleanTest:
		return rebuildBooleanTestWith(v, s.rebuildExpr)
	default:
		return nil, fmt.Errorf("pgnodes: RebuildViewQuery: unsupported node %T", n)
	}
}

// columnName maps a 1-based Var attribute number back to its column name via the
// RTE eref colnames list (attno-1). An out-of-range attno is a producer/reader
// mismatch (the eref must list every column of the relation).
func (s *viewRebuildScope) columnName(attno int32) (string, error) {
	i := int(attno) - 1
	if attno < 1 || i >= len(s.colnames) {
		return "", fmt.Errorf("pgnodes: RebuildViewQuery: Var attno %d out of range (relation eref has %d columns)", attno, len(s.colnames))
	}
	return s.colnames[i], nil
}
