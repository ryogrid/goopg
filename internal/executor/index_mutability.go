package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// index_mutability.go — PostgreSQL's "functions in index expression /
// index predicate must be marked IMMUTABLE" gate.
//
// Upstream sites (postgres/src/backend/commands/indexcmds.c):
//
//	CheckPredicate()      :1843-1857 — the WHERE clause of a partial index
//	ComputeIndexAttrs()   :2015-2019 — every non-Var index key expression
//
// Both raise ERRCODE_INVALID_OBJECT_DEFINITION (42P17) from
// contain_mutable_functions_after_planning(). The rule exists for a
// correctness reason, not a stylistic one: an index entry computed from a
// non-IMMUTABLE expression cannot be reproduced at probe time, so the index
// silently disagrees with the heap and the planner returns wrong answers from
// an index scan that a seq scan would have answered correctly.
//
// goopg had neither check. `CREATE INDEX ON t ((clock_timestamp()::text))`,
// `CREATE INDEX ON t (a) WHERE a > random()::int` and every user-defined
// VOLATILE/STABLE function in a key expression were accepted outright
// (verified live against PG 18.3 — 6 of 12 probe statements diverged).
// The sibling gate for partition keys DOES exist
// (validatePartKeyExprInner, operators_ddl_partition.go:283-325, PG's
// ComputePartitionAttrs), so this is the "sibling code paths must stay in
// sync" pattern: one of two ports of the same upstream predicate was
// written and the other was not. Both now share exprHasNonImmutableFunction
// below, which also fixes the partition-key path's own gap — it consulted
// ONLY ctx.Catalog.Routines() (user-defined functions), so a bare volatile
// BUILT-IN such as `PARTITION BY RANGE ((a + (random()*10)::int))` slipped
// through it too.
//
// M0134-0170 (sized out of sqljson_queryfuncs.sql, whose
// "Test mutabilily of query functions" block is 28 consecutive statements
// asserting exactly this error).

// exprHasNonImmutableFunction reports whether e contains a function call that
// PostgreSQL would classify as non-IMMUTABLE, i.e. whether
// contain_mutable_functions() would return true for the transformed tree.
//
// Resolution order per call, mirroring PG's name lookup:
//
//  1. a user-defined routine of that name in the catalog wins. A
//     LANGUAGE sql routine is INLINED by PG before the volatility test, so its
//     body is scanned instead of trusting its declared marker (that is why
//     `CREATE FUNCTION f() RETURNS int VOLATILE LANGUAGE sql AS 'SELECT 1'`
//     is still usable in an index expression upstream); any other language is
//     taken at its declared provolatile.
//  2. otherwise it is a built-in, classified by nonImmutableBuiltinNames —
//     see pg_nonimmutable_builtins.go for how that set is derived from
//     pg_proc.dat and why mixed-volatility names are deliberately absent.
//
// Unknown names are treated as immutable: an index expression naming a
// function that does not exist at all is a separate error PG raises earlier,
// and goopg does not raise it yet (ledger M0134-0170b).
func exprHasNonImmutableFunction(e parser.Expr, cat catalog.Catalog) bool {
	found := false
	walkParserExprFuncCalls(e, func(fc *parser.FuncCall) {
		if found {
			return
		}
		if funcCallIsNonImmutable(fc, cat) {
			found = true
		}
	})
	return found
}

// funcCallIsNonImmutable is the per-call classification described above.
func funcCallIsNonImmutable(fc *parser.FuncCall, cat catalog.Catalog) bool {
	if cat != nil {
		if rs := cat.Routines(); rs != nil {
			if routines := rs.LookupByName(fc.Name); len(routines) > 0 {
				for _, r := range routines {
					if r.Language == "sql" {
						if sqlRoutineBodyIsVolatile(r.Body, rs) {
							return true
						}
						continue
					}
					if r.Volatile != "i" {
						return true
					}
				}
				return false
			}
		}
	}
	// A schema-qualified name is only a built-in when it is pg_catalog's.
	if fc.Name.Schema != "" && !strings.EqualFold(fc.Name.Schema, "pg_catalog") {
		return false
	}
	return nonImmutableBuiltinNames[strings.ToLower(fc.Name.Name)] ||
		nonImmutableBuiltinNames[fc.Name.Name]
}

// sqlRoutineBodyIsVolatile parses a LANGUAGE sql routine body and reports
// whether the inlined expression would be non-IMMUTABLE. This is the
// generalisation of the partition-key path's original body scan: it now walks
// every expression node kind (via walkParserExprFuncCalls) rather than the
// five it used to, and consults the pg_proc.dat-derived built-in set rather
// than a 14-name hand list.
func sqlRoutineBodyIsVolatile(body string, rs *catalog.Routines) bool {
	stmts, err := parser.Parse(body)
	if err != nil {
		// An unparseable body cannot be inlined; PG would take the declared
		// marker. Callers reach here only for routines already marked
		// non-immutable-safe, so report "not volatile" and let the declared
		// marker decide.
		return false
	}
	for _, stmt := range stmts {
		sel, ok := stmt.(*parser.SelectStmt)
		if !ok {
			continue
		}
		for _, col := range sel.Targets {
			found := false
			walkParserExprFuncCalls(col.Expr, func(fc *parser.FuncCall) {
				if found {
					return
				}
				if rs != nil {
					if inner := rs.LookupByName(fc.Name); len(inner) > 0 {
						for _, r := range inner {
							if r.Language == "sql" {
								// Recursive inline. Guard against a self- or
								// mutually-recursive SQL body by refusing to
								// re-enter the same source text.
								if r.Body != body && sqlRoutineBodyIsVolatile(r.Body, rs) {
									found = true
								}
							} else if r.Volatile != "i" {
								found = true
							}
						}
						return
					}
				}
				if nonImmutableBuiltinNames[strings.ToLower(fc.Name.Name)] {
					found = true
				}
			})
			if found {
				return true
			}
		}
	}
	return false
}

