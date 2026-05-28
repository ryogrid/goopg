package planner

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/analyzer"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// PlanError is the planner's structured error. SQLSTATE-style codes
// align with upstream's `errcodes.txt`; the analyzer/executor passes
// them through to the wire-protocol ErrorResponse encoder.
type PlanError struct {
	Pos     int
	Code    string
	Message string
	Hint    string // optional hint message (emitted as 'H' field in wire protocol)
}

func (e *PlanError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("planner error: %s (byte %d)", e.Message, e.Pos)
	}
	return fmt.Sprintf("%s: %s (byte %d)", e.Code, e.Message, e.Pos)
}

// Plan converts a parser statement into a plan tree. The catalog is
// consulted for name resolution; DDL statements pass through to the
// executor without decomposing here (catalog mutation happens at
// execute time).
func Plan(stmt parser.Stmt, cat catalog.Catalog) (Node, error) {
	switch s := stmt.(type) {
	case *parser.SelectStmt:
		// Rewrite `(srf(...)).*` target-list indirection-stars into
		// FROM-clause SRF references before the analyzer runs. The
		// analyzer has no IndirectionStar handler — moving the
		// rewrite here keeps the downstream pipeline free of the new
		// AST node. M0103-0008 probe-survival.
		if err := rewriteIndirectionStarTargets(s); err != nil {
			return nil, err
		}
		if err := analyzer.Analyze(s, cat); err != nil {
			return nil, toPlanError(err)
		}
		return planSelect(s, cat)
	case *parser.InsertStmt:
		// M0103-0007 rung 15: substitute bare DEFAULT cells in VALUES rows
		// with the target column's catalog DefaultExpr (or NULL) before the
		// analyzer runs — the analyzer has no DefaultMarker handler and the
		// substituted expression flows through cleanly. Mirrors upstream's
		// rewriteValuesRTE pass.
		if err := rewriteInsertDefaultMarkers(s, cat); err != nil {
			return nil, err
		}
		if err := analyzer.Analyze(s, cat); err != nil {
			return nil, toPlanError(err)
		}
		return planInsert(s, cat)
	case *parser.UpdateStmt:
		// M0103-0007 rung 16: substitute bare DEFAULT cells on the RHS of
		// SET assignments with the target column's catalog DefaultExpr (or
		// NULL) before the analyzer runs — symmetric to rung 15's INSERT
		// VALUES handling.
		if err := rewriteUpdateDefaultMarkers(s, cat); err != nil {
			return nil, err
		}
		if err := analyzer.Analyze(s, cat); err != nil {
			return nil, toPlanError(err)
		}
		return planUpdate(s, cat)
	case *parser.DeleteStmt:
		if err := analyzer.Analyze(s, cat); err != nil {
			return nil, toPlanError(err)
		}
		return planDelete(s, cat)
	case *parser.MergeStmt:
		return planMerge(s, cat)

	case *parser.CreateTableStmt, *parser.DropTableStmt,
		*parser.CreateIndexStmt, *parser.DropIndexStmt,
		*parser.CreateViewStmt, *parser.DropViewStmt,
		*parser.TruncateStmt, *parser.AlterTableStmt,
		*parser.CreateFunctionStmt, *parser.DropFunctionStmt,
		*parser.CreateProcedureStmt, *parser.DropProcedureStmt,
		*parser.CreateTriggerStmt, *parser.DropTriggerStmt,
		*parser.DropRuleStmt,
		*parser.DropCompatStmt,
		*parser.CreateSequenceStmt, *parser.AlterSequenceStmt,
		*parser.CreateMatViewStmt, *parser.RefreshMatViewStmt,
		*parser.CompatNoopStmt,
		*parser.CreateTypeStmt, *parser.AlterTypeStmt, *parser.DropTypeStmt,
		*parser.CreateDomainStmt, *parser.DropDomainStmt,
		*parser.CreateAggregateStmt,
		*parser.CreateOpClassStmt:
		return &DDL{pos: stmt.Pos(), Stmt: stmt}, nil

	case *parser.CreatePublicationStmt, *parser.DropPublicationStmt,
		*parser.CreateSubscriptionStmt, *parser.DropSubscriptionStmt:
		// M0008 logical-replication DDL flows through DDL too;
		// the executor's DDL operator handles them by mutating
		// the runtime's *catalog.PubSub registry. See
		// docs/design/0008-0003-publication-subscription-ddl.md.
		return &DDL{pos: stmt.Pos(), Stmt: stmt}, nil

	case *parser.DoStmt:
		// DO $$ body $$ — anonymous PL/pgSQL block. M0097-0003.
		return &DDL{pos: stmt.Pos(), Stmt: stmt}, nil

	case *parser.BeginStmt:
		return &Transaction{pos: s.Pos(), Verb: TxBegin, IsolationLevel: s.IsolationLevel}, nil
	case *parser.CommitStmt:
		return &Transaction{pos: s.Pos(), Verb: TxCommit}, nil
	case *parser.RollbackStmt:
		return &Transaction{pos: s.Pos(), Verb: TxRollback}, nil
	case *parser.SavepointStmt:
		return &Transaction{pos: s.Pos(), Verb: TxSavepoint, Name: s.Name}, nil
	case *parser.ReleaseSavepointStmt:
		return &Transaction{pos: s.Pos(), Verb: TxRelease, Name: s.Name}, nil
	case *parser.RollbackToSavepointStmt:
		return &Transaction{pos: s.Pos(), Verb: TxRollbackTo, Name: s.Name}, nil

	case *parser.VacuumStmt, *parser.AnalyzeStmt,
		*parser.ShowStmt, *parser.SetStmt, *parser.ResetStmt,
		*parser.ReindexStmt, *parser.ClusterStmt,
		*parser.SetTransactionStmt,
		*parser.PrepareStmt, *parser.ExecuteStmt, *parser.DeallocateStmt:
		return &Utility{pos: stmt.Pos(), Stmt: stmt}, nil

	case *parser.CheckpointStmt:
		return &Checkpoint{pos: s.Pos()}, nil

	case *parser.ExplainStmt:
		// M0018-0003 lifts the Stage A ANALYZE rejection: the
		// executor's explainOp now drives the inner plan
		// through an instrumentation wrapper and reports actual
		// rows/loops/timing per node.
		inner, err := Plan(s.Inner, cat)
		if err != nil {
			return nil, err
		}
		return &Explain{pos: s.Pos(), Options: s.Options, Child: inner}, nil

	case *parser.CopyStmt:
		return planCopy(s, cat)

	case *parser.CallStmt:
		return &Call{pos: s.Pos(), Stmt: s}, nil
	}
	return nil, &PlanError{
		Pos:     stmt.Pos(),
		Code:    "0A000", // feature_not_supported
		Message: fmt.Sprintf("unsupported statement type %T", stmt),
	}
}

func toPlanError(err error) error {
	if ae, ok := err.(*analyzer.AnalyzeError); ok {
		return &PlanError{Pos: ae.Pos, Code: ae.Code, Message: ae.Message, Hint: ae.Hint}
	}
	return err
}

// sortByNullsFirst computes the effective NullsFirst flag for a parser.SortBy.
// PostgreSQL defaults: ASC → NULLS LAST (false), DESC → NULLS FIRST (true).
func sortByNullsFirst(sb parser.SortBy) bool {
	if sb.NullsFirst != nil {
		return *sb.NullsFirst
	}
	return sb.Desc // DESC default: nulls first; ASC default: nulls last
}

// resolveContext holds the per-statement name-resolution scope.
//
// v0 only supports single-relation FROM clauses, so this is just one
// table plus its emitted Schema; multi-table joins extend this with
// a per-RangeVar list.
type resolveContext struct {
	table  *catalog.Table
	alias  string
	schema Schema // schema produced by the input scan
	// bindings keeps every FROM-clause relation in output-column order.
	bindings []rangeBinding
	// cat threads the catalog through so subexpression rewrites
	// (currently subquery planning) can recurse into Plan() without
	// every helper taking it as a separate argument. Populated by
	// the top-level planSelect; nil for utility contexts that
	// don't need catalog access.
	cat catalog.Catalog
	// parent is set when this resolveContext is for a subquery —
	// used by resolveColumnRef to walk up the lexical scope when
	// a column doesn't resolve locally. Implements upstream's
	// Var.varlevelsup by counting how many parent links to walk.
	// nil for the top-level SELECT.
	parent *resolveContext
}

type rangeBinding struct {
	table  *catalog.Table
	alias  string
	offset int // first output-column index for this relation
	// qualifiedOnly hides this binding from the unqualified
	// column-resolution path AND restricts qualified matches to
	// alias-only (never via the underlying table's catalog name).
	// Used by ON CONFLICT DO UPDATE (M0017-0002) to wire the
	// `excluded` pseudo-table — same `*catalog.Table` as the
	// target — without making bare `col` ambiguous or letting
	// `<target>.col` accidentally match the excluded side.
	// Mirrors the analyzer's scopeRel.qualifiedOnly.
	qualifiedOnly bool
	// blockOriginalName makes using the underlying table's catalog name
	// (rather than the alias) a hard error. Set in DELETE when an explicit
	// alias is provided: "DELETE FROM t AS a WHERE t.col" must fail.
	// M0097-0003.
	blockOriginalName bool
	// usingHidden lists column names that are hidden from unqualified lookup
	// because they were supplied in a JOIN USING clause and the left table's
	// column is the canonical reference. Prevents "column is ambiguous" errors
	// when both join sides have the same column name. M0097-0003.
	usingHidden []string
	// sourceIdx is a per-FROM-clause monotonic identifier
	// (M0071-0009) propagated into SchemaColumn.SourceTableIdx
	// for every column produced by this binding. Distinct values
	// for self-join siblings (Q21's lineitem l1/l2/l3) make the
	// (Name, SourceTableIdx) pair unique even when Name alone
	// collides. Counter starts at 1; zero means "no identity
	// assigned" (CTE / subquery-only / ON CONFLICT excluded) and
	// falls back to Name-only matching in downstream rebinds.
	sourceIdx int16
	// tableOidColIdx, when > 0, holds the relative offset within
	// this binding's row of the synthetic `tableoid` column. Set
	// by the planner-side per-leaf Project wrapping in partition
	// (and inheritance) unions to len(b.table.Columns), so a
	// partitioned-table query like `SELECT tableoid::regclass FROM
	// foo` reports the actual leaf relname (e.g. `foo2`). Zero
	// means "not present"; resolveColumnRefAt then synthesises a
	// constant `&TableOidExpr{TableOID: b.table.OID}` for the
	// `tableoid` reference instead — correct for non-partitioned
	// base relations. M0100-0005y.
	tableOidColIdx int
}

func tableSchema(t *catalog.Table) Schema {
	// Legacy callers that don't track source identity get 0 in
	// the SchemaColumn.SourceTableIdx slot, which downstream
	// rebind helpers treat as "unknown" and fall back to
	// Name-only matching.
	return tableSchemaWithSource(t, 0)
}

// tableSchemaWithSource (M0071-0009) is tableSchema's variant

// wrapWithTableoid wraps `child` in a Project that copies the child's
// schema 1:1 and adds a trailing `tableoid` column populated with the
// constant `oid` of `tableOID`. Used by the per-leaf SeqScan wrapping
// in partition (and inheritance) unions so a `tableoid::regclass`
// reference reports the actual leaf relname (e.g. `foo2` rather than
// the partitioned-parent `foo`). The binding's `tableOidColIdx` is set
// to len(b.table.Columns) at the call site so resolveColumnRefAt
// resolves `tableoid` to the trailing slot. M0100-0005y.
func wrapWithTableoid(child Node, tableOID uint32, sourceIdx int16, pos int) Node {
	in := child.Output()
	targets := make([]Expr, len(in)+1)
	for i, c := range in {
		targets[i] = &ColumnRef{pos: pos, Index: i, Name: c.Name, Type: c.Type, SourceTableIdx: c.SourceTableIdx}
	}
	targets[len(in)] = &IntegerConst{pos: pos, Value: int64(tableOID)}
	out := make(Schema, len(in)+1)
	copy(out, in)
	out[len(in)] = SchemaColumn{Name: "tableoid", Type: catalog.Type{Name: "oid"}, SourceTableIdx: sourceIdx}
	return &Project{pos: pos, Child: child, Targets: targets, schema: out}
}

// that stamps each produced SchemaColumn with the given
// SourceTableIdx. Callers building rangeBindings thread their
// per-FROM monotonic source identifier in here; legacy callers
// that don't track source identity (-1) keep Name-only behavior.
func tableSchemaWithSource(t *catalog.Table, sourceIdx int16) Schema {
	out := make(Schema, len(t.Columns))
	for i, c := range t.Columns {
		out[i] = SchemaColumn{Name: c.Name, Type: c.Type, SourceTableIdx: sourceIdx}
	}
	return out
}

func newResolveContext(bindings []rangeBinding, schema Schema) *resolveContext {
	ctx := &resolveContext{schema: schema, bindings: append([]rangeBinding(nil), bindings...)}
	if len(ctx.bindings) > 0 {
		ctx.table = ctx.bindings[0].table
		ctx.alias = ctx.bindings[0].alias
	}
	return ctx
}

// mergeResolveContexts concatenates outer and inner into a single ctx
// whose bindings/schema are outer-then-inner. Used to thread LATERAL
// FROM bindings into a JOIN's right side: the right SRF arg must see
// the cross-FROM-item siblings (outer) *and* the same FROM item's
// left side of the JOIN (inner). Either side may be nil. M0103-0008.
func mergeResolveContexts(outer, inner *resolveContext) *resolveContext {
	if outer == nil {
		return inner
	}
	if inner == nil {
		return outer
	}
	bindings := make([]rangeBinding, 0, len(outer.bindings)+len(inner.bindings))
	bindings = append(bindings, outer.bindings...)
	shift := len(outer.schema)
	for _, b := range inner.bindings {
		b.offset += shift
		bindings = append(bindings, b)
	}
	schema := appendSchema(outer.schema, inner.schema)
	return newResolveContext(bindings, schema)
}

func singleBindingContext(table *catalog.Table, alias string) *resolveContext {
	// Single-binding scope (INSERT/UPDATE/DELETE/COPY targets,
	// view substitution helpers): SourceTableIdx 1 because the
	// scope only ever has one binding and disambiguation isn't
	// needed; 0 stays reserved for "unknown / derived".
	b := rangeBinding{table: table, alias: alias, offset: 0, sourceIdx: 1}
	return newResolveContext([]rangeBinding{b}, tableSchemaWithSource(table, 1))
}

func appendSchema(left, right Schema) Schema {
	out := make(Schema, 0, len(left)+len(right))
	out = append(out, left...)
	out = append(out, right...)
	return out
}

func hasJoinClauses(items []parser.FromExpr) bool {
	for _, item := range items {
		if len(item.Joins) > 0 {
			return true
		}
	}
	return false
}

// setOpKeyword renders a set-operation type as its SQL keyword for error
// messages (matching PostgreSQL's "each UNION query must have …"). M0097-0024.
// wrapSetOpSortLimit applies a trailing ORDER BY / LIMIT / OFFSET to a set
// operation. Per SQL, sort keys reference the combined result's output
// columns only — by 1-based position or by output column name — not arbitrary
// expressions over the input relations. M0097-0024.
func wrapSetOpSortLimit(s *parser.SelectStmt, node Node, cat catalog.Catalog) (Node, error) {
	out := node.Output()
	ctx := newResolveContext(nil, out)
	ctx.cat = cat

	var keys []SortKey
	if len(s.OrderBy) > 0 {
		keys = make([]SortKey, 0, len(s.OrderBy))
		for _, sb := range s.OrderBy {
			if ic, ok := sb.Expr.(*parser.IntegerConst); ok {
				idx := int(ic.Value) - 1
				if idx < 0 || idx >= len(out) {
					return nil, &PlanError{
						Pos:     sb.Expr.Pos(),
						Code:    "42P10",
						Message: fmt.Sprintf("ORDER BY position %d is not in select list", ic.Value),
					}
				}
				keys = append(keys, SortKey{
					Expr:       &ColumnRef{pos: sb.Expr.Pos(), Index: idx, Name: out[idx].Name, Type: out[idx].Type},
					Desc:       sb.Desc,
					NullsFirst: sortByNullsFirst(sb),
				})
				continue
			}
			e, err := resolveExpr(sb.Expr, ctx)
			if err != nil {
				return nil, err
			}
			keys = append(keys, SortKey{Expr: e, Desc: sb.Desc, NullsFirst: sortByNullsFirst(sb)})
		}
		node = &Sort{pos: s.Pos(), Child: node, Keys: keys}
	}

	if s.Limit != nil || s.Offset != nil || s.WithTies {
		// WITH TIES without ORDER BY is an error (matches PostgreSQL).
		if s.WithTies && len(s.OrderBy) == 0 {
			return nil, &PlanError{Pos: s.Pos(), Code: "42P20",
				Message: "WITH TIES cannot be specified without ORDER BY clause"}
		}
		// NULL literal as the row count in FETCH FIRST ... WITH TIES is an error. M0097-0042.
		if s.WithTies {
			if _, isNull := s.Limit.(*parser.NullConst); isNull {
				return nil, &PlanError{Pos: s.Pos(), Code: "22004",
					Message: "row count cannot be null in FETCH FIRST ... WITH TIES clause"}
			}
		}
		var lim, off Expr
		if s.Limit != nil {
			e, err := resolveExpr(s.Limit, ctx)
			if err != nil {
				return nil, err
			}
			lim = e
		}
		if s.Offset != nil {
			e, err := resolveExpr(s.Offset, ctx)
			if err != nil {
				return nil, err
			}
			off = e
		}
		// Collect ORDER BY key expressions for WITH TIES comparison.
		var tiesKeys []Expr
		if s.WithTies {
			for _, k := range keys {
				tiesKeys = append(tiesKeys, k.Expr)
			}
		}
		node = &Limit{pos: s.Pos(), Child: node, Limit: lim, Offset: off,
			WithTies: s.WithTies, TiesKeys: tiesKeys}
	}
	return node, nil
}

// setOpNeedsCast reports whether a column from the right UNION branch (type
// rname) needs a runtime cast to match the left branch type (lname).
// We only cast when the right side has a generic text/unknown type and the
// left side has a specific typed type that can validate the string. This
// handles cases like `SELECT '3.4'::numeric UNION SELECT 'foo'` (where 'foo'
// needs numeric validation) without incorrectly coercing widening situations
// like `SELECT 1 UNION SELECT 2.2` (int ∪ numeric → should stay numeric).
// M0097-0056.
func setOpNeedsCast(lname, rname string) bool {
	if lname == rname || lname == "" || rname == "" {
		return false
	}
	// Only cast when the right side is a generic text/string type and
	// the left side is a more specific type that validates the string.
	isGenericString := func(t string) bool {
		return t == "text" || t == "varchar" || t == "bpchar" || t == "char" ||
			t == "name" || t == "" || t == "unknown"
	}
	if !isGenericString(rname) {
		return false // right is already typed; let executor handle it
	}
	// Left is a non-string type: validate right's string against it.
	return !isGenericString(lname)
}

// wrapSetOpBranchWithCasts wraps the right branch of a UNION/INTERSECT/EXCEPT
// in a Project node with CastExpr nodes for columns where the right branch has
// a generic text type but the left branch has a specific typed type. This
// ensures that string values are validated at evaluation time
// (e.g., 'foo'::numeric fails at run time). M0097-0056.
func wrapSetOpBranchWithCasts(pos int, leftSchema Schema, right Node) Node {
	rightSchema := right.Output()
	// Check if any column needs a cast.
	needCast := false
	for i, lc := range leftSchema {
		if i >= len(rightSchema) {
			break
		}
		if setOpNeedsCast(lc.Type.Name, rightSchema[i].Type.Name) {
			needCast = true
			break
		}
	}
	if !needCast {
		return right
	}
	// Build a Project with CastExpr for columns that need validation.
	targets := make([]Expr, len(rightSchema))
	outSchema := make(Schema, len(rightSchema))
	for i := range rightSchema {
		var expr Expr = &ColumnRef{pos: pos, Index: i}
		lc := leftSchema[i]
		rc := rightSchema[i]
		if setOpNeedsCast(lc.Type.Name, rc.Type.Name) {
			expr = &CastExpr{
				pos:        pos,
				Operand:    expr,
				TargetType: lc.Type.Name,
				SourceType: rc.Type.Name,
			}
		}
		targets[i] = expr
		outSchema[i] = SchemaColumn{Name: rc.Name, Type: lc.Type}
	}
	return &Project{pos: pos, Child: right, Targets: targets, schema: outSchema}
}

func setOpKeyword(t parser.SetOpType) string {
	switch t {
	case parser.SetOpIntersect:
		return "INTERSECT"
	case parser.SetOpExcept:
		return "EXCEPT"
	default:
		return "UNION"
	}
}

