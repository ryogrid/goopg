package optimizer

// M0134-0187 — the per-base-rel NEEDED-COLUMN set, PG's `reltarget` +
// `attr_needed` (`build_base_rel_tlists`, initsplan.c:114) at the fidelity this
// seam can reach. See docs/design/not_ralph/pg-plan-parity/DESIGN.md §15/§21.
//
// `check_index_only` (indxpath.c:1010) asks one question: does this index
// supply every column of this relation that the rest of the query reads? PG
// answers it from `rel->reltarget->exprs` plus the attributes each clause
// needs, both attributed to a specific RTE. goopg's search boundary has
// neither — the statement's projection is not built until after the search
// returns. So the set is collected from the statement AST, by COLUMN NAME,
// and the approximation runs deliberately in ONE direction:
//
//   - Names are not attributed to a relation. A name used for `orders` also
//     counts as needed for `customer` if `customer` has a column of that name.
//     That OVER-states the requirement — an index has MORE to cover, so fewer
//     index-only paths are offered. It never omits a column.
//   - Anything the walker does not enumerate — a node type, a `SELECT *`, a
//     set operation — abandons the whole set (`ok=false`) and the producer
//     offers nothing. An index-only scan that drops a column the query reads
//     returns wrong rows, so silence is the only safe answer.
//
// The `default:` arms below are therefore load-bearing.
// `collectColumnRefTableNamesWalk` (reduce_outer_joins.go) has the same shape
// with the opposite default — correct for its own conservative question and a
// wrong-answer bug if reused here (rule #2).