// walkParserExprFuncCalls invokes visit for every parser.FuncCall reachable
// from e, depth-first. Unlike optimizer.walkExpr (planner.go:8105) it covers
// every parser.Expr implementation, which matters here because an index
// expression is arbitrary user SQL: CASE arms, ARRAY[…] elements, row
// constructors, subscripts, COLLATE wrappers and aggregate FILTER/ORDER BY
// tails all have to be reached or the gate is trivially bypassable
// (`CREATE INDEX ON t ((CASE WHEN true THEN random() ELSE 0 END))`).
//
// Sub-SELECTs (SubqueryExpr, ExistsExpr, ArraySubqueryExpr, InExpr's
// Subquery) are NOT descended into: PG rejects a subquery in an index
// expression or predicate much earlier, in transformExpr's EXPR_KIND_INDEX_*
// handling, so any function inside one is unreachable by this gate upstream
// too.
func walkParserExprFuncCalls(e parser.Expr, visit func(*parser.FuncCall)) {
	switch x := e.(type) {
	case nil:
		return
	case *parser.FuncCall:
		visit(x)
		for _, a := range x.Args {
			walkParserExprFuncCalls(a, visit)
		}
		walkParserExprFuncCalls(x.Filter, visit)
		for _, sb := range x.OrderBy {
			walkParserExprFuncCalls(sb.Expr, visit)
		}
		for _, sb := range x.WithinGroup {
			walkParserExprFuncCalls(sb.Expr, visit)
		}
	case *parser.BinaryOp:
		walkParserExprFuncCalls(x.Left, visit)
		walkParserExprFuncCalls(x.Right, visit)
	case *parser.UnaryOp:
		walkParserExprFuncCalls(x.Operand, visit)
	case *parser.CastExpr:
		walkParserExprFuncCalls(x.Operand, visit)
	case *parser.CollateExpr:
		walkParserExprFuncCalls(x.Operand, visit)
	case *parser.IsNullExpr:
		walkParserExprFuncCalls(x.Operand, visit)
	case *parser.IsBoolExpr:
		walkParserExprFuncCalls(x.Operand, visit)
	case *parser.IsDistinctFromExpr:
		walkParserExprFuncCalls(x.Left, visit)
		walkParserExprFuncCalls(x.Right, visit)
	case *parser.CaseExpr:
		walkParserExprFuncCalls(x.Operand, visit)
		for _, w := range x.Whens {
			walkParserExprFuncCalls(w.When, visit)
			walkParserExprFuncCalls(w.Then, visit)
		}
		walkParserExprFuncCalls(x.Else, visit)
	case *parser.RowExpr:
		for _, el := range x.Elems {
			walkParserExprFuncCalls(el, visit)
		}
	case *parser.ArrayConstructorExpr:
		for _, el := range x.Elements {
			walkParserExprFuncCalls(el, visit)
		}
	case *parser.ArraySubscriptExpr:
		walkParserExprFuncCalls(x.Base, visit)
		walkParserExprFuncCalls(x.Index, visit)
		walkParserExprFuncCalls(x.Upper, visit)
	case *parser.InExpr:
		walkParserExprFuncCalls(x.Operand, visit)
		for _, el := range x.List {
			walkParserExprFuncCalls(el, visit)
		}
	case *parser.LikeEscapePattern:
		walkParserExprFuncCalls(x.Pattern, visit)
		walkParserExprFuncCalls(x.Escape, visit)
	case *parser.SimilarToPattern:
		walkParserExprFuncCalls(x.Left, visit)
		walkParserExprFuncCalls(x.Pattern, visit)
		walkParserExprFuncCalls(x.Escape, visit)
	case *parser.ExtractExpr:
		walkParserExprFuncCalls(x.Source, visit)
	case *parser.GroupingCall:
		for _, a := range x.Args {
			walkParserExprFuncCalls(a, visit)
		}
	case *parser.IndirectionStar:
		walkParserExprFuncCalls(x.Source, visit)
	}
	// Every remaining implementation of parser.Expr is a leaf that cannot
	// contain a function call (the literal constants, ColumnRef, ParamRef,
	// StarExpr, DefaultMarker, TypedStringLit, IntervalLit,
	// PartitionRangeBoundKeyword) or a sub-SELECT wrapper, deliberately not
	// descended into per the doc comment above.
}

// checkIndexPredicateMutability is CheckPredicate (indexcmds.c:1843).
func checkIndexPredicateMutability(pred parser.Expr, cat catalog.Catalog, pos int) error {
	if pred == nil || !exprHasNonImmutableFunction(pred, cat) {
		return nil
	}
	return &ExecError{Code: "42P17", Pos: pos,
		Message: "functions in index predicate must be marked IMMUTABLE"}
}

// checkIndexExprMutability is ComputeIndexAttrs' per-expression test
// (indexcmds.c:2015-2019). exprs is the ColExprs slice, whose entries are nil
// for plain column keys — those are Vars upstream and are never tested.
func checkIndexExprMutability(exprs []parser.Expr, cat catalog.Catalog, pos int) error {
	for _, e := range exprs {
		if e == nil {
			continue
		}
		if exprHasNonImmutableFunction(e, cat) {
			return &ExecError{Code: "42P17", Pos: pos,
				Message: "functions in index expression must be marked IMMUTABLE"}
		}
	}
	return nil
}
