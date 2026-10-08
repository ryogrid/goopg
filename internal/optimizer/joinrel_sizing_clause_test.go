package optimizer

import "testing"

// clauseRecordingBuilder records the clause list each joinrel is sized with.
type clauseRecordingBuilder struct {
	recordingBuilder
	sizedWith map[RelSet][]*restrictInfo
}

func (b *clauseRecordingBuilder) sizeJoinRel(outer, inner *RelOptInfo, clauses []*restrictInfo, sj *SpecialJoinInfo) (float64, int) {
	if b.sizedWith == nil {
		b.sizedWith = map[RelSet][]*restrictInfo{}
	}
	b.sizedWith[outer.Relids|inner.Relids] = clauses
	return b.recordingBuilder.sizeJoinRel(outer, inner, clauses, sj)
}

// TestJoinRelIsSizedWithTheGeneratedClause pins M0146-0117 against
// set_joinrel_size_estimates (costsize.c): build_join_rel sizes a new joinrel
// with the restrictlist build_joinrel_restrictlist made for the pair that
// created it, which carries ONE generated clause per equivalence class
// (generate_join_implied_equalities_normal: first outer member = first inner
// member). goopg sized with the written clause instead (explicit-first), so
// TPC-DS Q59's top join was estimated through `wss.d_week_seq =
// (wss_1.d_week_seq - 52)`, two stats-less CTE columns at 1/200, where PG uses
// `d.d_week_seq = wss.d_week_seq` and date_dim's statistics (15 rows vs 1).
func TestJoinRelIsSizedWithTheGeneratedClause(t *testing.T) {
	cum, c := riTestCols(3)
	// Written a = b, then b = c: ec_members order is a, b, c. At {a,b} ⋈ {c}
	// the written clause is b = c; PG generates a = c.
	conjuncts := []Expr{riEq(c[0], c[1]), riEq(c[1], c[2])}
	conjuncts = append(conjuncts, inferTransitiveEqualities(conjuncts)...)
	l := buildRestrictInfos(conjuncts, len(conjuncts)-2, cum)
	l.ecReduce = true

	s := jslCtx(t, 3)
	s.clauses = l
	b := &clauseRecordingBuilder{}
	s.builder = b
	ab, err := s.makeJoinRel(s.findRel(rA), s.findRel(rB))
	if err != nil {
		t.Fatalf("makeJoinRel(a,b): %v", err)
	}
	if _, err := s.makeJoinRel(ab, s.findRel(rC)); err != nil {
		t.Fatalf("makeJoinRel(ab,c): %v", err)
	}
	got := b.sizedWith[rA|rB|rC]
	if len(got) != 1 {
		t.Fatalf("{a,b} ⋈ {c} sized with %d clauses, want the one generated clause", len(got))
	}
	lc, rc, ok := isColumnRefEquality(got[0].clause)
	if !ok || lc != c[0] || rc != c[2] {
		t.Fatalf("{a,b} ⋈ {c} sized with %v, want a = c", got[0].clause)
	}
}
