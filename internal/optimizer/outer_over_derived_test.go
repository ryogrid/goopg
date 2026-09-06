package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// baseTable is a leaf that really is a base relation: a catalog table with a
// scan node over it.
func baseTable() *catalog.Table { return &catalog.Table{Name: "base"} }

// cteTable is what with.go actually hands a CTE binding: a SYNTHESISED
// catalog.Table so column resolution works — non-nil, and with no statistics
// behind it. This is the case the firewall's original `table == nil` test
// missed, and it is why every fixture below that means "derived" builds the
// leaf THIS way and not with a nil table. A nil-table fixture would have
// passed on the broken classifier and proved nothing (2026-09-06, TPC-DS Q78:
// traced `derived=[false false false]` with all three CTE leaves in hand).
func cteTable(name string) *catalog.Table { return &catalog.Table{Name: name} }

func cteLeaf(name string) Node { return &CTEScan{Name: name} }

// wrappedCTELeaf is the shape a CTE output actually reaches the search in
// when a predicate was pushed onto it: `Filter -> CTEScan`. Q78's three
// leaves arrived this way, and a classifier that looks only at the top node
// classifies them as base.
func wrappedCTELeaf(name string) Node { return &Filter{Child: &CTEScan{Name: name}} }
func baseLeaf() Node          { return &SeqScan{} }

// TestProblemPairsOuterWithDerivedCTEOuter fires on the Q78 shape: three
// CTE-scan inputs — each carrying with.go's synthesised, non-nil table —
// joined by LEFT hands. The classifier must recognise them by NODE TYPE.
func TestProblemPairsOuterWithDerivedCTEOuter(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{cteLeaf("ss"), cteLeaf("ws"), cteLeaf("cs")},
		relInfos: []baseRelInfo{{table: cteTable("ss")}, {table: cteTable("ws")}, {table: cteTable("cs")}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}, {lo: 2, hi: 3}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinLeft, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
		{Jointype: parser.JoinLeft, SynLefthand: (1 << 0) | (1 << 1), SynRighthand: 1 << 2},
	}
	if !problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("Q78-shaped outer-over-derived problem must decline: three CTE " +
			"leaves with synthesised (non-nil) tables were classified as base")
	}
}

// TestProblemPairsOuterWithDerivedWrappedCTE pins the shape that actually
// escaped: CTE leaves wrapped in a pushed-down Filter. This is Q78 as the
// search really sees it, and it is the case the bare-CTEScan fixture above
// cannot exercise.
func TestProblemPairsOuterWithDerivedWrappedCTE(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{wrappedCTELeaf("ss"), wrappedCTELeaf("ws"), wrappedCTELeaf("cs")},
		relInfos: []baseRelInfo{{table: cteTable("ss")}, {table: cteTable("ws")}, {table: cteTable("cs")}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}, {lo: 2, hi: 3}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinLeft, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
		{Jointype: parser.JoinLeft, SynLefthand: (1 << 0) | (1 << 1), SynRighthand: 1 << 2},
	}
	if !problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("Filter-wrapped CTE leaves under LEFT hands must decline — the " +
			"classifier must unwrap to the scan, this is how Q78 escaped")
	}
}

// TestProblemPairsOuterWithDerivedRecursiveCTE pins the sibling node type:
// a recursive CTE's worktable is derived too.
func TestProblemPairsOuterWithDerivedRecursiveCTE(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{&WorkTableScan{}, baseLeaf()},
		relInfos: []baseRelInfo{{table: cteTable("r")}, {table: baseTable()}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinLeft, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
	}
	if !problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("a recursive CTE worktable under a LEFT hand must decline")
	}
}

// TestProblemPairsOuterWithDerivedNilTableFallback keeps the original
// `table == nil` arm: a leaf with no binding table at all is derived even if
// its node type is not one the switch names.
func TestProblemPairsOuterWithDerivedNilTableFallback(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{baseLeaf(), baseLeaf()},
		relInfos: []baseRelInfo{{table: nil}, {table: baseTable()}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinLeft, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
	}
	if !problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("a nil-table leaf under a LEFT hand must still decline (fallback arm)")
	}
}

