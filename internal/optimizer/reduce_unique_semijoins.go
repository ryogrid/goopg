package optimizer

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// pulledSemiRhsIsUnique is PG's `reduce_unique_semijoins`
// (postgres/src/backend/optimizer/plan/analyzejoins.c:844, run by
// `query_planner`, planmain.c:234) for one pulled sublink body: a semijoin
// whose right-hand side is a SINGLE rel that is provably unique for the join
// clauses is no semijoin at all — every outer row joins at most one RHS row —
// so PG deletes its SpecialJoinInfo and the pair is planned, estimated and
// legality-checked as an ordinary inner join. goopg kept every pulled
// SpecialJoinInfo, so TPC-H Q18's `orders ⋈ ANY_subquery` stayed a SEMI join
// whose paths tie the unique-ified inner join exactly, and the incumbent SEMI
// path won (M0145-0008ad).
//
// The proof is `innerrel_is_unique` → `rel_is_distinct_for`
// (analyzejoins.c), over the mergejoinable equality clauses linking the RHS to
// the left-hand side. Two RHS kinds are ported:
//
//   - RTE_SUBQUERY — the derived ANY_subquery leaf (M0145-0008aa). Its one
//     link clause is `operand = col0`, and `jtPulledBody.distinct` is
//     `query_is_distinct_for` on col0 (M0145-0008ab).
//   - RTE_RELATION — a base-table leaf with a non-partial UNIQUE index whose
//     every column is equated, by a plain `=` between same-typed columns, to
//     something on the left-hand side (`relation_has_unique_index_for`).
//
// Everything else answers false, which keeps the SpecialJoinInfo — the
// pre-M0145-0008ae behaviour, never a wrong answer. Restriction clauses that
// equate an RHS column to a constant (which PG's index proof also accepts) are
// not used: a missed proof only keeps the semijoin.
func pulledSemiRhsIsUnique(pb *jtPulledBody, rhs RelSet, spanning []Expr, spans []leafSpan, cat catalog.Catalog) bool {
	if pb == nil || pb.jointype != parser.JoinSemi {
		return false
	}
	// "Must be a semijoin to a single baserel" — a nested child body makes
	// the syntactic RHS larger than the body's own leaf.
	if len(pb.leafScans) != 1 || pb.subtreeLeaves != 1 {
		return false
	}
	// The RHS column names the equality clauses equate to the left side.
	var rhsCols []string
	for _, q := range spanning {
		b, ok := q.(*BinaryOp)
		if !ok || b.Op != parser.OpEq {
			continue
		}
		inner, other := equalityRhsSide(b, rhs, spans)
		if inner == nil {
			continue
		}
		if oc, isCol := other.(*ColumnRef); !isCol || oc.Type.Name != inner.Type.Name {
			// Keep the proof to same-typed column pairs, whose `=` is the
			// operator the unique index (or the grouping) itself used.
			continue
		}
		rhsCols = append(rhsCols, inner.Name)
	}
	if len(rhsCols) == 0 {
		return false
	}
	if pb.derived {
		// The derived leaf has exactly one output column, so any equated
		// RHS column is col0.
		return pb.distinct
	}
	scan, ok := pb.leafScans[0].(*SeqScan)
	if !ok || scan.Table == nil {
		return false
	}
	for _, key := range uniqueKeyColumnSets(cat, scan.Table) {
		if columnsCover(rhsCols, key) {
			return true
		}
	}
	return false
}

// equalityRhsSide splits `a = b` into the operand that is a bare column of the
// RHS rels and the other operand, which must not touch the RHS. It returns nil
// when neither side qualifies.
func equalityRhsSide(b *BinaryOp, rhs RelSet, spans []leafSpan) (*ColumnRef, Expr) {
	try := func(inner, other Expr) (*ColumnRef, Expr) {
		cr, ok := inner.(*ColumnRef)
		if !ok {
			return nil, nil
		}
		irs, iok := relidsOfExpr(cr, spans)
		ors, ook := relidsOfExpr(other, spans)
		if !iok || !ook || !relsSubset(irs, rhs) || relsOverlap(ors, rhs) {
			return nil, nil
		}
		return cr, other
	}
	if cr, other := try(b.Right, b.Left); cr != nil {
		return cr, other
	}
	return try(b.Left, b.Right)
}

// columnsCover reports whether every column of key appears in cols.
func columnsCover(cols, key []string) bool {
	have := make(map[string]bool, len(cols))
	for _, c := range cols {
		have[c] = true
	}
	for _, k := range key {
		if !have[k] {
			return false
		}
	}
	return true
}
