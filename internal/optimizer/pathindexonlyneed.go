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

	"github.com/goopg/goopg/internal/catalog"
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

// expandWholeRowColumnNames widens the needed and output sets for each
// whole-row reference (M0146-0047a). A bare name that is a FROM relation's
// alias — `SELECT a.id, b FROM wa a JOIN wb b ...` — reads every column of
// that relation (PG: a whole-row Var, varattno 0, which pull_varattnos /
// build_base_rel_tlists turn into attr_needed for all of its attributes).
// The collectors record it as an ordinary unqualified name, which matches no
// column, so the narrowed join kept only the join key and the row came out
// `(1,,)`. Each such relation's columns are added as qualified reads
// (neededQualKey), plus the plain name neededKeepSet matches on.
//
// A bare name is a whole-row reference only when it is not a column:
// transformColumnRef tries colNameToVar first and falls back to the relation
// name only when no column matches. The sets themselves carry no scope, so
// the decision is taken per scope from the names written IN that scope:
//
//   - the statement: its own references (the collectors do not descend into
//     FROM subqueries; sublinks are included and see these relations), and
//     the columns of its own FROM relations;
//   - each pulled-up derived body: the names collected from the body alone,
//     and the columns of the body's own FROM relations — a non-LATERAL
//     derived table does not see its parent's FROM list.
//
// TPC-H Q9's outer `nation` is the derived table's column, written in the
// outer scope; the body's `nation` relation is never named bare inside the
// body, so it is not expanded. A relation whose output names are unknown
// here (a function, a subquery not pulled up and without column aliases)
// contributes none, which can only expand more — over-keeping is the safe
// direction (file header).
func expandWholeRowColumnNames(ctx *resolveContext, s *parser.SelectStmt, cat catalog.Catalog) {
	if ctx == nil || s == nil || cat == nil {
		return
	}
	var sets []map[string]bool
	if ctx.neededColsKnown && ctx.neededCols != nil {
		sets = append(sets, ctx.neededCols)
	}
	if ctx.outputColsKnown && ctx.outputCols != nil {
		sets = append(sets, ctx.outputCols)
	}
	if len(sets) == 0 {
		return
	}
	pulledNames := map[string][]string{}
	for _, r := range ctx.pulledDerived {
		pulledNames[strings.ToLower(r.alias)] = r.names
	}
	lookup := func(rv parser.RangeVar) *catalog.Table {
		if rv.Subquery != nil || rv.TableFunc != nil || rv.Name == "" {
			return nil
		}
		t, ok := cat.LookupTable(parser.ObjectName{Schema: rv.Schema, Name: rv.Name})
		if !ok {
			return nil
		}
		return t
	}
	// expandScope expands, in every set, each relation of `from` that the
	// scope's own references name bare while no relation of the scope has a
	// column of that name.
	expandScope := func(stmt *parser.SelectStmt) {
		written := make(map[string]bool, 16)
		written[neededMarkersKey] = true
		if !collectStmtColumnNames(stmt, written) {
			// Unaccounted references: assume every relation is read whole.
			written = nil
		}
		visible := map[string]bool{}
		for _, rv := range stmt.From {
			switch {
			case rv.Subquery != nil && pulledNames[strings.ToLower(rv.Alias)] != nil:
				// A pulled body's names already apply its alias list,
				// leading columns renamed and the rest kept (M0146-0028g).
				for _, n := range pulledNames[strings.ToLower(rv.Alias)] {
					visible[strings.ToLower(n)] = true
				}
			case len(rv.Columns) > 0:
				for _, c := range rv.Columns {
					visible[strings.ToLower(c)] = true
				}
			case rv.Subquery != nil:
				for _, n := range pulledNames[strings.ToLower(rv.Alias)] {
					visible[strings.ToLower(n)] = true
				}
			default:
				if t := lookup(rv); t != nil {
					for _, c := range t.Columns {
						visible[strings.ToLower(c.Name)] = true
					}
				}
			}
		}
		for _, rv := range stmt.From {
			qual := rv.Alias
			if qual == "" {
				qual = rv.Name
			}
			if written != nil && !written[neededUnqualKey(qual)] {
				continue
			}
			if visible[strings.ToLower(qual)] {
				continue
			}
			tbl := lookup(rv)
			if tbl == nil {
				continue
			}
			for _, set := range sets {
				for _, c := range tbl.Columns {
					set[c.Name] = true
					set[neededQualKey(qual, c.Name)] = true
				}
			}
		}
	}
	expandScope(s)
	for _, r := range ctx.pulledDerived {
		if r.body != nil {
			expandScope(r.body)
		}
	}
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
	if s.SetOp != nil || s.SetOpOperand != nil ||
		len(s.ValuesRows) != 0 ||
		len(s.WindowClause) != 0 || len(s.Locking) != 0 {
		return false
	}
	if !collectWithBodyColumnNames(s.With, dst) {
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
		return collectStmtColumnNames(existsBodyForColumns(x.Subquery), dst)
	case *parser.SubqueryExpr:
		return collectStmtColumnNames(scalarBodyForColumns(x.Inner), dst)
	default:
		return false
	}
}

