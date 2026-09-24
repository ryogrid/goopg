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
