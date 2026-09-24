package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// M0142-0008-producer: unit tests for inUnnestSJInfo, the IN/NOT-IN
// sibling of existsUnnestSJInfo — built from real unnestInExpr /
// unnestNonCorrelatedInExpr fixtures (same pattern as
// exists_unnest_sjinfo_test.go). Without this producer the seam walk's
// semiAntiLinksHaveSJInfos gate decline-gated every IN-derived link, so
// no parser.JoinSemi/JoinAnti SJInfo ever reached ctx.joinInfoList from
// these two paths.
//
// The synthetic {1}/{2} relsets are placeholders by design — the seam
// walk renumbers them in place to real leaf bits
// (joinsearchseam.go:1445-1447), exactly as it does for the EXISTS
// producer's placeholder.

func TestInUnnestSJInfoCorrelatedSemi(t *testing.T) {
	pinLegacyPipeline(t)
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE y = t1.x)"
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
		t.Errorf("Min = {%v,%v}, want {1,2}", sj.MinLefthand, sj.MinRighthand)
	}
	if !sj.LhsStrict {
		t.Error("LhsStrict = false, want true (equijoin key is a strict operator)")
	}
	if !sj.SemiCanHash || !sj.SemiCanBtree {
		t.Errorf("SemiCanHash=%v SemiCanBtree=%v, want true/true (hash-keyed SEMI)", sj.SemiCanHash, sj.SemiCanBtree)
	}
	// SemiRhsExprs must carry the subquery-side ("y") operand of the
	// equijoin — the params' SubCols — not the outer side.
	if len(sj.SemiRhsExprs) != 1 {
		t.Fatalf("len(SemiRhsExprs) = %d, want 1", len(sj.SemiRhsExprs))
	}
	cr, ok := sj.SemiRhsExprs[0].(*ColumnRef)
	if !ok {
		t.Fatalf("SemiRhsExprs[0] = %T, want *ColumnRef", sj.SemiRhsExprs[0])
	}
	if cr.Name != "y" {
		t.Errorf("SemiRhsExprs[0].Name = %q, want %q", cr.Name, "y")
	}
}

func TestInUnnestSJInfoNonCorrelatedSemi(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE z > 0)"
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
	if !sj.LhsStrict {
		t.Error("LhsStrict = false, want true")
	}
	if !sj.SemiCanHash || !sj.SemiCanBtree {
		t.Errorf("SemiCanHash=%v SemiCanBtree=%v, want true/true", sj.SemiCanHash, sj.SemiCanBtree)
	}
	// The single equality conjunct's RHS is the inner plan's one output
	// column (innerOut[0] — "y"), whose SourceTableIdx must carry the
	// inner child's real source identity for createUniquePath's
	// schema-drift guard.
	if len(sj.SemiRhsExprs) != 1 {
		t.Fatalf("len(SemiRhsExprs) = %d, want 1", len(sj.SemiRhsExprs))
	}
	cr, ok := sj.SemiRhsExprs[0].(*ColumnRef)
	if !ok {
		t.Fatalf("SemiRhsExprs[0] = %T, want *ColumnRef", sj.SemiRhsExprs[0])
	}
	if cr.Name != "y" {
		t.Errorf("SemiRhsExprs[0].Name = %q, want %q", cr.Name, "y")
	}
	if out := j.Right.Output(); len(out) > 0 && cr.SourceTableIdx != out[0].SourceTableIdx {
		t.Errorf("SemiRhsExprs[0].SourceTableIdx = %d, want %d (innerOut[0]'s)", cr.SourceTableIdx, out[0].SourceTableIdx)
	}
}

// TestExtractSearchLeaves_InUnnestLinkPassesSJInfoGate is the reachability
// proof M0142-0008-producer exists for: a REAL IN-unnested plan's pinned
// Semi join now carries SJInfo, so the walk's link records it and
// semiAntiLinksHaveSJInfos — the gate that decline-gated every IN-derived
// link — accepts it. (The TPC-DS corpus cannot observe this today: every
// IN-subquery statement declines earlier at `leaf-count` /
// `outer-over-derived`, retired at M0145-0018 — measured in the
// m0142-0008-producer design doc.)
func TestExtractSearchLeaves_InUnnestLinkPassesSJInfoGate(t *testing.T) {
	cat := twoTablesCatalog(t)
	sql := "SELECT x FROM t1 WHERE x IN (SELECT y FROM t2 WHERE z > 0)"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no JoinTypeSemi found: %s", planString(node))
	}
	if j.SJInfo == nil {
		t.Fatal("Join.SJInfo is nil — producer did not fire")
	}
	if j.Predicate == nil && j.LeftKey != nil && j.RightKey != nil {
		j.Predicate = &BinaryOp{Op: parser.OpEq, Left: j.LeftKey, Right: j.RightKey}
	}
	scans, _, _, _, semiAnti, ok := extractSearchLeaves(j)
	if !ok {
		t.Fatalf("extractSearchLeaves(j) ok=false: %s", planString(node))
	}
	if len(semiAnti) != 1 {
		t.Fatalf("semiAnti = %d links, want 1", len(semiAnti))
	}
	if semiAnti[0].sjinfo == nil {
		t.Fatal("semiAnti[0].sjinfo = nil — the link did not carry the Join's SJInfo")
	}
	if semiAnti[0].sjinfo != j.SJInfo {
		t.Error("semiAnti[0].sjinfo is not j.SJInfo — walk recorded a different SJInfo")
	}
	if len(scans) != 2 {
		t.Fatalf("scans = %d leaves, want 2 (t1 + opaque RHS)", len(scans))
	}
	if !semiAntiLinksHaveSJInfos(semiAnti, []*SpecialJoinInfo{j.SJInfo}) {
		t.Error("semiAntiLinksHaveSJInfos = false — the producer's SJInfo fails the gate it was built to pass")
	}
}