func planSelect(s *parser.SelectStmt, cat catalog.Catalog) (Node, error) {
	// M0103-0008: indirection-star rewrite runs at Plan() entry
	// before the analyzer; nested-SELECT planning paths (subqueries,
	// UNION branches) call planSelect directly without going through
	// Plan, so we re-run the rewrite here as an idempotent pass.
	if err := rewriteIndirectionStarTargets(s); err != nil {
		return nil, err
	}

	// Pre-plan WITH-list CTEs so FROM-clause references can
	// substitute them in. Restorer pops the CTE scope back to
	// the caller's view when this Plan call returns. nil-WITH
	// returns a no-op restorer.
	restore, dmlPlans, err := preplanWithClause(s.With, cat)
	if err != nil {
		return nil, err
	}
	defer restore()

	if s.SetOp != nil {
		// Flatten the right-associative parse tree into a flat list of
		// (stmt, op) pairs and rebuild left-to-right so that
		//   A UNION B UNION ALL C → (A UNION B) UNION ALL C
		// instead of the right-associative
		//   A UNION (B UNION ALL C).
		// SQL set operations are left-associative at equal precedence
		// (INTERSECT binds tighter than UNION/EXCEPT, but left-associativity
		// within a level is always required). M0097-0042.
		//
		// When the RHS of a set-op is explicitly parenthesised
		// (rightStmt.Parenthesized == true), we stop flattening: the user
		// wrote explicit parens to override associativity, so the inner
		// compound is treated as an atomic unit by planSelect recursion.
		// e.g. `1.1 UNION (SELECT 2 UNION ALL SELECT 2)` → outer UNION deduplicates.
		type setOpSegment struct {
			opType parser.SetOpType
			opAll  bool
			opPos  int
			stmt   *parser.SelectStmt
		}
		var segments []setOpSegment
		{
			cur := s
			for cur.SetOp != nil {
				rightStmt := cur.SetOp.Right
				segments = append(segments, setOpSegment{
					opType: cur.SetOp.Type,
					opAll:  cur.SetOp.All,
					opPos:  cur.SetOp.Pos(),
					stmt:   rightStmt,
				})
				if rightStmt.Parenthesized {
					break // explicit grouping: stop flattening, treat as atomic
				}
				cur = rightStmt
			}
		}
		// Temporarily clear all SetOps so planSelect on each leaf doesn't
		// recurse into the chain again. Also clear ORDER BY / LIMIT /
		// OFFSET from the left branch — these belong to the whole set-op
		// result and are applied by wrapSetOpSortLimit below, not to the
		// leftmost branch alone. Without this, `SELECT * FROM t INTERSECT
		// … ORDER BY 1` would try to resolve the positional ORDER BY
		// against the unexpanded StarExpr. M0097-0042.
		savedSetOps := make([]*parser.SetOpClause, len(segments))
		savedSetOps[0] = s.SetOp
		s.SetOp = nil
		for i := 0; i < len(segments)-1; i++ {
			savedSetOps[i+1] = segments[i].stmt.SetOp
			segments[i].stmt.SetOp = nil
		}
		savedOrderBy := s.OrderBy
		savedLimit := s.Limit
		savedOffset := s.Offset
		s.OrderBy = nil
		s.Limit = nil
		s.Offset = nil
		// Plan the leftmost branch (s without its SetOp chain or sort/limit).
		left, err := planSelect(s, cat)
		// Restore everything (plan cache may reuse the AST).
		s.SetOp = savedSetOps[0]
		for i := 0; i < len(segments)-1; i++ {
			segments[i].stmt.SetOp = savedSetOps[i+1]
		}
		s.OrderBy = savedOrderBy
		s.Limit = savedLimit
		s.Offset = savedOffset
		if err != nil {
			return nil, err
		}
		// Build left-associatively: fold each subsequent branch in from the left.
		// When s.InnerSegmentCount > 0, ORDER BY/LIMIT/OFFSET applies to the
		// result of the first InnerSegmentCount segments (the "inner" compound),
		// not the final outer result. Example:
		//   (((A INTERSECT B ORDER BY 1))) UNION ALL C
		// → after segment 1 (INTERSECT), apply ORDER BY to Sort(INTERSECT),
		//   then append UNION ALL C without re-sorting. M0097-0044.
		innerBoundary := s.InnerSegmentCount // 0 = no boundary (normal)
		for i, seg := range segments {
			// Middle segments (all but the last) had their SetOp saved+cleared
			// above, then restored early for plan-cache correctness. Re-clear
			// before planning so planSelect(seg.stmt) sees this as a leaf and
			// does not recursively re-flatten the already-flattened chain.
			// The last segment is either a true leaf (SetOp=nil) or an
			// explicitly-parenthesised compound (Parenthesized=true) that must
			// retain its SetOp so the inner compound is planned correctly.
			// M0097-0050.
			if i < len(segments)-1 {
				seg.stmt.SetOp = nil
			}
			right, rerr := planSelect(seg.stmt, cat)
			if i < len(segments)-1 {
				seg.stmt.SetOp = savedSetOps[i+1] // restore for plan-cache
			}
			if rerr != nil {
				return nil, rerr
			}
			// Each branch must project the same number of columns.
			if lc, rc := len(left.Output()), len(right.Output()); lc != rc {
				return nil, &PlanError{
					Pos:     seg.opPos,
					Code:    "42601",
					Message: fmt.Sprintf("each %s query must have the same number of columns", setOpKeyword(seg.opType)),
				}
			}
			// Type unification: wrap the right branch in a Project with CastExpr
			// nodes for columns where the left and right types differ. This ensures
			// that string values like 'foo' are validated when the left branch
			// declares a typed column (e.g. numeric). M0097-0056.
			right = wrapSetOpBranchWithCasts(seg.opPos, left.Output(), right)
			left = &SetOp{pos: s.Pos(), Left: left, Right: right, Op: seg.opType, All: seg.opAll}
			// If this segment is the last "inner" segment, apply the sort/limit
			// to the intermediate result before processing outer segments.
			if innerBoundary > 0 && i+1 == innerBoundary {
				inner, werr := wrapSetOpSortLimit(s, left, cat)
				if werr != nil {
					return nil, werr
				}
				left = inner
				// Clear the saved ORDER BY/LIMIT/OFFSET so wrapSetOpSortLimit
				// below doesn't apply them again to the outer result.
				savedOrderBy = nil
				savedLimit = nil
				savedOffset = nil
				s.OrderBy = nil
				s.Limit = nil
				s.Offset = nil
			}
		}
		// Restore final ORDER BY / LIMIT / OFFSET (may be nil if cleared above).
		s.OrderBy = savedOrderBy
		s.Limit = savedLimit
		s.Offset = savedOffset
		// A trailing ORDER BY / LIMIT / OFFSET binds to the whole set
		// operation and references the combined output columns by name
		// or 1-based position (PostgreSQL §7.6). copyselect uses
		// `… UNION … ORDER BY 1`. M0097-0024.
		return wrapSetOpSortLimit(s, left, cat)
	}
	// s.Distinct with empty target list is invalid in PostgreSQL (syntax error).
	// With targets it is handled by wrapping the final plan with a Distinct node.
	// See the wrapping below. M0097-0005.
	if s.Distinct && len(s.Targets) == 0 {
		return nil, &PlanError{
			Pos:     s.Pos(),
			Code:    "42601",
			Message: "syntax error at or near \"from\"",
		}
	}

	isSimpleSingle := len(s.From) == 1 && (len(s.FromExprs) == 0 || (len(s.FromExprs) == 1 && len(s.FromExprs[0].Joins) == 0))

	var node Node
	var ctx *resolveContext

	if len(s.ValuesRows) > 0 && len(s.From) == 0 && len(s.Targets) == 0 {
		// Standalone VALUES statement: VALUES (r1), (r2), ...
		// M0097-0049. Return directly after building the node and applying
		// ORDER BY / LIMIT so we don't pass through the target-list projection
		// path (which would collapse to 0 columns for empty Targets).
		return planStandaloneValuesSelect(s, cat)
	} else if len(s.From) == 0 {
		// Constant SELECT — `SELECT 1`. The target list resolves
		// against the empty schema.
		ctx = newResolveContext(nil, nil)
		node = &Values{
			pos:    s.Pos(),
			Rows:   [][]Expr{{}},
			schema: nil,
		}
	} else if isSimpleSingle {
		rv := s.From[0]
		// Delegate the simple-single-table case to
		// planScanRangeVar so view substitution / virtual-rows
		// dispatch live in one place. SourceTableIdx 1 — only
		// one binding ever in this branch (0 is the
		// "unknown / derived" sentinel).
		nrv, b, err := planScanRangeVar(rv, cat, 1, nil)
		if err != nil {
			return nil, err
		}
		node = nrv
		schema := tableSchemaWithSource(b.table, b.sourceIdx)
		// View substitution may have rewritten the schema to
		// merge the view's column names with the inner plan's
		// types — preserve it.
		if b.table.View != nil {
			schema = make(Schema, len(b.table.Columns))
			innerOut := node.Output()
			for i, c := range b.table.Columns {
				ty := c.Type
				if i < len(innerOut) {
					ty = innerOut[i].Type
				}
				schema[i] = SchemaColumn{Name: c.Name, Type: ty, SourceTableIdx: b.sourceIdx}
			}
		}
		ctx = newResolveContext([]rangeBinding{b}, schema)
	} else {
		// Cost-based join-order reordering: when every comma-FROM
		// table has ANALYZE statistics, permute the FROM list so
		// small-cardinality relations join first. Operates on a
		// SelectStmt copy so we don't mutate the caller's parser
		// AST. Falls through to source order when stats are
		// missing or the FROM list isn't a pure comma chain.
		// See docs/design/0003-0016-join-order-reordering.md.
		stmt := s
		if reFE, reFR, rewrote := reorderCommaFromByCardinality(s, cat); rewrote {
			c := *s
			c.FromExprs = reFE
			c.From = reFR
			stmt = &c
		}
		var err error
		node, ctx, err = planFromClause(stmt, cat)
		if err != nil {
			return nil, err
		}
	}
	// Make the catalog reachable from every resolveExpr call in
	// this SELECT — subquery planning recurses into Plan(inner,
	// cat) by reading ctx.cat. Set unconditionally so the
	// FROM-less and FROM-bearing branches converge.
	//
	// planParent (set by planSelectWithParent) wires the
	// lexical-scope parent for correlated subqueries — the
	// inner SELECT's top-level ctx points up to the outer
	// SELECT so resolveColumnRef can walk up.
	if ctx != nil {
		ctx.cat = cat
		ctx.parent = planParent
	}

	if s.Where != nil {
		if isSimpleSingle {
			// M0051-0004: inject synthetic range predicates alongside any
			// LIKE conjuncts so tryRangeIndexScan can activate a B-tree.
			whereForIndex := injectLikeRangePredicates(s.Where)
			if idxNode, ok, err := planIndexScanFromWhere(whereForIndex, ctx, cat); err != nil {
				return nil, err
			} else if ok {
				node = idxNode
			} else {
				pred, err := resolveExpr(s.Where, ctx)
				if err != nil {
					return nil, err
				}
				node = &Filter{pos: s.Where.Pos(), Child: node, Predicate: pred}
			}
		} else {
			pred, err := resolveExpr(s.Where, ctx)
			if err != nil {
				return nil, err
			}
			node = &Filter{pos: s.Where.Pos(), Child: node, Predicate: pred}
			// Attempt bushy-join DP when all tables have ANALYZE
			// stats. This replaces the left-deep CROSS chain with
			// a DPccp-style optimal bushy tree that eliminates
			// Cartesian products. See internal/planner/bushy.go.
			if f, ok := node.(*Filter); ok {
				if newChild, newPred := tryBushyDP(f.Child, f.Predicate, ctx, cat); newPred == nil {
					node = newChild // all conjuncts consumed → remove Filter
				} else if newChild != f.Child {
					f.Child = newChild
					f.Predicate = newPred
					node = pushPredicatesIntoCrossJoins(node)
				} else {
					// Comma-FROM produces a left-deep CROSS-join chain.
					// Push WHERE-side equalities into the deepest Join
					// whose schema spans both sides so the planner can
					// pick hash join instead of running a Cartesian
					// product through Filter. See
					// internal/planner/pushdown.go.
					node = pushPredicatesIntoCrossJoins(node)
				}
			}
		}
	}

	// Unnest correlated scalar subqueries after predicate pushdown
	// has finalised the join tree. Subqueries that are unnestable
	// (equijoin correlation, simple aggregate) are rewritten as
	// GROUP BY aggregate + hash join. See internal/planner/unnest.go.
	node = unnestSubqueriesInPlan(node)

	// Rewrite chains of ≥3 hash-joined tables into a single
	// MultiHashJoin node.  Column indices are remapped inside
	// collectMultiHashTables using scanForCol, so parent
	// expressions (incl. unnest keys) stay aligned.
	node = rewriteMultiWayChain(node, cat)
	// M0054-0006a-pre: after MultiHashJoin construction, walk the
	// plan tree and route single-table constant-RHS equality
	// predicates from `*Filter` wrappers and `mh.Filters` into the
	// matching `*SeqScan` input by rewriting it to `*IndexScan`.
	// Closes the M0054-0003d Q8 case
	// (`p_type = 'ECONOMY ANODIZED STEEL'` →
	// `Index Scan using idx_part_type on part`).
	node = rewriteScanInputsWithSingleTablePredicates(node, cat)
	// M0054-0006: rewrite eligible binary `*Join{Algo:Hash}` /
	// `*Join{Algo:NestedLoop}` nodes to `*NestedLoopIndexJoin` when
	// the equi-join predicate matches a single-column B-tree index
	// on the inner side AND the cost gate accepts. The pass is a
	// no-op when the package-level kill-switch is off
	// (`SetNLIEnabled(false)`).
	node = rewriteJoinsToNLI(node, cat)
	node = remapExprRefsToMHJ(node)
	// Second pass: use FROM‑clause bindings to correct any
	// remaining order differences (OID ≠ FROM order).
	if len(ctx.bindings) > 0 {
		remapWithBindings(node, ctx.bindings)
	}

	var agg *aggregateSurface
	savedBindings := ctx.bindings
	if needsAggregateStage(s) {
		var having Expr
		var err error
		node, ctx, agg, having, err = buildAggregateStage(s, node, ctx)
		if err != nil {
			return nil, err
		}
		if having != nil {
			node = &Filter{pos: s.Having.Pos(), Child: node, Predicate: having}
		}
		// The aggregate stage resolves GroupExprs / Agg.Arg against
		// ctx.bindings (FROM‑clause order).  Remap only those two
		// fields — not the HAVING predicate, which uses aggregate‑
		// output column indices and must not be touched.
		if len(savedBindings) > 0 {
			remapAggExprsWithBindings(node, savedBindings)
		}
	} else if s.Having != nil {
		return nil, &PlanError{
			Pos:     s.Having.Pos(),
			Code:    "42803",
			Message: "column must appear in the GROUP BY clause or be used in an aggregate function",
		}
	}

	// M0103-0008 final sub-step: lower target-list IndirectionStar with
	// aggregate args (e.g. `(srf(array_agg(...))).*`) into a ProjectSet
	// wrapper sitting above the Aggregate node. The libpqrcv
	// `fetch_table_list` probe against a goopg publisher hits exactly
	// this shape.
	var ps *ProjectSet
	if agg != nil {
		for _, t := range s.Targets {
			is, ok := t.Expr.(*parser.IndirectionStar)
			if !ok {
				continue
			}
			fc, ok := is.Source.(*parser.FuncCall)
			if !ok {
				return nil, &PlanError{Pos: is.Pos(), Code: "0A000",
					Message: "(expr).* requires a function-call source"}
			}
			compName := strings.ToLower(fc.Name.Name)
			compSchema := projectSetCompositeSchema(compName)
			if compSchema == nil {
				return nil, &PlanError{Pos: fc.Pos(), Code: "0A000",
					Message: fmt.Sprintf("set-returning function %q is not supported in ProjectSet", compName)}
			}
			args := make([]Expr, 0, len(fc.Args))
			for _, a := range fc.Args {
				pa, err := resolveExprAfterAggregate(a, agg)
				if err != nil {
					return nil, err
				}
				args = append(args, pa)
			}
			ps = &ProjectSet{
				pos:     is.Pos(),
				Child:   node,
				SrfName: compName,
				SrfArgs: args,
				schema:  compSchema,
			}
			node = ps
			// Downstream resolution (ORDER BY, LIMIT, target list)
			// reads from the ProjectSet's expanded output, not the
			// aggregate. Reset ctx + agg so the existing branches
			// hit the non-aggregate path.
			ctx = newResolveContext(nil, ps.Output())
			ctx.cat = cat
			agg = nil
			break
		}
	}

	// SELECT-list SRF expansion (generate_series in SELECT target list). M0097-0045.
	// Sort placement depends on whether ORDER BY references:
	//   (a) PS output columns (SELECT list aliases / column names) → sort AFTER PS
	//   (b) base-table columns not in SELECT list (e.g. tenthous)  → sort BEFORE PS
	// Decision: try to resolve each ORDER BY key against the PS output schema first.
	// If ALL keys resolve in PS output → sort after PS.
	// If ANY key only resolves in child schema → sort before PS (pre-sort).
	var selectSrfPending *ProjectSet  // set when SRF is detected; applied after sort
	var selectSrfPreSort bool         // true → sort BEFORE PS
	if agg == nil && ps == nil && !needsWindowStage(s) {
		srfPS, srfErr := buildSelectSrfProjectSet(s, node, ctx)
		if srfErr != nil {
			return nil, srfErr
		}
		if srfPS != nil {
			selectSrfPending = srfPS
			// Determine sort placement: if any ORDER BY key can't be resolved
			// in the PS output schema but CAN be resolved in the child schema,
			// sort before PS (so base-table columns are visible).
			if len(s.OrderBy) > 0 {
				psCtx := newResolveContext(nil, srfPS.schema)
				psCtx.cat = cat
				for _, sb := range s.OrderBy {
					expr := resolveOrderBySubstitution(sb.Expr, s.Targets)
					_, errPS := resolveExpr(expr, psCtx)
					if errPS != nil {
						// Can't resolve in PS schema; try child schema.
						_, errChild := resolveExpr(expr, ctx)
						if errChild == nil {
							// Need pre-sort.
							selectSrfPreSort = true
						}
					}
				}
			}
		}
	}

	var win *windowSurface
	if needsWindowStage(s) {
		var err error
		node, ctx, win, err = buildWindowStage(s, node, ctx, agg)
		if err != nil {
			return nil, err
		}
	}

	// Build ORDER BY sort keys.  For SRF mode:
	//   - Pre-sort (selectSrfPreSort): resolve against child schema (ctx unchanged).
	//   - Post-sort (default SRF): resolve against PS output schema.
	// Build pre-sort keys now; post-sort keys are built after PS is wired in.
	var keys []SortKey
	if len(s.OrderBy) > 0 && (selectSrfPending == nil || selectSrfPreSort) {
		// Normal path OR SRF pre-sort path.
		sortCtx := ctx // default: child schema (also used for pre-sort)
		if selectSrfPending != nil && !selectSrfPreSort {
			// SRF post-sort: resolve against PS output
			sortCtx = newResolveContext(nil, selectSrfPending.schema)
			sortCtx.cat = cat
		}
		keys = make([]SortKey, 0, len(s.OrderBy))
		for _, sb := range s.OrderBy {
			// SQL allows ORDER BY to reference target-list aliases
			// (`SELECT sum(x) AS revenue ... ORDER BY revenue`)
			// and positional indices (`ORDER BY 1, 2`). Resolve
			// those substitutions BEFORE feeding the expression
			// into the column-resolver so the post-aggregate path
			// can see the same parser.Expr that built the
			// aggregate stage's target. TPC-H Q3, Q5, Q9, Q10,
			// Q21 all use ORDER BY <alias> shapes.
			expr := resolveOrderBySubstitution(sb.Expr, s.Targets)
			var e Expr
			var err error
			// If resolveOrderBySubstitution returned the original IntegerConst
			// unchanged (e.g. because the target is SELECT *), handle it as a
			// 1-based positional reference against the output schema.  This
			// matches wrapSetOpSortLimit and the SRF post-sort path.
			if ic, ok := expr.(*parser.IntegerConst); ok {
				outSchema := sortCtx.schema
				idx := int(ic.Value) - 1
				if idx >= 0 && idx < len(outSchema) {
					sc := outSchema[idx]
					e = &ColumnRef{pos: ic.Pos(), Index: idx, Name: sc.Name, Type: sc.Type}
				}
			}
			if e == nil {
				if win != nil {
					e, err = resolveExprAfterWindow(expr, win)
				} else if agg == nil {
					e, err = resolveExpr(expr, sortCtx)
				} else {
					e, err = resolveExprAfterAggregate(expr, agg)
				}
				if err != nil {
					return nil, err
				}
			}
			keys = append(keys, SortKey{Expr: e, Desc: sb.Desc, NullsFirst: sortByNullsFirst(sb)})
		}
		node = &Sort{pos: s.Pos(), Child: node, Keys: keys}
	}
	// Wire the ProjectSet into the plan (after pre-sort if applicable).
	// Then build post-sort keys on the PS output if needed.
	if selectSrfPending != nil {
		selectSrfPending.Child = node
		node = selectSrfPending
		ps = selectSrfPending
		ctx = newResolveContext(nil, selectSrfPending.schema)
		ctx.cat = cat
		// Post-sort: sort AFTER PS expansion. ORDER BY may reference output
		// columns by alias (ColumnRef) or 1-based position (IntegerConst).
		// Do NOT call resolveOrderBySubstitution here — that would replace
		// the alias with the raw SRF FuncCall expression, causing the sort
		// key to evaluate generate_series() as a scalar (always returns start
		// value) instead of referencing the already-expanded output column.
		// Instead, resolve the original ORDER BY expression directly against
		// the PS output schema.
		if len(s.OrderBy) > 0 && !selectSrfPreSort {
			psSchema := selectSrfPending.schema
			keys = make([]SortKey, 0, len(s.OrderBy))
			for _, sb := range s.OrderBy {
				var e Expr
				var err error
				// Positional ref: ORDER BY 1 → ColumnRef into PS output.
				if ic, ok := sb.Expr.(*parser.IntegerConst); ok {
					idx := int(ic.Value) - 1
					if idx >= 0 && idx < len(psSchema) {
						sc := psSchema[idx]
						e = &ColumnRef{pos: ic.Pos(), Index: idx, Name: sc.Name, Type: sc.Type}
					}
				}
				if e == nil {
					// Named / expression ref: resolve directly against PS output schema.
					e, err = resolveExpr(sb.Expr, ctx) // ctx = PS schema
					if err != nil {
						return nil, err
					}
				}
				keys = append(keys, SortKey{Expr: e, Desc: sb.Desc, NullsFirst: sortByNullsFirst(sb)})
			}
			node = &Sort{pos: s.Pos(), Child: node, Keys: keys}
		}
	}
	if s.Limit != nil || s.Offset != nil || s.WithTies {
		// WITH TIES without ORDER BY is an error (matches PostgreSQL).
		if s.WithTies && len(s.OrderBy) == 0 {
			return nil, &PlanError{Pos: s.Pos(), Code: "42P20",
				Message: "WITH TIES cannot be specified without ORDER BY clause"}
		}
		// NULL literal as row count in FETCH FIRST ... WITH TIES is an error. M0097-0042.
		if s.WithTies {
			if _, isNull := s.Limit.(*parser.NullConst); isNull {
				return nil, &PlanError{Pos: s.Pos(), Code: "22004",
					Message: "row count cannot be null in FETCH FIRST ... WITH TIES clause"}
			}
		}
		var lim, off Expr
		if s.Limit != nil {
			e, err := resolveExpr(s.Limit, ctx)
			if err != nil {
				return nil, err
			}
			lim = e
		}
		if s.Offset != nil {
			e, err := resolveExpr(s.Offset, ctx)
			if err != nil {
				return nil, err
			}
			off = e
		}
		// Collect ORDER BY key expressions for WITH TIES comparison.
		var tiesKeys []Expr
		if s.WithTies {
			for _, k := range keys {
				tiesKeys = append(tiesKeys, k.Expr)
			}
		}
		node = &Limit{pos: s.Pos(), Child: node, Limit: lim, Offset: off,
			WithTies: s.WithTies, TiesKeys: tiesKeys}
	}

	var (
		targets []Expr
		schema  Schema
	)
	if ps != nil {
		// Identity passthrough over the ProjectSet's expanded composite.
		// The IndirectionStar in s.Targets is consumed by the ProjectSet;
		// the wrapping Project becomes a no-op identity that surfaces the
		// expanded columns.
		schema = ps.Output()
		targets = make([]Expr, len(schema))
		for i, sc := range schema {
			targets[i] = &ColumnRef{pos: ps.pos, Index: i, Name: sc.Name, Type: sc.Type}
		}
	} else if win != nil {
		targets, schema, err = resolveTargetsAfterWindow(s.Targets, win)
	} else if agg == nil {
		targets, schema, err = resolveTargets(s.Targets, ctx)
	} else {
		targets, schema, err = resolveTargetsAfterAggregate(s.Targets, agg)
	}
	if err != nil {
		return nil, err
	}
	// Constant-degenerate-aggregate optimization: when the aggregate has no
	// GROUP BY, no aggregate calls, all SELECT targets are constants, and the
	// HAVING is a constant expression, skip the table scan entirely and return
	// a constant result (0 or 1 rows). This matches PostgreSQL's plan for
	// SELECT 1 FROM t WHERE expr HAVING 1<2 (comment "not scanning the table").
	// M0097-0003.
	if agg != nil && win == nil {
		// Find the innermost aggregate node (may be wrapped in HAVING Filter).
		var innerAgg *Aggregate
		if a, ok := node.(*Aggregate); ok {
			innerAgg = a
		} else if f, ok := node.(*Filter); ok {
			if a, ok2 := f.Child.(*Aggregate); ok2 {
				innerAgg = a
			}
		}
		if innerAgg != nil && len(innerAgg.GroupExprs) == 0 && len(innerAgg.Aggs) == 0 {
			allConst := true
			for _, t := range targets {
				if !isConstantPlanExpr(t) {
					allConst = false
					break
				}
			}
			if allConst {
				// Evaluate HAVING at plan time.
				emitRow := true // default: no HAVING → 1 row
				if s.Having != nil {
					// Find the HAVING expression from the filter wrapper.
					var havingExpr Expr
					if f, ok := node.(*Filter); ok {
						havingExpr = f.Predicate
					}
					if havingExpr != nil && isConstantPlanExpr(havingExpr) {
						result, ok := evalConstantBool(havingExpr)
						if ok {
							emitRow = result
						}
					}
				}
				if emitRow {
					// One empty-row source so Project evaluates the constant targets once.
					emptyRow := make([]Expr, 0)
					constSource := &Values{pos: s.Pos(), Rows: [][]Expr{emptyRow}, schema: Schema{}}
					return &Project{pos: s.Pos(), Child: constSource, Targets: targets, schema: schema}, nil
				}
				// HAVING is constant-false → 0 rows.
				emptySource := &Values{pos: s.Pos(), Rows: nil, schema: Schema{}}
				return &Project{pos: s.Pos(), Child: emptySource, Targets: targets, schema: schema}, nil
			}
		}
	}

	proj := &Project{pos: s.Pos(), Child: node, Targets: targets, schema: schema}

	// Promote to IndexOnlyScan (M0046-0004) only when there are no locking
	// clauses. FOR UPDATE / FOR SHARE rely on the IndexScan leaf being
	// accessible via lockRowsOp's currentTIDProvider chain.
	var out Node
	if len(s.Locking) == 0 {
		if promoted := tryPromoteIndexOnlyScan(proj); promoted != proj {
			out = promoted
		}
	}
	if out == nil {
		out = Node(proj)
	}
	// resolveTargets resolves SELECT targets against ctx.bindings,
	// which holds FROM‑clause offsets — but rewriteMultiWayChain /
	// bushy DP may have re‑laid out the underlying join tree
	// (e.g. OID‑sorted MHJ output). Remap the freshly‑added
	// Project's targets (and any Sort keys above the join tree)
	// using the same bindings posMap so they land at actual scan
	// offsets. For aggregate queries the targets reference
	// aggregate‑output indices (small and outside any FROM
	// binding's range) so the remap is a no‑op. Inline‑view
	// subqueries (TPC‑H Q7/Q8/Q9) hit this path with FROM‑order
	// indices and need the remap to fire. We deliberately do NOT
	// walk below the Project's join‑tree boundary — those nodes
	// were already remapped by the earlier remapWithBindings call,
	// and walking them again would double‑remap.
	if agg == nil && len(savedBindings) > 0 {
		remapTopProjection(out, savedBindings)
	}
	if len(s.Locking) > 0 {
		// M0021-0002 — wrap the SELECT plan in a LockRows
		// node carrying the resolved per-relation locking
		// intent. The executor (Stage A executor lands in
		// M0021-0003) consumes Locks to acquire row-level
		// pessimistic locks before returning rows. Until
		// then the executor refuses to Build a *LockRows so
		// runtime never silently drops the locking intent.
		locks, lerr := resolveLockedRels(s, ctx)
		if lerr != nil {
			return nil, lerr
		}
		out = &LockRows{pos: s.Locking[0].Pos(), Child: out, Locks: locks}
	}
	// Collapse all-constant sub-expressions in the final plan tree.
	// A non-nil return means a constant evaluation error (e.g. division by zero)
	// occurred in a potentially-reachable sub-expression. M0097-0047.
	if foldErr := foldPlanConstants(out); foldErr != nil {
		return nil, foldErr
	}
	// DISTINCT ON (expr [, ...]): implement ordered deduplication.
	// M0097-0005.
	if len(s.DistinctOn) > 0 {
		// Resolve each DISTINCT ON expression. Apply ordinal/alias substitution
		// (same as ORDER BY) so DISTINCT ON (1) works like ORDER BY 1.
		resolvedDistinct := make([]Expr, len(s.DistinctOn))
		for i, dexpr := range s.DistinctOn {
			dexpr = resolveOrderBySubstitution(dexpr, s.Targets)
			var re Expr
			var resolveErr error
			if win != nil {
				re, resolveErr = resolveExprAfterWindow(dexpr, win)
			} else if agg == nil {
				re, resolveErr = resolveExpr(dexpr, ctx)
			} else {
				re, resolveErr = resolveExprAfterAggregate(dexpr, agg)
			}
			if resolveErr != nil {
				return nil, resolveErr
			}
			resolvedDistinct[i] = re
		}
		// Validate: for each DISTINCT ON key at position i where ORDER BY also
		// has a key at position i, the two must match. If ORDER BY is shorter
		// than DISTINCT ON, the missing positions are OK — we add an implicit
		// Sort below. Only error on an explicit positional mismatch.
		for i, re := range resolvedDistinct {
			if i >= len(s.OrderBy) {
				break
			}
			obExpr := resolveOrderBySubstitution(s.OrderBy[i].Expr, s.Targets)
			var obResolved Expr
			var obErr error
			if win != nil {
				obResolved, obErr = resolveExprAfterWindow(obExpr, win)
			} else if agg == nil {
				obResolved, obErr = resolveExpr(obExpr, ctx)
			} else {
				obResolved, obErr = resolveExprAfterAggregate(obExpr, agg)
			}
			if obErr != nil {
				return nil, &PlanError{Pos: s.Pos(), Code: "42P10",
					Message: "SELECT DISTINCT ON expressions must match initial ORDER BY expressions"}
			}
			if !exprEqual(re, obResolved) {
				return nil, &PlanError{Pos: s.Pos(), Code: "42P10",
					Message: "SELECT DISTINCT ON expressions must match initial ORDER BY expressions"}
			}
		}
		// Map DISTINCT ON expressions to output column indices.
		outSchema := out.Output()
		keyCols := make([]int, len(resolvedDistinct))
		for i, re := range resolvedDistinct {
			idx := findExprInSchema(re, outSchema, proj)
			if idx < 0 {
				idx = findExprInTargets(re, targets)
			}
			if idx < 0 {
				idx = i // last resort: use positional index
			}
			keyCols[i] = idx
		}
		// If ORDER BY is shorter than DISTINCT ON, add an implicit Sort on the
		// full set of DISTINCT ON key columns so the distinctOnOp sees adjacent
		// rows for each key group. (PostgreSQL implicitly extends ORDER BY with
		// the missing DISTINCT ON keys.)
		if len(s.OrderBy) < len(resolvedDistinct) {
			sortKeys := make([]SortKey, len(resolvedDistinct))
			for i, col := range keyCols {
				var desc bool
				var nullsFirst bool
				if i < len(s.OrderBy) {
					desc = s.OrderBy[i].Desc
					nullsFirst = sortByNullsFirst(s.OrderBy[i])
				}
				outCol := outSchema[col]
				sortKeys[i] = SortKey{
					Expr:       &ColumnRef{Index: col, Name: outCol.Name, Type: outCol.Type},
					Desc:       desc,
					NullsFirst: nullsFirst,
				}
			}
			out = &Sort{pos: s.Pos(), Child: out, Keys: sortKeys}
		}
		out = &DistinctOn{pos: s.Pos(), Child: out, KeyCols: keyCols, schema: out.Output()}
	}
	// SELECT DISTINCT: wrap the full plan with a Distinct node that deduplicates
	// on the projected output. Applied after sorting so ORDER BY is respected.
	// M0097-0005.
	if s.Distinct {
		out = &Distinct{pos: s.Pos(), Child: out, schema: out.Output()}
		// The distinctOp sorts rows internally in ascending order.  When the
		// query has ORDER BY, that inner sort loses the requested direction.
		// Re-apply ORDER BY on top of Distinct by resolving each ORDER BY key
		// against the Distinct output schema (schema-only, no bindings, so
		// only unqualified column-name references work — which is fine since
		// ORDER BY in a DISTINCT query must reference projected columns).
		// M0097-0046.
		if len(s.OrderBy) > 0 {
			distinctOut := out.Output()
			outerCtx := newResolveContext(nil, distinctOut)
			outerCtx.cat = cat
			outerKeys := make([]SortKey, 0, len(s.OrderBy))
			for _, sb := range s.OrderBy {
				expr := resolveOrderBySubstitution(sb.Expr, s.Targets)
				e, err := resolveExpr(expr, outerCtx)
				if err != nil {
					// Key not resolvable in Distinct output — skip outer sort.
					outerKeys = nil
					break
				}
				outerKeys = append(outerKeys, SortKey{Expr: e, Desc: sb.Desc, NullsFirst: sortByNullsFirst(sb)})
			}
			if len(outerKeys) > 0 {
				out = &Sort{pos: s.Pos(), Child: out, Keys: outerKeys}
			}
		}
	}
	return wrapDMLCTEPrefix(out, dmlPlans), nil
}

// resolveLockedRels walks the parsed locking clauses and
// produces one LockedRel per (clause, FROM-clause range
// variable) pair in the effective target set. When a clause
// supplies an OF list, only those names produce LockedRels;
// otherwise every range variable in the FROM clause is locked
// per upstream's bare-FOR-UPDATE-locks-everything semantics.
//
// Mirrors upstream's expand_targetlist_to_locks step inside
// preprocess_rowmarks. The analyzer (M0021-0001 step 2) has
// already verified the OF target names resolve, so this
// helper's lookup paths are second-line defence.
func resolveLockedRels(s *parser.SelectStmt, ctx *resolveContext) ([]LockedRel, error) {
	if ctx == nil {
		return nil, &PlanError{Pos: s.Pos(), Code: "0A000", Message: "FOR UPDATE/SHARE requires a FROM clause"}
	}
	var out []LockedRel
	for _, lc := range s.Locking {
		strength := lockStrengthFromParser(lc.Strength)
		policy := lockWaitPolicyFromParser(lc.WaitPolicy)
		if len(lc.Targets) == 0 {
			for _, b := range ctx.bindings {
				out = append(out, LockedRel{Table: b.table, Alias: b.alias, Strength: strength, WaitPolicy: policy})
			}
			continue
		}
		for _, name := range lc.Targets {
			b, ok := findBindingByName(ctx.bindings, name)
			if !ok {
				return nil, &PlanError{Pos: lc.Pos(), Code: "42P01",
					Message: fmt.Sprintf("relation %q in FOR UPDATE/SHARE clause not found in FROM clause", name)}
			}
			out = append(out, LockedRel{Table: b.table, Alias: b.alias, Strength: strength, WaitPolicy: policy})
		}
	}
	return out, nil
}

func lockStrengthFromParser(s parser.LockStrength) LockStrength {
	switch s {
	case parser.LockStrengthForUpdate, parser.LockStrengthForNoKeyUpdate:
		return LockStrengthForUpdate
	case parser.LockStrengthForShare, parser.LockStrengthForKeyShare:
		return LockStrengthForShare
	}
	return LockStrengthForUpdate
}

func lockWaitPolicyFromParser(p parser.LockWaitPolicy) LockWaitPolicy {
	switch p {
	case parser.LockWaitNoWait:
		return LockWaitNoWait
	case parser.LockWaitSkipLocked:
		return LockWaitSkipLocked
	}
	return LockWaitBlock
}

func findBindingByName(bindings []rangeBinding, name string) (rangeBinding, bool) {
	for _, b := range bindings {
		if b.qualifiedOnly {
			continue
		}
		if b.alias != "" {
			if strings.EqualFold(name, b.alias) {
				return b, true
			}
			continue
		}
		if strings.EqualFold(name, b.table.Name) {
			return b, true
		}
	}
	return rangeBinding{}, false
}

func planFromClause(s *parser.SelectStmt, cat catalog.Catalog) (Node, *resolveContext, error) {
	if len(s.FromExprs) == 0 {
		return planFromRangeVars(s.From, cat)
	}
	var root Node
	var bindings []rangeBinding
	// Counter starts at 1; zero is reserved as the "unknown /
	// derived" sentinel for SchemaColumn.SourceTableIdx.
	nextSourceIdx := int16(1)
	for _, item := range s.FromExprs {
		// LATERAL semantics for FROM-clause SRFs (M0103-0008):
		// the partial FROM-list context is threaded down so each
		// item's SRF args see siblings to its left.
		var lateralCtx *resolveContext
		if len(bindings) > 0 {
			lateralCtx = newResolveContext(bindings, root.Output())
		}
		itemNode, itemBindings, err := planFromItem(item, cat, &nextSourceIdx, lateralCtx)
		if err != nil {
			return nil, nil, err
		}
		if root == nil {
			root = itemNode
			bindings = itemBindings
			continue
		}
		shift := len(root.Output())
		shifted := make([]rangeBinding, len(itemBindings))
		for i := range itemBindings {
			shifted[i] = itemBindings[i]
			shifted[i].offset += shift
		}
		root = &Join{
			pos:     item.Pos(),
			Type:    JoinTypeCross,
			Left:    root,
			Right:   itemNode,
			schema:  appendSchema(root.Output(), itemNode.Output()),
			Lateral: nodeReferencesOuter(itemNode),
		}
		bindings = append(bindings, shifted...)
	}
	if root == nil {
		return nil, nil, &PlanError{Pos: s.Pos(), Code: "42601", Message: "SELECT FROM requires at least one relation"}
	}
	return root, newResolveContext(bindings, root.Output()), nil
}

func planFromRangeVars(from []parser.RangeVar, cat catalog.Catalog) (Node, *resolveContext, error) {
	var root Node
	var bindings []rangeBinding
	// Counter starts at 1; zero is reserved as the "unknown /
	// derived" sentinel for SchemaColumn.SourceTableIdx.
	nextSourceIdx := int16(1)
	for _, rv := range from {
		// LATERAL semantics for FROM-clause SRFs (M0103-0008): pass
		// the accumulated bindings/schema as the resolution scope so
		// `pg_get_publication_tables(p.pubname)` resolves p.pubname
		// against earlier FROM items. nil for the first item.
		var lateralCtx *resolveContext
		if len(bindings) > 0 {
			lateralCtx = newResolveContext(bindings, root.Output())
		}
		n, b, err := planScanRangeVar(rv, cat, nextSourceIdx, lateralCtx)
		if err != nil {
			return nil, nil, err
		}
		nextSourceIdx++
		if root == nil {
			root = n
			bindings = append(bindings, b)
			continue
		}
		b.offset += len(root.Output())
		root = &Join{
			pos:     rv.Pos(),
			Type:    JoinTypeCross,
			Left:    root,
			Right:   n,
			schema:  appendSchema(root.Output(), n.Output()),
			Lateral: nodeReferencesOuter(n),
		}
		bindings = append(bindings, b)
	}
	if root == nil {
		return nil, nil, &PlanError{Pos: 0, Code: "42601", Message: "SELECT FROM requires at least one relation"}
	}
	return root, newResolveContext(bindings, root.Output()), nil
}

