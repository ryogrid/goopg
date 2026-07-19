package pgnodes

// resolver_query.go — M0123-S3 (sub-slice 2, part a): resolve a goopg view
// definition (parser.SelectStmt) into the canonical query-tree IR (Query) so a
// real PG18 standby can PLAN/EXECUTE goopg's stored user views. This is the
// forward direction that pairs with the S3 sub-slice-1 codec (OutRuleAction /
// ReadRuleAction) and the S0 OID indexes; it is the query-tree analogue of the
// S2 scalar resolver (ResolveExpr) and, like it, is a pure internal/pgnodes
// addition with no engine wiring yet (the writeViewRewriteRow / relhasrules
// wiring is sub-slice 2 part c).
//
// Scope of THIS sub-slice: the single-base-relation SELECT view shape only —
// exactly the shape the S3 codec round-trips:
//
//	CREATE VIEW v AS SELECT <targets> FROM <one table> [WHERE <qual>]
//
// with a flat target list of column references and scalar expressions (Vars,
// OpExprs, FuncExprs over the base relation's columns and literals) and an
// optional WHERE qualifier. Anything outside that subset — a JOIN, subquery,
// GROUP BY/HAVING/ORDER BY/LIMIT, DISTINCT, set operation, window, CTE, an
// aliased FROM, a bare `*`, or any expression the scalar resolver cannot handle
// — makes ResolveViewQuery return ErrUnsupported so the writer degrades to
// storing the view as SQL text (all-or-nothing; see unsupported.go and 02e §3's
// graceful-degradation invariant — never partial-emit, and keep
// pg_class.relhasrules=false for such a view).
//
// Provenance: the produced Query mirrors what PostgreSQL's parse-analysis
// (transformSelectStmt / addRangeTableEntryForRelation / transformTargetList)
// stores in pg_rewrite.ev_action for the same DDL. Fixed fields (rtekind,
// rellockmode=AccessShareLock, requiredPerms=ACL_SELECT, perminfoindex, the Var
// syn fields, resjunk=false, …) match that path; the selectedCols Bitmapset
// offsets each referenced attribute number by -FirstLowInvalidHeapAttributeNumber
// (= +7, postgres/src/include/access/sysattr.h) exactly as
// markVarForSelectPriv → add to selectedCols does.
//
// Design: docs/design/0123-0004-pgnodes-query-serializer.md.

import "github.com/goopg/goopg/internal/parser"

// selectedColsBias is -FirstLowInvalidHeapAttributeNumber
// (postgres/src/include/access/sysattr.h: FirstLowInvalidHeapAttributeNumber ==
// -7). PostgreSQL stores a referenced attribute number in the selectedCols /
// insertedCols / updatedCols Bitmapsets biased by this amount so that system
// columns (negative attnos) map to non-negative members; a user column with
// attno 1 is stored as member 8.
const selectedColsBias = 7

// ColumnInfo describes one user column of a base relation, in the form the
// query resolver needs to build a Var and the eref column-name list. Attno is
// the 1-based pg_attribute.attnum. Collation is 0 for a non-collatable type and
// DEFAULT_COLLATION_OID (100) for a collatable one (text/varchar/…), matching
// what PostgreSQL stamps on a Var's varcollid.
type ColumnInfo struct {
	Name      string
	Attno     int32
	TypeOID   uint32
	Typmod    int32
	Collation uint32
}

// RelationInfo describes the single base relation a supported view selects from.
// Columns is the full user-column list in attribute-number order (it becomes the
// RTE's eref.colnames, which PostgreSQL always populates with every column of
// the relation, not merely the selected ones).
type RelationInfo struct {
	Relid   uint32
	Relname string
	Relkind byte // 'r', 'v', 'm', 'p', …
	Columns []ColumnInfo
}

// RelationResolver resolves an unqualified or schema-qualified base-table name
// (as written in the view's FROM clause) to its metadata. The wiring layer
// (executor/catalog) implements it against the live catalog; the resolver's
// test supplies a stub. schema is "" for an unqualified name.
type RelationResolver interface {
	LookupRelation(schema, name string) (*RelationInfo, bool)
}