import (
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// neededColumnNames returns every column name the statement reads, and whether
// the answer is TRUSTWORTHY. A false second return means "assume every column
// of every relation is needed" — the caller must then offer no index-only path.
func neededColumnNames(s *parser.SelectStmt) (map[string]bool, bool) {
	if s == nil {
		return nil, false
	}
	dst := make(map[string]bool, 16)
	dst[neededMarkersKey] = true
	if !collectStmtColumnNames(s, dst) {
		return nil, false
	}
	return dst, true
}

// M0146-0005bq-b — per-relation attribution for the qualified references.
//
// The name set over-states on purpose (file header), but for a table read
// under two aliases it over-states by a whole relation: TPC-DS Q18 reads
// `cd1.cd_gender` and `cd2.cd_demo_sk`, so `cd_gender` counted as needed for
// cd2 too and PG's `Index Only Scan ... cd2` probe was refused. Alongside the
// plain names the collector records, per reference, the qualifier it was
// written with (or that it had none), in marker keys no column name can
// collide with. A relation read as alias A then needs a column only when some
// reference is UNQUALIFIED (it may be A's) or is qualified by A. An
// unqualified reference still counts for every relation, so the attribution
// narrows only what another alias's qualified reads added — never below what
// the relation itself reads. A qualifier that names some other scope's
// relation by the same alias still counts for A: over-stating, never under.
const neededMarkersKey = "\x00markers"

func neededUnqualKey(col string) string { return "\x00u\x00" + strings.ToLower(col) }

func neededQualKey(qual, col string) string {
	return "\x00q\x00" + strings.ToLower(qual) + "\x00" + strings.ToLower(col)
}

// neededColumnNamedFor reports whether column col of the relation read under
// qualifier qual is needed. Without the markers (a set built by an older
// path), or without a qualifier to attribute by, it falls back to the name
// alone.
func neededColumnNamedFor(needed map[string]bool, qual, col string) bool {
	if qual == "" || !needed[neededMarkersKey] {
		return needed[col]
	}
	return needed[neededUnqualKey(col)] || needed[neededQualKey(qual, col)]
}

// outputColumnNames returns every column name read ABOVE the statement's
// scan/join tree — the SELECT targets, GROUP BY, DISTINCT ON, ORDER BY,
// HAVING, LIMIT and OFFSET expressions — and whether the answer is
// TRUSTWORTHY. A false second return means "assume every column is read
// above", and the caller must then narrow nothing beyond the statement-wide
// set.
//
// Take2 P4-01 Slice 3 (planner-p4-01-target DESIGN, "Slice 3+"): the union
// needed above the scan/join tree, from which per-joinrel keep-sets derive
// (F1). It is a SUBSET of what neededColumnNames collects — the WHERE, ON,
// USING and FROM clauses are deliberately skipped: their references are
// placed as join quals inside the tree (read at or below the narrow point)
// or survive in the above-root residual (which the seam checks against the
// padded coordinates with a fallback, rather than reasoning about here).
//
// The one exception inside the skipped clauses is SUBLINK constructs
// (EXISTS, scalar subqueries, IN-with-subquery): their outer references —
// the IN operand, correlations — are read by the (possibly unnested)
// semi/anti spine or subplan ABOVE the tree, so the walk descends into
// those constructs alone, collecting everything inside them in needed mode
// (inner-table names over-keep, which is safe). Plain conjuncts contribute
// nothing: a plain reference that is neither placed in-tree nor residual
// is a leaf-local filter evaluated below.
//
// The decline shapes mirror neededColumnNames exactly (same `default:`
// load-bearing arms): anything unenumerated declines the whole set rather
// than risk an under-keep.
func outputColumnNames(s *parser.SelectStmt) (map[string]bool, bool) {
	if s == nil {
		return nil, false
	}
	dst := make(map[string]bool, 16)
	dst[neededMarkersKey] = true
	if !collectOutputColumnNames(s, dst) {
		return nil, false
	}
	return dst, true
}

// collectOutputColumnNames walks one SELECT's above-tree clauses. Derived
// tables are skipped: each is planned by its own `planSelect` call and
// therefore gets its own set.
func collectOutputColumnNames(s *parser.SelectStmt, dst map[string]bool) bool {
	if s == nil {
		return false
	}
	// Shapes whose column usage this walker does not model; an unaccounted
	// reference is a dropped column, so they decline as a group. Mirrors
	// collectStmtColumnNames.
	if s.SetOp != nil || s.SetOpOperand != nil || s.With != nil ||
		len(s.ValuesRows) != 0 ||
		len(s.WindowClause) != 0 || len(s.Locking) != 0 {
		return false
	}
	// M0146-0005bq-b: a grouping-set clause reads exactly the expressions its
	// sets list (ROLLUP/CUBE/GROUPING SETS expand to them), so walking every
	// set's expressions accounts for it — TPC-DS Q18's ROLLUP had made the
	// whole needed set unknown and every index-only path unofferable.
	if !collectGroupingSetColumnNames(s.GroupingSets, dst) {
		return false
	}
	for _, t := range s.Targets {
		if !collectExprColumnNames(t.Expr, dst) {
			return false
		}
	}
	for _, e := range []parser.Expr{s.Having, s.Limit, s.Offset} {
		if !collectExprColumnNames(e, dst) {
			return false
		}
	}
	for _, group := range [][]parser.Expr{s.GroupBy, s.DistinctOn} {
		for _, e := range group {
			if !collectExprColumnNames(e, dst) {
				return false
			}
		}
	}
	for _, sb := range s.OrderBy {
		if !collectExprColumnNames(sb.Expr, dst) {
			return false
		}
	}
	// WHERE and JOIN ON clauses: plain references are tree-internal (see the
	// outputColumnNames doc); only sublink constructs are descended into.
	// USING(c) and NATURAL name join keys, which the path tree carries as
	// HashKeys/Residual — but NATURAL does not even spell its columns out,
	// so it declines, mirroring the needed collector.
	if !collectSublinkOuterNames(s.Where, dst) {
		return false
	}
	for i := range s.FromExprs {
		for j := range s.FromExprs[i].Joins {
			jn := &s.FromExprs[i].Joins[j]
			if jn.Natural {
				return false
			}
			if !collectSublinkOuterNames(jn.On, dst) {
				return false
			}
		}
	}
	return true
}

// collectSublinkOuterNames adds the outer references of the sublink
// constructs in e to dst, skipping every plain reference. It mirrors
// collectExprColumnNames arm for arm so the two cannot drift: a plain
// ColumnRef contributes nothing (tree-internal), while EXISTS / scalar
// subquery / IN-with-subquery interiors are collected whole in needed
// mode via collectStmtColumnNames (their outer references are read above
// the tree; inner-table names over-keep, which is safe). Anything
// collectExprColumnNames declines, this declines too.
func collectSublinkOuterNames(e parser.Expr, dst map[string]bool) bool {
	if e == nil {
		return true
	}
	switch x := e.(type) {
	case *parser.ColumnRef:
		return true

	// Leaves that provably carry no column reference.
	case *parser.IntegerConst, *parser.StringConst, *parser.NumericConst,
		*parser.NullConst, *parser.BooleanConst, *parser.TypedStringLit,
		*parser.IntervalLit, *parser.ParamRef, *parser.DefaultMarker:
		return true

	case *parser.BinaryOp:
		return collectSublinkOuterNames(x.Left, dst) && collectSublinkOuterNames(x.Right, dst)
	case *parser.UnaryOp:
		return collectSublinkOuterNames(x.Operand, dst)
	case *parser.IsNullExpr:
		return collectSublinkOuterNames(x.Operand, dst)
	case *parser.IsBoolExpr:
		return collectSublinkOuterNames(x.Operand, dst)
	case *parser.CastExpr:
		return collectSublinkOuterNames(x.Operand, dst)
	case *parser.CollateExpr:
		return collectSublinkOuterNames(x.Operand, dst)
	case *parser.IsDistinctFromExpr:
		return collectSublinkOuterNames(x.Left, dst) && collectSublinkOuterNames(x.Right, dst)
	case *parser.CaseExpr:
		if !collectSublinkOuterNames(x.Operand, dst) || !collectSublinkOuterNames(x.Else, dst) {
			return false
		}
		for _, w := range x.Whens {
			if !collectSublinkOuterNames(w.When, dst) || !collectSublinkOuterNames(w.Then, dst) {
				return false
			}
		}
		return true
	case *parser.FuncCall:
		if x.Over != nil {
			return false
		}
		for _, a := range x.Args {
			if !collectSublinkOuterNames(a, dst) {
				return false
			}
		}
		if !collectSublinkOuterNames(x.Filter, dst) {
			return false
		}
		for _, group := range [][]parser.SortBy{x.OrderBy, x.WithinGroup} {
			for _, sb := range group {
				if !collectSublinkOuterNames(sb.Expr, dst) {
					return false
				}
			}
		}
		return true
	case *parser.InExpr:
		// The operand is an OUTER reference (read by the unnested spine),
		// so unlike a plain ColumnRef it is collected whole in needed mode.
		if !collectExprColumnNames(x.Operand, dst) {
			return false
		}
		for _, v := range x.List {
			if !collectSublinkOuterNames(v, dst) {
				return false
			}
		}
		if x.Subquery != nil {
			return collectStmtColumnNames(x.Subquery, dst)
		}
		return true
	case *parser.ExtractExpr:
		return collectSublinkOuterNames(x.Source, dst)
	case *parser.ExistsExpr:
		return collectStmtColumnNames(x.Subquery, dst)
	case *parser.SubqueryExpr:
		return collectStmtColumnNames(x.Inner, dst)
	default:
		return false
	}
}

// collectStmtColumnNames walks one SELECT's non-FROM clauses. Derived tables
// are skipped: each is planned by its own `planSelect` call and therefore gets
// its own set.
func collectStmtColumnNames(s *parser.SelectStmt, dst map[string]bool) bool {
	if s == nil {
		return false
	}
	// Shapes whose column usage this walker does not model; an unaccounted
	// reference is a dropped column, so they decline as a group.
	if s.SetOp != nil || s.SetOpOperand != nil || s.With != nil ||
		len(s.ValuesRows) != 0 ||
		len(s.WindowClause) != 0 || len(s.Locking) != 0 {
		return false
	}
	// M0146-0005bq-b: a grouping-set clause reads exactly the expressions its
	// sets list (ROLLUP/CUBE/GROUPING SETS expand to them), so walking every
	// set's expressions accounts for it — TPC-DS Q18's ROLLUP had made the
	// whole needed set unknown and every index-only path unofferable.
	if !collectGroupingSetColumnNames(s.GroupingSets, dst) {
		return false
	}
	for _, t := range s.Targets {
		if !collectExprColumnNames(t.Expr, dst) {
			return false
		}
	}
	for _, e := range []parser.Expr{s.Where, s.Having, s.Limit, s.Offset} {
		if !collectExprColumnNames(e, dst) {
			return false
		}
	}
	for _, group := range [][]parser.Expr{s.GroupBy, s.DistinctOn} {
		for _, e := range group {
			if !collectExprColumnNames(e, dst) {
				return false
			}
		}
	}
	for _, sb := range s.OrderBy {
		if !collectExprColumnNames(sb.Expr, dst) {
			return false
		}
	}
	// ON clauses: a column that appears ONLY in an ON clause is still read by
	// the scan beneath it.
	for i := range s.FromExprs {
		if !collectRangeVarColumnNames(&s.FromExprs[i].Base) {
			return false
		}
		for j := range s.FromExprs[i].Joins {
			jn := &s.FromExprs[i].Joins[j]
			if !collectRangeVarColumnNames(&jn.Right) {
				return false
			}
			// USING(c) and NATURAL both name columns on both sides; NATURAL
			// does not even spell them out, so it declines.
			if jn.Natural {
				return false
			}
			for _, u := range jn.Using {
				dst[u] = true
				dst[neededUnqualKey(u)] = true
			}
			if !collectExprColumnNames(jn.On, dst) {
				return false
			}
		}
	}
	for i := range s.From {
		if !collectRangeVarColumnNames(&s.From[i]) {
			return false
		}
	}
	return true
}

// collectRangeVarColumnNames accounts for one FROM item. A plain table or a
// derived table (planned by its own planSelect) contributes nothing; anything
// that can hide an expression declines.
func collectRangeVarColumnNames(rv *parser.RangeVar) bool {
	if rv == nil {
		return true
	}
	return rv.TableFunc == nil && rv.TableSample == nil && !rv.Lateral
}

// collectExprColumnNames adds every `ColumnRef.Column` in e to dst. Returns
// false the moment it meets something it cannot account for.
func collectExprColumnNames(e parser.Expr, dst map[string]bool) bool {
	if e == nil {
		return true
	}
	switch x := e.(type) {
	case *parser.ColumnRef:
		dst[x.Column] = true
		// M0146-0005bq-b: which relation the name was read FROM, as far as
		// the text says — see neededQualKey.
		if x.Table == "" {
			dst[neededUnqualKey(x.Column)] = true
		} else {
			dst[neededQualKey(x.Table, x.Column)] = true
		}
		return true

	// Leaves that provably carry no column reference.
	case *parser.IntegerConst, *parser.StringConst, *parser.NumericConst,
		*parser.NullConst, *parser.BooleanConst, *parser.TypedStringLit,
		*parser.IntervalLit, *parser.ParamRef, *parser.DefaultMarker:
		return true

	case *parser.BinaryOp:
		return collectExprColumnNames(x.Left, dst) && collectExprColumnNames(x.Right, dst)
	case *parser.UnaryOp:
		return collectExprColumnNames(x.Operand, dst)
	case *parser.IsNullExpr:
		return collectExprColumnNames(x.Operand, dst)
	case *parser.IsBoolExpr:
		return collectExprColumnNames(x.Operand, dst)
	case *parser.CastExpr:
		return collectExprColumnNames(x.Operand, dst)
	case *parser.CollateExpr:
		return collectExprColumnNames(x.Operand, dst)
	case *parser.IsDistinctFromExpr:
		return collectExprColumnNames(x.Left, dst) && collectExprColumnNames(x.Right, dst)
	case *parser.CaseExpr:
		if !collectExprColumnNames(x.Operand, dst) || !collectExprColumnNames(x.Else, dst) {
			return false
		}
		for _, w := range x.Whens {
			if !collectExprColumnNames(w.When, dst) || !collectExprColumnNames(w.Then, dst) {
				return false
			}
		}
		return true
	case *parser.FuncCall:
		// A window function's frame can name columns; decline rather than
		// model the WindowDef shape here. `count(*)` reads no column.
		if x.Over != nil {
			return false
		}
		for _, a := range x.Args {
			if !collectExprColumnNames(a, dst) {
				return false
			}
		}
		if !collectExprColumnNames(x.Filter, dst) {
			return false
		}
		for _, group := range [][]parser.SortBy{x.OrderBy, x.WithinGroup} {
			for _, sb := range group {
				if !collectExprColumnNames(sb.Expr, dst) {
					return false
				}
			}
		}
		return true
	case *parser.InExpr:
		if !collectExprColumnNames(x.Operand, dst) {
			return false
		}
		for _, v := range x.List {
			if !collectExprColumnNames(v, dst) {
				return false
			}
		}
		if x.Subquery != nil {
			// A correlated subquery reads OUTER columns by name, so its own
			// walk keeps those in this set too.
			return collectStmtColumnNames(x.Subquery, dst)
		}
		return true
	case *parser.ExtractExpr:
		// `extract(field from src)` reads only `src`; the field is a literal
		// keyword, not a column. Declining it cost more than it looks: the
		// default arm below returns false for the WHOLE statement, so a single
		// extract anywhere made `neededColsKnown` false and disabled both
		// index-only scans and any needed-column narrowing for that query.
		// TPC-H Q7, Q8 and Q9 all use `extract(year from ...)` — Q9's sits
		// inside the derived table that owns its six-way join tree, which is
		// exactly the plan take2 P4-01 is justified by.
		return collectExprColumnNames(x.Source, dst)
	case *parser.ExistsExpr:
		return collectStmtColumnNames(x.Subquery, dst)
	case *parser.SubqueryExpr:
		return collectStmtColumnNames(x.Inner, dst)
	default:
		// `StarExpr`, `IndirectionStar`, `RowExpr`, the array forms, and any
		// node added after this file was written. Declining is the only answer
		// that cannot silently drop a column. (`ExtractExpr` was in this list
		// and is now handled above — it was declining TPC-H Q7/Q8/Q9.)
		return false
	}
}

// collectGroupingSetColumnNames adds every column the grouping sets name.
func collectGroupingSetColumnNames(gs *parser.GroupingSetsSpec, dst map[string]bool) bool {
	if gs == nil {
		return true
	}
	for _, set := range gs.Sets {
		for _, e := range set {
			if !collectExprColumnNames(e, dst) {
				return false
			}
		}
	}
	return true
}