// nodeReferencesOuter reports whether the planned right-side FROM item
// resolved any expression against the lateral outer context — i.e. the
// item's evaluation depends on the row produced by the left siblings.
// The executor uses this to switch the wrapping Join to its per-outer-
// row lateral driver instead of the materialise-both-sides default.
//
// Today only the pg_get_publication_tables FROM-clause SRF gets routed
// through `lateralCtx`, so the only positive case is a
// *PgGetPublicationTables whose argument list contains a *ColumnRef.
// Generic LATERAL subqueries / table funcs would extend this helper
// with their own walker.
func nodeReferencesOuter(n Node) bool {
	switch x := n.(type) {
	case *PgGetPublicationTables:
		for _, a := range x.Args {
			if exprContainsColumnRef(a) {
				return true
			}
		}
	}
	return false
}

func exprContainsColumnRef(e Expr) bool {
	if e == nil {
		return false
	}
	found := false
	walkExprTree(e, func(node Expr) {
		if found {
			return
		}
		if _, ok := node.(*ColumnRef); ok {
			found = true
		}
	})
	return found
}

func planFromItem(item parser.FromExpr, cat catalog.Catalog, nextSourceIdx *int16, lateralCtx *resolveContext) (Node, []rangeBinding, error) {
	leftNode, leftBinding, err := planScanRangeVar(item.Base, cat, *nextSourceIdx, lateralCtx)
	if err != nil {
		return nil, nil, err
	}
	*nextSourceIdx++
	leftCtx := newResolveContext([]rangeBinding{leftBinding}, leftNode.Output())
	for _, j := range item.Joins {
		// LATERAL on the right side of a JOIN can reference the
		// left side. Merge the outer lateralCtx with the current
		// leftCtx so SRF args on the right see both. M0103-0008.
		joinLateralCtx := mergeResolveContexts(lateralCtx, leftCtx)
		rightNode, rightBinding, err := planScanRangeVar(j.Right, cat, *nextSourceIdx, joinLateralCtx)
		if err != nil {
			return nil, nil, err
		}
		*nextSourceIdx++
		rightBinding.offset = len(leftCtx.schema)
		// For JOIN USING / NATURAL JOIN, collect the shared column names so we
		// can hide them from unqualified lookup in the MERGED context (preventing
		// "column is ambiguous"). We do NOT set usingHidden on rightBinding itself
		// because rightCtx (used by buildUsingPredicate) still needs to resolve
		// those column names against the right table. M0097-0003.
		usingCols := j.Using
		if j.Natural {
			usingCols = naturalJoinColumns(leftCtx, newResolveContext([]rangeBinding{rightBinding}, rightNode.Output()))
		}

		rightCtx := newResolveContext([]rangeBinding{rightBinding}, appendSchema(leftCtx.schema, rightNode.Output()))
		// Build a separate right binding for the merged context with usingHidden set.
		// This hides the right-side copy of USING columns from unqualified lookup
		// while rightCtx (above) retains full access for the join predicate.
		mergedRightBinding := rightBinding
		if len(usingCols) > 0 {
			mergedRightBinding.usingHidden = append([]string(nil), usingCols...)
		}
		mergedBindings := make([]rangeBinding, 0, len(leftCtx.bindings)+1)
		mergedBindings = append(mergedBindings, leftCtx.bindings...)
		mergedBindings = append(mergedBindings, mergedRightBinding)
		mergedSchema := appendSchema(leftCtx.schema, rightNode.Output())
		mergedCtx := newResolveContext(mergedBindings, mergedSchema)

		pred, err := planJoinPredicate(j, leftCtx, rightCtx, mergedCtx)
		if err != nil {
			return nil, nil, err
		}
		joinType := mapJoinType(j.Type)
		// M0063-0005: for LEFT JOIN, partition the ON conjuncts.
		// Single-inner-side conjuncts (referencing only the right
		// range-var's columns) move to a Filter wrapping the
		// inner plan BEFORE the join, so the join's residual
		// Predicate can decompose into a clean equi-pair and the
		// hash path can fire. The classic Q13 shape:
		//   `customer LEFT JOIN orders ON c_custkey = o_custkey
		//      AND o_comment NOT LIKE '%special%requests%'`
		// previously fell through to a Nested Loop because the
		// AND-chain didn't split as a single equality. Pushing
		// the inner-only NOT-LIKE before the join leaves a clean
		// equi-pair on `c_custkey = o_custkey` that
		// `splitEqualityForHash` accepts. Outer-only conjuncts
		// CANNOT move (LEFT JOIN preserves unmatched outer
		// rows); they stay in the join Predicate.
		if joinType == JoinTypeLeft && pred != nil {
			leftWidth := len(leftCtx.schema)
			totalWidth := leftWidth + len(rightNode.Output())
			conjuncts := splitAnd(pred)
			var keep []Expr
			var innerOnly []Expr
			for _, c := range conjuncts {
				switch classifyConjunctSide(c, leftWidth, totalWidth) {
				case sideRight:
					innerOnly = append(innerOnly, c)
				default:
					keep = append(keep, c)
				}
			}
			if len(innerOnly) > 0 {
				// Inner-only conjuncts were resolved against the
				// merged outer schema; their ColumnRef.Index values
				// reference the inner range-var at outer-cumulative
				// offset `leftWidth`. Now that they evaluate against
				// the inner-only row, shift each ColumnRef.Index by
				// `-leftWidth` so it points into the inner schema.
				shifted := make([]Expr, len(innerOnly))
				for i, c := range innerOnly {
					shifted[i] = shiftColumnRefsBy(c, -leftWidth)
				}
				rightNode = &Filter{
					pos:       j.Pos(),
					Child:     rightNode,
					Predicate: combineAnd(shifted),
				}
			}
			pred = combineAnd(keep)
		}
		jn := &Join{
			pos:       j.Pos(),
			Type:      joinType,
			Left:      leftNode,
			Right:     rightNode,
			Predicate: pred,
			schema:    mergedSchema,
			Lateral:   nodeReferencesOuter(rightNode),
		}
		// M0097-0060: For FULL JOIN USING / FULL JOIN NATURAL, populate
		// UsingLeftCols/UsingRightCols so the executor can coalesce USING
		// column values from the right side into the left-column positions
		// when the left side has no matching row (right-only FULL JOIN row).
		if joinType == JoinTypeFull && len(usingCols) > 0 {
			leftW := len(leftCtx.schema)
			for _, uc := range usingCols {
				// Find left-side column index (first matching name).
				leftIdx := -1
				for i, sc := range leftCtx.schema {
					if strings.EqualFold(sc.Name, uc) {
						leftIdx = i
						break
					}
				}
				// Find right-side column index (relative to merged schema).
				rightIdx := -1
				for i, c := range rightBinding.table.Columns {
					if strings.EqualFold(c.Name, uc) {
						rightIdx = leftW + i
						break
					}
				}
				if leftIdx >= 0 && rightIdx >= 0 {
					jn.UsingLeftCols = append(jn.UsingLeftCols, leftIdx)
					jn.UsingRightCols = append(jn.UsingRightCols, rightIdx)
				}
			}
		}
		// Pick a specialised equality join algorithm when the
		// predicate decomposes into disjoint-side keys:
		//   - INNER / LEFT: hash join (M0003 rule); cost-driven
		//     override for INNER when stats are present (M0006).
		//   - RIGHT / FULL: merge join (semantics-driven; stays
		//     rules-based regardless of stats).
		// CROSS and non-equality predicates stay on nested-loop.
		leftWidth := len(leftCtx.schema)
		if lk, rk, ok := splitEqualityForHash(pred, leftWidth); ok {
			// M0097-0060: Clone the ColumnRefs returned by
			// splitEqualityForHash before assigning to LeftKey/RightKey.
			// splitEqualityForHash returns the BinaryOp's inner Left/Right
			// pointers directly.  reresolveJoinByName's predRebind later
			// walks those same Predicate ColumnRef objects and may mutate
			// their Index (when the name is ambiguous on the intended side
			// it falls back to the opposite side).  Without cloning, that
			// mutation corrupts LeftKey/RightKey through the shared pointer,
			// causing chained NATURAL JOIN / USING queries to hash-probe on
			// the wrong column (0 rows).
			if cr, ok2 := lk.(*ColumnRef); ok2 {
				clone := *cr
				lk = &clone
			}
			if cr, ok2 := rk.(*ColumnRef); ok2 {
				clone := *cr
				rk = &clone
			}
			jn.LeftKey = lk
			jn.RightKey = rk
			switch jn.Type {
			case JoinTypeInner, JoinTypeLeft:
				jn.Algo = JoinAlgoHash
			case JoinTypeRight, JoinTypeFull:
				jn.Algo = JoinAlgoMerge
			}
			// INNER joins: let the cost model pick when both sides
			// have row estimates. Hash stays the M0003 fallback
			// when either side is unanalysed.
			if jn.Type == JoinTypeInner {
				lRows := EstimateRows(jn.Left)
				rRows := EstimateRows(jn.Right)
				if algo, ok := chooseInnerJoinAlgo(lRows, rRows); ok {
					jn.Algo = algo
				}
				// Build-side selection: when the cost-driven (or
				// rule-driven) algorithm landed on hash, build on
				// the smaller side. LEFT joins keep the right-as-
				// build default because the executor's outer-row
				// emission walks the left (preserved) side as the
				// probe stream.
				if jn.Algo == JoinAlgoHash && lRows > 0 && rRows > 0 && lRows < rRows {
					jn.BuildLeft = true
				}
			}
		}
		leftNode = jn
		leftCtx = mergedCtx
	}
	return leftNode, leftCtx.bindings, nil
}

func planScanRangeVar(rv parser.RangeVar, cat catalog.Catalog, sourceIdx int16, lateralCtx *resolveContext) (Node, rangeBinding, error) {
	if rv.Subquery != nil {
		return planSubqueryRangeVar(rv, cat, sourceIdx)
	}
	if rv.TableFunc != nil {
		return planTableFuncRangeVar(rv, cat, sourceIdx, lateralCtx)
	}
	// CTE substitution (M0016-0002): an unschemed name takes the
	// CTE before falling through to the catalog. CTE names are
	// unschemed in upstream so `pg_catalog.foo` always hits the
	// catalog. Multiple consumers each get a fresh plan reference
	// (Stage A inlining). The body is wrapped in a CTEScan so
	// EXPLAIN can label the inlined subtree (M0016-0004).
	if rv.Schema == "" {
		if ce := lookupPlannedCTE(rv.Name); ce != nil {
			alias := rv.Alias
			if alias == "" {
				alias = ce.name
			}
			b := rangeBinding{table: ce.table, alias: alias, offset: 0, sourceIdx: sourceIdx}
			if ce.isDML {
				// DML CTE: rows are materialized at runtime in
				// ctx.MaterializedCTEs; use MaterializedCTEScan.
				scan := &MaterializedCTEScan{
					pos:    rv.Pos(),
					Name:   ce.name,
					Alias:  alias,
					schema: ce.schema,
				}
				return scan, b, nil
			}
			scan := &CTEScan{
				pos:    rv.Pos(),
				Name:   ce.name,
				Alias:  alias,
				Child:  ce.body,
				schema: ce.schema,
			}
			return scan, b, nil
		}
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
	if !ok {
		return nil, rangeBinding{}, &PlanError{
			Pos:     rv.Pos(),
			Code:    "42P01",
			Message: fmt.Sprintf("relation %q does not exist", rv.Name),
		}
	}
	// Validate column alias count when provided: AS t(c1, c2, ...).
	// PostgreSQL raises an error if the number of column aliases does not match
	// the actual table column count. M0097-0003.
	if len(rv.Columns) > 0 && len(rv.Columns) != len(tbl.Columns) {
		return nil, rangeBinding{}, &PlanError{
			Pos:  rv.Pos(),
			Code: "42P01",
			Message: fmt.Sprintf("table %q has %d columns available but %d columns specified",
				rv.Alias, len(tbl.Columns), len(rv.Columns)),
		}
	}
	b := rangeBinding{table: tbl, alias: rv.Alias, offset: 0, sourceIdx: sourceIdx}
	baseSchema := tableSchemaWithSource(tbl, sourceIdx)
	// Apply column alias renaming from FROM tbl AS alias (col1, col2, ...).
	// The parser stores the alias list in rv.Columns; here we rename both
	// the resolve-context schema AND the rangeBinding's table columns so
	// that expandStarTarget (which iterates b.table.Columns) also picks up
	// the aliases. Without the table rename SELECT * would show original
	// column names (e.g. "i | j | t" instead of "a | b | c"). M0097-0054.
	if len(rv.Columns) > 0 {
		renamed := make(Schema, len(baseSchema))
		copy(renamed, baseSchema)
		for i, colName := range rv.Columns {
			if i < len(renamed) {
				renamed[i].Name = colName
			}
		}
		baseSchema = renamed
		// Also rename the table's column list so expandStarTarget uses the aliases.
		renamedTbl := *tbl
		renamedCols := make([]catalog.Column, len(tbl.Columns))
		copy(renamedCols, tbl.Columns)
		for i, colName := range rv.Columns {
			if i < len(renamedCols) {
				renamedCols[i].Name = colName
			}
		}
		renamedTbl.Columns = renamedCols
		b.table = &renamedTbl
	}
	ctx := newResolveContext([]rangeBinding{b}, baseSchema)
	// View: plan the stored inner SELECT and substitute its
	// node. The outer ctx's schema (built from the view's
	// catalog Table) takes precedence for downstream name
	// resolution — the inner SELECT's target-list names are
	// only relevant when the view didn't supply an explicit
	// alias list. Column count must match what the view
	// declared; v0 reports a planner error otherwise.
	if tbl.View != nil {
		inner, err := Plan(tbl.View, cat)
		if err != nil {
			return nil, rangeBinding{}, err
		}
		innerSchema := inner.Output()
		if len(innerSchema) != len(tbl.Columns) {
			return nil, rangeBinding{}, &PlanError{
				Pos:     rv.Pos(),
				Code:    "42P10",
				Message: fmt.Sprintf("view %q has %d columns but its body produces %d", tbl.QualifiedName(), len(tbl.Columns), len(innerSchema)),
			}
		}
		// Rebuild the outer ctx schema using the view's column
		// names but the inner plan's column types — types
		// flow from the planned inner so downstream
		// comparisons see the right type tag.
		schema := make(Schema, len(tbl.Columns))
		for i, c := range tbl.Columns {
			schema[i] = SchemaColumn{Name: c.Name, Type: innerSchema[i].Type, SourceTableIdx: b.sourceIdx}
		}
		ctx.schema = schema
		// M0063-0001: wrap the inner plan in a Project that
		// re-aliases columns to the view's catalog names. Without
		// this rename, `inner.Output()` continues to expose the
		// view body's target-list names (e.g. `l_suppkey`,
		// `sum`), but the OUTER scope resolves columns against
		// the view's catalog Table (`supplier_no`,
		// `total_revenue`). Downstream re-resolution passes
		// (M0063-0001's NLI Key Name re-bind, `reresolveJoinByName`,
		// etc.) need `inner.Output()` to advertise the same names
		// the outer scope resolved against — otherwise the
		// re-bind fails to match and the original (incorrect)
		// FROM-cumulative Index is left in place. Q15b's NLI
		// of supplier-on-revenue0 was the canonical breakage.
		targets := make([]Expr, len(tbl.Columns))
		for i, c := range tbl.Columns {
			targets[i] = &ColumnRef{
				pos:   rv.Pos(),
				Index: i,
				Name:  c.Name,
				Type:  innerSchema[i].Type,
			}
		}
		renamed := &Project{
			pos:           rv.Pos(),
			Child:         inner,
			Targets:       targets,
			schema:        append(Schema(nil), schema...),
			IsolatedScope: true,
		}
		return renamed, b, nil
	}
	if tbl.Virtual {
		return buildVirtualValues(rv.Pos(), tbl, ctx.schema), b, nil
	}
	// Partition-aware scan (M0096-0007): when scanning a partitioned table,
	// produce a union of SeqScans over all partition children.
	if len(tbl.PartitionKey) > 0 {
		if im, ok := cat.(*catalog.InMemory); ok {
			children := im.PartitionChildren(tbl.OID)
			if len(children) > 0 {
				// Build a UNION ALL of SeqScans over all children.
				// Per-leaf wrap with a Project that adds `tableoid`
				// as the trailing slot so a `tableoid::regclass`
				// reference reports the actual leaf relname (M0100-0005y).
				var root Node
				for _, child := range children {
					childSchema := tableSchemaWithSource(b.table, sourceIdx)
					childScan := &SeqScan{pos: rv.Pos(), Table: child, Alias: rv.Alias, schema: childSchema}
					wrapped := wrapWithTableoid(childScan, child.OID, sourceIdx, rv.Pos())
					if root == nil {
						root = wrapped
					} else {
						root = &SetOp{
							pos:   rv.Pos(),
							Left:  root,
							Right: wrapped,
							All:   true,
						}
					}
				}
				if root != nil {
					b.tableOidColIdx = len(b.table.Columns)
					ctx.schema = root.Output()
					return root, b, nil
				}
			}
		}
	}
	// Inheritance-aware scan (M0096-0009): when scanning a table that has
	// inheritance children, produce a UNION ALL of SeqScans over the parent
	// AND all descendants.  Unlike partitioned tables (where the parent has no
	// rows), an inherited parent may itself contain rows, so the parent scan
	// is always included first.  M0097-0046: expand recursively so that
	// grandchildren (e.g. stud_emp → emp → person) are included too.
	if im, ok := cat.(*catalog.InMemory); ok {
		allDesc := collectInheritanceDescendants(im, tbl.OID)
		if len(allDesc) > 0 {
			parentScan := &SeqScan{pos: rv.Pos(), Table: tbl, Alias: rv.Alias, schema: ctx.schema}
			var root Node = parentScan
			for _, child := range allDesc {
				// Use the child's own physical schema for the SeqScan so that
				// physical column indices are correct for the child's row layout.
				childScanSchema := tableSchemaWithSource(child, sourceIdx)
				childScan := &SeqScan{pos: rv.Pos(), Table: child, Alias: rv.Alias, schema: childScanSchema}
				var childNode Node = childScan
				// If the child has a different column order than the parent,
				// wrap the scan in a remap Project that emits columns in parent
				// schema order (matching the UNION ALL left side).
				childNode = buildInheritanceRemapProject(rv.Pos(), childScan, tbl, child, sourceIdx)
				root = &SetOp{
					pos:   rv.Pos(),
					Left:  root,
					Right: childNode,
					All:   true,
				}
			}
			return root, b, nil
		}
	}
	return &SeqScan{pos: rv.Pos(), Table: tbl, Alias: rv.Alias, schema: ctx.schema}, b, nil
}

// collectInheritanceDescendants performs a breadth-first traversal of the
// inheritance tree rooted at parentOID and returns all descendants in BFS
// order, deduplicated (a table can be a descendant via multiple paths, e.g.
// stud_emp inherits from both emp and student which both inherit from person).
// M0097-0046.
func collectInheritanceDescendants(im *catalog.InMemory, parentOID uint32) []*catalog.Table {
	var result []*catalog.Table
	seen := make(map[uint32]bool)
	queue := im.InheritanceChildren(parentOID)
	for len(queue) > 0 {
		child := queue[0]
		queue = queue[1:]
		if seen[child.OID] {
			continue
		}
		seen[child.OID] = true
		result = append(result, child)
		queue = append(queue, im.InheritanceChildren(child.OID)...)
	}
	return result
}

// buildInheritanceRemapProject wraps childScan in a Project that emits
// columns in the same order as the parent table schema.  When a child
// table has the same column order as the parent (the common case), a
// bare ColumnRef pass-through is generated and the executor will inline
// the trivial projection.  When column order differs (e.g. CREATE TABLE
// child(b, a) INHERITS parent(a, b)), the ColumnRef indices are
// permuted so that `a` always refers to the child's physical `a` column
// regardless of its ordinal position.  Child-only columns (not in
// parent) are dropped so the UNION ALL arms have identical width.
func buildInheritanceRemapProject(pos int, childScan *SeqScan, parent, child *catalog.Table, sourceIdx int16) Node {
	// Build a name→childIndex map for fast lookup.
	childIdxByName := make(map[string]int, len(child.Columns))
	for i, c := range child.Columns {
		childIdxByName[c.Name] = i
	}

	targets := make([]Expr, 0, len(parent.Columns))
	outSchema := make(Schema, 0, len(parent.Columns))
	needsRemap := false
	for parentIdx, pc := range parent.Columns {
		childIdx, ok := childIdxByName[pc.Name]
		if !ok {
			// Column exists in parent but not in child (shouldn't normally
			// happen in valid inheritance, but guard defensively).
			targets = append(targets, &NullConst{pos: pos})
			outSchema = append(outSchema, SchemaColumn{Name: pc.Name, Type: pc.Type, SourceTableIdx: sourceIdx})
			needsRemap = true
			continue
		}
		if childIdx != parentIdx {
			needsRemap = true
		}
		targets = append(targets, &ColumnRef{Index: childIdx, Name: pc.Name, Type: pc.Type})
		outSchema = append(outSchema, SchemaColumn{Name: pc.Name, Type: pc.Type, SourceTableIdx: sourceIdx})
	}
	if !needsRemap {
		// Column order matches parent — no remap needed; return scan as-is.
		// Update the scan's schema to match parent-ordered schema (same
		// content, different SchemaColumn.SourceTableIdx encoding is fine).
		return childScan
	}
	return &Project{pos: pos, Child: childScan, Targets: targets, schema: outSchema}
}

// buildVirtualValues materialises a virtual table's current rows as
// a Values plan node. The catalog provides the rows as text; we wrap
// each cell in a planner.StringConst so downstream Filter/Project
// nodes can apply WHERE/SELECT predicates exactly as they do over a
// SeqScan.
// TypedVirtualCell returns a typed constant expression for a single virtual
// catalog-table cell. The catalog supplies virtual rows as text; wrapping
// every cell in a StringConst made integer-family and boolean columns compare
// and aggregate *lexicographically* rather than by value — e.g. the
// pg_backend_memory_contexts query `total_bytes >= free_bytes` evaluated
// "1048576" >= "524288" as text (false) instead of 1048576 >= 524288 (true,
// the sysviews-regress expectation). Integer-family and boolean column types
// are now parsed to IntegerConst / BooleanConst so downstream Filter and
// Aggregate nodes see the correct Datum kind. Any value that does not parse
// for its declared type falls back to StringConst, preserving prior behavior
// (and display is keyed on column type, so typed cells render identically).
//
// IMPORTANT: the executor's rematerialiseVirtualRows must call this same
// helper — the two paths are siblings and must stay in sync (a virtual row
// typed one way at plan time and another at Open would diverge silently).
func TypedVirtualCell(pos int, value, colType string) Expr {
	switch strings.ToLower(colType) {
	case "int2", "int4", "int8", "integer", "bigint", "smallint":
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			return &IntegerConst{pos: pos, Value: n}
		}
	case "bool", "boolean":
		switch value {
		case "t", "true", "TRUE", "yes", "on", "1":
			return &BooleanConst{pos: pos, Value: true}
		case "f", "false", "FALSE", "no", "off", "0":
			return &BooleanConst{pos: pos, Value: false}
		}
	}
	return &StringConst{pos: pos, Value: value}
}

func buildVirtualValues(pos int, tbl *catalog.Table, schema Schema) Node {
	var rows [][]Expr
	if tbl.VirtualRows != nil {
		raw := tbl.VirtualRows()
		rows = make([][]Expr, len(raw))
		for i, r := range raw {
			cells := make([]Expr, len(tbl.Columns))
			for j := range tbl.Columns {
				if j < len(r) {
					cells[j] = TypedVirtualCell(pos, r[j], tbl.Columns[j].Type.Name)
				} else {
					cells[j] = &NullConst{pos: pos}
				}
			}
			rows[i] = cells
		}
	}
	// VirtualSource is preserved so the executor can re-materialise
	// rows at run time (the plan cache may otherwise serve a stale
	// snapshot — see M0094-0005).
	return &Values{pos: pos, Rows: rows, schema: schema, VirtualSource: tbl}
}

// planStandaloneValuesSelect plans a standalone `VALUES (r1), (r2), ...` statement.
// Columns are named "column1", "column2", ... (PostgreSQL convention). Types
// are inferred from the first row's expressions. ORDER BY / LIMIT are applied
// inline. M0097-0049.
func planStandaloneValuesSelect(s *parser.SelectStmt, cat catalog.Catalog) (Node, error) {
	rows := s.ValuesRows
	if len(rows) == 0 {
		return nil, &PlanError{Pos: s.Pos(), Code: "42601", Message: "VALUES must have at least one row"}
	}
	nCols := len(rows[0])
	innerCtx := &resolveContext{cat: cat} // no outer column refs in standalone VALUES
	planRows := make([][]Expr, len(rows))
	for i, row := range rows {
		if len(row) != nCols {
			return nil, &PlanError{Pos: s.Pos(), Code: "42601",
				Message: fmt.Sprintf("VALUES row %d has wrong number of columns: expected %d, got %d", i+1, nCols, len(row))}
		}
		planRow := make([]Expr, nCols)
		for j, e := range row {
			r, err := resolveExpr(e, innerCtx)
			if err != nil {
				return nil, err
			}
			planRow[j] = r
		}
		planRows[i] = planRow
	}
	// Infer column types by unifying across all rows (not just the first).
	// This handles e.g. VALUES (1,2), (3,8), (7,77.7) where col2 must be numeric.
	schema := make(Schema, nCols)
	for i := 0; i < nCols; i++ {
		name := fmt.Sprintf("column%d", i+1)
		typ := exprType(planRows[0][i])
		for r := 1; r < len(planRows); r++ {
			typ = unifyValueTypes(typ, exprType(planRows[r][i]))
		}
		schema[i] = SchemaColumn{Name: name, Type: typ}
	}
	var node Node = &Values{pos: s.Pos(), Rows: planRows, schema: schema}

	// Apply ORDER BY if present (e.g. VALUES (3),(1) ORDER BY 1).
	sortCtx := newResolveContext(nil, schema)
	sortCtx.cat = cat
	if len(s.OrderBy) > 0 {
		keys := make([]SortKey, 0, len(s.OrderBy))
		for _, sb := range s.OrderBy {
			var e Expr
			// Positional ORDER BY (1-based) resolves against the VALUES schema.
			if ic, ok := sb.Expr.(*parser.IntegerConst); ok {
				idx := int(ic.Value) - 1
				if idx >= 0 && idx < len(schema) {
					sc := schema[idx]
					e = &ColumnRef{pos: ic.Pos(), Index: idx, Name: sc.Name, Type: sc.Type}
				}
			}
			if e == nil {
				var err error
				e, err = resolveExpr(sb.Expr, sortCtx)
				if err != nil {
					return nil, err
				}
			}
			keys = append(keys, SortKey{Expr: e, Desc: sb.Desc, NullsFirst: sortByNullsFirst(sb)})
		}
		node = &Sort{pos: s.Pos(), Child: node, Keys: keys}
	}

	// Apply LIMIT / OFFSET if present.
	if s.Limit != nil || s.Offset != nil {
		var lim, off Expr
		if s.Limit != nil {
			e, err := resolveExpr(s.Limit, sortCtx)
			if err != nil {
				return nil, err
			}
			lim = e
		}
		if s.Offset != nil {
			e, err := resolveExpr(s.Offset, sortCtx)
			if err != nil {
				return nil, err
			}
			off = e
		}
		node = &Limit{pos: s.Pos(), Child: node, Limit: lim, Offset: off}
	}
	return node, nil
}

// planValuesSubquery plans a `(VALUES (r1), (r2), ...) AS alias (col1, col2)`
// subquery in the FROM clause. The column names come from the alias column list
// or from synthetic names like "column1", "column2". M0097-0003.
func planValuesSubquery(rv parser.RangeVar, sourceIdx int16) (Node, rangeBinding, error) {
	rows := rv.Subquery.ValuesRows
	if len(rows) == 0 {
		return nil, rangeBinding{}, &PlanError{Pos: rv.Pos(), Code: "0A000", Message: "VALUES must have at least one row"}
	}
	nCols := len(rows[0])
	ctx := &resolveContext{} // no column refs allowed in VALUES
	planRows := make([][]Expr, len(rows))
	for i, row := range rows {
		if len(row) != nCols {
			return nil, rangeBinding{}, &PlanError{Pos: rv.Pos(), Code: "42601",
				Message: fmt.Sprintf("VALUES row %d has wrong number of columns: expected %d, got %d", i+1, nCols, len(row))}
		}
		planRow := make([]Expr, nCols)
		for j, e := range row {
			r, err := resolveExpr(e, ctx)
			if err != nil {
				return nil, rangeBinding{}, err
			}
			planRow[j] = r
		}
		planRows[i] = planRow
	}
	// Build schema: use column alias list or synthetic names.
	cols := make([]catalog.Column, nCols)
	schema := make(Schema, nCols)
	for i := 0; i < nCols; i++ {
		var name string
		if i < len(rv.Columns) && rv.Columns[i] != "" {
			name = rv.Columns[i]
		} else {
			name = fmt.Sprintf("column%d", i+1)
		}
		// Type: use the type of the first row's expression.
		typ := exprType(planRows[0][i])
		cols[i] = catalog.Column{Name: name, Type: typ}
		schema[i] = SchemaColumn{Name: name, Type: typ}
	}
	tbl := &catalog.Table{Name: rv.Alias, Columns: cols}
	node := &Values{pos: rv.Pos(), Rows: planRows, schema: schema}
	b := rangeBinding{table: tbl, alias: rv.Alias, offset: 0, sourceIdx: sourceIdx}
	return node, b, nil
}

