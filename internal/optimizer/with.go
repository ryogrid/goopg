package optimizer

// Non-recursive CTE planning (M0016-0002). Builds on the parser
// AST + analyzer scope rules from M0016-0001.
//
// Strategy: inline-substitute per consumer. Each FROM-clause
// reference to a CTE name returns a freshly-cloned plan of that
// CTE's body. Multiple consumers in the same statement each get
// their own copy of the plan tree — correctness-first; the
// "materialise once, feed many" optimisation lands later under
// M0016-0004.
//
// See docs/design/0016-0002-nonrecursive-cte-planner-executor.md.

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// plannedCTE is the planner-side record for one CTE: its planned
// body Node, the synthetic *catalog.Table that downstream
// rangeBinding code needs, and the body's output Schema (with
// alias-renamed column names if the CTE's `(col, ...)` list was
// specified).
type plannedCTE struct {
	name   string
	body   Node
	schema Schema
	table  *catalog.Table
	isDML  bool // data-modifying CTE; body is an INSERT/UPDATE/DELETE/MERGE plan
	// refs counts the CTEScan nodes planned against this entry, i.e. how
	// many times the statement references this CTE. Incremented at the
	// single consumption site (planScanRangeVar), so sublink-subquery
	// references are counted too. Final once the whole statement is
	// planned, which is why pushQualsThroughSingleRefCTEs reads it only
	// from Plan()'s tail. M0125-0035 CTE-body arm.
	refs int
	// inlineEligible marks a plain non-recursive SELECT body — the only
	// kind whose Child a qual may descend into when refs==1. DML bodies
	// (side effects run once, rows replayed) and recursive bodies
	// (WorkTableScan protocol) never qualify.
	inlineEligible bool
	// declSeq orders CTEs by WITH-list declaration (left to right, and an
	// enclosing statement's list after any list declared inside a body it
	// planned first). EXPLAIN reads it through CTEScan.DeclSeq to print the
	// `CTE <name>` sections in PG's order: upstream walks the top plan
	// node's subplan list, which is built in declaration order
	// (`SS_process_ctes`, optimizer/plan/subselect.c). M0125-0049.
	declSeq int
	// declPos is the source offset of the declaring parser.CommonTableExpr.
	// Together with name it identifies the DECLARATION — see
	// CTEScan.DeclKey, which is what the executor keys its materialization
	// buffer by. M0125-0050.
	declPos int
}

// cteDeclSeq stamps plannedCTE.declSeq. Package-global and unsynchronised,
// exactly like planCTEs above (planning is single-goroutine per statement);
// only the relative order within one plan tree is ever read, so the counter
// never needs resetting.
var cteDeclSeq int

func nextCTEDeclSeq() int {
	cteDeclSeq++
	return cteDeclSeq
}

// planCTEs is the goroutine-thread-unsafe "current WITH-list"
// scope visible to nested Plan calls. Mirrors the existing
// `planParent` pattern. nil when no WITH clause is in scope.
//
// A CTE body's recursive Plan call inherits planCTEs from its
// surrounding statement so a sibling CTE can reference an earlier
// declaration (left-to-right visibility). When a nested statement
// (subquery, CTE body) declares its own WITH, savePlanCTEs makes
// the existing entries available too — name shadowing then mirrors
// PostgreSQL.
var planCTEs map[string]*plannedCTE