// queryScope is the single-relation resolution scope: it maps a column name to
// its metadata and records which attribute numbers were referenced (for the
// selectedCols Bitmapset). effName is the name a qualified column reference must
// match (the relation name, since aliased FROM is not in this sub-slice).
type queryScope struct {
	rel     *RelationInfo
	varno   int32
	effName string
	byName  map[string]ColumnInfo
	used    map[int32]bool
}

// ResolveViewQuery converts a goopg view definition into the canonical
// query-tree IR. The returned *Query serializes (via OutRuleAction) to bytes
// byte-identical to what PostgreSQL stores in pg_rewrite.ev_action for the same
// DDL. Returns ErrUnsupported for any view outside the single-base-relation
// SELECT subset, which is the writer's signal to store SQL text and leave
// relhasrules=false.
func ResolveViewQuery(sel *parser.SelectStmt, r RelationResolver) (*Query, error) {
	if err := rejectUnsupportedClauses(sel); err != nil {
		return nil, err
	}

	// Exactly one plain base relation in FROM (no JOIN, subquery, function,
	// ONLY, or alias). sel.From is the planner's flattened range-var list — a
	// single base relation gives len 1 — and sel.FromExprs preserves explicit
	// JOIN structure (a plain FROM still populates one FromExpr with an empty
	// Joins list), so a JOIN is rejected both by len(From) != 1 and by any
	// non-empty Joins.
	if len(sel.From) != 1 {
		return nil, ErrUnsupported
	}
	for _, fe := range sel.FromExprs {
		if len(fe.Joins) != 0 {
			return nil, ErrUnsupported
		}
	}
	rv := sel.From[0]
	if rv.Subquery != nil || rv.TableFunc != nil || rv.Only ||
		rv.Alias != "" || len(rv.Columns) != 0 {
		return nil, ErrUnsupported
	}
	rel, ok := r.LookupRelation(rv.Schema, rv.Name)
	if !ok {
		return nil, ErrUnsupported
	}

	scope := newQueryScope(rel)

	// Target list: each ResTarget becomes a TargetEntry. A bare `*` or any
	// expression the scalar/scoped resolver cannot handle is unsupported.
	if len(sel.Targets) == 0 {
		return nil, ErrUnsupported
	}
	targets := make([]Node, 0, len(sel.Targets))
	for i, rt := range sel.Targets {
		te, err := scope.resolveTarget(rt, int32(i+1))
		if err != nil {
			return nil, err
		}
		targets = append(targets, te)
	}

	// Optional WHERE qualifier.
	var quals Node
	if sel.Where != nil {
		n, _, err := scope.resolveExpr(sel.Where, 0)
		if err != nil {
			return nil, err
		}
		quals = n
	}

	colnames := make([]string, len(rel.Columns))
	for i, c := range rel.Columns {
		colnames[i] = c.Name
	}

	rte := &RangeTblEntry{
		Alias:         nil, // no AS clause in this subset -> "<>"
		Eref:          &Alias{Aliasname: rel.Relname, Colnames: colnames},
		Rtekind:       0, // RTE_RELATION
		Relid:         rel.Relid,
		Inh:           true,
		Relkind:       rel.Relkind,
		Rellockmode:   1, // AccessShareLock
		Perminfoindex: 1,
		Lateral:       false,
		InFromCl:      true,
	}
	perminfo := &RTEPermissionInfo{
		Relid:         rel.Relid,
		Inh:           true,
		RequiredPerms: 2, // ACL_SELECT
		CheckAsUser:   0,
		SelectedCols:  scope.selectedCols(),
	}
	jointree := &FromExpr{
		Fromlist: []Node{&RangeTblRef{Rtindex: scope.varno}},
		Quals:    quals,
	}
	return &Query{
		CommandType:  1, // CMD_SELECT
		Rtable:       []Node{rte},
		RtePermInfos: []Node{perminfo},
		Jointree:     jointree,
		TargetList:   targets,
	}, nil
}