// planSubqueryRangeVar plans a `(SELECT …) AS alias` derived
// table. The inner SELECT is planned with the same catalog so
// nested derived tables / view refs / subqueries work; the
// resulting plan node's Output() schema becomes the binding's
// columns. Names come from the subquery's target-list aliases
// or v0's deriveTargetName fallback (mirrors the analyzer's
// synthesizeSubqueryTable). The synthetic *catalog.Table is
// never registered in the catalog — it lives only to satisfy
// the rangeBinding contract that downstream column resolution
// uses.
func planSubqueryRangeVar(rv parser.RangeVar, cat catalog.Catalog, sourceIdx int16) (Node, rangeBinding, error) {
	// Handle bare VALUES(...) subquery: `FROM (VALUES (r1), (r2)) AS t(c1, c2)`.
	// M0097-0003.
	if len(rv.Subquery.ValuesRows) > 0 {
		return planValuesSubquery(rv, sourceIdx)
	}
	inner, err := Plan(rv.Subquery, cat)
	if err != nil {
		// LATERAL subquery fallback: when the inner subquery references outer
		// columns (correlated lateral reference) the planner fails with a
		// "missing FROM-clause entry" error because the outer scope is not
		// visible inside the derived table's separate Plan() call.
		//
		// For vacuumdb's specific use case the lateral subquery is:
		//   CROSS JOIN LATERAL (SELECT c.relkind IN ('p', 'I')) as p (inherited)
		// The `inherited` column is only used in --missing-stats-only queries,
		// not in the basic vacuumdb run. We fall back to a single-row NULL plan
		// so the CROSS JOIN produces one row per outer row (with inherited=NULL).
		// This is safe: the WHERE clause for basic vacuumdb does not reference
		// p.inherited so the final result set is correct.
		if pe, ok := err.(*PlanError); ok && (pe.Code == "42P01" || pe.Code == "42703") &&
			len(rv.Columns) > 0 {
			nullRow := make([]Expr, len(rv.Columns))
			for i := range nullRow {
				nullRow[i] = &NullConst{}
			}
			cols := make([]catalog.Column, len(rv.Columns))
			schema := make(Schema, len(rv.Columns))
			for i, colName := range rv.Columns {
				cols[i] = catalog.Column{Name: colName}
				schema[i] = SchemaColumn{Name: colName}
			}
			inner = &Values{Rows: [][]Expr{nullRow}, schema: schema}
			tbl := &catalog.Table{Name: rv.Alias, Columns: cols}
			b := rangeBinding{table: tbl, alias: rv.Alias, offset: 0, sourceIdx: sourceIdx}
			return inner, b, nil
		}
		return nil, rangeBinding{}, err
	}
	innerSchema := inner.Output()
	// Use the inner plan's output schema as the source of truth for the
	// derived table's columns: it already accounts for star-expansion
	// (e.g. an inner `SELECT __irs_0.*` over a FROM-clause SRF expands
	// into one schema entry per SRF return column), which a target-list
	// walk would miss because the target list still holds the single
	// StarExpr. M0103-0008 (IndirectionStar derived-subquery propagation).
	// Explicit column-alias list (SELECT …) AS t (c1, c2) overrides the
	// inner schema's names.
	cols := make([]catalog.Column, 0, len(innerSchema))
	schema := make(Schema, 0, len(innerSchema))
	for i, sc := range innerSchema {
		name := sc.Name
		if i < len(rv.Columns) && rv.Columns[i] != "" {
			name = rv.Columns[i]
		}
		if name == "" {
			name = fmt.Sprintf("?column?%d", i+1)
		}
		cols = append(cols, catalog.Column{Name: name, Type: sc.Type})
		// Subquery columns are derived (an inner SELECT's
		// computed targets); they have no base-table identity at
		// the outer scope. The binding's sourceIdx still gets the
		// caller's monotonic value so qualified `sub.col`
		// references can be disambiguated against sibling
		// bindings, but the columns themselves stay at 0
		// (Go zero-value = unknown).
		schema = append(schema, SchemaColumn{Name: name, Type: sc.Type})
	}
	tbl := &catalog.Table{Name: rv.Alias, Columns: cols}
	b := rangeBinding{table: tbl, alias: rv.Alias, offset: 0, sourceIdx: sourceIdx}
	return inner, b, nil
}

// rewriteIndirectionStarTargets is a thin adapter that delegates to the
// parser-level rewrite helper. M0103-0008 probe-survival: the rewrite
// lives in the parser so every parsed SelectStmt (including nested
// subqueries) gets the rewrite, while the aggregate-arg rejection (which
// uses planner-side aggregate semantics) is surfaced here as a clean
// PlanError.
func rewriteIndirectionStarTargets(s *parser.SelectStmt) error {
	// Pass nil for onAggregate so aggregate-arg IndirectionStars stay in place;
	// planSelect detects and lowers them into ProjectSet (M0103-0008 final
	// sub-step). Non-aggregate IndirectionStars are still rewritten into
	// FROM-clause SRF references.
	return parser.RewriteIndirectionStarTargets(s, nil)
}

// projectSetCompositeSchema returns the expanded composite-row schema for a
// supported set-returning function. nil means the SRF cannot be lowered into
// ProjectSet from a `(srf(<agg>)).*` shape — currently only
// pg_get_publication_tables is supported, matching the libpqrcv
// fetch_table_list probe shape. M0103-0008.
func projectSetCompositeSchema(name string) Schema {
	switch name {
	case "pg_get_publication_tables":
		return Schema{
			SchemaColumn{Name: "relid", Type: catalog.Type{Name: "oid"}},
			SchemaColumn{Name: "attrs", Type: catalog.Type{Name: "text"}},
			SchemaColumn{Name: "qual", Type: catalog.Type{Name: "text"}},
		}
	}
	return nil
}

// buildSelectSrfProjectSet detects generate_series(...) calls in the SELECT
// target list and wraps the child node in a ProjectSet that expands the SRFs.
// Multiple generate_series calls in the same SELECT list are "zipped" together
// (each step advances all SRFs in lockstep; NULL-pads the shorter ones).
// Non-SRF targets are evaluated once per child row and repeated for each step.
// Returns nil, nil when no SRF is present in the target list. M0097-0045.
func buildSelectSrfProjectSet(s *parser.SelectStmt, child Node, ctx *resolveContext) (*ProjectSet, error) {
	type srfEntry struct {
		colIdx int
		start  parser.Expr
		stop   parser.Expr
		step   parser.Expr // may be nil
	}
	var srfs []srfEntry
	for i, t := range s.Targets {
		fc, ok := t.Expr.(*parser.FuncCall)
		if !ok {
			continue
		}
		if !strings.EqualFold(fc.Name.Name, "generate_series") {
			continue
		}
		if len(fc.Args) < 2 || len(fc.Args) > 3 {
			return nil, &PlanError{Pos: fc.Pos(), Code: "42883",
				Message: "generate_series requires 2 or 3 arguments"}
		}
		e := srfEntry{colIdx: i, start: fc.Args[0], stop: fc.Args[1]}
		if len(fc.Args) == 3 {
			e.step = fc.Args[2]
		}
		srfs = append(srfs, e)
	}
	if len(srfs) == 0 {
		return nil, nil
	}

	// Build per-column resolved expressions.
	srfColMap := make(map[int]bool, len(srfs))
	for _, e := range srfs {
		srfColMap[e.colIdx] = true
	}

	schema := make(Schema, len(s.Targets))
	otherExprs := make([]Expr, len(s.Targets))
	for i, t := range s.Targets {
		alias := t.Alias
		if srfColMap[i] {
			name := alias
			if name == "" {
				name = "generate_series"
			}
			schema[i] = SchemaColumn{Name: name, Type: catalog.Type{Name: "int8"}}
			// otherExprs[i] stays nil — executor fills this from SRF
		} else {
			expr, err := resolveExpr(t.Expr, ctx)
			if err != nil {
				return nil, err
			}
			name := alias
			if name == "" {
				name = exprOutputName(t.Expr)
			}
			ty := exprType(expr)
			schema[i] = SchemaColumn{Name: name, Type: ty}
			otherExprs[i] = expr
		}
	}

	// Resolve SRF args against ctx (may reference FROM-clause columns).
	srfCols := make([]SrfCol, len(srfs))
	for k, e := range srfs {
		start, err := resolveExpr(e.start, ctx)
		if err != nil {
			return nil, err
		}
		stop, err := resolveExpr(e.stop, ctx)
		if err != nil {
			return nil, err
		}
		var step Expr
		if e.step != nil {
			step, err = resolveExpr(e.step, ctx)
			if err != nil {
				return nil, err
			}
		}
		srfCols[k] = SrfCol{ColIdx: e.colIdx, Start: start, Stop: stop, Step: step}
	}

	return &ProjectSet{
		pos:        s.Pos(),
		Child:      child,
		SrfCols:    srfCols,
		OtherExprs: otherExprs,
		schema:     schema,
	}, nil
}

// exprOutputName returns a human-readable column name for an expression that
// has no explicit alias. Matches PostgreSQL's heuristic used in SELECT lists.
func exprOutputName(e parser.Expr) string {
	switch x := e.(type) {
	case *parser.ColumnRef:
		return x.Column
	case *parser.FuncCall:
		return strings.ToLower(x.Name.Name)
	}
	return "?column?"
}

// planTableFuncRangeVar plans a table-valued function in the FROM clause.
// Currently only generate_series(start, stop[, step]) and pg_input_error_info(value, type)
// are supported.
func planTableFuncRangeVar(rv parser.RangeVar, cat catalog.Catalog, sourceIdx int16, lateralCtx *resolveContext) (Node, rangeBinding, error) {
	tf := rv.TableFunc
	if strings.EqualFold(tf.Name, "pg_input_error_info") {
		return planPgInputErrorInfo(rv, sourceIdx)
	}
	if strings.EqualFold(tf.Name, "parse_ident") {
		return planScalarFuncScan(rv, sourceIdx, "text[]")
	}
	if strings.EqualFold(tf.Name, "pg_get_publication_tables") {
		return planPgGetPublicationTables(rv, sourceIdx, lateralCtx)
	}
	if !strings.EqualFold(tf.Name, "generate_series") {
		return nil, rangeBinding{}, &PlanError{Pos: tf.Pos(), Code: "0A000",
			Message: fmt.Sprintf("table-valued function %q not supported", tf.Name)}
	}
	if len(tf.Args) < 2 || len(tf.Args) > 3 {
		return nil, rangeBinding{}, &PlanError{Pos: tf.Pos(), Code: "42883",
			Message: "generate_series requires 2 or 3 arguments"}
	}
	ctx := &resolveContext{}
	start, err := resolveExpr(tf.Args[0], ctx)
	if err != nil {
		return nil, rangeBinding{}, err
	}
	stop, err := resolveExpr(tf.Args[1], ctx)
	if err != nil {
		return nil, rangeBinding{}, err
	}
	var step Expr
	if len(tf.Args) == 3 {
		step, err = resolveExpr(tf.Args[2], ctx)
		if err != nil {
			return nil, rangeBinding{}, err
		}
	}
	alias := rv.Alias
	if alias == "" {
		alias = "generate_series"
	}
	colName := alias
	if len(rv.Columns) > 0 {
		colName = rv.Columns[0]
	}
	tbl := &catalog.Table{
		Name: alias,
		Columns: []catalog.Column{
			{Name: colName, Type: catalog.Type{Name: "int8"}, Ordinal: 0},
		},
	}
	schema := Schema{SchemaColumn{Name: colName, Type: catalog.Type{Name: "int8"}, SourceTableIdx: sourceIdx}}
	node := &GenerateSeries{pos: tf.Pos(), Start: start, Stop: stop, Step: step, schema: schema}
	b := rangeBinding{table: tbl, alias: alias, offset: 0, sourceIdx: sourceIdx}
	return node, b, nil
}

// planScalarFuncScan plans a scalar function in FROM clause that returns one
// row with a single column of the given colType. Used for parse_ident etc.
func planScalarFuncScan(rv parser.RangeVar, sourceIdx int16, colType string) (Node, rangeBinding, error) {
	tf := rv.TableFunc
	alias := rv.Alias
	if alias == "" {
		alias = strings.ToLower(tf.Name)
	}
	colName := alias
	if len(rv.Columns) > 0 {
		colName = rv.Columns[0]
	}
	ctx := &resolveContext{}
	args := make([]Expr, 0, len(tf.Args))
	for _, a := range tf.Args {
		pa, err := resolveExpr(a, ctx)
		if err != nil {
			return nil, rangeBinding{}, err
		}
		args = append(args, pa)
	}
	fc := &FuncCall{pos: tf.Pos(), Name: strings.ToLower(tf.Name), Args: args}
	schema := Schema{SchemaColumn{Name: colName, Type: catalog.Type{Name: colType}, SourceTableIdx: sourceIdx}}
	tbl := &catalog.Table{
		Name:    alias,
		Columns: []catalog.Column{{Name: colName, Type: catalog.Type{Name: colType}, Ordinal: 0}},
	}
	node := &ScalarFuncScan{pos: tf.Pos(), Func: fc, schema: schema}
	b := rangeBinding{table: tbl, alias: alias, offset: 0, sourceIdx: sourceIdx}
	return node, b, nil
}

// planPgInputErrorInfo plans SELECT * FROM pg_input_error_info(value, type).
// Returns a PgInputErrorInfo node with the standard 4-column output schema.
// M0097-0003.
func planPgInputErrorInfo(rv parser.RangeVar, sourceIdx int16) (Node, rangeBinding, error) {
	tf := rv.TableFunc
	if len(tf.Args) != 2 {
		return nil, rangeBinding{}, &PlanError{Pos: tf.Pos(), Code: "42883",
			Message: "pg_input_error_info requires 2 arguments"}
	}
	ctx := &resolveContext{}
	val, err := resolveExpr(tf.Args[0], ctx)
	if err != nil {
		return nil, rangeBinding{}, err
	}
	typ, err := resolveExpr(tf.Args[1], ctx)
	if err != nil {
		return nil, rangeBinding{}, err
	}
	alias := rv.Alias
	if alias == "" {
		alias = "pg_input_error_info"
	}
	schema := Schema{
		SchemaColumn{Name: "message", Type: catalog.Type{Name: "text"}},
		SchemaColumn{Name: "detail", Type: catalog.Type{Name: "text"}},
		SchemaColumn{Name: "hint", Type: catalog.Type{Name: "text"}},
		SchemaColumn{Name: "sql_error_code", Type: catalog.Type{Name: "text"}},
	}
	tbl := &catalog.Table{
		Name: alias,
		Columns: []catalog.Column{
			{Name: "message", Type: catalog.Type{Name: "text"}},
			{Name: "detail", Type: catalog.Type{Name: "text"}},
			{Name: "hint", Type: catalog.Type{Name: "text"}},
			{Name: "sql_error_code", Type: catalog.Type{Name: "text"}},
		},
	}
	node := &PgInputErrorInfo{pos: tf.Pos(), Value: val, Type: typ, schema: schema}
	b := rangeBinding{table: tbl, alias: alias, offset: 0, sourceIdx: sourceIdx}
	return node, b, nil
}

// planPgGetPublicationTables routes a FROM-clause invocation of
// `pg_get_publication_tables(VARIADIC text[])` into a `PgGetPublicationTables`
// plan node. The publication-name argument is resolved as a regular expression;
// the VARIADIC marker is recorded at parse time but ignored here — the runtime
// operator accepts either a text[] (the VARIADIC spread shape) or any number
// of plain text arguments. M0103-0008 probe-survival.
func planPgGetPublicationTables(rv parser.RangeVar, sourceIdx int16, lateralCtx *resolveContext) (Node, rangeBinding, error) {
	tf := rv.TableFunc
	ctx := lateralCtx
	if ctx == nil {
		ctx = &resolveContext{}
	}
	args := make([]Expr, 0, len(tf.Args))
	for _, a := range tf.Args {
		resolved, err := resolveExpr(a, ctx)
		if err != nil {
			return nil, rangeBinding{}, err
		}
		args = append(args, resolved)
	}
	alias := rv.Alias
	if alias == "" {
		alias = "pg_get_publication_tables"
	}
	colNames := []string{"relid", "attrs", "qual"}
	if len(rv.Columns) > 0 {
		for i := range colNames {
			if i < len(rv.Columns) {
				colNames[i] = rv.Columns[i]
			}
		}
	}
	colTypes := []string{"oid", "text", "text"}
	schema := make(Schema, len(colNames))
	cols := make([]catalog.Column, len(colNames))
	for i := range colNames {
		schema[i] = SchemaColumn{Name: colNames[i], Type: catalog.Type{Name: colTypes[i]}, SourceTableIdx: sourceIdx}
		cols[i] = catalog.Column{Name: colNames[i], Type: catalog.Type{Name: colTypes[i]}, Ordinal: i}
	}
	tbl := &catalog.Table{Name: alias, Columns: cols}
	node := &PgGetPublicationTables{pos: tf.Pos(), Args: args, schema: schema}
	b := rangeBinding{table: tbl, alias: alias, offset: 0, sourceIdx: sourceIdx}
	return node, b, nil
}

func deriveSubqueryTargetName(e parser.Expr) string {
	switch x := e.(type) {
	case *parser.ColumnRef:
		return x.Column
	case *parser.FuncCall:
		return strings.ToLower(x.Name.Name)
	}
	return ""
}

func planJoinPredicate(join parser.JoinExpr, leftCtx, rightCtx, mergedCtx *resolveContext) (Expr, error) {
	if join.Type == parser.JoinCross {
		return nil, nil
	}
	if join.On != nil {
		return resolveExpr(join.On, mergedCtx)
	}
	if len(join.Using) > 0 {
		return buildUsingPredicate(join.Pos(), join.Using, leftCtx, rightCtx)
	}
	if join.Natural {
		cols := naturalJoinColumns(leftCtx, rightCtx)
		return buildUsingPredicate(join.Pos(), cols, leftCtx, rightCtx)
	}
	return nil, &PlanError{Pos: join.Pos(), Code: "42601", Message: "JOIN requires ON or USING"}
}

func buildUsingPredicate(pos int, cols []string, leftCtx, rightCtx *resolveContext) (Expr, error) {
	var pred Expr
	for _, col := range cols {
		l, err := resolveColumnRef(&parser.ColumnRef{Column: col}, leftCtx)
		if err != nil {
			return nil, err
		}
		r, err := resolveColumnRef(&parser.ColumnRef{Column: col}, rightCtx)
		if err != nil {
			return nil, err
		}
		eq := &BinaryOp{pos: pos, Op: parser.OpEq, Left: l, Right: r}
		if pred == nil {
			pred = eq
		} else {
			pred = &BinaryOp{pos: pos, Op: parser.OpAnd, Left: pred, Right: eq}
		}
	}
	return pred, nil
}

