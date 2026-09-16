package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0142-0008a-2: unit tests for existsUnnestSJInfo, built from real
// unnestExistsExpr fixtures rather than the parser-facing
// makeSpecialJoinInfoScoped path — specialjoin_test.go's existing
// TestSpecialJoinInfoSemiJoin/TestSpecialJoinInfoAntiJoin never exercise a
// real SEMI/ANTI join (the parser has no SEMI JOIN syntax, so those tests
// stand in with LEFT JOIN); these close that "never exercised end-to-end"
// gap for the EXISTS/NOT EXISTS producer. Join.SJInfo is inert today (no
// consumer reads it) — these tests pin its VALUE for when M0142-0008a-3
// wires a reader, not any observable plan-shape change.

func TestExistsUnnestSJInfoSemiHashKey(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found: %s", planString(node))
	}
	sj := j.SJInfo
	if sj == nil {
		t.Fatal("Join.SJInfo is nil")
	}
	if sj.Jointype != parser.JoinSemi {
		t.Errorf("Jointype = %v, want JoinSemi", sj.Jointype)
	}
	if sj.SynLefthand != RelSet(1) || sj.SynRighthand != RelSet(2) {
		t.Errorf("Syn = {%v,%v}, want {1,2}", sj.SynLefthand, sj.SynRighthand)
	}
	if sj.MinLefthand != RelSet(1) || sj.MinRighthand != RelSet(2) {
		t.Errorf("Min = {%v,%v}, want {1,2} (2-relation join: min==syn)", sj.MinLefthand, sj.MinRighthand)
	}
	if !sj.LhsStrict {
		t.Error("LhsStrict = false, want true (equijoin key is a strict operator)")
	}
	if !sj.SemiCanHash || !sj.SemiCanBtree {
		t.Errorf("SemiCanHash=%v SemiCanBtree=%v, want true/true (hash-keyed SEMI)", sj.SemiCanHash, sj.SemiCanBtree)
	}
	// M0142-0008c-1: SemiRhsExprs must carry the subquery-side ("z")
	// operand of the equijoin, not the outer-side ("t1.x") one —
	// createUniquePath keys the RHS's own dedup on this list.
	if len(sj.SemiRhsExprs) != 1 {
		t.Fatalf("len(SemiRhsExprs) = %d, want 1", len(sj.SemiRhsExprs))
	}
	cr, ok := sj.SemiRhsExprs[0].(*ColumnRef)
	if !ok {
		t.Fatalf("SemiRhsExprs[0] = %T, want *ColumnRef", sj.SemiRhsExprs[0])
	}
	if cr.Name != "z" {
		t.Errorf("SemiRhsExprs[0].Name = %q, want %q", cr.Name, "z")
	}
	// M0142-0008a-3i-plumbing-c9: SemiRhsExprs[0] and j.RightKey are both
	// built from the same params[0].SubCol, so they must carry the SAME
	// (post-remapSourceTableIdx) SourceTableIdx. Before the c9 fix,
	// SemiRhsExprs held the verbatim pre-remap value while RightKey already
	// had +srcTableOffset applied, so this assertion catches a regression
	// back to that drift — the exact mismatch createUniquePath's own
	// schema-drift guard (createuniquepath.go) declines on, silently making
	// every SEMI unique-ify attempt fail even when Name/Index both agree.
	rightKey, ok := j.RightKey.(*ColumnRef)
	if !ok {
		t.Fatalf("j.RightKey = %T, want *ColumnRef (hash-keyed SEMI fixture)", j.RightKey)
	}
	if cr.SourceTableIdx != rightKey.SourceTableIdx {
		t.Errorf("SemiRhsExprs[0].SourceTableIdx = %d, want %d (j.RightKey.SourceTableIdx)",
			cr.SourceTableIdx, rightKey.SourceTableIdx)
	}
}

func TestExistsUnnestSJInfoAntiHashKey(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE NOT EXISTS (SELECT 1 FROM t2 WHERE z = t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeAnti)
	if j == nil {
		t.Fatalf("no JoinTypeAnti found: %s", planString(node))
	}
	sj := j.SJInfo
	if sj == nil {
		t.Fatal("Join.SJInfo is nil")
	}
	if sj.Jointype != parser.JoinAnti {
		t.Errorf("Jointype = %v, want JoinAnti", sj.Jointype)
	}
	if sj.MinLefthand != RelSet(1) || sj.MinRighthand != RelSet(2) {
		t.Errorf("Min = {%v,%v}, want {1,2}", sj.MinLefthand, sj.MinRighthand)
	}
	if !sj.LhsStrict {
		t.Error("LhsStrict = false, want true (equijoin key is a strict operator)")
	}
	// PG's compute_semijoin_info populates Semi* only for JOIN_SEMI, never
	// JOIN_ANTI (specialjoin.go:239-247) — ANTI must stay false/false.
	if sj.SemiCanHash || sj.SemiCanBtree {
		t.Errorf("SemiCanHash=%v SemiCanBtree=%v, want false/false for ANTI", sj.SemiCanHash, sj.SemiCanBtree)
	}
	if sj.SemiRhsExprs != nil {
		t.Errorf("SemiRhsExprs = %v, want nil for ANTI", sj.SemiRhsExprs)
	}
}

func TestExistsUnnestSJInfoKeylessSemi(t *testing.T) {
	// Matrix M14 (S4a/D3.2): zero-equijoin EXISTS, decorrelated as a
	// nested-loop semi join carrying the residual as its predicate — no
	// hash key exists, so LhsStrict and the Semi* capability flags must
	// fall back to their safe defaults even though the clause still spans
	// both sides (the residual alone is enough to avoid the empty-clause
	// punt).
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE z > t1.x)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found: %s", planString(node))
	}
	if j.Algo != JoinAlgoNestedLoop {
		t.Fatalf("Algo = %d, want JoinAlgoNestedLoop (keyless fixture)", j.Algo)
	}
	sj := j.SJInfo
	if sj == nil {
		t.Fatal("Join.SJInfo is nil")
	}
	if sj.MinLefthand != RelSet(1) || sj.MinRighthand != RelSet(2) {
		t.Errorf("Min = {%v,%v}, want {1,2} (residual alone spans both sides)", sj.MinLefthand, sj.MinRighthand)
	}
	if sj.LhsStrict {
		t.Error("LhsStrict = true, want false (no equijoin key, PG's safe default)")
	}
	if sj.SemiCanHash || sj.SemiCanBtree {
		t.Errorf("SemiCanHash=%v SemiCanBtree=%v, want false/false (no hash key to derive them from)", sj.SemiCanHash, sj.SemiCanBtree)
	}
}