// collectWithBodyColumnNames walks every CTE body of a WITH list into dst,
// in needed mode (M0146-0147). The WITH list used to decline the whole set.
// Each body is its own query level with its own set, but a body may read an
// enclosing level's columns as outer references (a WITH inside a sublink
// body reads the sublink's outer query), and an inlined body is pulled into
// the referencing scope. Collecting every body's names over-includes, which
// only keeps more columns — the safe direction. PG's check_index_only
// (indxpath.c) reads the attributes the query uses, WITH or not; the decline
// cost TPC-DS Q95 its Index Only Scan on web_returns_pkey. A data-modifying
// CTE, or a body the walker cannot model (a recursive UNION), still declines.
func collectWithBodyColumnNames(w *parser.WithClause, dst map[string]bool) bool {
	if w == nil {
		return true
	}
	for _, cte := range w.CTEs {
		if cte == nil || cte.DMLBody != nil || cte.Query == nil ||
			!collectStmtColumnNames(cte.Query, dst) {
			return false
		}
	}
	return true
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
	if s.SetOp != nil || s.SetOpOperand != nil ||
		len(s.ValuesRows) != 0 ||
		len(s.WindowClause) != 0 || len(s.Locking) != 0 {
		return false
	}
	if !collectWithBodyColumnNames(s.With, dst) {
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
		return collectStmtColumnNames(existsBodyForColumns(x.Subquery), dst)
	case *parser.SubqueryExpr:
		return collectStmtColumnNames(scalarBodyForColumns(x.Inner), dst)
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

// existsBodyForColumns is simplify_EXISTS_query's target-list discard
// (subselect.c) as the column collector sees it: an EXISTS reads no column
// through its SELECT list, so `EXISTS (SELECT * FROM wr1 WHERE ...)` — TPC-DS
// Q94's anti-join probe — must not void the statement's needed set with its
// star. Only a list of stars and constants is dropped; any other target is
// still walked, which can only over-state (M0146-0005bs).
func existsBodyForColumns(sub *parser.SelectStmt) *parser.SelectStmt {
	if sub == nil || len(sub.Targets) == 0 {
		return sub
	}
	for _, t := range sub.Targets {
		switch t.Expr.(type) {
		case *parser.StarExpr, *parser.IntegerConst, *parser.StringConst,
			*parser.NumericConst, *parser.BooleanConst, *parser.NullConst:
		default:
			return sub
		}
	}
	cp := *sub
	cp.Targets = nil
	return &cp
}

// scalarBodyForColumns drops a scalar sublink's unqualified `*` targets before
// the walk (M0146-0122). transformExpr expands such a star over the sublink's
// OWN FROM list (ExpandColumnRefStar: the current level's namespace only), so
// every Var it makes has varlevelsup 0 and none is an outer relation's column.
// The sublink is planned by its own planSelect, which computes its own needed
// set, and an EXPR sublink is never pulled up into the outer join tree
// (pull_up_sublinks converts only ANY / EXISTS). TPC-DS Q23's
// `HAVING sum(...) > 0.95 * (SELECT * FROM max_store_sales)` had voided the
// whole set, so best_ss_customer's `customer` got no index-only path. A
// qualified star (`t.*`) may name an outer relation and is still walked —
// which declines.
func scalarBodyForColumns(sub *parser.SelectStmt) *parser.SelectStmt {
	if sub == nil || len(sub.Targets) == 0 {
		return sub
	}
	keep := make([]parser.ResTarget, 0, len(sub.Targets))
	for _, t := range sub.Targets {
		if st, ok := t.Expr.(*parser.StarExpr); ok && st.Schema == "" && st.Table == "" {
			continue
		}
		keep = append(keep, t)
	}
	if len(keep) == len(sub.Targets) {
		return sub
	}
	cp := *sub
	cp.Targets = keep
	return &cp
}