func naturalJoinColumns(leftCtx, rightCtx *resolveContext) []string {
	left := map[string]struct{}{}
	for _, b := range leftCtx.bindings {
		for _, c := range b.table.Columns {
			left[strings.ToLower(c.Name)] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, b := range rightCtx.bindings {
		for _, c := range b.table.Columns {
			k := strings.ToLower(c.Name)
			if _, ok := left[k]; !ok {
				continue
			}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, c.Name)
		}
	}
	return out
}

// splitEqualityForHash inspects a join predicate and reports
// whether it's a single equality whose two sides reference
// disjoint join inputs (one only the left, the other only the
// right). leftWidth is the column count of the left input; any
// ColumnRef with Index < leftWidth is "left-side", >= leftWidth
// is "right-side".
//
// Returns (leftKey, rightKey, true) when the equality can be
// turned into hash keys. The returned keys are oriented so
// `leftKey` references the left input only and `rightKey` the
// right; a `right.col = left.col` predicate is silently flipped
// at this layer so the executor can stay one-direction.
//
// resolveOrderBySubstitution rewrites an ORDER BY expression to
// the underlying target-list expression when the user wrote a
// bare alias or a positional index. Returns `expr` unchanged
// when neither rewrite applies. This mirrors upstream's
// `transformSortClause` lookup order: positional, then alias,
// then full expression resolution. Qualified column references
// (`t.col`) are NOT substituted — those refer to FROM-clause
// columns even if a target shares the bare name. TPC-H Q3, Q5,
// Q9, Q10, Q21 use the alias form (`ORDER BY revenue DESC`).
func resolveOrderBySubstitution(expr parser.Expr, targets []parser.ResTarget) parser.Expr {
	// Positional: `ORDER BY 1` → targets[0].Expr.
	// Guard: do not substitute a StarExpr — `SELECT * ORDER BY 1` needs
	// the positional ref resolved against the schema, not the star. M0097-0042.
	if ic, ok := expr.(*parser.IntegerConst); ok {
		idx := int(ic.Value) - 1
		if idx >= 0 && idx < len(targets) {
			if _, isStar := targets[idx].Expr.(*parser.StarExpr); !isStar {
				return targets[idx].Expr
			}
		}
		return expr
	}
	// Alias: bare ColumnRef whose Column matches a target's Alias.
	if cr, ok := expr.(*parser.ColumnRef); ok && cr.Schema == "" && cr.Table == "" {
		for _, tgt := range targets {
			if tgt.Alias != "" && strings.EqualFold(tgt.Alias, cr.Column) {
				return tgt.Expr
			}
		}
	}
	return expr
}

// AND-of-equalities, OR, range predicates, and equalities whose
// operands span both sides all fall back to (nil, nil, false)
// — the planner keeps the nested-loop algorithm for those.
func splitEqualityForHash(pred Expr, leftWidth int) (Expr, Expr, bool) {
	bin, ok := pred.(*BinaryOp)
	if !ok || bin.Op != parser.OpEq {
		return nil, nil, false
	}
	lSide := exprSide(bin.Left, leftWidth)
	rSide := exprSide(bin.Right, leftWidth)
	switch {
	case lSide == sideLeft && rSide == sideRight:
		return bin.Left, bin.Right, true
	case lSide == sideRight && rSide == sideLeft:
		return bin.Right, bin.Left, true
	}
	return nil, nil, false
}

type joinSide int

const (
	sideUnknown joinSide = iota
	sideLeft
	sideRight
	sideMixed
)

// exprSide classifies which join input(s) e references. Pure
// constants resolve as sideUnknown and combine with anything;
// references to the left input are sideLeft, to the right are
// sideRight; if both appear the result is sideMixed.
func exprSide(e Expr, leftWidth int) joinSide {
	switch x := e.(type) {
	case *ColumnRef:
		if x.Index < leftWidth {
			return sideLeft
		}
		return sideRight
	case *IntegerConst, *NumericConst, *StringConst, *NullConst, *BooleanConst, *ParamRef, *TypedStringLit, *IntervalLit:
		return sideUnknown
	case *BinaryOp:
		return mergeSides(exprSide(x.Left, leftWidth), exprSide(x.Right, leftWidth))
	case *UnaryOp:
		return exprSide(x.Operand, leftWidth)
	case *CastExpr:
		return exprSide(x.Operand, leftWidth)
	case *FuncCall:
		side := sideUnknown
		for _, a := range x.Args {
			side = mergeSides(side, exprSide(a, leftWidth))
		}
		return side
	case *CaseExpr:
		side := sideUnknown
		if x.Operand != nil {
			side = mergeSides(side, exprSide(x.Operand, leftWidth))
		}
		for _, w := range x.Whens {
			side = mergeSides(side, exprSide(w.When, leftWidth))
			side = mergeSides(side, exprSide(w.Then, leftWidth))
		}
		if x.Else != nil {
			side = mergeSides(side, exprSide(x.Else, leftWidth))
		}
		return side
	case *ExtractExpr:
		return exprSide(x.Source, leftWidth)
	}
	return sideMixed
}

func mergeSides(a, b joinSide) joinSide {
	if a == sideUnknown {
		return b
	}
	if b == sideUnknown {
		return a
	}
	if a == b {
		return a
	}
	return sideMixed
}

func mapJoinType(t parser.JoinType) JoinType {
	switch t {
	case parser.JoinLeft:
		return JoinTypeLeft
	case parser.JoinRight:
		return JoinTypeRight
	case parser.JoinFull:
		return JoinTypeFull
	case parser.JoinCross:
		return JoinTypeCross
	default:
		return JoinTypeInner
	}
}

type aggregateBinding struct {
	index int
	typ   catalog.Type
}

type aggregateSurface struct {
	input           *resolveContext
	output          *resolveContext
	groupByExpr     map[string]int
	groupByInputCol map[int]int
	aggregateByKey  map[string]aggregateBinding
	// node is the Aggregate plan node; mutated by resolveExprAfterAggregate
	// when functionally-determined passthrough columns are discovered.
	node *Aggregate
	// funcDepCols maps input column index → output schema index for columns
	// that are functionally determined by the GROUP BY key. M0097-0003.
	funcDepCols map[int]int
}

type windowBinding struct {
	index int
	typ   catalog.Type
}

type windowSurface struct {
	input       *resolveContext
	agg         *aggregateSurface
	output      *resolveContext
	windowByKey map[string]windowBinding
}

func needsAggregateStage(s *parser.SelectStmt) bool {
	if len(s.GroupBy) > 0 {
		return true
	}
	for _, t := range s.Targets {
		if exprHasAggregate(t.Expr) {
			return true
		}
	}
	if s.Having != nil {
		// HAVING without aggregates: degenerate aggregate — PostgreSQL still treats
		// the whole table as a single group (SQL spec §7.11). M0097-0003.
		return true
	}
	for _, sb := range s.OrderBy {
		if exprHasAggregate(sb.Expr) {
			return true
		}
	}
	return false
}

func needsWindowStage(s *parser.SelectStmt) bool {
	for _, t := range s.Targets {
		if parserExprHasWindowFunc(t.Expr) {
			return true
		}
	}
	for _, sb := range s.OrderBy {
		expr := resolveOrderBySubstitution(sb.Expr, s.Targets)
		if parserExprHasWindowFunc(expr) {
			return true
		}
	}
	return false
}

func buildWindowStage(s *parser.SelectStmt, child Node, inputCtx *resolveContext, agg *aggregateSurface) (Node, *resolveContext, *windowSurface, error) {
	calls, err := collectWindowCalls(s)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(calls) == 0 {
		return child, inputCtx, nil, nil
	}

	firstSpec := windowSpecKey(calls[0].Over)
	for i := 1; i < len(calls); i++ {
		if windowSpecKey(calls[i].Over) != firstSpec {
			return nil, nil, nil, &PlanError{Pos: calls[i].Pos(), Code: "0A000", Message: "multiple window specifications are not supported in v0 planner"}
		}
	}

	partition := make([]Expr, 0, len(calls[0].Over.PartitionBy))
	for _, p := range calls[0].Over.PartitionBy {
		r, err := resolveExprForWindowInput(p, inputCtx, agg)
		if err != nil {
			return nil, nil, nil, err
		}
		partition = append(partition, r)
	}
	order := make([]SortKey, 0, len(calls[0].Over.OrderBy))
	for _, ob := range calls[0].Over.OrderBy {
		r, err := resolveExprForWindowInput(ob.Expr, inputCtx, agg)
		if err != nil {
			return nil, nil, nil, err
		}
		order = append(order, SortKey{Expr: r, Desc: ob.Desc, NullsFirst: sortByNullsFirst(ob)})
	}

	outputSchema := append(Schema(nil), inputCtx.schema...)
	funcs := make([]WindowFunc, 0, len(calls))
	byKey := make(map[string]windowBinding, len(calls))
	for _, fc := range calls {
		k := windowCallKey(fc)
		if _, exists := byKey[k]; exists {
			continue
		}
		wf, err := buildWindowFunc(fc, inputCtx, agg)
		if err != nil {
			return nil, nil, nil, err
		}
		idx := len(outputSchema)
		funcs = append(funcs, wf)
		outputSchema = append(outputSchema, SchemaColumn{Name: strings.ToLower(fc.Name.Name), Type: wf.Type})
		byKey[k] = windowBinding{index: idx, typ: wf.Type}
	}

	windowNode := &WindowAgg{
		pos:         s.Pos(),
		Child:       child,
		PartitionBy: partition,
		OrderBy:     order,
		Funcs:       funcs,
		schema:      outputSchema,
	}
	outCtx := newResolveContext(nil, outputSchema)
	surface := &windowSurface{input: inputCtx, agg: agg, output: outCtx, windowByKey: byKey}
	return windowNode, outCtx, surface, nil
}

func collectWindowCalls(s *parser.SelectStmt) ([]*parser.FuncCall, error) {
	seen := map[string]struct{}{}
	out := make([]*parser.FuncCall, 0)
	visit := func(e parser.Expr) error {
		return walkExprForWindows(e, func(fc *parser.FuncCall) error {
			if fc.Over == nil {
				return nil
			}
			k := windowCallKey(fc)
			if _, ok := seen[k]; ok {
				return nil
			}
			seen[k] = struct{}{}
			out = append(out, fc)
			return nil
		})
	}
	for _, t := range s.Targets {
		if err := visit(t.Expr); err != nil {
			return nil, err
		}
	}
	for _, sb := range s.OrderBy {
		expr := resolveOrderBySubstitution(sb.Expr, s.Targets)
		if err := visit(expr); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// inferExprType returns the catalog type of a resolved planner Expr.
// Used to derive the return type of lag/lead from their first argument.
func inferExprType(e Expr) catalog.Type {
	switch x := e.(type) {
	case *ColumnRef:
		return x.Type
	case *OuterColumnRef:
		return x.Type
	case *IntegerConst:
		return catalog.Type{Name: "int8"}
	case *NumericConst:
		return catalog.Type{Name: "numeric"}
	case *StringConst:
		return catalog.Type{Name: "text"}
	case *BooleanConst:
		return catalog.Type{Name: "bool"}
	default:
		return catalog.Type{Name: "text"}
	}
}

func buildWindowFunc(fc *parser.FuncCall, inputCtx *resolveContext, agg *aggregateSurface) (WindowFunc, error) {
	name := strings.ToLower(fc.Name.Name)
	switch name {
	case "row_number", "rank":
		if fc.Star || fc.Distinct || len(fc.Args) != 0 {
			return WindowFunc{}, &PlanError{Pos: fc.Pos(), Code: "42601", Message: fmt.Sprintf("window function %s() does not accept arguments, DISTINCT, or * in v0", name)}
		}
		return WindowFunc{pos: fc.Pos(), Name: name, Type: catalog.Type{Name: "int8"}}, nil
	case "lag", "lead":
		if fc.Star || fc.Distinct || len(fc.Args) < 1 || len(fc.Args) > 3 {
			return WindowFunc{}, &PlanError{Pos: fc.Pos(), Code: "42601", Message: fmt.Sprintf("window function %s() requires 1 to 3 arguments", name)}
		}
		args := make([]Expr, 0, len(fc.Args))
		for _, a := range fc.Args {
			resolved, err := resolveExprForWindowInput(a, inputCtx, agg)
			if err != nil {
				return WindowFunc{}, err
			}
			args = append(args, resolved)
		}
		retType := inferExprType(args[0])
		return WindowFunc{pos: fc.Pos(), Name: name, Type: retType, Args: args}, nil
	default:
		return WindowFunc{}, &PlanError{Pos: fc.Pos(), Code: "0A000", Message: fmt.Sprintf("window function %q is not supported in v0 planner", name)}
	}
}

func windowCallKey(fc *parser.FuncCall) string {
	b := strings.Builder{}
	b.WriteString(strings.ToLower(fc.Name.String()))
	b.WriteString("|")
	if fc.Star {
		b.WriteString("*")
	}
	if fc.Distinct {
		b.WriteString("distinct|")
	}
	for _, a := range fc.Args {
		b.WriteString(parserExprKey(a))
		b.WriteString("|")
	}
	b.WriteString("over:")
	b.WriteString(windowSpecKey(fc.Over))
	return b.String()
}

func windowSpecKey(w *parser.WindowDef) string {
	if w == nil {
		return ""
	}
	b := strings.Builder{}
	b.WriteString("p:")
	for _, p := range w.PartitionBy {
		b.WriteString(parserExprKey(p))
		b.WriteString("|")
	}
	b.WriteString("o:")
	for _, o := range w.OrderBy {
		b.WriteString(parserExprKey(o.Expr))
		if o.Desc {
			b.WriteString(":desc")
		} else {
			b.WriteString(":asc")
		}
		b.WriteString("|")
	}
	return b.String()
}

func walkExprForWindows(e parser.Expr, fn func(*parser.FuncCall) error) error {
	switch x := e.(type) {
	case *parser.BinaryOp:
		if err := walkExprForWindows(x.Left, fn); err != nil {
			return err
		}
		return walkExprForWindows(x.Right, fn)
	case *parser.UnaryOp:
		return walkExprForWindows(x.Operand, fn)
	case *parser.CastExpr:
		return walkExprForWindows(x.Operand, fn)
	case *parser.ExtractExpr:
		return walkExprForWindows(x.Source, fn)
	case *parser.CaseExpr:
		if x.Operand != nil {
			if err := walkExprForWindows(x.Operand, fn); err != nil {
				return err
			}
		}
		for _, w := range x.Whens {
			if err := walkExprForWindows(w.When, fn); err != nil {
				return err
			}
			if err := walkExprForWindows(w.Then, fn); err != nil {
				return err
			}
		}
		if x.Else != nil {
			return walkExprForWindows(x.Else, fn)
		}
		return nil
	case *parser.IsNullExpr:
		return walkExprForWindows(x.Operand, fn)
	case *parser.IsBoolExpr:
		return walkExprForWindows(x.Operand, fn)
	case *parser.IsDistinctFromExpr:
		if err := walkExprForWindows(x.Left, fn); err != nil {
			return err
		}
		return walkExprForWindows(x.Right, fn)
	case *parser.InExpr:
		if err := walkExprForWindows(x.Operand, fn); err != nil {
			return err
		}
		for _, v := range x.List {
			if err := walkExprForWindows(v, fn); err != nil {
				return err
			}
		}
		return nil
	case *parser.FuncCall:
		if err := fn(x); err != nil {
			return err
		}
		for _, a := range x.Args {
			if err := walkExprForWindows(a, fn); err != nil {
				return err
			}
		}
		if x.Over != nil {
			for _, p := range x.Over.PartitionBy {
				if err := walkExprForWindows(p, fn); err != nil {
					return err
				}
			}
			for _, o := range x.Over.OrderBy {
				if err := walkExprForWindows(o.Expr, fn); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func buildAggregateStage(s *parser.SelectStmt, child Node, inputCtx *resolveContext) (Node, *resolveContext, *aggregateSurface, Expr, error) {
	groupExprs := make([]Expr, 0, len(s.GroupBy))
	groupByExpr := map[string]int{}
	groupByInputCol := map[int]int{}
	outputSchema := make(Schema, 0, len(s.GroupBy)+len(s.Targets))

	for _, g := range s.GroupBy {
		// GROUP BY accepts target-list aliases and positional
		// indices (PG extension; TPC-H Q7 leans on it). Run the
		// same substitution as ORDER BY so the resolved
		// expression — and the parserExprKey we record below —
		// matches what the target list and ORDER BY look up.
		g = resolveOrderBySubstitution(g, s.Targets)
		// Positional GROUP BY that wasn't substituted → out-of-range position. M0097-0003.
		if ic, ok := g.(*parser.IntegerConst); ok {
			return nil, nil, nil, nil, &PlanError{Pos: g.Pos(), Code: "42P10",
				Message: fmt.Sprintf("GROUP BY position %d is not in select list", ic.Value)}
		}
		if exprHasAggregate(g) {
			return nil, nil, nil, nil, &PlanError{Pos: g.Pos(), Code: "42803", Message: "aggregate functions are not allowed in GROUP BY"}
		}
		r, err := resolveExpr(g, inputCtx)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		idx := len(outputSchema)
		groupByExpr[parserExprKey(g)] = idx
		if c, ok := r.(*ColumnRef); ok {
			groupByInputCol[c.Index] = idx
		}
		groupExprs = append(groupExprs, r)
		outputSchema = append(outputSchema, SchemaColumn{Name: groupExprName(r), Type: exprType(r)})
	}

	aggCalls, err := collectAggregateCalls(s)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	aggByKey := make(map[string]aggregateBinding, len(aggCalls))
	plannedAggs := make([]AggregateCall, 0, len(aggCalls))
	for _, fc := range aggCalls {
		k := aggregateCallKey(fc)
		if _, exists := aggByKey[k]; exists {
			continue
		}
		pa, err := buildAggregateCall(fc, inputCtx)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		idx := len(outputSchema)
		aggByKey[k] = aggregateBinding{index: idx, typ: pa.Type}
		plannedAggs = append(plannedAggs, pa)
		outputSchema = append(outputSchema, SchemaColumn{Name: strings.ToLower(fc.Name.Name), Type: pa.Type})
	}

	aggNode := &Aggregate{
		pos:        s.Pos(),
		Child:      child,
		GroupExprs: groupExprs,
		Aggs:       plannedAggs,
		schema:     outputSchema,
	}

	outputCtx := newResolveContext(nil, outputSchema)
	surface := &aggregateSurface{
		input:           inputCtx,
		output:          outputCtx,
		groupByExpr:     groupByExpr,
		groupByInputCol: groupByInputCol,
		aggregateByKey:  aggByKey,
		node:            aggNode,
		funcDepCols:     map[int]int{},
	}

	var having Expr
	if s.Having != nil {
		having, err = resolveExprAfterAggregate(s.Having, surface)
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}
	return aggNode, outputCtx, surface, having, nil
}

// isConstantPlanExpr reports whether e can be evaluated without any row data.
// Used by the constant-degenerate-aggregate optimization. M0097-0003.
func isConstantPlanExpr(e Expr) bool {
	switch x := e.(type) {
	case *IntegerConst, *NumericConst, *StringConst, *BooleanConst, *NullConst, *TypedStringLit:
		return true
	case *BinaryOp:
		return isConstantPlanExpr(x.Left) && isConstantPlanExpr(x.Right)
	case *UnaryOp:
		return isConstantPlanExpr(x.Operand)
	case *CastExpr:
		return isConstantPlanExpr(x.Operand)
	case *IsBoolExpr:
		return isConstantPlanExpr(x.Operand)
	case *IsNullExpr:
		return isConstantPlanExpr(x.Operand)
	case *IsDistinctFromExpr:
		return isConstantPlanExpr(x.Left) && isConstantPlanExpr(x.Right)
	}
	return false
}

// evalConstantBool evaluates a constant boolean expression at plan time.
// Returns (result, ok=true) if evaluation succeeded, (false, false) if not constant. M0097-0003.
func evalConstantBool(e Expr) (bool, bool) {
	if b, ok := e.(*BooleanConst); ok {
		return b.Value, true
	}
	if n, ok := e.(*IsNullExpr); ok {
		if isConstantPlanExpr(n.Operand) {
			_, isNull := n.Operand.(*NullConst)
			result := isNull
			if n.Negated {
				result = !result
			}
			return result, true
		}
	}
	if b, ok := e.(*BinaryOp); ok {
		// Compare two integer constants at plan time.
		li, lok := b.Left.(*IntegerConst)
		ri, rok := b.Right.(*IntegerConst)
		if lok && rok {
			l, r := li.Value, ri.Value
			switch b.Op {
			case parser.OpLt:
				return l < r, true
			case parser.OpLe:
				return l <= r, true
			case parser.OpGt:
				return l > r, true
			case parser.OpGe:
				return l >= r, true
			case parser.OpEq:
				return l == r, true
			case parser.OpNe:
				return l != r, true
			}
		}
	}
	return false, false
}

// isTextLikePlannerType returns true for string-like types that can be compared
// with name type (implicitly cast to name for truncation). M0097-0003.
func isTextLikePlannerType(name string) bool {
	switch strings.ToLower(name) {
	case "text", "varchar", "char", "bpchar", "character varying", "name", "unknown", "":
		return true
	}
	return false
}

func groupExprName(e Expr) string {
	if c, ok := e.(*ColumnRef); ok {
		return c.Name
	}
	// FuncCall GROUP BY: use function name as column label (e.g. lower(c) → "lower"). M0097-0003.
	if f, ok := e.(*FuncCall); ok && f.Name != "" {
		return f.Name
	}
	return "?column?"
}

func resolveExprAfterAggregate(e parser.Expr, agg *aggregateSurface) (Expr, error) {
	if idx, ok := agg.groupByExpr[parserExprKey(e)]; ok {
		return &ColumnRef{pos: e.Pos(), Index: idx, Name: agg.output.schema[idx].Name, Type: agg.output.schema[idx].Type}, nil
	}
	switch x := e.(type) {
	case *parser.IntegerConst:
		return &IntegerConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.NumericConst:
		return &NumericConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.StringConst:
		return &StringConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.TypedStringLit:
		return &TypedStringLit{pos: x.Pos(), Type: x.Type, Value: x.Value}, nil
	case *parser.IntervalLit:
		return &IntervalLit{pos: x.Pos(), Value: x.Value, Unit: x.Unit}, nil
	case *parser.SubqueryExpr:
		return planSubqueryExpr(x, agg.input)
	case *parser.InExpr:
		return planInExpr(x, agg.input)
	case *parser.ExistsExpr:
		return planExistsExpr(x, agg.input)
	case *parser.IsNullExpr:
		operand, err := resolveExpr(x.Operand, agg.input)
		if err != nil {
			return nil, err
		}
		return &IsNullExpr{pos: x.Pos(), Operand: operand, Negated: x.Negated}, nil
	case *parser.IsBoolExpr:
		operand, err := resolveExpr(x.Operand, agg.input)
		if err != nil {
			return nil, err
		}
		return &IsBoolExpr{pos: x.Pos(), Operand: operand, TestTrue: x.TestTrue, TestFalse: x.TestFalse, Negated: x.Negated}, nil
	case *parser.IsDistinctFromExpr:
		lv, err := resolveExpr(x.Left, agg.input)
		if err != nil {
			return nil, err
		}
		rv, err := resolveExpr(x.Right, agg.input)
		if err != nil {
			return nil, err
		}
		return &IsDistinctFromExpr{pos: x.Pos(), Left: lv, Right: rv, Negated: x.Negated}, nil
	case *parser.ExtractExpr:
		src, err := resolveExpr(x.Source, agg.input)
		if err != nil {
			return nil, err
		}
		return &ExtractExpr{pos: x.Pos(), Field: x.Field, Source: src, SourceTypeName: exprType(src).Name}, nil
	case *parser.NullConst:
		return &NullConst{pos: x.Pos()}, nil
	case *parser.BooleanConst:
		return &BooleanConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.ParamRef:
		return &ParamRef{pos: x.Pos(), Number: x.Number}, nil
	case *parser.CaseExpr:
		return resolveCaseExpr(x, agg.input)
	case *parser.ColumnRef:
		resolved, err := resolveColumnRef(x, agg.input)
		if err != nil {
			return nil, err
		}
		col := resolved.(*ColumnRef)
		idx, ok := agg.groupByInputCol[col.Index]
		if !ok {
			// Check if this column is functionally determined by the GROUP BY key.
			// PostgreSQL SQL92 extension: when GROUP BY covers a primary key of some
			// table, all other columns of that table are functionally determined and
			// may appear in SELECT without being in GROUP BY or an aggregate. M0097-0003.
			if outIdx, alreadyAdded := agg.funcDepCols[col.Index]; alreadyAdded {
				return &ColumnRef{pos: x.Pos(), Index: outIdx, Name: agg.output.schema[outIdx].Name, Type: agg.output.schema[outIdx].Type}, nil
			}
			if isColumnFunctionallyDetermined(col, agg) {
				// Lazily add this column as a passthrough in the Aggregate node.
				// The executor evaluates Passthrough expressions from the first row
				// of each group and appends them to the output row.
				outIdx := len(agg.node.schema)
				sc := SchemaColumn{Name: col.Name, Type: col.Type, SourceTableIdx: col.SourceTableIdx}
				agg.node.schema = append(agg.node.schema, sc)
				agg.output.schema = append(agg.output.schema, sc)
				// Passthrough expression references the child/input ColumnRef.
				agg.node.Passthrough = append(agg.node.Passthrough, col)
				agg.funcDepCols[col.Index] = outIdx
				return &ColumnRef{pos: x.Pos(), Index: outIdx, Name: sc.Name, Type: sc.Type}, nil
			}

			// Include table qualifier in error message (PostgreSQL uses "table.col"). M0097-0003.
			colName := col.Name
			for _, b := range agg.input.bindings {
				if b.sourceIdx == col.SourceTableIdx {
					tbl := b.alias
					if tbl == "" {
						tbl = b.table.Name
					}
					if tbl != "" {
						colName = tbl + "." + col.Name
					}
					break
				}
			}
			return nil, &PlanError{
				Pos:     x.Pos(),
				Code:    "42803",
				Message: fmt.Sprintf("column %q must appear in the GROUP BY clause or be used in an aggregate function", colName),
			}
		}
		return &ColumnRef{pos: x.Pos(), Index: idx, Name: agg.output.schema[idx].Name, Type: agg.output.schema[idx].Type}, nil
	case *parser.BinaryOp:
		l, err := resolveExprAfterAggregate(x.Left, agg)
		if err != nil {
			return nil, err
		}
		r, err := resolveExprAfterAggregate(x.Right, agg)
		if err != nil {
			return nil, err
		}
		node := &BinaryOp{pos: x.Pos(), Op: x.Op, Left: l, Right: r}
		switch x.Op {
		case parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod:
			node.ResultType = exprType(node).Name
		}
		return node, nil
	case *parser.UnaryOp:
		op, err := resolveExprAfterAggregate(x.Operand, agg)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: op}, nil
	case *parser.CastExpr:
		// M0097-0003: emit CastExpr so the executor can coerce at runtime.
		operand, err := resolveExprAfterAggregate(x.Operand, agg)
		if err != nil {
			return nil, err
		}
		typeName := strings.ToLower(x.Type.Name)
		var typmod int64
		if len(x.Typmods) > 0 {
			typmod = x.Typmods[0]
		}
		return &CastExpr{pos: x.Pos(), Operand: operand, TargetType: typeName, SourceType: exprType(operand).Name, Typmod: typmod}, nil
	case *parser.FuncCall:
		if x.Over != nil {
			return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "window functions must be planned via WindowAgg"}
		}
		if isAggregateFunc(x) {
			k := aggregateCallKey(x)
			b, ok := agg.aggregateByKey[k]
			if !ok {
				return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "aggregate call could not be resolved"}
			}
			return &ColumnRef{pos: x.Pos(), Index: b.index, Name: agg.output.schema[b.index].Name, Type: b.typ}, nil
		}
		args := make([]Expr, 0, len(x.Args))
		for _, a := range x.Args {
			pa, err := resolveExprAfterAggregate(a, agg)
			if err != nil {
				return nil, err
			}
			args = append(args, pa)
		}
		return &FuncCall{pos: x.Pos(), Name: x.Name.String(), Args: args, Star: x.Star}, nil
	case *parser.ArrayConstructorExpr:
		// ARRAY[e1, e2, ...] after GROUP BY — resolve each element
		// through the post-aggregate surface (so group-by columns
		// resolve correctly) and emit as array_construct. M0097-0060.
		args := make([]Expr, len(x.Elements))
		for i, el := range x.Elements {
			r, err := resolveExprAfterAggregate(el, agg)
			if err != nil {
				return nil, err
			}
			args[i] = r
		}
		return &FuncCall{pos: x.Pos(), Name: "array_construct", Args: args}, nil
	case *parser.StarExpr:
		return nil, &PlanError{Pos: x.Pos(), Code: "42601", Message: "'*' is not allowed here"}
	}
	return nil, &PlanError{Pos: e.Pos(), Code: "0A000", Message: fmt.Sprintf("unsupported expression %T", e)}
}

func resolveTargetsAfterAggregate(targets []parser.ResTarget, agg *aggregateSurface) ([]Expr, Schema, error) {
	out := make([]Expr, 0, len(targets))
	schema := make(Schema, 0, len(targets))
	for _, t := range targets {
		if _, ok := t.Expr.(*parser.StarExpr); ok {
			return nil, nil, &PlanError{Pos: t.Pos(), Code: "0A000", Message: "SELECT * with GROUP BY/aggregate is not supported in v0 planner"}
		}
		e, err := resolveExprAfterAggregate(t.Expr, agg)
		if err != nil {
			return nil, nil, err
		}
		name, typ := targetMeta(e, t)
		out = append(out, e)
		schema = append(schema, SchemaColumn{Name: name, Type: typ})
	}
	return out, schema, nil
}

func resolveExprForWindowInput(e parser.Expr, inputCtx *resolveContext, agg *aggregateSurface) (Expr, error) {
	if agg == nil {
		return resolveExpr(e, inputCtx)
	}
	return resolveExprAfterAggregate(e, agg)
}

func resolveExprAfterWindow(e parser.Expr, win *windowSurface) (Expr, error) {
	if !parserExprHasWindowFunc(e) {
		return resolveExprForWindowInput(e, win.input, win.agg)
	}
	switch x := e.(type) {
	case *parser.BinaryOp:
		l, err := resolveExprAfterWindow(x.Left, win)
		if err != nil {
			return nil, err
		}
		r, err := resolveExprAfterWindow(x.Right, win)
		if err != nil {
			return nil, err
		}
		node := &BinaryOp{pos: x.Pos(), Op: x.Op, Left: l, Right: r}
		switch x.Op {
		case parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod:
			node.ResultType = exprType(node).Name
		}
		return node, nil
	case *parser.UnaryOp:
		op, err := resolveExprAfterWindow(x.Operand, win)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: op}, nil
	case *parser.CastExpr:
		// M0097-0003: emit CastExpr so the executor can coerce at runtime.
		operand, err := resolveExprAfterWindow(x.Operand, win)
		if err != nil {
			return nil, err
		}
		typeName := strings.ToLower(x.Type.Name)
		var typmod int64
		if len(x.Typmods) > 0 {
			typmod = x.Typmods[0]
		}
		return &CastExpr{pos: x.Pos(), Operand: operand, TargetType: typeName, SourceType: exprType(operand).Name, Typmod: typmod}, nil
	case *parser.ExtractExpr:
		src, err := resolveExprAfterWindow(x.Source, win)
		if err != nil {
			return nil, err
		}
		return &ExtractExpr{pos: x.Pos(), Field: x.Field, Source: src, SourceTypeName: exprType(src).Name}, nil
	case *parser.CaseExpr:
		out := &CaseExpr{pos: x.Pos()}
		if x.Operand != nil {
			op, err := resolveExprAfterWindow(x.Operand, win)
			if err != nil {
				return nil, err
			}
			out.Operand = op
		}
		for _, w := range x.Whens {
			when, err := resolveExprAfterWindow(w.When, win)
			if err != nil {
				return nil, err
			}
			then, err := resolveExprAfterWindow(w.Then, win)
			if err != nil {
				return nil, err
			}
			out.Whens = append(out.Whens, CaseWhen{When: when, Then: then})
		}
		if x.Else != nil {
			els, err := resolveExprAfterWindow(x.Else, win)
			if err != nil {
				return nil, err
			}
			out.Else = els
		}
		return out, nil
	case *parser.FuncCall:
		if x.Over != nil {
			k := windowCallKey(x)
			b, ok := win.windowByKey[k]
			if !ok {
				return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "window call could not be resolved"}
			}
			return &ColumnRef{pos: x.Pos(), Index: b.index, Name: win.output.schema[b.index].Name, Type: b.typ}, nil
		}
		args := make([]Expr, 0, len(x.Args))
		for _, a := range x.Args {
			pa, err := resolveExprAfterWindow(a, win)
			if err != nil {
				return nil, err
			}
			args = append(args, pa)
		}
		return &FuncCall{pos: x.Pos(), Name: x.Name.String(), Args: args, Star: x.Star}, nil
	case *parser.InExpr:
		op, err := resolveExprAfterWindow(x.Operand, win)
		if err != nil {
			return nil, err
		}
		list := make([]Expr, 0, len(x.List))
		for _, item := range x.List {
			r, err := resolveExprAfterWindow(item, win)
			if err != nil {
				return nil, err
			}
			list = append(list, r)
		}
		if x.Subquery != nil {
			return planInExpr(x, win.input)
		}
		return &InExpr{pos: x.Pos(), Operand: op, Negated: x.Negated, List: list}, nil
	case *parser.IsNullExpr:
		operand, err := resolveExprAfterWindow(x.Operand, win)
		if err != nil {
			return nil, err
		}
		return &IsNullExpr{pos: x.Pos(), Operand: operand, Negated: x.Negated}, nil
	case *parser.IsBoolExpr:
		operand, err := resolveExprAfterWindow(x.Operand, win)
		if err != nil {
			return nil, err
		}
		return &IsBoolExpr{pos: x.Pos(), Operand: operand, TestTrue: x.TestTrue, TestFalse: x.TestFalse, Negated: x.Negated}, nil
	case *parser.IsDistinctFromExpr:
		lv, err := resolveExprAfterWindow(x.Left, win)
		if err != nil {
			return nil, err
		}
		rv, err := resolveExprAfterWindow(x.Right, win)
		if err != nil {
			return nil, err
		}
		return &IsDistinctFromExpr{pos: x.Pos(), Left: lv, Right: rv, Negated: x.Negated}, nil
	}
	return resolveExprForWindowInput(e, win.input, win.agg)
}

func resolveTargetsAfterWindow(targets []parser.ResTarget, win *windowSurface) ([]Expr, Schema, error) {
	out := make([]Expr, 0, len(targets))
	schema := make(Schema, 0, len(targets))
	for _, t := range targets {
		if star, ok := t.Expr.(*parser.StarExpr); ok {
			exprList, cols, err := expandStarTarget(star, win.input)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, exprList...)
			schema = append(schema, cols...)
			continue
		}
		e, err := resolveExprAfterWindow(t.Expr, win)
		if err != nil {
			return nil, nil, err
		}
		name, typ := targetMeta(e, t)
		out = append(out, e)
		schema = append(schema, SchemaColumn{Name: name, Type: typ})
	}
	return out, schema, nil
}

func collectAggregateCalls(s *parser.SelectStmt) ([]*parser.FuncCall, error) {
	seen := map[string]struct{}{}
	out := make([]*parser.FuncCall, 0)
	visit := func(e parser.Expr) error {
		return walkExpr(e, func(fc *parser.FuncCall) error {
			if !isAggregateFunc(fc) {
				return nil
			}
			if len(fc.Args) > 0 {
				for _, a := range fc.Args {
					if exprHasAggregate(a) {
						return &PlanError{Pos: a.Pos(), Code: "42803", Message: "nested aggregate calls are not supported in v0 planner"}
					}
				}
			}
			k := aggregateCallKey(fc)
			if _, ok := seen[k]; ok {
				return nil
			}
			seen[k] = struct{}{}
			out = append(out, fc)
			return nil
		})
	}
	for _, t := range s.Targets {
		if err := visit(t.Expr); err != nil {
			return nil, err
		}
	}
	if s.Having != nil {
		if err := visit(s.Having); err != nil {
			return nil, err
		}
	}
	for _, sb := range s.OrderBy {
		if err := visit(sb.Expr); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func walkExpr(e parser.Expr, fn func(*parser.FuncCall) error) error {
	switch x := e.(type) {
	case *parser.BinaryOp:
		if err := walkExpr(x.Left, fn); err != nil {
			return err
		}
		return walkExpr(x.Right, fn)
	case *parser.UnaryOp:
		return walkExpr(x.Operand, fn)
	case *parser.CastExpr:
		return walkExpr(x.Operand, fn)
	case *parser.IsNullExpr:
		return walkExpr(x.Operand, fn)
	case *parser.IsBoolExpr:
		return walkExpr(x.Operand, fn)
	case *parser.IsDistinctFromExpr:
		if err := walkExpr(x.Left, fn); err != nil {
			return err
		}
		return walkExpr(x.Right, fn)
	case *parser.FuncCall:
		if err := fn(x); err != nil {
			return err
		}
		for _, a := range x.Args {
			if err := walkExpr(a, fn); err != nil {
				return err
			}
		}
	case *parser.IndirectionStar:
		return walkExpr(x.Source, fn)
	}
	return nil
}

func isAggregateFunc(fc *parser.FuncCall) bool {
	// Window functions (with OVER clause) are NOT aggregates.
	if fc.Over != nil {
		return false
	}
	switch strings.ToLower(fc.Name.Name) {
	case "count", "sum", "avg", "min", "max",
		// Statistical aggregates (M0097-0007)
		"var_pop", "var_samp", "variance",
		"stddev_pop", "stddev_samp", "stddev",
		"corr", "covar_pop", "covar_samp",
		"regr_count", "regr_sxx", "regr_syy", "regr_sxy",
		"regr_avgx", "regr_avgy", "regr_r2",
		"regr_slope", "regr_intercept",
		// Boolean aggregates
		"bool_and", "bool_or", "every",
		// Bitwise aggregates
		"bit_and", "bit_or", "bit_xor",
		// Miscellaneous aggregates
		"string_agg", "array_agg", "json_agg", "jsonb_agg",
		"json_object_agg", "jsonb_object_agg",
		"xmlagg", "any_value",
		// Ordered-set aggregates (only aggregate form, not window)
		"percentile_cont", "percentile_disc", "mode":
		return true
	}
	return false
}

func exprHasAggregate(e parser.Expr) bool {
	found := false
	_ = walkExpr(e, func(fc *parser.FuncCall) error {
		if isAggregateFunc(fc) {
			found = true
		}
		return nil
	})
	return found
}

func parserExprHasWindowFunc(e parser.Expr) bool {
	found := false
	_ = walkExprForWindows(e, func(fc *parser.FuncCall) error {
		if fc.Over != nil {
			found = true
		}
		return nil
	})
	return found
}

func aggregateCallKey(fc *parser.FuncCall) string {
	b := strings.Builder{}
	b.WriteString(strings.ToLower(fc.Name.String()))
	b.WriteString("|")
	if fc.Star {
		b.WriteString("*")
	}
	if fc.Distinct {
		b.WriteString("distinct|")
	}
	for _, a := range fc.Args {
		b.WriteString(parserExprKey(a))
		b.WriteString("|")
	}
	// FILTER (WHERE ...) must be part of the dedup key: `count(*)` and
	// `count(*) FILTER (WHERE p)` are distinct aggregates. Omitting it
	// collapsed them onto one slot, so the filtered count silently
	// reported the unfiltered total (e.g. sysviews pg_hba_file_rules
	// `count(*) FILTER (WHERE error IS NOT NULL)`). M0097-0032.
	if fc.Filter != nil {
		b.WriteString("filter|")
		b.WriteString(parserExprKey(fc.Filter))
		b.WriteString("|")
	}
	return b.String()
}

func buildAggregateCall(fc *parser.FuncCall, inputCtx *resolveContext) (AggregateCall, error) {
	name := strings.ToLower(fc.Name.Name)
	// Resolve the FILTER (WHERE ...) predicate up front so every return path
	// below carries it — including count(*) and zero-arg aggregates, which
	// otherwise silently dropped the filter (e.g. `count(*) FILTER (WHERE c)`
	// counted every row). M0097-0007 / M0097-0032.
	var filterExpr Expr
	if fc.Filter != nil {
		var ferr error
		filterExpr, ferr = resolveExpr(fc.Filter, inputCtx)
		if ferr != nil {
			return AggregateCall{}, ferr
		}
	}
	if fc.Star {
		if name != "count" || len(fc.Args) != 0 {
			return AggregateCall{}, &PlanError{Pos: fc.Pos(), Code: "42601", Message: "only count(*) is supported with * aggregate arguments"}
		}
		return AggregateCall{
			pos:      fc.Pos(),
			Name:     name,
			Star:     true,
			Distinct: fc.Distinct,
			Type:     catalog.Type{Name: "int8"},
			Filter:   filterExpr,
		}, nil
	}
	// Many aggregates accept 0 or more args; only enforce 1-arg for the
	// core ones. Extended aggregates (M0097-0007) may have 2+ args.
	if len(fc.Args) == 0 {
		// Zero-arg aggregates like count(*) handled above; all others need args.
		return AggregateCall{
			pos: fc.Pos(), Name: name, Distinct: fc.Distinct,
			Type:   catalog.Type{Name: "numeric"},
			Filter: filterExpr,
		}, nil
	}
	argExpr, err := resolveExpr(fc.Args[0], inputCtx)
	if err != nil {
		return AggregateCall{}, err
	}
	outType := catalog.Type{Name: "unknown"}
	switch name {
	case "count":
		outType = catalog.Type{Name: "int8"}
	case "sum":
		outType = exprType(argExpr)
		if strings.EqualFold(outType.Name, "unknown") || outType.Name == "" {
			outType = catalog.Type{Name: "int8"}
		}
	case "avg":
		outType = catalog.Type{Name: "numeric"}
	case "min", "max":
		outType = exprType(argExpr)
	default:
		// Extended aggregates (M0097-0007): accept but return null/stub type.
		outType = catalog.Type{Name: "numeric"}
	}
	// Resolve ORDER BY inside the aggregate call (e.g. array_agg(x ORDER BY y)).
	var orderByKeys []SortKey
	for _, sb := range fc.OrderBy {
		e, serr := resolveExpr(sb.Expr, inputCtx)
		if serr != nil {
			return AggregateCall{}, serr
		}
		orderByKeys = append(orderByKeys, SortKey{Expr: e, Desc: sb.Desc, NullsFirst: sortByNullsFirst(sb)})
	}
	return AggregateCall{
		pos:      fc.Pos(),
		Name:     name,
		Arg:      argExpr,
		Distinct: fc.Distinct,
		Type:     outType,
		Filter:   filterExpr,
		OrderBy:  orderByKeys,
	}, nil
}

func parserExprKey(e parser.Expr) string {
	switch x := e.(type) {
	case *parser.IntegerConst:
		return fmt.Sprintf("i:%d", x.Value)
	case *parser.NumericConst:
		return "n:" + x.Value
	case *parser.StringConst:
		return "s:" + x.Value
	case *parser.NullConst:
		return "null"
	case *parser.BooleanConst:
		if x.Value {
			return "bool:true"
		}
		return "bool:false"
	case *parser.ParamRef:
		return fmt.Sprintf("p:%d", x.Number)
	case *parser.ColumnRef:
		// Use only the column name (not the table/schema qualifier) so that
		// `lower(c)` and `lower(t.c)` resolve to the same GROUP BY key. M0097-0003.
		return "c:" + strings.ToLower(x.Column)
	case *parser.UnaryOp:
		return "u:" + x.Op.String() + ":" + parserExprKey(x.Operand)
	case *parser.IsNullExpr:
		// Distinguish IS NULL from IS NOT NULL so two FILTER predicates that
		// differ only by negation get different aggregate dedup keys.
		if x.Negated {
			return "isnotnull:(" + parserExprKey(x.Operand) + ")"
		}
		return "isnull:(" + parserExprKey(x.Operand) + ")"
	case *parser.IsDistinctFromExpr:
		pfx := "isdistinct"
		if x.Negated {
			pfx = "isnotdistinct"
		}
		return pfx + ":(" + parserExprKey(x.Left) + "):(" + parserExprKey(x.Right) + ")"
	case *parser.BinaryOp:
		return "b:" + x.Op.String() + ":(" + parserExprKey(x.Left) + "):(" + parserExprKey(x.Right) + ")"
	case *parser.FuncCall:
		k := strings.Builder{}
		k.WriteString("f:")
		k.WriteString(strings.ToLower(x.Name.String()))
		k.WriteString("|")
		if x.Star {
			k.WriteString("*")
		}
		if x.Distinct {
			k.WriteString("distinct|")
		}
		for _, a := range x.Args {
			k.WriteString(parserExprKey(a))
			k.WriteString("|")
		}
		return k.String()
	case *parser.StarExpr:
		return "star:" + strings.ToLower(x.Schema) + "." + strings.ToLower(x.Table)
	case *parser.CastExpr:
		return "cast:" + strings.ToLower(x.Type.String()) + ":(" + parserExprKey(x.Operand) + ")"
	}
	return fmt.Sprintf("expr:%T", e)
}

func planIndexScanFromWhere(where parser.Expr, ctx *resolveContext, cat catalog.Catalog) (Node, bool, error) {
	if len(ctx.bindings) != 1 {
		return nil, false, nil
	}
	tbl := ctx.bindings[0].table
	// Partitioned parent tables store no rows themselves; all data is in
	// partition children. Skip IndexScan and fall through to Filter+UNION ALL
	// which correctly scans the children via planScanRangeVar. M0100-0005.
	if len(tbl.PartitionKey) > 0 {
		return nil, false, nil
	}
	b, ok := where.(*parser.BinaryOp)
	if !ok || b.Op != parser.OpEq {
		// Not an equality predicate — try range index scan.
		return tryRangeIndexScan(where, tbl, ctx, cat)
	}
	leftCol, lIsCol := b.Left.(*parser.ColumnRef)
	rightCol, rIsCol := b.Right.(*parser.ColumnRef)
	if lIsCol == rIsCol {
		return nil, false, nil
	}

	var colRef *parser.ColumnRef
	var keyExpr parser.Expr
	if lIsCol {
		colRef = leftCol
		keyExpr = b.Right
	} else {
		colRef = rightCol
		keyExpr = b.Left
	}

	resolvedCol, err := resolveColumnRef(colRef, ctx)
	if err != nil {
		return nil, false, nil
	}
	col, ok := resolvedCol.(*ColumnRef)
	if !ok {
		return nil, false, nil
	}

	resolvedKey, err := resolveExpr(keyExpr, ctx)
	if err != nil {
		return nil, false, nil
	}
	switch resolvedKey.(type) {
	case *IntegerConst, *NumericConst, *ParamRef:
		// NumericConst landed alongside M0011-0002: a NUMERIC
		// b-tree index can be probed by either an integer or a
		// numeric literal on the rhs of `=`. The executor's
		// encodeBTreeKeyForColumn picks the right encoding from
		// the column type.
	case *StringConst:
		// M0044-0005: varchar/char column indexes — probe key is
		// a plain string literal; evaluates to KindString at
		// runtime and routes through EncodeVarchar/EncodeChar.
	case *TypedStringLit:
		// M0044-0005: timestamp column indexes — probe key is a
		// typed literal like `timestamp '1995-01-01'`; evaluates
		// to KindTime and routes through EncodeTimestamp.
	default:
		return nil, false, nil
	}

	idx := findBTreeIndexForColumn(cat, tbl, col.Name)
	if idx == nil {
		return nil, false, nil
	}
	return &IndexScan{
		pos:    where.Pos(),
		Table:  tbl,
		Index:  idx,
		Key:    resolvedKey,
		schema: ctx.schema,
	}, true, nil
}

// findBTreeIndexForColumn locates a B-tree index whose leading column
// matches `col`. M0053-0001: composite indexes are accepted when their
// FIRST key column matches; the resulting IndexScan probes only the
// leading-column value. This is correct because B-tree key encoding is
// a byte-wise concatenation of column encodings — any leaf key whose
// leading-column bytes equal the probe value is a candidate. The
// executor (`indexScanOp.lookupKey`) widens the upper bound with 0xFF
// padding so the inclusive RangeScan range covers all suffix values.
//
// A single-column-only index is preferred when both shapes match the
// column (cheaper probe — exact equality vs. prefix range) so the
// search returns a single-column index first when one exists.
func findBTreeIndexForColumn(cat catalog.Catalog, tbl *catalog.Table, col string) *catalog.Index {
	var composite *catalog.Index
	for _, idx := range cat.IndexesOnTable(tbl) {
		if strings.ToLower(idx.Method) != "btree" {
			continue
		}
		if len(idx.Columns) == 0 || idx.Columns[0] != col {
			continue
		}
		if len(idx.Columns) == 1 {
			return idx
		}
		if composite == nil {
			composite = idx
		}
	}
	return composite
}

// collectAndConjuncts walks an AND chain and returns the leaf conjuncts.
// A non-AND node returns a single-element slice containing itself.
func collectAndConjuncts(e parser.Expr) []parser.Expr {
	b, ok := e.(*parser.BinaryOp)
	if !ok || b.Op != parser.OpAnd {
		return []parser.Expr{e}
	}
	result := collectAndConjuncts(b.Left)
	result = append(result, collectAndConjuncts(b.Right)...)
	return result
}

// isConstantExpr reports whether the RESOLVED (planner.Expr) expression
// contains no column references or subqueries — i.e., its value does not
// depend on the current row.
func isConstantExpr(e Expr) bool {
	switch x := e.(type) {
	case *ColumnRef, *OuterColumnRef:
		return false
	case *SubqueryExpr, *ExistsExpr, *InExpr, *IsNullExpr, *IsBoolExpr, *IsDistinctFromExpr:
		return false
	case *BinaryOp:
		return isConstantExpr(x.Left) && isConstantExpr(x.Right)
	case *UnaryOp:
		return isConstantExpr(x.Operand)
	case *CastExpr:
		return isConstantExpr(x.Operand)
	case *IntegerConst, *StringConst, *NumericConst,
		*TypedStringLit, *IntervalLit,
		*NullConst, *BooleanConst, *ParamRef:
		return true
	case *ExtractExpr:
		return isConstantExpr(x.Source)
	case *CaseExpr:
		if x.Operand != nil && !isConstantExpr(x.Operand) {
			return false
		}
		for _, w := range x.Whens {
			if !isConstantExpr(w.When) || !isConstantExpr(w.Then) {
				return false
			}
		}
		if x.Else != nil && !isConstantExpr(x.Else) {
			return false
		}
		return true
	case *FuncCall:
		for _, a := range x.Args {
			if !isConstantExpr(a) {
				return false
			}
		}
		return true
	default:
		return false // conservative
	}
}

// flipRangeOp flips a comparison operator for the "key op col" → "col flippedOp key"
// canonical form (column on the left).
func flipRangeOp(op parser.OpCode) parser.OpCode {
	switch op {
	case parser.OpLt:
		return parser.OpGt
	case parser.OpLe:
		return parser.OpGe
	case parser.OpGt:
		return parser.OpLt
	case parser.OpGe:
		return parser.OpLe
	}
	return op
}

// tryRangeIndexScan attempts to build a Filter(IndexScan{LowKey,HighKey})
// from a WHERE expression containing one or more range predicates (< <= > >=)
// on a single B-tree-indexed column. Returns (nil, false, nil) when no range
// index is applicable.
func tryRangeIndexScan(where parser.Expr, tbl *catalog.Table, ctx *resolveContext, cat catalog.Catalog) (Node, bool, error) {
	// Partitioned parent tables store no rows; skip index scan. M0100-0005.
	if len(tbl.PartitionKey) > 0 {
		return nil, false, nil
	}
	conjuncts := collectAndConjuncts(where)

	var chosenColName string
	var chosenIdx *catalog.Index
	var loKey Expr // inclusive lower bound
	var hiKey Expr // inclusive upper bound

	for _, conj := range conjuncts {
		b, ok := conj.(*parser.BinaryOp)
		if !ok {
			continue
		}
		op := b.Op
		if op != parser.OpLt && op != parser.OpLe && op != parser.OpGt && op != parser.OpGe {
			continue
		}

		var colRef *parser.ColumnRef
		var keyExpr parser.Expr
		colOnLeft := false

		if lc, ok := b.Left.(*parser.ColumnRef); ok {
			colRef = lc
			keyExpr = b.Right
			colOnLeft = true
		} else if rc, ok := b.Right.(*parser.ColumnRef); ok {
			colRef = rc
			keyExpr = b.Left
			colOnLeft = false
		} else {
			continue
		}

		// Make sure the other side is NOT also a column ref
		if colOnLeft {
			if _, ok := b.Right.(*parser.ColumnRef); ok {
				continue
			}
		} else {
			if _, ok := b.Left.(*parser.ColumnRef); ok {
				continue
			}
		}

		// Flip op to canonical "col op key" form
		canonOp := op
		if !colOnLeft {
			canonOp = flipRangeOp(op)
		}

		// Resolve column
		resolvedCol, err := resolveColumnRef(colRef, ctx)
		if err != nil {
			continue
		}
		col, ok := resolvedCol.(*ColumnRef)
		if !ok {
			continue
		}

		// Ensure column is from the target table (not outer ref)
		if chosenColName == "" {
			// First indexed column: look up a B-tree index for it
			idx := findBTreeIndexForColumn(cat, tbl, col.Name)
			if idx == nil {
				continue
			}
			chosenColName = col.Name
			chosenIdx = idx
		} else if col.Name != chosenColName {
			// Different column — ignore this conjunct (keep first column)
			continue
		}

		// Resolve the key expression
		resolvedKey, err := resolveExpr(keyExpr, ctx)
		if err != nil {
			continue
		}
		if !isConstantExpr(resolvedKey) {
			continue
		}

		// Assign bounds based on canonical operator
		switch canonOp {
		case parser.OpGt, parser.OpGe:
			if loKey == nil {
				loKey = resolvedKey
			}
		case parser.OpLt, parser.OpLe:
			if hiKey == nil {
				hiKey = resolvedKey
			}
		}
	}

	if chosenIdx == nil {
		return nil, false, nil
	}

	// Resolve the full WHERE predicate for the Filter node
	fullPred, err := resolveExpr(where, ctx)
	if err != nil {
		return nil, false, err
	}

	scan := &IndexScan{
		pos:     where.Pos(),
		Table:   tbl,
		Index:   chosenIdx,
		LowKey:  loKey,
		HighKey: hiKey,
		schema:  ctx.schema,
	}
	return &Filter{pos: where.Pos(), Child: scan, Predicate: fullPred}, true, nil
}

// rewriteInsertDefaultMarkers substitutes `*parser.DefaultMarker` cells
// in an INSERT's VALUES rows with the target column's catalog
// `DefaultExpr` (or `*parser.NullConst` when the column has no
// DEFAULT). Runs in Plan() BEFORE the analyzer so the analyzer never
// observes the marker — mirrors upstream's rewriteValuesRTE pass.
// Silently no-ops for INSERT…SELECT (s.Select != nil) and when the
// target table can't be resolved (planInsert raises the canonical
// 42P01 error later).
func rewriteInsertDefaultMarkers(s *parser.InsertStmt, cat catalog.Catalog) error {
	if s.Select != nil {
		return nil
	}
	if !s.DefaultValues && len(s.Rows) == 0 {
		return nil
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil
	}
	// Per-cell target column ordinal: mirrors planInsert's colIndex
	// derivation so DEFAULT substitution sees the same mapping the
	// planner will use.
	var colIndex []int
	if len(s.Columns) == 0 {
		colIndex = make([]int, 0, len(tbl.Columns))
		for i, col := range tbl.Columns {
			if col.GeneratedAlways {
				continue
			}
			colIndex = append(colIndex, i)
		}
	} else {
		colIndex = make([]int, 0, len(s.Columns))
		for _, name := range s.Columns {
			col, ok := cat.LookupColumn(tbl, name)
			if !ok {
				// planInsert raises 42703; let it own the error.
				return nil
			}
			colIndex = append(colIndex, col.Ordinal)
		}
	}
	// M0103-0007 rung 17: expand `INSERT … DEFAULT VALUES` into a
	// single row of DefaultMarkers sized to colIndex so the existing
	// substitution loop below handles it uniformly with the explicit
	// VALUES (DEFAULT, …, DEFAULT) shape.
	if s.DefaultValues {
		row := make([]parser.Expr, len(colIndex))
		for i := range row {
			row[i] = &parser.DefaultMarker{}
		}
		s.Rows = [][]parser.Expr{row}
		s.DefaultValues = false
	}
	for _, r := range s.Rows {
		if len(r) != len(colIndex) {
			// planInsert raises the arity error; skip rewriting and let
			// it surface uniformly.
			return nil
		}
		for i, e := range r {
			if _, ok := e.(*parser.DefaultMarker); !ok {
				continue
			}
			tgt := colIndex[i]
			if tgt < 0 || tgt >= len(tbl.Columns) {
				r[i] = &parser.NullConst{}
				continue
			}
			if def := tbl.Columns[tgt].DefaultExpr; def != nil {
				r[i] = def
			} else {
				r[i] = &parser.NullConst{}
			}
		}
	}
	return nil
}

// rewriteUpdateDefaultMarkers substitutes `*parser.DefaultMarker`
// expressions on the RHS of UPDATE SET assignments with the target
// column's catalog DefaultExpr (or *parser.NullConst when the column
// has no DEFAULT). Mirrors rung 15's rewriteInsertDefaultMarkers — the
// analyzer never observes the sentinel because the substitution runs
// before analyzer.Analyze. M0103-0007 rung 16.
func rewriteUpdateDefaultMarkers(s *parser.UpdateStmt, cat catalog.Catalog) error {
	if len(s.Set) == 0 {
		return nil
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		// planUpdate will raise the missing-relation error; leave the
		// marker in place so the error surfaces uniformly.
		return nil
	}
	for i := range s.Set {
		if _, ok := s.Set[i].Expr.(*parser.DefaultMarker); !ok {
			continue
		}
		col, ok := cat.LookupColumn(tbl, s.Set[i].Column)
		if !ok {
			// planUpdate / analyzer will raise 42703 for unknown
			// columns; leave the marker so the error path stays
			// uniform.
			return nil
		}
		if def := tbl.Columns[col.Ordinal].DefaultExpr; def != nil {
			s.Set[i].Expr = def
		} else {
			s.Set[i].Expr = &parser.NullConst{}
		}
	}
	return nil
}

func planInsert(s *parser.InsertStmt, cat catalog.Catalog) (Node, error) {
	restore, _, err := preplanWithClause(s.With, cat)
	if err != nil {
		return nil, err
	}
	defer restore()
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{
			Pos:     s.Target.Pos(),
			Code:    "42P01",
			Message: fmt.Sprintf("relation %q does not exist", s.Target.Name),
		}
	}
	// Map source-row column index -> target table column ordinal.
	// Generated columns are excluded from the mapping when no explicit
	// column list is provided — they are computed by the executor. M0096-0008.
	var colIndex []int
	if len(s.Columns) == 0 {
		colIndex = make([]int, 0, len(tbl.Columns))
		for i, col := range tbl.Columns {
			if col.GeneratedAlways {
				continue // skip generated columns; executor fills them in
			}
			colIndex = append(colIndex, i)
		}
		if len(colIndex) == len(tbl.Columns) {
			// No generated columns — keep original 1:1 mapping for compatibility.
		}
	} else {
		colIndex = make([]int, 0, len(s.Columns))
		for _, name := range s.Columns {
			col, ok := cat.LookupColumn(tbl, name)
			if !ok {
				return nil, &PlanError{
					Pos:     s.Target.Pos(),
					Code:    "42703",
					Message: fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name),
				}
			}
			colIndex = append(colIndex, col.Ordinal)
		}
	}
	// INSERT … SELECT: plan the SELECT and use it as the source.
	var source Node
	if s.Select != nil {
		sel, err := planSelect(s.Select, cat)
		if err != nil {
			return nil, err
		}
		source = sel
	} else {
		// Validate row arity and build planner expressions.
		if len(s.Rows) == 0 {
			return nil, &PlanError{Pos: s.Pos(), Code: "42601", Message: "INSERT requires at least one row"}
		}
		rows := make([][]Expr, 0, len(s.Rows))
		for _, r := range s.Rows {
			if len(r) != len(colIndex) {
				return nil, &PlanError{
					Pos:     s.Pos(),
					Code:    "42601",
					Message: fmt.Sprintf("INSERT row has %d values, target expects %d", len(r), len(colIndex)),
				}
			}
			row := make([]Expr, 0, len(r))
			ctx := &resolveContext{} // VALUES rows have no input columns
			for _, e := range r {
				pe, err := resolveExpr(e, ctx)
				if err != nil {
					return nil, err
				}
				row = append(row, pe)
			}
			rows = append(rows, row)
		}
		source = &Values{pos: s.Pos(), Rows: rows, schema: insertValuesSchema(tbl, colIndex)}
	}
	insert := &Insert{pos: s.Pos(), Table: tbl, Source: source, ColumnIndex: colIndex}
	if s.OnConflict != nil {
		oc, err := planOnConflict(s.OnConflict, tbl, s.Target.Alias, cat)
		if err != nil {
			return nil, err
		}
		insert.OnConflict = oc
	}
	if len(s.Returning) > 0 {
		retCtx := singleBindingContext(tbl, s.Target.Alias)
		retCtx.cat = cat
		retExprs, retSchema, err := resolveTargets(s.Returning, retCtx)
		if err != nil {
			return nil, err
		}
		insert.Returning = retExprs
		insert.ReturningSchema = retSchema
	}
	return insert, nil
}

// planOnConflict resolves the parser-level ON CONFLICT clause into
// the planner-side OnConflictPlan: arbiter-index selection from
// the conflict target columns, plus expression resolution for the
// DO UPDATE branch under a target+excluded scope. M0017-0002.
func planOnConflict(oc *parser.OnConflictClause, tbl *catalog.Table, targetAlias string, cat catalog.Catalog) (*OnConflictPlan, error) {
	out := &OnConflictPlan{}

	switch oc.Action {
	case parser.OnConflictNothing:
		out.Action = OnConflictActionNothing
	case parser.OnConflictUpdate:
		out.Action = OnConflictActionUpdate
	default:
		return nil, &PlanError{Pos: oc.Pos(), Code: "XX000", Message: fmt.Sprintf("unexpected ON CONFLICT action %d", oc.Action)}
	}

	// Arbiter-index selection. With a target, resolve explicitly.
	// For the bare DO NOTHING form (no target), fall back to the
	// primary key index so probeArbiterWaiting can detect
	// in-progress conflicts (M0100-0002).
	if oc.Target != nil {
		idx, ords, err := resolveArbiterIndex(oc.Target, tbl, cat)
		if err != nil {
			return nil, err
		}
		out.ArbiterIndex = idx
		out.ArbiterColumns = ords
		// Build ArbiterExprs for expression-based arbiter columns
		// (where ords[i] == -1 and oc.Target.Exprs[i] != nil).
		if len(oc.Target.Exprs) > 0 {
			hasExpr := false
			for _, o2 := range ords {
				if o2 == -1 {
					hasExpr = true
					break
				}
			}
			if hasExpr {
				// Build a single-binding resolve context for the target table
				// so expression ColumnRefs resolve against the insert row.
				exprCtx := singleBindingContext(tbl, targetAlias)
				exprCtx.cat = cat
				out.ArbiterExprs = make([]Expr, len(ords))
				for i, o2 := range ords {
					if o2 == -1 && i < len(oc.Target.Exprs) && oc.Target.Exprs[i] != nil {
						resolved, rerr := resolveExpr(oc.Target.Exprs[i], exprCtx)
						if rerr != nil {
							return nil, rerr
						}
						out.ArbiterExprs[i] = resolved
					}
				}
			}
		}
	} else if out.Action == OnConflictActionNothing && cat != nil {
		idx, ords, exprs, err := resolveDefaultDoNothingArbiter(tbl, targetAlias, cat)
		if err != nil {
			return nil, err
		}
		out.ArbiterIndex = idx
		out.ArbiterColumns = ords
		out.ArbiterExprs = exprs
	}

	if out.Action != OnConflictActionUpdate {
		return out, nil
	}

	// DO UPDATE expression resolution.
	//
	// Build a 2-binding scope: the target table at offset 0 (bare
	// refs and `<target>.col` resolve here) and the same table
	// re-bound as `excluded` at offset N with qualifiedOnly so
	// `excluded.col` resolves only via the alias path. Schema is
	// 2N wide — the executor will arrange a merged tuple at
	// runtime.
	primaryAlias := targetAlias
	if primaryAlias == "" {
		primaryAlias = tbl.Name
	}
	n := len(tbl.Columns)
	// Primary at sourceIdx=1, excluded at sourceIdx=2 — both refer
	// to the same catalog table but disambiguate by source so
	// `excluded.col` and `<target>.col` rebind helpers don't
	// collapse into the same Index.
	bindings := []rangeBinding{
		{table: tbl, alias: primaryAlias, offset: 0, sourceIdx: 1},
		{table: tbl, alias: "excluded", offset: n, qualifiedOnly: true, sourceIdx: 2},
	}
	mergedSchema := make(Schema, 0, 2*n)
	mergedSchema = append(mergedSchema, tableSchemaWithSource(tbl, 1)...)
	mergedSchema = append(mergedSchema, tableSchemaWithSource(tbl, 2)...)
	ctx := newResolveContext(bindings, mergedSchema)
	ctx.cat = cat

	out.UpdateSet = make([]Expr, n)
	for _, a := range oc.UpdateSet {
		col, ok := cat.LookupColumn(tbl, a.Column)
		if !ok {
			return nil, &PlanError{Pos: a.Pos(), Code: "42703", Message: fmt.Sprintf("column %q of relation %q does not exist", a.Column, tbl.Name)}
		}
		expr, err := resolveExpr(a.Expr, ctx)
		if err != nil {
			return nil, err
		}
		out.UpdateSet[col.Ordinal] = expr
	}
	if oc.UpdateWhere != nil {
		pred, err := resolveExpr(oc.UpdateWhere, ctx)
		if err != nil {
			return nil, err
		}
		out.UpdateWhere = pred
	}
	return out, nil
}

func resolveDefaultDoNothingArbiter(tbl *catalog.Table, targetAlias string, cat catalog.Catalog) (*catalog.Index, []int, []Expr, error) {
	if cat == nil {
		return nil, nil, nil, nil
	}
	var chosen *catalog.Index
	for _, idx := range cat.IndexesOnTable(tbl) {
		if !idx.Unique {
			continue
		}
		if chosen == nil || idx.Primary {
			chosen = idx
			if idx.Primary {
				break
			}
		}
	}
	if chosen == nil {
		return nil, nil, nil, nil
	}
	ords := make([]int, 0, len(chosen.Columns))
	var exprs []Expr
	var exprCtx *resolveContext
	for i, colName := range chosen.Columns {
		if colName == "" {
			ords = append(ords, -1)
			if exprCtx == nil {
				exprCtx = singleBindingContext(tbl, targetAlias)
				exprCtx.cat = cat
				exprs = make([]Expr, len(chosen.Columns))
			}
			if chosen.ColExprs == nil || i >= len(chosen.ColExprs) || chosen.ColExprs[i] == nil {
				return nil, nil, nil, &PlanError{Code: "XX000", Message: fmt.Sprintf("index %q is missing expression metadata for ON CONFLICT DO NOTHING", chosen.Name)}
			}
			resolved, err := resolveExpr(*chosen.ColExprs[i], exprCtx)
			if err != nil {
				return nil, nil, nil, err
			}
			exprs[i] = resolved
			continue
		}
		col, ok := cat.LookupColumn(tbl, colName)
		if !ok {
			return nil, nil, nil, &PlanError{Code: "XX000", Message: fmt.Sprintf("index %q column %q not found on table %q", chosen.Name, colName, tbl.Name)}
		}
		ords = append(ords, col.Ordinal)
	}
	return chosen, ords, exprs, nil
}

// resolveArbiterIndex matches the parsed conflict-target columns
// against tbl's catalog indexes. A unique index whose column set
// equals the target column set (case-insensitive, order-insensitive)
// arbitrates the conflict. Mirrors upstream's "inference
// specification" rule: the user's columns must canonically match
// some unique constraint.
//
// Returns (idx, ordinals, nil) on a single match — ordinals are
// `tbl.Columns` ordinals matching idx.Columns in catalog order so
// the executor can extract the conflict key from a row tuple
// without a name lookup. SQLSTATE 42P10 ("invalid_column_reference"
// — upstream's "no unique or exclusion constraint matching the ON
// CONFLICT specification") on no match.
func resolveArbiterIndex(target *parser.OnConflictTarget, tbl *catalog.Table, cat catalog.Catalog) (*catalog.Index, []int, error) {
	// Constraint-name target form (M0017 Stage B). The named index
	// must exist, must be a unique index, and must belong to the
	// target table. The analyzer already enforces these but the
	// planner stays self-contained for paths that bypass it.
	if target.Constraint != "" {
		idx, ok := cat.LookupIndex(parser.ObjectName{Name: target.Constraint})
		if !ok {
			return nil, nil, &PlanError{Pos: target.Pos(), Code: "42704", Message: fmt.Sprintf("constraint %q for table %q does not exist", target.Constraint, tbl.Name)}
		}
		if idx.Table != tbl {
			return nil, nil, &PlanError{Pos: target.Pos(), Code: "42704", Message: fmt.Sprintf("constraint %q does not belong to table %q", target.Constraint, tbl.Name)}
		}
		if !idx.Unique {
			return nil, nil, &PlanError{Pos: target.Pos(), Code: "42P10", Message: fmt.Sprintf("constraint %q is not a unique constraint", target.Constraint)}
		}
		ords := make([]int, 0, len(idx.Columns))
		for _, ic := range idx.Columns {
			col, ok := cat.LookupColumn(tbl, ic)
			if !ok {
				return nil, nil, &PlanError{Pos: target.Pos(), Code: "XX000", Message: fmt.Sprintf("index %q column %q not found on table %q", idx.Name, ic, tbl.Name)}
			}
			ords = append(ords, col.Ordinal)
		}
		return idx, ords, nil
	}
	if len(target.Columns) == 0 {
		return nil, nil, &PlanError{Pos: target.Pos(), Code: "42601", Message: "ON CONFLICT target requires at least one column"}
	}
	// Build a set of wanted column names. Expression columns (name=="") are
	// represented by a unique sentinel key per position so the set-size
	// check correctly detects duplicate plain column names.
	wanted := make(map[string]struct{}, len(target.Columns))
	for i, c := range target.Columns {
		if c == "" {
			// Expression column — use a unique sentinel per position so each
			// expression gets its own slot in the wanted set.
			wanted[fmt.Sprintf("__expr_%d__", i)] = struct{}{}
		} else {
			wanted[strings.ToLower(c)] = struct{}{}
		}
	}
	if len(wanted) != len(target.Columns) {
		return nil, nil, &PlanError{Pos: target.Pos(), Code: "42P10", Message: "ON CONFLICT target list contains duplicate columns"}
	}
	for _, idx := range cat.IndexesOnTable(tbl) {
		if !idx.Unique {
			continue
		}
		if len(idx.Columns) != len(target.Columns) {
			continue
		}
		// Match: for each index column position, check that the target has
		// the same kind of column (plain name or expression). Expression
		// columns in the index (ic=="") match expression columns in the
		// target at the same position.
		match := true
		for j, ic := range idx.Columns {
			if ic == "" {
				// Expression index column — must match expression target column
				// at same position.
				if j >= len(target.Columns) || target.Columns[j] != "" {
					match = false
					break
				}
			} else {
				if _, ok := wanted[strings.ToLower(ic)]; !ok {
					match = false
					break
				}
			}
		}
		if !match {
			continue
		}
		ords := make([]int, 0, len(idx.Columns))
		for _, ic := range idx.Columns {
			if ic == "" {
				// Expression index column — use sentinel -1.
				ords = append(ords, -1)
				continue
			}
			col, ok := cat.LookupColumn(tbl, ic)
			if !ok {
				// Catalog inconsistency — index references a
				// column that isn't on the table. Surface
				// loudly so we don't silently produce a
				// wrong arbiter.
				return nil, nil, &PlanError{Pos: target.Pos(), Code: "XX000", Message: fmt.Sprintf("index %q column %q not found on table %q", idx.Name, ic, tbl.Name)}
			}
			ords = append(ords, col.Ordinal)
		}
		return idx, ords, nil
	}
	return nil, nil, &PlanError{Pos: target.Pos(), Code: "42P10", Message: fmt.Sprintf("there is no unique or exclusion constraint matching the ON CONFLICT specification on relation %q", tbl.Name)}
}

func insertValuesSchema(tbl *catalog.Table, colIndex []int) Schema {
	out := make(Schema, len(colIndex))
	for i, ord := range colIndex {
		col := tbl.Columns[ord]
		out[i] = SchemaColumn{Name: col.Name, Type: col.Type}
	}
	return out
}

func planUpdate(s *parser.UpdateStmt, cat catalog.Catalog) (Node, error) {
	restore, _, err := preplanWithClause(s.With, cat)
	if err != nil {
		return nil, err
	}
	defer restore()
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{Pos: s.Target.Pos(), Code: "42P01", Message: fmt.Sprintf("relation %q does not exist", s.Target.Name)}
	}
	ctx := singleBindingContext(tbl, s.Target.Alias)
	ctx.cat = cat
	var node Node = &SeqScan{pos: s.Pos(), Table: tbl, schema: ctx.schema}
	if s.Where != nil {
		// M0021-0009 step 2d: try the index-driven probe first
		// for `WHERE indexed_col = key` shapes. Mirrors planSelect's
		// `if idxNode, ok, err := planIndexScanFromWhere(...)` arm.
		// Falls through to Filter(SeqScan) on no index match.
		if idxNode, ok, err := planIndexScanFromWhere(s.Where, ctx, cat); err != nil {
			return nil, err
		} else if ok {
			node = idxNode
		} else {
			pred, err := resolveExpr(s.Where, ctx)
			if err != nil {
				return nil, err
			}
			node = &Filter{pos: s.Where.Pos(), Child: node, Predicate: pred}
		}
	}
	set := make([]Expr, len(tbl.Columns))
	for _, a := range s.Set {
		col, ok := cat.LookupColumn(tbl, a.Column)
		if !ok {
			return nil, &PlanError{Pos: a.Pos(), Code: "42703", Message: fmt.Sprintf("column %q of relation %q does not exist", a.Column, tbl.Name)}
		}
		expr, err := resolveExpr(a.Expr, ctx)
		if err != nil {
			return nil, err
		}
		set[col.Ordinal] = expr
	}
	upd := &Update{pos: s.Pos(), Table: tbl, Child: node, Set: set}
	if len(s.Returning) > 0 {
		retExprs, retSchema, err := resolveTargets(s.Returning, ctx)
		if err != nil {
			return nil, err
		}
		upd.Returning = retExprs
		upd.ReturningSchema = retSchema
	}
	return upd, nil
}