// rejectUnsupportedClauses returns ErrUnsupported for any SELECT feature outside
// the single-base-relation shape the S3 codec models. Each rejected clause
// (JOIN handled by the caller via FromExprs, GROUP BY, aggregates, sublinks,
// etc.) would require Query fields the fixed skeleton pins at their view-default
// values, so a canonical emit would be wrong — fall back to SQL text instead.
func rejectUnsupportedClauses(sel *parser.SelectStmt) error {
	if sel.With != nil || sel.Distinct || len(sel.DistinctOn) != 0 ||
		len(sel.GroupBy) != 0 || sel.Having != nil || len(sel.OrderBy) != 0 ||
		sel.Limit != nil || sel.Offset != nil || sel.SetOp != nil ||
		len(sel.ValuesRows) != 0 || len(sel.WindowClause) != 0 ||
		sel.GroupingSets != nil || len(sel.Locking) != 0 {
		return ErrUnsupported
	}
	return nil
}

func newQueryScope(rel *RelationInfo) *queryScope {
	byName := make(map[string]ColumnInfo, len(rel.Columns))
	for _, c := range rel.Columns {
		byName[c.Name] = c
	}
	return &queryScope{
		rel:     rel,
		varno:   1,
		effName: rel.Relname,
		byName:  byName,
		used:    make(map[int32]bool),
	}
}

// resolveTarget builds a TargetEntry for one SELECT item at 1-based position
// resno. resname follows PostgreSQL's target-name rule for the supported subset:
// an explicit AS alias, else a bare column reference's own name, else a function
// call's name. resorigtbl/resorigcol carry the source relation OID + attno when
// the target is a plain column (a Var), and are 0 for a computed expression —
// exactly as transformTargetEntry sets them.
func (s *queryScope) resolveTarget(rt parser.ResTarget, resno int32) (Node, error) {
	expr, _, err := s.resolveExpr(rt.Expr, 0)
	if err != nil {
		return nil, err
	}
	resname, err := s.targetName(rt)
	if err != nil {
		return nil, err
	}
	var resorigtbl uint32
	var resorigcol int32
	if v, ok := expr.(*Var); ok {
		resorigtbl = s.rel.Relid
		resorigcol = v.Varattno
	}
	return &TargetEntry{
		Expr:            expr,
		Resno:           resno,
		Resname:         resname,
		Ressortgroupref: 0,
		Resorigtbl:      resorigtbl,
		Resorigcol:      resorigcol,
		Resjunk:         false,
	}, nil
}

// targetName derives a TargetEntry.resname. An explicit alias always wins; a
// bare column reference is named by its column; a function call by the function
// name. Anything else (a bare `*`, an unaliased operator expression, …) has no
// reliable auto-name in this subset and is unsupported.
func (s *queryScope) targetName(rt parser.ResTarget) (string, error) {
	if rt.Alias != "" {
		return rt.Alias, nil
	}
	switch e := rt.Expr.(type) {
	case *parser.ColumnRef:
		return e.Column, nil
	case *parser.FuncCall:
		return e.Name.Name, nil
	default:
		return "", ErrUnsupported
	}
}