// preplanWithClause planens every CTE in `with` (left-to-right,
// each visible to subsequent siblings) and stamps the resulting
// plannedCTE entries into the package-local planCTEs map. The
// caller MUST defer the returned restorer to release the scope
// once the WITH-prefixed statement has finished planning.
//
// Returns nil restorer when with is nil so the caller can defer
// unconditionally with `defer restore()` and a no-op stub.
func preplanWithClause(with *parser.WithClause, cat catalog.Catalog) (restore func(), dmlPlans []dmlCTEPlan, err error) {
	if with == nil {
		return func() {}, nil, nil
	}
	if with.Recursive {
		// WITH RECURSIVE: each CTE body must be a UNION ALL.
		// Second line of defence — the analyzer already rejects
		// non-UNION-ALL forms.
		prev := planCTEs
		cur := make(map[string]*plannedCTE, len(with.CTEs))
		for k, v := range prev {
			cur[k] = v
		}
		planCTEs = cur
		restore = func() { planCTEs = prev }

		for _, cte := range with.CTEs {
			body, err := planRecursiveCTE(cte, cat)
			if err != nil {
				restore()
				return nil, nil, err
			}
			schema := body.Output()
			if len(cte.Columns) > 0 {
				// PG (parse_cte.c analyzeCTE) allows an alias list shorter than
				// the inner query's output — trailing columns keep their query
				// names; only over-aliasing is the 42P10 error.
				if len(cte.Columns) > len(schema) {
					restore()
					return nil, nil, &PlanError{
						Pos:     cte.Pos(),
						Code:    "42P10",
						Message: fmt.Sprintf("CTE %q has %d column aliases but inner query produces %d columns", cte.Name, len(cte.Columns), len(schema)),
					}
				}
				renamed := make(Schema, len(schema))
				for i, c := range schema {
					name := c.Name
					if i < len(cte.Columns) {
						name = cte.Columns[i]
					}
					renamed[i] = SchemaColumn{Name: name, Type: c.Type}
				}
				schema = renamed
			}
			cols := make([]catalog.Column, len(schema))
			for i, c := range schema {
				cols[i] = catalog.Column{Name: c.Name, Type: c.Type}
			}
			cur[strings.ToLower(cte.Name)] = &plannedCTE{
				name:    cte.Name,
				body:    body,
				schema:  schema,
				table:   &catalog.Table{Name: cte.Name, Columns: cols},
				declSeq: nextCTEDeclSeq(),
				declPos: cte.Pos(),
			}
		}
		return restore, nil, nil
	}
	prev := planCTEs
	cur := make(map[string]*plannedCTE, len(with.CTEs))
	// Outer-scope CTEs stay visible — a nested WITH inherits but
	// its own names shadow on conflict. Mirrors PG.
	for k, v := range prev {
		cur[k] = v
	}
	planCTEs = cur
	restore = func() { planCTEs = prev }

	for _, cte := range with.CTEs {
		// Data-modifying CTE (INSERT/UPDATE/DELETE/MERGE body).
		if cte.DMLBody != nil {
			body, dmlErr := planDMLCTEBody(cte.DMLBody, cat)
			if dmlErr != nil {
				restore()
				return nil, nil, dmlErr
			}
			schema := body.Output()
			if len(cte.Columns) > 0 && len(cte.Columns) == len(schema) {
				renamed := make(Schema, len(schema))
				for i, c := range schema {
					renamed[i] = SchemaColumn{Name: cte.Columns[i], Type: c.Type}
				}
				schema = renamed
			}
			cols := make([]catalog.Column, len(schema))
			for i, c := range schema {
				cols[i] = catalog.Column{Name: c.Name, Type: c.Type}
			}
			entry := &plannedCTE{
				name:    cte.Name,
				body:    body,
				schema:  schema,
				table:   &catalog.Table{Name: cte.Name, Columns: cols},
				isDML:   true,
				declSeq: nextCTEDeclSeq(),
				declPos: cte.Pos(),
			}
			cur[strings.ToLower(cte.Name)] = entry
			dmlPlans = append(dmlPlans, dmlCTEPlan{name: cte.Name, plan: body, schema: schema})
			continue
		}

		// Bypass Plan() entry's Analyze pass: the outer statement's
		// Plan call already analyzed the whole tree (including this
		// CTE body — analyzer.analyzeWith recurses into each CTE
		// query under the right scope). A second analyzer pass at
		// recursive-Plan time would re-validate WITHOUT the
		// parent's CTE scope and erroneously reject sibling
		// references like `WITH a AS (SELECT 1), b AS (SELECT *
		// FROM a) ...`. Calling planSelect directly skips Analyze
		// while still seeing the in-progress planCTEs map so an
		// earlier sibling is visible to a later one.
		body, err := planSelect(cte.Query, cat)
		if err != nil {
			restore()
			return nil, nil, err
		}
		schema := body.Output()
		if len(cte.Columns) > 0 {
			// PG (parse_cte.c analyzeCTE) allows an alias list shorter than the
			// inner query's output — trailing columns keep their query names;
			// only over-aliasing is the 42P10 error.
			if len(cte.Columns) > len(schema) {
				restore()
				return nil, nil, &PlanError{
					Pos:     cte.Pos(),
					Code:    "42P10",
					Message: fmt.Sprintf("CTE %q has %d column aliases but inner query produces %d columns", cte.Name, len(cte.Columns), len(schema)),
				}
			}
			renamed := make(Schema, len(schema))
			for i, c := range schema {
				name := c.Name
				if i < len(cte.Columns) {
					name = cte.Columns[i]
				}
				renamed[i] = SchemaColumn{Name: name, Type: c.Type}
			}
			schema = renamed
		}
		cols := make([]catalog.Column, len(schema))
		for i, c := range schema {
			cols[i] = catalog.Column{Name: c.Name, Type: c.Type}
		}
		entry := &plannedCTE{
			name:    cte.Name,
			body:    body,
			schema:  schema,
			table:   &catalog.Table{Name: cte.Name, Columns: cols},
			declSeq: nextCTEDeclSeq(),
			declPos: cte.Pos(),
			// Plain non-recursive SELECT body: the only shape whose Child
			// a single-reference qual may descend into. The WITH RECURSIVE
			// branch above and the DML branch never set this.
			inlineEligible: true,
		}
		cur[strings.ToLower(cte.Name)] = entry
	}
	return restore, dmlPlans, nil
}