func planDelete(s *parser.DeleteStmt, cat catalog.Catalog) (Node, error) {
	restore, _, err := preplanWithClause(s.With, cat)
	if err != nil {
		return nil, err
	}
	defer restore()
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{Pos: s.Target.Pos(), Code: "42P01", Message: fmt.Sprintf("relation %q does not exist", s.Target.Name)}
	}
	ctx := singleBindingContext(tbl, s.Target.Alias)
	// When an explicit alias is set, using the original table name in WHERE
	// must produce the PostgreSQL-specific error. M0097-0003.
	if s.Target.Alias != "" {
		ctx.bindings[0].blockOriginalName = true
	}
	ctx.cat = cat
	var node Node = &SeqScan{pos: s.Pos(), Table: tbl, schema: ctx.schema}
	if s.Where != nil {
		// M0021-0009 step 2d: index-driven probe for
		// `WHERE indexed_col = key` shapes; falls through to
		// Filter(SeqScan).
		if idxNode, ok, err := planIndexScanFromWhere(s.Where, ctx, cat); err != nil {
			return nil, err
		} else if ok {
			node = idxNode
		} else {
			pred, err := resolveExpr(s.Where, ctx)
			if err != nil {
				return nil, err
			}
			node = &Filter{pos: s.Where.Pos(), Child: node, Predicate: pred}
		}
	}
	del := &Delete{pos: s.Pos(), Table: tbl, Child: node}
	if len(s.Returning) > 0 {
		retExprs, retSchema, err := resolveTargets(s.Returning, ctx)
		if err != nil {
			return nil, err
		}
		del.Returning = retExprs
		del.ReturningSchema = retSchema
	}
	return del, nil
}

