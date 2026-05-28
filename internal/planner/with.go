package planner

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
				if len(cte.Columns) != len(schema) {
					restore()
					return nil, nil, &PlanError{
						Pos:     cte.Pos(),
						Code:    "42P10",
						Message: fmt.Sprintf("CTE %q has %d column aliases but inner query produces %d columns", cte.Name, len(cte.Columns), len(schema)),
					}
				}
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
			cur[strings.ToLower(cte.Name)] = &plannedCTE{
				name:   cte.Name,
				body:   body,
				schema: schema,
				table:  &catalog.Table{Name: cte.Name, Columns: cols},
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
				name:   cte.Name,
				body:   body,
				schema: schema,
				table:  &catalog.Table{Name: cte.Name, Columns: cols},
				isDML:  true,
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
			if len(cte.Columns) != len(schema) {
				restore()
				return nil, nil, &PlanError{
					Pos:     cte.Pos(),
					Code:    "42P10",
					Message: fmt.Sprintf("CTE %q has %d column aliases but inner query produces %d columns", cte.Name, len(cte.Columns), len(schema)),
				}
			}
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
			name:   cte.Name,
			body:   body,
			schema: schema,
			table:  &catalog.Table{Name: cte.Name, Columns: cols},
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

	if cte.Query.SetOp == nil {
		return nil, &PlanError{
			Pos:     cte.Pos(),
			Code:    "0A000",
			Message: "WITH RECURSIVE body must use UNION or UNION ALL",
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
		if len(cte.Columns) != len(anchorSchema) {
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
			renamed[i] = SchemaColumn{Name: cte.Columns[i], Type: c.Type}
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