// planRecursiveCTE plans a WITH RECURSIVE CTE body as a RecursiveUnion.
// The CTE must have a UNION ALL body; the left side is the anchor
// and the right side is the recursive member.
func planRecursiveCTE(cte *parser.CommonTableExpr, cat catalog.Catalog) (Node, error) {
	key := strings.ToLower(cte.Name)

	// PostgreSQL allows WITH RECURSIVE CTEs whose bodies don't actually
	// recurse (no UNION self-reference). In that case, treat the CTE as a
	// regular non-recursive CTE — plan it with planSelect and register it.
	if cte.Query.SetOp == nil {
		body, err := planSelect(cte.Query, cat)
		if err != nil {
			return nil, err
		}
		schema := body.Output()
		if len(cte.Columns) > 0 {
			// Under-aliasing is allowed (PG parse_cte.c); only over-aliasing
			// is the 42P10 error. Trailing columns keep their query names.
			if len(cte.Columns) > len(schema) {
				return nil, &PlanError{
					Pos:     cte.Pos(),
					Code:    "42P10",
					Message: fmt.Sprintf("CTE %q has %d column aliases but inner query produces %d columns", cte.Name, len(cte.Columns), len(schema)),
				}
			}
			renamed := make(Schema, len(schema))
			for i, c := range schema {
				name := c.Name
				if i < len(cte.Columns) {
					name = cte.Columns[i]
				}
				renamed[i] = SchemaColumn{Name: name, Type: c.Type}
			}
			schema = renamed
		}
		cols := make([]catalog.Column, len(schema))
		for i, c := range schema {
			cols[i] = catalog.Column{Name: c.Name, Type: c.Type}
		}
		planCTEs[key] = &plannedCTE{
			name:   cte.Name,
			body:   body,
			schema: schema,
			table:  &catalog.Table{Name: cte.Name, Columns: cols},
		}
		return body, nil
	}
	// PostgreSQL rejects several constructs in the CTE body of a WITH RECURSIVE.
	if len(cte.Query.OrderBy) > 0 {
		return nil, &PlanError{
			Pos:     cte.Pos(),
			Code:    "0A000",
			Message: "ORDER BY in a recursive query is not implemented",
		}
	}
	if cte.Query.Offset != nil {
		return nil, &PlanError{
			Pos:     cte.Pos(),
			Code:    "0A000",
			Message: "OFFSET in a recursive query is not implemented",
		}
	}
	if len(cte.Query.Locking) > 0 {
		return nil, &PlanError{
			Pos:     cte.Pos(),
			Code:    "0A000",
			Message: "FOR UPDATE/SHARE in a recursive query is not implemented",
		}
	}
	unionAll := cte.Query.SetOp.All // false = UNION (dedup), true = UNION ALL

	// Save and clear SetOp so planSelect for the anchor does NOT
	// enter the UNION ALL handler (which would try to plan the
	// recursive member before the WorkTableScan is registered).
	savedEntry, hadEntry := planCTEs[key]
	if hadEntry {
		delete(planCTEs, key)
	}
	savedSetOp := cte.Query.SetOp
	cte.Query.SetOp = nil
	anchor, err := planSelect(cte.Query, cat)
	cte.Query.SetOp = savedSetOp
	if err != nil {
		if hadEntry {
			planCTEs[key] = savedEntry
		}
		return nil, err
	}

	anchorSchema := anchor.Output()
	// Apply explicit column alias list (the `(col, ...)` after the CTE name).
	// Without this, the WorkTableScan and the CTE catalog entry use the raw
	// planner output names (e.g. "?column?1"), causing the recursive member to
	// fail with "column n does not exist" when the query uses aliases like
	// `WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL ...)`. Mirror the renaming
	// that the non-recursive CTE path does at lines 165-178.
	if len(cte.Columns) > 0 {
		// Under-aliasing is allowed (PG parse_cte.c); only over-aliasing is the
		// 42P10 error. Trailing columns keep their query-derived names.
		if len(cte.Columns) > len(anchorSchema) {
			if hadEntry {
				planCTEs[key] = savedEntry
			}
			return nil, &PlanError{
				Pos:     cte.Pos(),
				Code:    "42P10",
				Message: fmt.Sprintf("CTE %q has %d column aliases but inner query produces %d columns", cte.Name, len(cte.Columns), len(anchorSchema)),
			}
		}
		renamed := make(Schema, len(anchorSchema))
		for i, c := range anchorSchema {
			name := c.Name
			if i < len(cte.Columns) {
				name = cte.Columns[i]
			}
			renamed[i] = SchemaColumn{Name: name, Type: c.Type}
		}
		anchorSchema = renamed
	}
	wts := &WorkTableScan{pos: cte.Pos(), schema: anchorSchema}
	wtCols := make([]catalog.Column, len(anchorSchema))
	for i, c := range anchorSchema {
		wtCols[i] = catalog.Column{Name: c.Name, Type: c.Type}
	}
	planCTEs[key] = &plannedCTE{
		name:   cte.Name,
		body:   wts,
		schema: anchorSchema,
		table:  &catalog.Table{Name: cte.Name, Columns: wtCols},
	}

	// Validate the recursive member for structural constraints before planning.
	if err := validateRecursiveMember(key, cte.Name, cte.Pos(), cte.Query.SetOp.Right); err != nil {
		if hadEntry {
			planCTEs[key] = savedEntry
		} else {
			delete(planCTEs, key)
		}
		return nil, err
	}

	rec, err := planSelect(cte.Query.SetOp.Right, cat)
	if err != nil {
		if hadEntry {
			planCTEs[key] = savedEntry
		} else {
			delete(planCTEs, key)
		}
		return nil, err
	}

	return &RecursiveUnion{
		pos:       cte.Pos(),
		Anchor:    anchor,
		Recursive: rec,
		schema:    anchorSchema,
		UnionAll:  unionAll,
	}, nil
}