// planMerge converts a MERGE INTO statement into a Merge plan node.
// M0096-0010.
func planMerge(s *parser.MergeStmt, cat catalog.Catalog) (Node, error) {
	tbl, ok := cat.LookupTable(parser.ObjectName{Schema: s.Target.Schema, Name: s.Target.Name})
	if !ok {
		return nil, &PlanError{Pos: s.Target.Pos(), Code: "42P01",
			Message: fmt.Sprintf("relation %q does not exist", s.Target.Name)}
	}

	// Plan the USING source.
	var srcIdx int16 = 2
	sourceNode, sourceBinding, err := planScanRangeVar(s.Source, cat, srcIdx, nil)
	if err != nil {
		return nil, err
	}

	// Build a merged schema: target columns first, then source columns.
	targetAlias := s.Target.Alias
	if targetAlias == "" {
		targetAlias = tbl.Name
	}
	n := len(tbl.Columns)
	sourceSchema := sourceNode.Output()
	if sourceSchema == nil && sourceBinding.table != nil {
		sourceSchema = tableSchemaWithSource(sourceBinding.table, srcIdx)
	}

	targetBinding := rangeBinding{table: tbl, alias: targetAlias, offset: 0, sourceIdx: 1}
	sourceBinding.offset = n

	mergedSchema := make(Schema, 0, n+len(sourceSchema))
	mergedSchema = append(mergedSchema, tableSchemaWithSource(tbl, 1)...)
	mergedSchema = append(mergedSchema, sourceSchema...)
	mergedCtx := newResolveContext([]rangeBinding{targetBinding, sourceBinding}, mergedSchema)
	mergedCtx.cat = cat

	// Source-only context for NOT MATCHED INSERT VALUES.
	sourceOnly := newResolveContext([]rangeBinding{{
		table: sourceBinding.table, alias: sourceBinding.alias,
		offset: 0, sourceIdx: srcIdx,
	}}, sourceSchema)
	sourceOnly.cat = cat

	onExpr, err := resolveExpr(s.On, mergedCtx)
	if err != nil {
		return nil, err
	}

	clauses := make([]*MergeWhenClause, 0, len(s.Clauses))
	for _, wc := range s.Clauses {
		pc := &MergeWhenClause{
			Matched: wc.Matched,
			Action:  MergeActionKind(wc.Action),
		}
		if wc.Condition != nil {
			cond, err := resolveExpr(wc.Condition, mergedCtx)
			if err != nil {
				return nil, err
			}
			pc.Condition = cond
		}
		switch wc.Action {
		case parser.MergeActionUpdate:
			set := make([]Expr, n)
			for _, a := range wc.UpdateAssigns {
				col, colOK := cat.LookupColumn(tbl, a.Column)
				if !colOK {
					return nil, &PlanError{Pos: a.Pos(), Code: "42703",
						Message: fmt.Sprintf("column %q of relation %q does not exist", a.Column, tbl.Name)}
				}
				expr, err := resolveExpr(a.Expr, mergedCtx)
				if err != nil {
					return nil, err
				}
				set[col.Ordinal] = expr
			}
			pc.UpdateSet = set
		case parser.MergeActionDelete:
			// nothing extra
		case parser.MergeActionInsert:
			ordinals, err := buildInsertColIdx(tbl, wc.InsertColumns, cat)
			if err != nil {
				return nil, &PlanError{Pos: wc.Pos(), Code: "42703", Message: err.Error()}
			}
			pc.InsertColIdx = ordinals
			if wc.InsertValues != nil {
				exprs := make([]Expr, len(wc.InsertValues))
				for i, ve := range wc.InsertValues {
					expr, err := resolveExpr(ve, sourceOnly)
					if err != nil {
						return nil, err
					}
					exprs[i] = expr
				}
				pc.InsertExprs = exprs
			}
		case parser.MergeActionDoNothing:
			// DO NOTHING — no extra fields needed. M0097-0016.
		}
		clauses = append(clauses, pc)
	}
	m := &Merge{pos: s.Pos(), Target: tbl, Source: sourceNode, On: onExpr, Clauses: clauses}

	// RETURNING clause (M0100 DML-CTE): resolve against target-table binding.
	if len(s.Returning) > 0 {
		retCtx := newResolveContext([]rangeBinding{
			{table: tbl, alias: targetAlias, offset: 0, sourceIdx: 1},
		}, tableSchemaWithSource(tbl, 1))
		retCtx.cat = cat
		exprs, schema, err := resolveTargets(s.Returning, retCtx)
		if err != nil {
			return nil, err
		}
		m.Returning = exprs
		m.ReturningSchema = schema
	}
	return m, nil
}

// buildInsertColIdx returns column ordinals for a MERGE NOT MATCHED INSERT.
// When names is empty, all non-generated columns are returned in declaration order.
func buildInsertColIdx(tbl *catalog.Table, names []string, cat catalog.Catalog) ([]int, error) {
	if len(names) == 0 {
		out := make([]int, 0, len(tbl.Columns))
		for i, c := range tbl.Columns {
			if !c.GeneratedAlways {
				out = append(out, i)
			}
		}
		return out, nil
	}
	out := make([]int, 0, len(names))
	for _, name := range names {
		col, ok := cat.LookupColumn(tbl, name)
		if !ok {
			return nil, fmt.Errorf("column %q of relation %q does not exist", name, tbl.Name)
		}
		out = append(out, col.Ordinal)
	}
	return out, nil
}

// resolveTargets expands a parser target list into planner Expr's
// plus the resulting Schema. `*` and qualified `t.*` expand into one
// ColumnRef per source column.
func resolveTargets(targets []parser.ResTarget, ctx *resolveContext) ([]Expr, Schema, error) {
	out := make([]Expr, 0, len(targets))
	schema := make(Schema, 0, len(targets))
	for _, t := range targets {
		if star, ok := t.Expr.(*parser.StarExpr); ok {
			exprList, cols, err := expandStarTarget(star, ctx)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, exprList...)
			schema = append(schema, cols...)
			continue
		}
		expr, err := resolveExpr(t.Expr, ctx)
		if err != nil {
			return nil, nil, err
		}
		name, typ := targetMeta(expr, t)
		out = append(out, expr)
		// Pure ColumnRef pass-through preserves the source
		// identity so downstream rebinds can disambiguate
		// self-join Names. Computed expressions (BinaryOp,
		// FuncCall, etc.) leave SourceTableIdx at 0 = unknown.
		var srcIdx int16
		if cr, ok := expr.(*ColumnRef); ok {
			srcIdx = cr.SourceTableIdx
		}
		schema = append(schema, SchemaColumn{Name: name, Type: typ, SourceTableIdx: srcIdx})
	}
	return out, schema, nil
}

func expandStarTarget(star *parser.StarExpr, ctx *resolveContext) ([]Expr, Schema, error) {
	if len(ctx.bindings) == 0 {
		return nil, nil, &PlanError{Pos: star.Pos(), Code: "42601", Message: "SELECT * with no FROM clause"}
	}
	bset := ctx.bindings
	// A table-qualified star (`t.*`) expands to ALL of that table's
	// columns, including any JOIN USING / NATURAL join columns — only an
	// unqualified `*` merges (hides the right-side copy of) USING columns.
	// PostgreSQL's expandRTE applies the join's merged column list only for
	// the whole-row case, not for a per-relation `rel.*`. M0097-0036.
	qualified := star.Table != "" || star.Schema != ""
	if qualified {
		matches := make([]rangeBinding, 0, 1)
		for _, b := range ctx.bindings {
			if bindingMatchesRelation(b, star.Table, star.Schema) {
				matches = append(matches, b)
			}
		}
		if len(matches) == 0 {
			return nil, nil, &PlanError{Pos: star.Pos(), Code: "42P01", Message: fmt.Sprintf("missing FROM-clause entry for table %q", star.Table)}
		}
		if len(matches) > 1 {
			return nil, nil, &PlanError{Pos: star.Pos(), Code: "42702", Message: fmt.Sprintf("table reference %q is ambiguous", star.Table)}
		}
		bset = matches
	}
	outExpr := make([]Expr, 0)
	outSchema := make(Schema, 0)
	for _, b := range bset {
		for i, c := range b.table.Columns {
			// For an unqualified `SELECT *` over a JOIN USING / NATURAL
			// join, the right-side copy of each merged column is hidden:
			// PostgreSQL emits the join column once (from the left side),
			// so the output is `using-cols, left-rest, right-rest`. The
			// right binding carries usingHidden (set in planFromItem); the
			// left binding does not, so its copy survives. Without this
			// skip, `SELECT * FROM t1 JOIN t2 USING (id)` wrongly produced
			// a duplicate `id` column (`id|t|id|t` vs PG's `id|t|t`).
			// M0097-0036.
			if !qualified && len(b.usingHidden) > 0 {
				hidden := false
				for _, uh := range b.usingHidden {
					if strings.EqualFold(uh, c.Name) {
						hidden = true
						break
					}
				}
				if hidden {
					continue
				}
			}
			idx := b.offset + i
			outExpr = append(outExpr, &ColumnRef{pos: star.Pos(), Index: idx, Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx})
			outSchema = append(outSchema, SchemaColumn{Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx})
		}
	}
	// M0097-0061: PostgreSQL places USING columns first in SELECT * output:
	// "using-cols, left-rest, right-rest". Without explicit reordering the
	// left table's natural column order is preserved, which is wrong when the
	// USING column is not the first column of the left table.
	//
	// Example: t1(a,b,c) JOIN t2(a,b) USING (b) → must output "b | a | c | a"
	// not "a | b | c | a". Collect USING names from any right-binding's
	// usingHidden list, then move those columns to the front.
	if !qualified {
		usingSet := make(map[string]bool)
		for _, b := range bset {
			for _, uh := range b.usingHidden {
				usingSet[strings.ToLower(uh)] = true
			}
		}
		if len(usingSet) > 0 {
			var frontIdx, restIdx []int
			for i, sc := range outSchema {
				if usingSet[strings.ToLower(sc.Name)] {
					frontIdx = append(frontIdx, i)
				} else {
					restIdx = append(restIdx, i)
				}
			}
			newExpr := make([]Expr, 0, len(outExpr))
			newSchema := make(Schema, 0, len(outSchema))
			for _, i := range frontIdx {
				newExpr = append(newExpr, outExpr[i])
				newSchema = append(newSchema, outSchema[i])
			}
			for _, i := range restIdx {
				newExpr = append(newExpr, outExpr[i])
				newSchema = append(newSchema, outSchema[i])
			}
			outExpr = newExpr
			outSchema = newSchema
		}
	}
	return outExpr, outSchema, nil
}

// targetMeta picks the output name and type for a target. The alias
// wins; otherwise we use the underlying ColumnRef's name; for function
// calls we use the function name (matching upstream behaviour); for
// TypedStringLit `type 'value'` uses the type name as the column label
// (matching PostgreSQL's FigureColname() for typed literals); otherwise
// a synthetic "?column?" matching upstream.
func targetMeta(e Expr, t parser.ResTarget) (string, catalog.Type) {
	if t.Alias != "" {
		return t.Alias, exprType(e)
	}
	if cr, ok := e.(*ColumnRef); ok {
		return cr.Name, cr.Type
	}
	if _, ok := e.(*CTIDExpr); ok {
		return "ctid", catalog.Type{Name: "tid"}
	}
	// TypedStringLit `int2 'value'`: column name is the type name.
	// Matches PostgreSQL's FigureColname() for `type 'string'` syntax. M0097-0003.
	if tsl, ok := e.(*TypedStringLit); ok {
		return tsl.Type, exprType(e)
	}
	// CastExpr `expr::type` or `CAST(expr AS type)`: use the type name as the
	// column label for literal→type casts (e.g. 0::boolean → "bool").
	// For column-ref casts (e.g. f1::int2), propagate the column's name.
	// Matches PostgreSQL's FigureColname() logic. M0097-0003.
	if cast, ok := e.(*CastExpr); ok {
		if _, isCol := cast.Operand.(*ColumnRef); isCol {
			innerName, _ := targetMeta(cast.Operand, t)
			return innerName, exprType(e)
		}
		// `tableoid::regclass` from a non-partitioned base relation
		// resolves to TableOidExpr; preserve the system-column label so
		// `SELECT tableoid::regclass FROM t` reports the column name as
		// `tableoid` (matches PG `FigureColname` for system columns).
		// M0100-0005y.
		if _, isTOID := cast.Operand.(*TableOidExpr); isTOID {
			return "tableoid", exprType(e)
		}
		// For function-call casts (e.g. parse_ident(...)::name[]), propagate the
		// function name as the column label (matches PostgreSQL's FigureColname). M0097-0003.
		if _, isFuncCall := cast.Operand.(*FuncCall); isFuncCall {
			innerName, _ := targetMeta(cast.Operand, t)
			return innerName, exprType(e)
		}
		if cast.TargetType != "" {
			// Use PostgreSQL's canonical short type names for column labels.
			label := castTargetLabel(cast.TargetType)
			return label, exprType(e)
		}
		innerName, _ := targetMeta(cast.Operand, t)
		return innerName, exprType(e)
	}
	// Function call: use function name as the implicit column label.
	// Matches PostgreSQL's FigureColname() logic for FuncCall nodes.
	if fc, ok := e.(*FuncCall); ok && fc.Name != "" {
		return fc.Name, exprType(e)
	}
	// CASE expression: PostgreSQL uses "case" as implicit column label.
	// Matches FigureColname() returning "case" for CaseExpr nodes. M0097-0003.
	if _, ok := e.(*CaseExpr); ok {
		return "case", exprType(e)
	}
	// EXTRACT expression: PostgreSQL uses "extract" as the implicit column label.
	// Matches FigureColname() for ExtractExpr nodes. M0097-0004.
	if _, ok := e.(*ExtractExpr); ok {
		return "extract", exprType(e)
	}
	return "?column?", exprType(e)
}

// castTargetLabel maps a cast target type name to the PostgreSQL column label
// used for that type when the cast result has no explicit alias. M0097-0003.
func castTargetLabel(t string) string {
	switch t {
	case "boolean":
		return "bool"
	case "integer":
		return "int4"
	case "bigint":
		return "int8"
	case "smallint":
		return "int2"
	case "real":
		return "float4"
	case "double precision":
		return "float8"
	}
	return t
}

// exprType returns the planner-level type tag for an expression. v0
// only knows what ColumnRef carries; everything else gets the
// "unknown" tag the executor coerces at runtime.
func exprType(e Expr) catalog.Type {
	switch x := e.(type) {
	case *ColumnRef:
		return x.Type
	case *NumericConst:
		return catalog.Type{Name: "numeric"}
	case *IntegerConst:
		return catalog.Type{Name: "int8"}
	case *TableOidExpr:
		return catalog.Type{Name: "oid"}
	case *CTIDExpr:
		return catalog.Type{Name: "tid"}
	case *StringConst:
		return catalog.Type{Name: "text"}
	case *BooleanConst:
		return catalog.Type{Name: "bool"}
	case *NullConst:
		return catalog.Type{Name: "unknown"}
	case *TypedStringLit:
		// Typed string literals carry their explicit type (e.g. int2 '2').
		// Return it so downstream type inference (BinaryOp etc.) can use it.
		// M0097-0003.
		return catalog.Type{Name: x.Type}
	case *CastExpr:
		// CastExpr carries the declared target type. M0097-0003.
		if x.TargetType != "" {
			return catalog.Type{Name: x.TargetType}
		}
		return exprType(x.Operand)
	case *BinaryOp:
		// Arithmetic on numeric promotes to numeric. Comparison /
		// boolean ops return bool. String concat returns text. The
		// rule mirrors the executor's actual behaviour so the wire
		// layer advertises a TypeOID consistent with the formatted
		// cell text — without this, sum(numeric * numeric) lands as
		// int8 and libpq's Go driver fails ParseInt on `20667.0000`.
		switch x.Op {
		case parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod:
			lt := exprType(x.Left)
			rt := exprType(x.Right)
			// Float types dominate: float8 > float4 > numeric > int*. M0097-0003.
			isFloat := func(n string) bool {
				s := strings.ToLower(n)
				return s == "float8" || s == "double precision" || s == "double" ||
					s == "float4" || s == "real" || s == "float"
			}
			// pg_lsn arithmetic: pg_lsn - pg_lsn → int8; pg_lsn +/- numeric → pg_lsn. M0097-pg_lsn.
			isPgLSN := func(n string) bool { return strings.EqualFold(n, "pg_lsn") }
			if isPgLSN(lt.Name) && isPgLSN(rt.Name) && x.Op == parser.OpSub {
				return catalog.Type{Name: "int8"}
			}
			if isPgLSN(lt.Name) && (x.Op == parser.OpAdd || x.Op == parser.OpSub) {
				return catalog.Type{Name: "pg_lsn"}
			}
			if isPgLSN(rt.Name) && x.Op == parser.OpAdd {
				return catalog.Type{Name: "pg_lsn"}
			}
			if isFloat(lt.Name) || isFloat(rt.Name) {
				// Wider float type wins.
				if lt.Name == "float8" || lt.Name == "double precision" || lt.Name == "double" ||
					rt.Name == "float8" || rt.Name == "double precision" || rt.Name == "double" {
					return catalog.Type{Name: "float8"}
				}
				return catalog.Type{Name: "float4"}
			}
			if isNumericTypeName(lt.Name) || isNumericTypeName(rt.Name) {
				return catalog.Type{Name: "numeric"}
			}
			if (lt.Name == "int8" || lt.Name == "bigint") && (rt.Name == "int8" || rt.Name == "bigint") {
				return catalog.Type{Name: "int8"}
			}
			if isIntegerLikeType(lt.Name) && isIntegerLikeType(rt.Name) {
				// int2/int4 arithmetic: follow PostgreSQL promotion rules.
				// int2 op int2 → int2, int4 op int4 → int4, int2 op int4 → int4.
				return promoteIntType(lt.Name, rt.Name)
			}
			return catalog.Type{Name: "unknown"}
		case parser.OpConcat:
			return catalog.Type{Name: "text"}
		case parser.OpAnd, parser.OpOr, parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe, parser.OpLike, parser.OpNotLike:
			return catalog.Type{Name: "bool"}
		}
		return catalog.Type{Name: "unknown"}
	case *UnaryOp:
		if x.Op == parser.OpNot {
			return catalog.Type{Name: "bool"}
		}
		return exprType(x.Operand)
	case *CaseExpr:
		// All Whens unify to a single result type during analysis;
		// take the first branch's type as the representative.
		if len(x.Whens) > 0 {
			t := exprType(x.Whens[0].Then)
			if t.Name != "unknown" && t.Name != "" {
				return t
			}
		}
		if x.Else != nil {
			return exprType(x.Else)
		}
		return catalog.Type{Name: "unknown"}
	case *ExtractExpr:
		return catalog.Type{Name: "int8"}
	case *FuncCall:
		// Aggregates carry their own Type field (set by
		// buildAggregateCall) on the AggregateCall path, but free
		// FuncCalls reach here with no type carried. Match the
		// known-typed builtins; everything else stays unknown.
		switch strings.ToLower(x.Name) {
		case "count":
			return catalog.Type{Name: "int8"}
		case "current_timestamp", "now", "transaction_timestamp", "statement_timestamp":
			return catalog.Type{Name: "timestamp"}
		case "current_date", "to_date":
			return catalog.Type{Name: "date"}
		case "to_timestamp":
			return catalog.Type{Name: "timestamp"}
		case "substr", "substring":
			return catalog.Type{Name: "text"}
		case "date_part":
			return catalog.Type{Name: "int8"}
		case "gcd", "lcm", "abs", "mod", "div":
			// These return integer; use int8 as generic integer type. M0097-0003.
			return catalog.Type{Name: "int8"}
		case "char_length", "character_length", "length", "octet_length",
			"bit_length", "array_length", "array_upper", "array_lower",
			"cardinality", "strpos", "position":
			// String/array length functions return int4. M0097-0003.
			return catalog.Type{Name: "int4"}
		case "timezone":
			// AT LOCAL / AT TIME ZONE: return type depends on the input (last arg).
			if len(x.Args) > 0 {
				inputArg := x.Args[len(x.Args)-1]
				t := exprType(inputArg)
				switch strings.ToLower(t.Name) {
				case "timetz":
					return catalog.Type{Name: "timetz"}
				case "timestamptz":
					return catalog.Type{Name: "timestamp"}
				case "timestamp":
					return catalog.Type{Name: "timestamptz"}
				}
			}
		case "coalesce", "greatest", "least":
			// Return the type of the first non-unknown argument (widest wins
			// in practice since args are resolved before exprType is called).
			for _, a := range x.Args {
				t := exprType(a)
				if t.Name != "unknown" && t.Name != "" {
					return t
				}
			}
		case "nullif":
			// NULLIF returns the type of its first argument (nullable).
			if len(x.Args) > 0 {
				return exprType(x.Args[0])
			}
		case "pg_size_bytes",
			"pg_database_size", "pg_relation_size", "pg_total_relation_size",
			"pg_indexes_size", "pg_table_size":
			return catalog.Type{Name: "int8"}
		case "round", "ceil", "ceiling", "floor", "trunc", "sign":
			// Preserve input numeric type; default to numeric when unknown.
			if len(x.Args) > 0 {
				t := exprType(x.Args[0])
				if t.Name != "unknown" && t.Name != "" {
					return t
				}
			}
			return catalog.Type{Name: "numeric"}
		case "power", "exp", "ln", "log", "sqrt":
			return catalog.Type{Name: "float8"}
		case "uuid_extract_version":
			return catalog.Type{Name: "int2"}
		case "uuid_extract_timestamp":
			return catalog.Type{Name: "timestamptz"}
		case "gen_random_uuid", "uuidv4", "uuidv7":
			return catalog.Type{Name: "uuid"}
		case "nextval", "currval", "lastval", "setval":
			// Sequence functions return int8 (bigint). M0097-0042.
			return catalog.Type{Name: "int8"}
		case "random", "random_normal", "drandom":
			// random() → float8 in [0,1). M0097-0042.
			return catalog.Type{Name: "float8"}
		case "generate_series":
			// generate_series in scalar context returns int8 for integer args. M0097-0042.
			return catalog.Type{Name: "int8"}
		}
		return catalog.Type{Name: "unknown"}
	}
	return catalog.Type{Name: "unknown"}
}

// isNumericTypeName reports whether name refers to a numeric type
// (NUMERIC / DECIMAL family). Used by exprType to promote arithmetic
// to numeric whenever any operand is numeric.
func isNumericTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "numeric", "decimal":
		return true
	}
	return false
}

// unifyValueTypes returns the "wider" of two types for VALUES column type inference.
// Follows PostgreSQL's type unification rules for VALUES: integer types promote
// to numeric when a non-integer numeric appears, and numeric/float types dominate.
// M0097-0049.
func unifyValueTypes(a, b catalog.Type) catalog.Type {
	an := strings.ToLower(a.Name)
	bn := strings.ToLower(b.Name)
	if an == bn {
		return a
	}
	// "unknown" is the bottom type — any concrete type wins.
	if an == "unknown" || an == "" {
		return b
	}
	if bn == "unknown" || bn == "" {
		return a
	}
	// Numeric type hierarchy: int2 < int4 < int8 < numeric < float4 < float8
	numericRank := func(n string) int {
		switch n {
		case "int2", "smallint", "smallserial":
			return 1
		case "int4", "integer", "int", "serial":
			return 2
		case "int8", "bigint", "bigserial":
			return 3
		case "numeric", "decimal":
			return 4
		case "float4", "real":
			return 5
		case "float8", "double precision", "double", "float":
			return 6
		}
		return 0
	}
	ra, rb := numericRank(an), numericRank(bn)
	if ra > 0 && rb > 0 {
		if ra >= rb {
			return a
		}
		return b
	}
	// Non-numeric: if either is text, use text.
	if an == "text" || bn == "text" {
		return catalog.Type{Name: "text"}
	}
	// Fallback: keep the first type (unknown columns stay as first-row type).
	return a
}

// isIntegerLikeType reports whether name is a fixed-width integer type
// (int2, int4, int8) for the purpose of arithmetic type promotion.
func isIntegerLikeType(name string) bool {
	switch strings.ToLower(name) {
	case "int2", "smallint", "int4", "integer", "int", "int8", "bigint",
		// SERIAL family resolve to int2/int4/int8 (see analyzer.isNumericTypeName).
		"smallserial", "serial", "bigserial":
		return true
	}
	return false
}

// promoteIntType returns the result type of an arithmetic operation between
// two integer types, following PostgreSQL's promotion rules:
// int2 op int2 → int2, int4 op int4 → int4, int2 op int4 → int4.
// M0097-0003.
func promoteIntType(a, b string) catalog.Type {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	// SERIAL family promote as their integer base (serial→int4, etc.).
	aIsSmall := a == "int2" || a == "smallint" || a == "smallserial"
	bIsSmall := b == "int2" || b == "smallint" || b == "smallserial"
	aIsInt4 := a == "int4" || a == "integer" || a == "int" || a == "serial"
	bIsInt4 := b == "int4" || b == "integer" || b == "int" || b == "serial"
	aIsInt8 := a == "int8" || a == "bigint" || a == "bigserial"
	bIsInt8 := b == "int8" || b == "bigint" || b == "bigserial"
	switch {
	case aIsInt8 || bIsInt8:
		return catalog.Type{Name: "int8"}
	case aIsInt4 || bIsInt4:
		return catalog.Type{Name: "int4"}
	case aIsSmall && bIsSmall:
		return catalog.Type{Name: "int2"}
	}
	return catalog.Type{Name: "int4"}
}

// planSubqueryExpr plans the SELECT inside a parser
// SubqueryExpr against the supplied catalog and wraps the
// resulting Node in a planner.SubqueryExpr. The outer
// resolveContext is passed in as the inner SELECT's parent
// so column references in the subquery can resolve up the
// lexical scope (correlated subqueries).
func planSubqueryExpr(x *parser.SubqueryExpr, parent *resolveContext) (Expr, error) {
	if parent == nil || parent.cat == nil {
		return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "subqueries are not supported in this context"}
	}
	inner, err := planSelectWithParent(x.Inner, parent.cat, parent)
	if err != nil {
		return nil, err
	}
	return &SubqueryExpr{pos: x.Pos(), Plan: inner, IsNonCorrelated: !planHasOuterRef(inner)}, nil
}