// resolveExpr resolves an expression inside the relation scope, returning the IR
// node and its result-type OID. It handles a column reference (-> Var) and
// delegates every other shape to the shared scalar builders so a literal,
// unary-minus fold, binary operator, or function call resolves identically to
// the S2 DEFAULT-expression resolver — except that operands may now be columns.
func (s *queryScope) resolveExpr(e parser.Expr, expected uint32) (Node, uint32, error) {
	switch v := e.(type) {
	case *parser.ColumnRef:
		return s.resolveColumnRef(v)

	case *parser.IntegerConst:
		return resolveIntLiteral(v.Value, expected)

	case *parser.NumericConst:
		return resolveNumericLiteral(v.Value, false)

	case *parser.BooleanConst:
		return NewBoolConst(v.Value), OidBool, nil

	case *parser.StringConst:
		if expected == OidText || expected == 0 {
			return NewTextConst(v.Value), OidText, nil
		}
		return nil, 0, ErrUnsupported

	case *parser.IsNullExpr:
		// x IS [NOT] NULL over a view column/expression -> NULLTEST, with the
		// argument resolved in the relation scope (so it may be a column Var).
		return resolveNullTestWith(v, s.resolveExpr)

	case *parser.UnaryOp:
		switch v.Op {
		case parser.OpUnaryPos:
			return s.resolveExpr(v.Operand, expected)
		case parser.OpUnaryNeg:
			if lit, ok := v.Operand.(*parser.IntegerConst); ok {
				return resolveIntLiteral(-lit.Value, expected)
			}
			if lit, ok := v.Operand.(*parser.NumericConst); ok {
				return resolveNumericLiteral(lit.Value, true)
			}
			return nil, 0, ErrUnsupported
		case parser.OpNot:
			// NOT x -> single-arg NOT BOOLEXPR, argument in the relation scope.
			return resolveBoolNotWith(v.Operand, s.resolveExpr)
		default:
			return nil, 0, ErrUnsupported
		}

	case *parser.BinaryOp:
		// AND/OR fold to a (flattened) BOOLEXPR; every other operator to an
		// OpExpr. Operands resolve in the relation scope so a multi-condition
		// view qual (`a IS NOT NULL AND b > 0`) becomes canonical ev_action.
		switch v.Op {
		case parser.OpAnd:
			return resolveBoolBinaryWith(v, BoolExprAnd, s.resolveExpr)
		case parser.OpOr:
			return resolveBoolBinaryWith(v, BoolExprOr, s.resolveExpr)
		}
		lNode, lType, err := s.resolveExpr(v.Left, 0)
		if err != nil {
			return nil, 0, err
		}
		rNode, rType, err := s.resolveExpr(v.Right, 0)
		if err != nil {
			return nil, 0, err
		}
		return buildOpExpr(lNode, lType, rNode, rType, v.Op.String())

	case *parser.FuncCall:
		if err := funcCallGuard(v); err != nil {
			return nil, 0, err
		}
		argNodes := make([]Node, 0, len(v.Args))
		argOIDs := make([]uint32, 0, len(v.Args))
		for _, a := range v.Args {
			n, t, err := s.resolveExpr(a, 0)
			if err != nil {
				return nil, 0, err
			}
			argNodes = append(argNodes, n)
			argOIDs = append(argOIDs, t)
		}
		return buildFuncExpr(v.Name.Name, argNodes, argOIDs)

	default:
		return nil, 0, ErrUnsupported
	}
}

// resolveColumnRef resolves a column reference to a Var over the single base
// relation (varno 1). A schema qualifier, or a table qualifier that does not
// match the relation name, is unsupported (this subset has no alias and one
// relation). The referenced attribute number is recorded for selectedCols.
func (s *queryScope) resolveColumnRef(c *parser.ColumnRef) (Node, uint32, error) {
	if c.Schema != "" {
		return nil, 0, ErrUnsupported
	}
	if c.Table != "" && c.Table != s.effName {
		return nil, 0, ErrUnsupported
	}
	col, ok := s.byName[c.Column]
	if !ok {
		return nil, 0, ErrUnsupported
	}
	s.used[col.Attno] = true
	return &Var{
		Varno:            s.varno,
		Varattno:         col.Attno,
		Vartype:          col.TypeOID,
		Vartypmod:        col.Typmod,
		Varcollid:        col.Collation,
		Varnullingrels:   nil, // empty -> "(b)"
		Varlevelsup:      0,
		Varreturningtype: 0, // VAR_RETURNING_DEFAULT
		Varnosyn:         s.varno,
		Varattnosyn:      col.Attno,
		Location:         -1,
	}, col.TypeOID, nil
}

// selectedCols returns the referenced attribute numbers as a PostgreSQL
// selectedCols Bitmapset: each attno biased by +selectedColsBias and sorted
// ascending (Bitmapset members are always emitted low-to-high).
func (s *queryScope) selectedCols() Bitmapset {
	if len(s.used) == 0 {
		return nil
	}
	attnos := make([]int32, 0, len(s.used))
	for a := range s.used {
		attnos = append(attnos, a)
	}
	// Insertion sort keeps the file dependency-free; the set is tiny.
	for i := 1; i < len(attnos); i++ {
		for j := i; j > 0 && attnos[j-1] > attnos[j]; j-- {
			attnos[j-1], attnos[j] = attnos[j], attnos[j-1]
		}
	}
	bms := make(Bitmapset, len(attnos))
	for i, a := range attnos {
		bms[i] = a + selectedColsBias
	}
	return bms
}