// lookupPlannedCTE consults planCTEs for an unschemed name. CTE
// names are unschemed in upstream so a `pg_catalog.foo` reference
// always falls through to the catalog. Returns nil when no CTE
// matches.
func lookupPlannedCTE(name string) *plannedCTE {
	if planCTEs == nil || name == "" {
		return nil
	}
	return planCTEs[strings.ToLower(name)]
}

// validateRecursiveMember checks that the recursive member of a WITH RECURSIVE
// CTE satisfies PostgreSQL's structural constraints (PostgreSQL 14+ checks).
// Returns a PlanError with SQLSTATE 0A000 if any constraint is violated.
// cteNameLower is the lowercase CTE name; ctePos is used for error position.
func validateRecursiveMember(cteNameLower string, cteName string, ctePos int, rec *parser.SelectStmt) error {
	w := &recRefWalker{name: cteNameLower, origName: cteName, pos: ctePos}
	w.walkSelect(rec, false, false, false)
	if w.err != nil {
		return w.err
	}
	return validateNoAggregatesInRecursiveMember(rec, ctePos)
}

// validateNoAggregatesInRecursiveMember walks the SELECT targets and SetOp chain
// of the recursive member for aggregate function calls. PostgreSQL rejects these
// with "aggregate functions are not allowed in a recursive query's recursive term".
func validateNoAggregatesInRecursiveMember(sel *parser.SelectStmt, ctePos int) error {
	if sel == nil {
		return nil
	}
	for _, tgt := range sel.Targets {
		if _, ok := tgt.Expr.(*parser.FuncCall); ok {
			fc := tgt.Expr.(*parser.FuncCall)
			if isAggregateFunc(fc) {
				return &PlanError{Pos: ctePos, Code: "0A000",
					Message: "aggregate functions are not allowed in a recursive query's recursive term"}
			}
		}
	}
	if sel.SetOp != nil {
		return validateNoAggregatesInRecursiveMember(sel.SetOp.Right, ctePos)
	}
	// A grouping node holds its parenthesised operand below itself. M0125-0020.
	if sel.SetOpOperand != nil {
		return validateNoAggregatesInRecursiveMember(sel.SetOpOperand, ctePos)
	}
	return nil
}