// TestProblemPairsOuterWithDerivedBaseOnly pins the Q72 shape: base leaves
// under LEFT hands stay searched.
func TestProblemPairsOuterWithDerivedBaseOnly(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{baseLeaf(), baseLeaf(), baseLeaf()},
		relInfos: []baseRelInfo{{table: baseTable()}, {table: baseTable()}, {table: baseTable()}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}, {lo: 2, hi: 3}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinLeft, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
	}
	if problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("base-only outer problem must stay searched")
	}
}

// TestProblemPairsOuterWithDerivedInnerOverDerived pins the A4 carve-out:
// inner-only problems over derived inputs are unaffected.
func TestProblemPairsOuterWithDerivedInnerOverDerived(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{cteLeaf("a"), cteLeaf("b")},
		relInfos: []baseRelInfo{{table: cteTable("a")}, {table: cteTable("b")}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinInner, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
	}
	if problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("inner-only problem over derived inputs must stay searched")
	}
}

// TestProblemPairsOuterWithDerivedReadsThroughSubproblems pins the
// read-through rule: a multi-leaf item whose statement leaves are all base
// is not derived even though a searched sub-problem's own rel is table-less.
func TestProblemPairsOuterWithDerivedReadsThroughSubproblems(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{baseLeaf(), baseLeaf(), baseLeaf()},
		relInfos: []baseRelInfo{{table: baseTable()}, {table: baseTable()}, {table: baseTable()}},
	}
	items := []joinlistRel{
		{lo: 0, hi: 2, info: baseRelInfo{}}, // searched sub-problem: table-less rel, base leaves
		{lo: 2, hi: 3},
	}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinLeft, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
	}
	if problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("outer over a base-leaf sub-problem must stay searched (read through to statement leaves)")
	}
}

// TestProblemPairsOuterWithDerivedUntouchedDerived pins the touching rule:
// a derived input the outer hands do not read does not decline.
func TestProblemPairsOuterWithDerivedUntouchedDerived(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{baseLeaf(), baseLeaf(), cteLeaf("c")},
		relInfos: []baseRelInfo{{table: baseTable()}, {table: baseTable()}, {table: cteTable("c")}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}, {lo: 2, hi: 3}}
	sjis := []*SpecialJoinInfo{
		{Jointype: parser.JoinLeft, SynLefthand: 1 << 0, SynRighthand: 1 << 1},
	}
	if problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("outer hands that do not touch the derived input must stay searched")
	}
}

// TestProblemPairsOuterWithDerivedReducedRightLink is C-04b's unit fixture:
// a RIGHT link's SpecialJoinInfo is the reduced LEFT one (`reduceRightLink`
// — hands swapped, the preserved leaf on the LEFT), and the firewall must
// read it exactly as it reads a LEFT link's: Filter-wrapped CTE leaves under
// its hands decline. The end-to-end version, through the seam and read off
// the trace, is TestSeamRightLinkOverDerivedInputsDeclines.
func TestProblemPairsOuterWithDerivedReducedRightLink(t *testing.T) {
	prob := &joinlistProblem{
		scans:    []Node{wrappedCTELeaf("ss"), wrappedCTELeaf("ws"), wrappedCTELeaf("cs")},
		relInfos: []baseRelInfo{{table: cteTable("ss")}, {table: cteTable("ws")}, {table: cteTable("cs")}},
	}
	items := []joinlistRel{{lo: 0, hi: 1}, {lo: 1, hi: 2}, {lo: 2, hi: 3}}
	jt, synL, synR := reduceRightLink((1<<0)|(1<<1), 1<<2)
	sjis := []*SpecialJoinInfo{{Jointype: jt, SynLefthand: synL, SynRighthand: synR, MinLefthand: synL, MinRighthand: synR}}
	if jt != parser.JoinLeft || synL != 1<<2 || synR != (1<<0)|(1<<1) {
		t.Fatalf("reduceRightLink = (%s, %#x, %#x), want (LEFT, {2}, {0,1})", joinTypeName(jt), synL, synR)
	}
	if !problemPairsOuterWithDerived(sjis, items, prob) {
		t.Errorf("Filter-wrapped CTE leaves under a reduced RIGHT link's hands must decline")
	}
}