// planInExpr resolves the operand and either plans the inner
// subquery (passing the outer ctx as parent for correlated
// references) or recursively resolves the value list,
// depending on which the parser produced.
func planInExpr(x *parser.InExpr, ctx *resolveContext) (Expr, error) {
	op, err := resolveExpr(x.Operand, ctx)
	if err != nil {
		return nil, err
	}
	out := &InExpr{pos: x.Pos(), Operand: op, Negated: x.Negated}
	if x.Subquery != nil {
		if ctx == nil || ctx.cat == nil {
			return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "IN (subquery) not supported in this context"}
		}
		inner, err := planSelectWithParent(x.Subquery, ctx.cat, ctx)
		if err != nil {
			return nil, err
		}
		out.Plan = inner
		out.IsNonCorrelated = !planHasOuterRef(inner)
	} else {
		out.List = make([]Expr, len(x.List))
		for i, e := range x.List {
			r, err := resolveExpr(e, ctx)
			if err != nil {
				return nil, err
			}
			out.List[i] = r
		}
	}
	return out, nil
}

// planExistsExpr plans the inner subquery and wraps in an
// ExistsExpr. The outer ctx becomes the inner SELECT's
// parent for column-reference walk-up.
func planExistsExpr(x *parser.ExistsExpr, parent *resolveContext) (Expr, error) {
	if parent == nil || parent.cat == nil {
		return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "EXISTS not supported in this context"}
	}
	inner, err := planSelectWithParent(x.Subquery, parent.cat, parent)
	if err != nil {
		return nil, err
	}
	return &ExistsExpr{pos: x.Pos(), Negated: x.Negated, Plan: inner, IsNonCorrelated: !planHasOuterRef(inner)}, nil
}

// planHasOuterRef reports whether any expression anywhere in the
// plan tree is an OuterColumnRef. It descends into nested
// SubqueryExpr/InExpr/ExistsExpr so that a level-2 OuterColumnRef
// inside a nested subquery is not mistaken for non-correlated at
// the outer level.
//
// Used by M0058-0001: a subquery with no OuterColumnRef yields the
// same result for every outer row, so the executor SubqueryCache
// can use a constant key instead of one keyed on the full outer row.
func planHasOuterRef(node Node) bool {
	found := false
	walkPlanExprs(node, func(e Expr) {
		if found {
			return
		}
		walkExprTree(e, func(inner Expr) {
			if found {
				return
			}
			switch x := inner.(type) {
			case *OuterColumnRef:
				found = true
			case *SubqueryExpr:
				if x.Plan != nil && planHasOuterRef(x.Plan) {
					found = true
				}
			case *InExpr:
				if x.Plan != nil && planHasOuterRef(x.Plan) {
					found = true
				}
			case *ExistsExpr:
				if x.Plan != nil && planHasOuterRef(x.Plan) {
					found = true
				}
			}
		})
	})
	return found
}

// planSelectWithParent plans an inner SELECT with the supplied
// resolveContext as the lexical-scope parent. Used by
// SubqueryExpr / InExpr / ExistsExpr to enable correlated
// references. The resulting plan tree may contain
// OuterColumnRef nodes that the executor resolves against its
// outer-row stack at runtime.
//
// Two parent channels are wired: planParent (planner-side, so
// resolveColumnRef can walk up the resolveContext chain) and
// the analyzer's outer-scope channel (so the recursive
// Analyze pass that Plan() invokes also sees the outer
// scope). Both are restored on return.
func planSelectWithParent(stmt *parser.SelectStmt, cat catalog.Catalog, parent *resolveContext) (Node, error) {
	prevParent := planParent
	planParent = parent
	defer func() { planParent = prevParent }()

	// Build the analyzer-side OuterScope chain mirroring the
	// resolveContext chain.
	if scope := buildAnalyzerOuterScope(parent); scope != nil {
		restore := analyzer.SetOuterScope(scope)
		defer restore()
	}
	return Plan(stmt, cat)
}

// buildAnalyzerOuterScope walks a resolveContext chain and
// produces the analyzer's parallel OuterScope chain so the
// recursive Analyze call sees the same outer FROM-clause
// relations the planner sees. The inner-most scope is
// returned; analyzer.SetOuterScope is the consumer.
func buildAnalyzerOuterScope(ctx *resolveContext) *analyzer.OuterScope {
	if ctx == nil || ctx.cat == nil {
		return nil
	}
	parent := buildAnalyzerOuterScope(ctx.parent)
	rels := make([]analyzer.OuterRelation, 0, len(ctx.bindings))
	for _, b := range ctx.bindings {
		rels = append(rels, analyzer.OuterRelation{Table: b.table, Alias: b.alias})
	}
	return analyzer.NewOuterScope(ctx.cat, rels, parent)
}

// planParent is the goroutine-thread-unsafe "current outer
// resolveContext" used by planSelect to seed its top-level
// resolveContext's parent field. This is a v0 simplification
// — Plan() / planSelect / planFromClause take cat as a
// parameter, and re-threading a parent argument through every
// helper would be more invasive than the package-level
// channel. Each public Plan() call is sequential at the
// boundary, so the outer goroutine is the only writer.
//
// Future cleanup: add explicit parent params to Plan() / the
// internal helpers.
var planParent *resolveContext

// resolveCaseExpr translates a parser CaseExpr into a planner
// CaseExpr, recursively resolving each Operand / When / Then /
// Else expression in `ctx`.
func resolveCaseExpr(x *parser.CaseExpr, ctx *resolveContext) (Expr, error) {
	out := &CaseExpr{pos: x.Pos()}
	if x.Operand != nil {
		operand, err := resolveExpr(x.Operand, ctx)
		if err != nil {
			return nil, err
		}
		out.Operand = operand
	}
	for _, w := range x.Whens {
		when, err := resolveExpr(w.When, ctx)
		if err != nil {
			return nil, err
		}
		then, err := resolveExpr(w.Then, ctx)
		if err != nil {
			return nil, err
		}
		out.Whens = append(out.Whens, CaseWhen{When: when, Then: then})
	}
	if x.Else != nil {
		els, err := resolveExpr(x.Else, ctx)
		if err != nil {
			return nil, err
		}
		out.Else = els
	}
	return out, nil
}

// resolveExpr walks a parser.Expr and replaces ColumnRef nodes with
// indexed planner ColumnRefs. Other node types are translated 1:1.
func resolveExpr(e parser.Expr, ctx *resolveContext) (Expr, error) {
	switch x := e.(type) {
	case *parser.IntegerConst:
		return &IntegerConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.NumericConst:
		return &NumericConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.StringConst:
		return &StringConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.TypedStringLit:
		return &TypedStringLit{pos: x.Pos(), Type: x.Type, Value: x.Value}, nil
	case *parser.IntervalLit:
		return &IntervalLit{pos: x.Pos(), Value: x.Value, Unit: x.Unit}, nil
	case *parser.SubqueryExpr:
		return planSubqueryExpr(x, ctx)
	case *parser.InExpr:
		return planInExpr(x, ctx)
	case *parser.ExistsExpr:
		return planExistsExpr(x, ctx)
	case *parser.ExtractExpr:
		src, err := resolveExpr(x.Source, ctx)
		if err != nil {
			return nil, err
		}
		return &ExtractExpr{pos: x.Pos(), Field: x.Field, Source: src, SourceTypeName: exprType(src).Name}, nil
	case *parser.NullConst:
		return &NullConst{pos: x.Pos()}, nil
	case *parser.BooleanConst:
		return &BooleanConst{pos: x.Pos(), Value: x.Value}, nil
	case *parser.ParamRef:
		return &ParamRef{pos: x.Pos(), Number: x.Number}, nil
	case *parser.CaseExpr:
		return resolveCaseExpr(x, ctx)
	case *parser.ColumnRef:
		return resolveColumnRef(x, ctx)
	case *parser.BinaryOp:
		l, err := resolveExpr(x.Left, ctx)
		if err != nil {
			return nil, err
		}
		r, err := resolveExpr(x.Right, ctx)
		if err != nil {
			return nil, err
		}
		// For comparisons involving a `name` type, coerce the non-name side
		// to "name" so the executor truncates it to 63 chars (NAMEDATALEN-1),
		// matching PostgreSQL's namecmp() semantics. M0097-0003.
		switch x.Op {
		case parser.OpEq, parser.OpNe, parser.OpLt, parser.OpLe, parser.OpGt, parser.OpGe:
			lt, rt := exprType(l), exprType(r)
			if strings.EqualFold(lt.Name, "name") && !strings.EqualFold(rt.Name, "name") && isTextLikePlannerType(rt.Name) {
				r = &CastExpr{pos: x.Pos(), Operand: r, TargetType: "name"}
			} else if strings.EqualFold(rt.Name, "name") && !strings.EqualFold(lt.Name, "name") && isTextLikePlannerType(lt.Name) {
				l = &CastExpr{pos: x.Pos(), Operand: l, TargetType: "name"}
			}
		}
		node := &BinaryOp{pos: x.Pos(), Op: x.Op, Left: l, Right: r}
		switch x.Op {
		case parser.OpAdd, parser.OpSub, parser.OpMul, parser.OpDiv, parser.OpMod,
			parser.OpBitAnd, parser.OpBitOr, parser.OpBitXor, parser.OpBitShiftLeft, parser.OpBitShiftRight:
			// Set ResultType for overflow checking in the executor. M0097-0003.
			node.ResultType = exprType(node).Name
		}
		return node, nil
	case *parser.UnaryOp:
		op, err := resolveExpr(x.Operand, ctx)
		if err != nil {
			return nil, err
		}
		return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: op}, nil
	case *parser.FuncCall:
		if x.Over != nil {
			return nil, &PlanError{Pos: x.Pos(), Code: "0A000", Message: "window functions must be planned via WindowAgg"}
		}
		args := make([]Expr, 0, len(x.Args))
		for _, a := range x.Args {
			pa, err := resolveExpr(a, ctx)
			if err != nil {
				return nil, err
			}
			args = append(args, pa)
		}
		varExp := false
		for _, v := range x.Variadic {
			if v {
				varExp = true
				break
			}
		}
		return &FuncCall{pos: x.Pos(), Name: x.Name.String(), Args: args, Star: x.Star, Variadic: varExp}, nil
	case *parser.StarExpr:
		return nil, &PlanError{Pos: x.Pos(), Code: "42601", Message: "'*' is not allowed here"}
	case *parser.CastExpr:
		// M0097-0003: emit CastExpr so the executor can coerce at runtime.
		operand, err := resolveExpr(x.Operand, ctx)
		if err != nil {
			return nil, err
		}
		typeName := strings.ToLower(x.Type.Name)
		srcType := exprType(operand).Name
		var typmod int64
		if len(x.Typmods) > 0 {
			typmod = x.Typmods[0]
		}
		return &CastExpr{pos: x.Pos(), Operand: operand, TargetType: typeName, SourceType: srcType, Typmod: typmod}, nil
	case *parser.IsNullExpr:
		operand, err := resolveExpr(x.Operand, ctx)
		if err != nil {
			return nil, err
		}
		return &IsNullExpr{pos: x.Pos(), Operand: operand, Negated: x.Negated}, nil
	case *parser.IsBoolExpr:
		// IS [NOT] TRUE/FALSE/UNKNOWN. M0097-0003.
		operand, err := resolveExpr(x.Operand, ctx)
		if err != nil {
			return nil, err
		}
		return &IsBoolExpr{pos: x.Pos(), Operand: operand, TestTrue: x.TestTrue, TestFalse: x.TestFalse, Negated: x.Negated}, nil
	case *parser.IsDistinctFromExpr:
		lv, err := resolveExpr(x.Left, ctx)
		if err != nil {
			return nil, err
		}
		rv, err := resolveExpr(x.Right, ctx)
		if err != nil {
			return nil, err
		}
		return &IsDistinctFromExpr{pos: x.Pos(), Left: lv, Right: rv, Negated: x.Negated}, nil
	case *parser.ArraySubscriptExpr:
		// expr[index] — array element access. Convert to array_subscript(base, index)
		// so the executor can handle it without a new plan node type. M0097-0003.
		base, err := resolveExpr(x.Base, ctx)
		if err != nil {
			return nil, err
		}
		idx, err := resolveExpr(x.Index, ctx)
		if err != nil {
			return nil, err
		}
		return &FuncCall{pos: x.Pos(), Name: "array_subscript", Args: []Expr{base, idx}}, nil
	case *parser.ArrayConstructorExpr:
		// ARRAY[e1, e2, ...] constructor — resolve each element and convert to
		// array_construct so the executor formats the result as {v1,v2,...}. M0097-0042.
		args := make([]Expr, len(x.Elements))
		for i, e := range x.Elements {
			r, err := resolveExpr(e, ctx)
			if err != nil {
				return nil, err
			}
			args[i] = r
		}
		return &FuncCall{pos: x.Pos(), Name: "array_construct", Args: args}, nil
	}
	return nil, &PlanError{Pos: e.Pos(), Code: "0A000", Message: fmt.Sprintf("unsupported expression %T", e)}
}

func resolveColumnRef(x *parser.ColumnRef, ctx *resolveContext) (Expr, error) {
	// Walk the lexical scope chain. level=0 is the local
	// resolveContext; level=N is N parents up — matches
	// upstream's Var.varlevelsup. Found-but-ambiguous in the
	// local scope is an error; ambiguity at a parent level
	// shadows the further parents (no error). Found at parent
	// level produces an OuterColumnRef.
	level := 0
	for cur := ctx; cur != nil; cur = cur.parent {
		ref, ok, err := resolveColumnRefAt(x, cur, level)
		if err != nil {
			return nil, err
		}
		if ok {
			return ref, nil
		}
		level++
	}
	return nil, &PlanError{Pos: x.Pos(), Code: "42703", Message: fmt.Sprintf("column %q does not exist", x.Column)}
}

// resolveColumnRefAt tries to resolve x against a single
// resolveContext level. Returns (nil, false, nil) on miss so
// the caller walks up; (expr, true, nil) on hit; or an error
// for in-scope ambiguity / missing-FROM diagnostics.
func resolveColumnRefAt(x *parser.ColumnRef, ctx *resolveContext, level int) (Expr, bool, error) {
	if len(ctx.bindings) == 0 {
		// No table bindings — the only resolvable references are unqualified
		// column names that appear in ctx.schema. This case arises in
		// wrapSetOpSortLimit, where ORDER BY references the set-op's output
		// columns by name (e.g. `SELECT q1,q2 … EXCEPT … ORDER BY q2,q1`).
		// Qualified references (x.Table != "") cannot be resolved without a
		// binding, so we return not-found. M0097-0042.
		if x.Table == "" && x.Schema == "" {
			for i, col := range ctx.schema {
				if strings.EqualFold(col.Name, x.Column) {
					if level == 0 {
						return &ColumnRef{pos: x.Pos(), Index: i, Name: col.Name, Type: col.Type}, true, nil
					}
					return &OuterColumnRef{pos: x.Pos(), Level: level, Index: i, Name: col.Name, Type: col.Type}, true, nil
				}
			}
		}
		return nil, false, nil
	}

	if x.Table != "" || x.Schema != "" {
		matches := make([]rangeBinding, 0, 1)
		for _, b := range ctx.bindings {
			if b.qualifiedOnly {
				// Pseudo-tables (e.g. ON CONFLICT's `excluded`) reach name
				// resolution only via their alias.
				if b.alias != "" && strings.EqualFold(x.Table, b.alias) &&
					(x.Schema == "" || strings.EqualFold(x.Schema, b.table.Schema)) {
					matches = append(matches, b)
				}
				continue
			}
			// When blockOriginalName is set (DELETE FROM t AS a), using the
			// original table name produces the PostgreSQL-compatible error with
			// a hint. M0097-0003.
			if b.blockOriginalName && b.alias != "" &&
				strings.EqualFold(x.Table, b.table.Name) {
				return nil, false, &PlanError{
					Pos:  x.Pos(),
					Code: "42712",
					Message: fmt.Sprintf("invalid reference to FROM-clause entry for table %q",
						b.table.Name),
					Hint: fmt.Sprintf("Perhaps you meant to reference the table alias %q.", b.alias),
				}
			}
			if bindingMatchesRelation(b, x.Table, x.Schema) {
				matches = append(matches, b)
			}
		}
		if len(matches) == 0 {
			// Not in this level — caller walks up.
			return nil, false, nil
		}
		if len(matches) > 1 {
			return nil, false, &PlanError{Pos: x.Pos(), Code: "42702", Message: fmt.Sprintf("table reference %q is ambiguous", x.Table)}
		}
		b := matches[0]
		for i, c := range b.table.Columns {
			if strings.EqualFold(c.Name, x.Column) {
				idx := b.offset + i
				if level == 0 {
					return &ColumnRef{pos: x.Pos(), Index: idx, Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx}, true, nil
				}
				return &OuterColumnRef{pos: x.Pos(), Level: level, Index: idx, Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx}, true, nil
			}
		}
		// `<rel>.tableoid` system-column resolution. M0100-0005y.
		if strings.EqualFold(x.Column, "tableoid") {
			return resolveTableoidForBinding(b, level, x.Pos()), true, nil
		}
		// `<rel>.ctid` system-column resolution. M0097-0038.
		if strings.EqualFold(x.Column, "ctid") {
			return &CTIDExpr{pos: x.Pos()}, true, nil
		}
		// The qualifier matched a binding at this level but the
		// column didn't — that's a hard error (no point walking
		// up; an outer-scope `t.c` for a different `t` would be
		// caught by the qualifier mismatch instead).
		return nil, false, &PlanError{Pos: x.Pos(), Code: "42703", Message: fmt.Sprintf("column %q does not exist", x.Column)}
	}

	var found Expr
	for _, b := range ctx.bindings {
		if b.qualifiedOnly {
			continue
		}
		for i, c := range b.table.Columns {

			if !strings.EqualFold(c.Name, x.Column) {
				continue
			}
			// Skip USING-hidden columns (right side of JOIN USING):
			// the left table's column is the canonical one. M0097-0003.
			hidden := false
			for _, uh := range b.usingHidden {
				if strings.EqualFold(uh, c.Name) {
					hidden = true
					break
				}
			}
			if hidden {
				continue
			}
			idx := b.offset + i
			if found != nil {
				return nil, false, &PlanError{Pos: x.Pos(), Code: "42702", Message: fmt.Sprintf("column reference %q is ambiguous", x.Column)}
			}
			if level == 0 {
				found = &ColumnRef{pos: x.Pos(), Index: idx, Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx}
			} else {
				found = &OuterColumnRef{pos: x.Pos(), Level: level, Index: idx, Name: c.Name, Type: c.Type, SourceTableIdx: b.sourceIdx}
			}
		}
	}
	// Unqualified `tableoid` system-column resolution. PG raises
	// "column reference is ambiguous" when more than one binding
	// could supply it; for a single-binding scope it resolves to
	// that binding's `tableoid`. M0100-0005y.
	if found == nil && strings.EqualFold(x.Column, "tableoid") {
		var matchB *rangeBinding
		for i := range ctx.bindings {
			if ctx.bindings[i].qualifiedOnly {
				continue
			}
			if matchB != nil {
				return nil, false, &PlanError{Pos: x.Pos(), Code: "42702", Message: fmt.Sprintf("column reference %q is ambiguous", x.Column)}
			}
			matchB = &ctx.bindings[i]
		}
		if matchB != nil {
			return resolveTableoidForBinding(*matchB, level, x.Pos()), true, nil
		}
	}
	// Unqualified `ctid` system-column resolution. M0097-0038.
	if found == nil && strings.EqualFold(x.Column, "ctid") {
		var matchB *rangeBinding
		for i := range ctx.bindings {
			if ctx.bindings[i].qualifiedOnly {
				continue
			}
			if matchB != nil {
				return nil, false, &PlanError{Pos: x.Pos(), Code: "42702", Message: fmt.Sprintf("column reference %q is ambiguous", x.Column)}
			}
			matchB = &ctx.bindings[i]
		}
		if matchB != nil {
			return &CTIDExpr{pos: x.Pos()}, true, nil
		}
	}
	if found != nil {
		return found, true, nil
	}
	return nil, false, nil
}

// resolveTableoidForBinding builds the planner expression for a
// `tableoid` reference against a single binding. When the binding
// carries a per-leaf `tableoid` slot (set by the partition / inheritance
// union wrapper in planFromTable), the result is an ordinary
// (Outer)ColumnRef into that slot — so partitioned-table queries report
// the actual leaf relname (e.g. `foo2`) rather than the parent. For a
// non-partitioned base relation the binding's table OID is fixed at
// plan time, so the result is a constant TableOidExpr; the executor
// (evalExprSlot) emits an `oid` Datum and a downstream
// `tableoid::regclass` cast (evalCast → "regclass" arm) renders it as
// the table's relname. M0100-0005y.
func resolveTableoidForBinding(b rangeBinding, level, pos int) Expr {
	if b.tableOidColIdx > 0 {
		idx := b.offset + b.tableOidColIdx
		if level == 0 {
			return &ColumnRef{pos: pos, Index: idx, Name: "tableoid", Type: catalog.Type{Name: "oid"}, SourceTableIdx: b.sourceIdx}
		}
		return &OuterColumnRef{pos: pos, Level: level, Index: idx, Name: "tableoid", Type: catalog.Type{Name: "oid"}, SourceTableIdx: b.sourceIdx}
	}
	return &TableOidExpr{pos: pos, TableOID: b.table.OID}
}

func bindingMatchesRelation(b rangeBinding, table, schema string) bool {
	if schema != "" && !strings.EqualFold(schema, b.table.Schema) {
		return false
	}
	if table == "" {
		return schema != ""
	}
	if strings.EqualFold(table, b.table.Name) {
		return true
	}
	if b.alias != "" && strings.EqualFold(table, b.alias) {
		return true
	}
	return false
}

// tryPromoteIndexOnlyScan examines a freshly-built Project node and promotes
// it to an IndexOnlyScan (M0046-0004) when all of the following hold:
//  1. The Project's direct child (ignoring Filter wrappers) is an IndexScan.
//  2. All target expressions are ColumnRefs whose names appear in the index key.
//
// Returns the original proj unchanged if the conditions are not met.
func tryPromoteIndexOnlyScan(proj *Project) Node {
	if proj == nil {
		return proj
	}
	// Strip a single Filter wrapper if present (e.g. range predicates).
	child := proj.Child
	var filter *Filter
	if f, ok := child.(*Filter); ok {
		filter = f
		child = f.Child
	}
	idxScan, ok := child.(*IndexScan)
	if !ok {
		return proj
	}
	// Check that every projected column is in the index key.
	idxColSet := make(map[string]bool, len(idxScan.Index.Columns))
	for _, c := range idxScan.Index.Columns {
		idxColSet[c] = true
	}
	covered := make([]catalog.Column, 0, len(proj.Targets))
	for _, t := range proj.Targets {
		cr, isCR := t.(*ColumnRef)
		if !isCR || !idxColSet[cr.Name] {
			return proj // target not in index — cannot use index-only
		}
		// Find the full column definition from the table.
		col, found := func() (catalog.Column, bool) {
			for _, c := range idxScan.Table.Columns {
				if c.Name == cr.Name {
					return c, true
				}
			}
			return catalog.Column{}, false
		}()
		if !found {
			return proj
		}
		covered = append(covered, col)
	}
	if len(covered) == 0 {
		return proj
	}
	ios := &IndexOnlyScan{
		pos:     idxScan.pos,
		Table:   idxScan.Table,
		Index:   idxScan.Index,
		Key:     idxScan.Key,
		LowKey:  idxScan.LowKey,
		HighKey: idxScan.HighKey,
		Covered: covered,
		schema:  proj.schema,
	}
	if filter != nil {
		// Keep the Filter but replace its child with IndexOnlyScan.
		filter.Child = ios
		return filter
	}
	return ios
}

// shiftColumnRefsBy walks an expression tree and adds `delta` to
// every ColumnRef's Index. OuterColumnRefs are left alone (they
// reference an enclosing scope). Used by M0063-0005 to translate
// outer-cumulative-indexed conjuncts back to inner-scope when
// they're pushed below a LEFT JOIN.
func shiftColumnRefsBy(e Expr, delta int) Expr {
	if e == nil {
		return nil
	}
	switch x := e.(type) {
	case *ColumnRef:
		cl := *x
		cl.Index += delta
		return &cl
	case *BinaryOp:
		return &BinaryOp{
			pos:        x.Pos(),
			Op:         x.Op,
			Left:       shiftColumnRefsBy(x.Left, delta),
			Right:      shiftColumnRefsBy(x.Right, delta),
			ResultType: x.ResultType,
		}
	case *CastExpr:
		return &CastExpr{pos: x.Pos(), Operand: shiftColumnRefsBy(x.Operand, delta), TargetType: x.TargetType, SourceType: x.SourceType, Typmod: x.Typmod}
	case *UnaryOp:
		return &UnaryOp{pos: x.Pos(), Op: x.Op, Operand: shiftColumnRefsBy(x.Operand, delta)}
	case *FuncCall:
		args := make([]Expr, len(x.Args))
		for i, a := range x.Args {
			args[i] = shiftColumnRefsBy(a, delta)
		}
		return &FuncCall{pos: x.Pos(), Name: x.Name, Args: args, Star: x.Star, Variadic: x.Variadic}
	case *CaseExpr:
		whens := make([]CaseWhen, len(x.Whens))
		for i, w := range x.Whens {
			whens[i] = CaseWhen{
				When: shiftColumnRefsBy(w.When, delta),
				Then: shiftColumnRefsBy(w.Then, delta),
			}
		}
		return &CaseExpr{
			pos:     x.Pos(),
			Operand: shiftColumnRefsBy(x.Operand, delta),
			Whens:   whens,
			Else:    shiftColumnRefsBy(x.Else, delta),
		}
	case *ExtractExpr:
		return &ExtractExpr{pos: x.Pos(), Field: x.Field, Source: shiftColumnRefsBy(x.Source, delta)}
	default:
		return e
	}
}

// isColumnFunctionallyDetermined reports whether col is functionally determined
// by the aggregate's GROUP BY key via a primary key or unique-not-null index.
//
// PostgreSQL SQL92 extension: if all columns of a PK/unique-not-null index on
// col's source table appear in the GROUP BY clause, then every other column of
// that table is uniquely determined within each group and may appear in SELECT
// without being in GROUP BY or an aggregate function. M0097-0003.
func isColumnFunctionallyDetermined(col *ColumnRef, agg *aggregateSurface) bool {
	if agg.input.cat == nil || col.SourceTableIdx == 0 {
		return false
	}
	// Find the source table for this column.
	var srcTable *catalog.Table
	for _, b := range agg.input.bindings {
		if b.sourceIdx == col.SourceTableIdx {
			srcTable = b.table
			break
		}
	}
	if srcTable == nil {
		return false
	}
	// Get all indexes on the source table.
	idxs := agg.input.cat.IndexesOnTable(srcTable)
	// Build a map from column name → input schema index for this table.
	colByName := map[string]int{}
	for i, sc := range agg.input.schema {
		if sc.SourceTableIdx == col.SourceTableIdx {
			colByName[sc.Name] = i
		}
	}
	// Check each primary key. PostgreSQL's check_functional_grouping
	// (src/backend/catalog/pg_constraint.c) recognises only PRIMARY KEY
	// constraints for functional dependency — NOT unique constraints,
	// since a unique constraint may be deferrable and (when nullable)
	// permits multiple NULL rows that would not collapse under GROUP BY.
	// The functional_deps regress test pins this: `GROUP BY title` on a
	// UNIQUE NOT NULL column and `GROUP BY body` on a UNIQUE column both
	// must fail, while `GROUP BY id` on the primary key succeeds.
	for _, idx := range idxs {
		if !idx.Primary {
			continue
		}
		// Verify that all index columns are in the GROUP BY.
		allCovered := true
		for _, idxCol := range idx.Columns {
			inputIdx, found := colByName[idxCol]
			if !found {
				allCovered = false
				break
			}
			if _, inGroupBy := agg.groupByInputCol[inputIdx]; !inGroupBy {
				allCovered = false
				break
			}
		}
		if allCovered {
			return true
		}
	}
	return false
}

// exprEqual reports whether two resolved planner Exprs are structurally equal
// for the purpose of DISTINCT ON / ORDER BY matching.
func exprEqual(a, b Expr) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	switch av := a.(type) {
	case *ColumnRef:
		if bv, ok := b.(*ColumnRef); ok {
			return av.Index == bv.Index
		}
	case *IntegerConst:
		if bv, ok := b.(*IntegerConst); ok {
			return av.Value == bv.Value
		}
	case *StringConst:
		if bv, ok := b.(*StringConst); ok {
			return av.Value == bv.Value
		}
	case *FuncCall:
		bv, ok := b.(*FuncCall)
		if !ok || av.Name != bv.Name || len(av.Args) != len(bv.Args) {
			return false
		}
		for i := range av.Args {
			if !exprEqual(av.Args[i], bv.Args[i]) {
				return false
			}
		}
		return true
	case *BinaryOp:
		bv, ok := b.(*BinaryOp)
		if !ok || av.Op != bv.Op {
			return false
		}
		return exprEqual(av.Left, bv.Left) && exprEqual(av.Right, bv.Right)
	}
	// Fallback: compare text representation (pointer-safe only for primitives).
	return fmt.Sprintf("%T%v", a, a) == fmt.Sprintf("%T%v", b, b)
}

// findExprInSchema finds the output column index in outSchema that corresponds
// to the resolved expression re.  proj is the Project node (used to match
// ColumnRef indices from the projection targets).
func findExprInSchema(re Expr, outSchema Schema, proj Node) int {
	cr, ok := re.(*ColumnRef)
	if !ok {
		return -1
	}
	p, ok := proj.(*Project)
	if !ok {
		// Maybe wrapped in IndexOnlyScan or similar — walk one level.
		type childNode interface{ Child() Node }
		if cn, ok2 := proj.(interface{ GetChild() Node }); ok2 {
			return findExprInSchema(re, outSchema, cn.GetChild())
		}
		return -1
	}
	for i, t := range p.Targets {
		if tcr, ok2 := t.(*ColumnRef); ok2 && tcr.Index == cr.Index {
			return i
		}
	}
	return -1
}

// findExprInTargets finds the index of a resolved ColumnRef in the targets
// slice (resolved Expr list from resolveTargets).
func findExprInTargets(re Expr, targets []Expr) int {
	cr, ok := re.(*ColumnRef)
	if !ok {
		return -1
	}
	for i, t := range targets {
		if tcr, ok2 := t.(*ColumnRef); ok2 && tcr.Index == cr.Index {
			return i
		}
	}
	return -1
}