// recRefWalker walks a recursive member's AST counting references to the
// recursive CTE name and detecting structural constraint violations.
type recRefWalker struct {
	name     string // lowercase CTE name
	origName string // original (display) name
	pos      int    // position for error reporting
	refs     int    // total references found so far
	err      error  // first error found
}

func (w *recRefWalker) setErr(err error) {
	if w.err == nil {
		w.err = err
	}
}

func (w *recRefWalker) walkSelect(sel *parser.SelectStmt, inSub, inExceptRight, inOuter bool) {
	if sel == nil || w.err != nil {
		return
	}
	if sel.SetOp != nil {
		if sel.SetOp.Type == parser.SetOpExcept {
			sub := &recRefWalker{name: w.name, origName: w.origName, pos: w.pos}
			sub.walkSelect(sel.SetOp.Right, inSub, true, false)
			if sub.refs > 0 {
				w.setErr(&PlanError{Pos: w.pos, Code: "0A000",
					Message: fmt.Sprintf("recursive reference to query %q must not appear within EXCEPT", w.origName)})
				return
			}
		} else {
			w.walkSelect(sel.SetOp.Right, inSub, inExceptRight, inOuter)
		}
	}
	// A grouping node's parenthesised operand is its left branch, and inherits
	// the same EXCEPT / subquery context as the node itself. M0125-0020.
	if sel.SetOpOperand != nil {
		w.walkSelect(sel.SetOpOperand, inSub, inExceptRight, inOuter)
	}
	if len(sel.FromExprs) > 0 {
		for _, fexpr := range sel.FromExprs {
			w.walkFromExpr(fexpr, inSub, inExceptRight)
			if w.err != nil {
				return
			}
		}
	} else {
		for _, rv := range sel.From {
			w.walkRangeVar(rv, inSub, inExceptRight, inOuter)
			if w.err != nil {
				return
			}
		}
	}
}

func (w *recRefWalker) walkRangeVar(rv parser.RangeVar, inSub, inExceptRight, inOuter bool) {
	if w.err != nil {
		return
	}
	if rv.Subquery != nil {
		sub := &recRefWalker{name: w.name, origName: w.origName, pos: w.pos}
		sub.walkSelect(rv.Subquery, true, inExceptRight, false)
		if sub.refs > 0 {
			w.setErr(&PlanError{Pos: w.pos, Code: "0A000",
				Message: fmt.Sprintf("recursive reference to query %q must not appear within a subquery", w.origName)})
		}
		w.refs += sub.refs
		return
	}
	if strings.ToLower(rv.Name) != w.name {
		return
	}
	w.refs++
	if inExceptRight {
		w.setErr(&PlanError{Pos: w.pos, Code: "0A000",
			Message: fmt.Sprintf("recursive reference to query %q must not appear within EXCEPT", w.origName)})
	} else if inSub {
		w.setErr(&PlanError{Pos: w.pos, Code: "0A000",
			Message: fmt.Sprintf("recursive reference to query %q must not appear within a subquery", w.origName)})
	} else if inOuter {
		w.setErr(&PlanError{Pos: w.pos, Code: "0A000",
			Message: fmt.Sprintf("recursive reference to query %q must not appear within an outer join", w.origName)})
	} else if w.refs > 1 {
		w.setErr(&PlanError{Pos: w.pos, Code: "0A000",
			Message: fmt.Sprintf("recursive reference to query %q must not appear more than once", w.origName)})
	}
}

func (w *recRefWalker) walkFromExpr(fexpr parser.FromExpr, inSub, inExceptRight bool) {
	if w.err != nil {
		return
	}
	w.walkRangeVar(fexpr.Base, inSub, inExceptRight, false)
	for _, j := range fexpr.Joins {
		if w.err != nil {
			return
		}
		isOuter := j.Type == parser.JoinLeft || j.Type == parser.JoinRight || j.Type == parser.JoinFull
		w.walkRangeVar(j.Right, inSub, inExceptRight, isOuter)
	}
}

// dmlCTEPlan holds a single data-modifying CTE plan and its output schema.
type dmlCTEPlan struct {
	name   string
	plan   Node
	schema Schema
}

// planDMLCTEBody plans an INSERT/UPDATE/DELETE/MERGE statement that
// appears as a CTE body. The plan's Output() schema reflects the
// RETURNING columns (empty if no RETURNING clause).
func planDMLCTEBody(stmt parser.Stmt, cat catalog.Catalog) (Node, error) {
	switch s := stmt.(type) {
	case *parser.InsertStmt:
		return planInsert(s, cat)
	case *parser.UpdateStmt:
		return planUpdate(s, cat)
	case *parser.DeleteStmt:
		return planDelete(s, cat)
	case *parser.MergeStmt:
		return planMerge(s, cat)
	default:
		return nil, &PlanError{Code: "0A000", Message: "unsupported DML statement in CTE body"}
	}
}

// wrapDMLCTEPrefix wraps outerPlan in a CTEDMLPrefix node when there
// are data-modifying CTEs. If dmlPlans is empty the outer plan is
// returned unchanged.
func wrapDMLCTEPrefix(outerPlan Node, dmlPlans []dmlCTEPlan) Node {
	if len(dmlPlans) == 0 {
		return outerPlan
	}
	names := make([]string, len(dmlPlans))
	plans := make([]Node, len(dmlPlans))
	for i, d := range dmlPlans {
		names[i] = d.name
		plans[i] = d.plan
	}
	return &CTEDMLPrefix{Names: names, DMls: plans, Body: outerPlan}
}
