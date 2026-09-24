package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// flatScan builds a bare *SeqScan fixture with the given column names.
func flatScan(names ...string) *SeqScan {
	s := &SeqScan{}
	for _, n := range names {
		s.schema = append(s.schema, SchemaColumn{Name: n})
	}
	return s
}

// flatJoin builds an inner Join fixture whose schema is its inputs'
// concatenation (the leaf-concat shape decomposeFlatBodyTree expects).
func flatJoin(left, right Node, pred Expr) *Join {
	return &Join{
		Type:      JoinTypeInner,
		Left:      left,
		Right:     right,
		Predicate: pred,
		schema:    append(append(Schema(nil), left.Output()...), right.Output()...),
	}
}

func flatGt(idx int, v int64) *BinaryOp {
	return &BinaryOp{Op: parser.OpGt, Left: &ColumnRef{Index: idx}, Right: &IntegerConst{Value: v}}
}

func flatEq(a, b int) *BinaryOp {
	return &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: a}, Right: &ColumnRef{Index: b}}
}

// colIndex extracts the first ColumnRef index found in e (test helper for
// coordinate assertions on pooled quals).
func colIndex(e Expr) (int, bool) {
	switch x := e.(type) {
	case *ColumnRef:
		return x.Index, true
	case *BinaryOp:
		if i, ok := colIndex(x.Left); ok {
			return i, true
		}
		return colIndex(x.Right)
	}
	return 0, false
}

// M0142-0008a-3i-route-a step 2 (design doc §step-2): decomposeFlatBodyTree
// decomposes a simple planned sublink body into real scan leaves + pooled
// conjuncts rebased into the body's global leaf-concat coordinates.
func TestDecomposeFlatBodyTreeShapes(t *testing.T) {
	t.Run("bare scan", func(t *testing.T) {
		s := flatScan("y", "z")
		leaves, quals, _, body, ok := decomposeFlatBodyTree(s, false)
		if !ok || len(leaves) != 1 || leaves[0] != Node(s) || len(quals) != 0 || body != Node(s) {
			t.Fatalf("leaves=%v quals=%v body=%T ok=%v", leaves, quals, body, ok)
		}
	})

	t.Run("leaf-local filter pools shifted qual", func(t *testing.T) {
		s := flatScan("y", "z")
		f := &Filter{Child: s, Predicate: flatGt(0, 5), LeafLocal: true}
		leaves, quals, _, _, ok := decomposeFlatBodyTree(f, false)
		if !ok || len(leaves) != 1 || len(quals) != 1 {
			t.Fatalf("leaves=%d quals=%d ok=%v", len(leaves), len(quals), ok)
		}
		// base=0: the leaf-local index survives unchanged.
		if i, _ := colIndex(quals[0]); i != 0 {
			t.Errorf("qual ref index=%d, want 0", i)
		}
	})

	t.Run("two-leaf join shifts right subtree quals", func(t *testing.T) {
		t2 := flatScan("y", "z")
		t3 := flatScan("a", "b")
		body := flatJoin(
			&Filter{Child: t2, Predicate: flatGt(0, 5), LeafLocal: true},
			&Filter{Child: t3, Predicate: flatGt(0, 7), LeafLocal: true},
			flatEq(0, 2),
		)
		leaves, quals, _, bodyOut, ok := decomposeFlatBodyTree(body, false)
		if !ok {
			t.Fatalf("decompose declined: leaves=%v", leaves)
		}
		if len(leaves) != 2 || leaves[0] != Node(t2) || leaves[1] != Node(t3) {
			t.Fatalf("leaves=%v", leaves)
		}
		if bodyOut != Node(body) {
			t.Fatalf("body=%T, want the join itself", bodyOut)
		}
		if len(quals) != 3 {
			t.Fatalf("quals=%d, want 3 (leaf t2, leaf t3, join pred)", len(quals))
		}
		// t3's leaf-local qual must be shifted by t2's width (2).
		var t3QualSeen bool
		for _, q := range quals {
			if i, ok := colIndex(q); ok && i == 2 {
				if bo, isBin := q.(*BinaryOp); isBin && bo.Op == parser.OpGt {
					t3QualSeen = true
				}
			}
		}
		if !t3QualSeen {
			t.Errorf("no pooled qual references leaf-concat index 2 (t3's shifted qual)")
		}
	})

	t.Run("root project peeled for wantTarget", func(t *testing.T) {
		s := flatScan("y", "z")
		tgt := &ColumnRef{Index: 0, Name: "y"}
		p := &Project{
			Child:   s,
			Targets: []Expr{tgt},
			schema:  Schema{{Name: "y"}},
		}
		leaves, _, gotTgt, body, ok := decomposeFlatBodyTree(p, true)
		if !ok || len(leaves) != 1 || gotTgt != tgt || body != Node(s) {
			t.Fatalf("ok=%v leaves=%v tgt=%v body=%T", ok, leaves, gotTgt, body)
		}
	})

	t.Run("wantTarget single-column bare body recovers target", func(t *testing.T) {
		s := flatScan("y")
		_, _, tgt, _, ok := decomposeFlatBodyTree(s, true)
		if !ok || tgt == nil {
			t.Fatalf("ok=%v tgt=%v", ok, tgt)
		}
		cr, isRef := tgt.(*ColumnRef)
		if !isRef || cr.Index != 0 {
			t.Fatalf("tgt=%T %v, want ColumnRef{Index:0}", tgt, tgt)
		}
	})
}

func TestDecomposeFlatBodyTreeDeclines(t *testing.T) {
	t2 := flatScan("y", "z")
	t3 := flatScan("a", "b")

	cases := map[string]Node{
		"index scan":   &IndexScan{schema: Schema{{Name: "y"}}},
		"gather":       &Gather{Child: t2},
		"lateral join": &Join{Type: JoinTypeInner, Left: t2, Right: t3, Lateral: true},
		"outer join":   &Join{Type: JoinTypeLeft, Left: t2, Right: t3},
		"leaf-local over non-scan": &Filter{
			Child:     flatJoin(t2, t3, nil),
			Predicate: flatGt(0, 1),
			LeafLocal: true,
		},
		"nested non-identity project": &Filter{
			Child: &Project{
				Child:   t2,
				Targets: []Expr{&IntegerConst{Value: 1}, &IntegerConst{Value: 2}},
				schema:  t2.Output(),
			},
			Predicate: flatGt(0, 1),
		},
		"sublink qual": &Filter{
			Child:     t2,
			Predicate: &ExistsExpr{Plan: flatScan("q")},
		},
		"non-identity root project": &Project{
			Child:   flatJoin(t2, t3, nil),
			Targets: []Expr{&IntegerConst{Value: 1}},
			schema:  Schema{{Name: "one"}},
		},
		"output not leaf-concat": &Join{
			Type: JoinTypeInner, Left: t2, Right: t3,
			schema: Schema{{Name: "y"}}, // narrowed: 1 col vs leaf-concat 4
		},
	}
	for name, n := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, _, _, ok := decomposeFlatBodyTree(n, false); ok {
				t.Errorf("decompose accepted %s", name)
			}
		})
	}

	// wantTarget with a root Project that does not carry exactly one
	// target, or a non-Project root wider than one column, cannot recover
	// the IN comparison column.
	t.Run("wantTarget multi-target project", func(t *testing.T) {
		s := flatScan("y", "z")
		p := &Project{
			Child:   s,
			Targets: []Expr{&ColumnRef{Index: 0}, &ColumnRef{Index: 1}},
			schema:  s.Output(),
		}
		if _, _, _, _, ok := decomposeFlatBodyTree(p, true); ok {
			t.Error("accepted a two-target root project")
		}
	})
	t.Run("wantTarget multi-column bare body", func(t *testing.T) {
		if _, _, _, _, ok := decomposeFlatBodyTree(flatScan("y", "z"), true); ok {
			t.Error("accepted a two-column bare body")
		}
	})
}

// flatBodyScopeProject rebuilds a stripped IN/NOT-IN body as an
// IsolatedScope positional-identity Project — restoring the executor's
// leaf-concat row contract and the NLI/pushdown protection the body's
// original root project carried.
func TestFlatBodyScopeProjectRoundTrip(t *testing.T) {
	t2 := flatScan("y", "z")
	t3 := flatScan("a", "b")
	body := flatJoin(
		&Filter{Child: t2, Predicate: flatGt(0, 5), LeafLocal: true},
		&Filter{Child: t3, Predicate: flatGt(0, 7), LeafLocal: true},
		flatEq(0, 2),
	)
	p := flatBodyScopeProject(body)
	if !p.IsolatedScope {
		t.Error("IsolatedScope not set — the NLI/pushdown protection is lost")
	}
	if len(p.Output()) != len(body.Output()) {
		t.Fatalf("output width=%d, want leaf-concat %d", len(p.Output()), len(body.Output()))
	}
	if !projectIsPositionalIdentityAnyScope(p) {
		t.Error("wrapper is not a positional-identity project")
	}
	if projectIsPositionalIdentity(p) {
		t.Error("strict identity check should reject the IsolatedScope wrapper")
	}
	// The wrapped body still decomposes: the identity project preserves the
	// leaf-concat output the seam's inner-operand convention requires.
	leaves, quals, _, back, ok := decomposeFlatBodyTree(p, false)
	if !ok || len(leaves) != 2 || len(quals) != 3 {
		t.Fatalf("decompose(wrapped): ok=%v leaves=%d quals=%d", ok, len(leaves), len(quals))
	}
	if back != Node(p) {
		t.Errorf("body=%T, want the wrapper itself (wantTarget=false peels nothing)", back)
	}
}

func TestSchemaIsLeafConcat(t *testing.T) {
	t2 := flatScan("y", "z")
	t3 := flatScan("a", "b")
	concat := append(append(Schema(nil), t2.Output()...), t3.Output()...)
	if !schemaIsLeafConcat(concat, []Node{t2, t3}) {
		t.Error("concat declined")
	}
	if schemaIsLeafConcat(concat[:3], []Node{t2, t3}) {
		t.Error("narrowed schema accepted")
	}
	widened := append(append(Schema(nil), concat...), SchemaColumn{Name: "extra"})
	if schemaIsLeafConcat(widened, []Node{t2, t3}) {
		t.Error("widened schema accepted")
	}
	// Column identity is Name + SourceTableIdx.
	mismatch := append(Schema(nil), concat...)
	mismatch[0] = SchemaColumn{Name: "other"}
	if schemaIsLeafConcat(mismatch, []Node{t2, t3}) {
		t.Error("renamed column accepted")
	}
}

func TestLeafSpanWindow(t *testing.T) {
	spans := []leafSpan{{0, 1}, {1, 3}, {3, 5}}
	check := func(lo, hi int, want leafSpan, wantOK bool) {
		got, ok := leafSpanWindow(spans, lo, hi)
		if ok != wantOK || (ok && got != want) {
			t.Errorf("window(%d,%d) = %v,%v; want %v,%v", lo, hi, got, ok, want, wantOK)
		}
	}
	check(0, 1, leafSpan{0, 1}, true)
	check(1, 3, leafSpan{1, 5}, true)
	check(0, 3, leafSpan{0, 5}, true)
	check(0, 0, leafSpan{}, false)
	check(-1, 2, leafSpan{}, false)
	check(0, 4, leafSpan{}, false)
	// A gap inside the window cannot tile.
	gapped := []leafSpan{{0, 1}, {3, 5}}
	if _, ok := leafSpanWindow(gapped, 0, 2); ok {
		t.Error("gapped window accepted")
	}
	// Overlapping spans cover more than [base,end).
	overlapping := []leafSpan{{0, 2}, {1, 3}}
	if _, ok := leafSpanWindow(overlapping, 0, 2); ok {
		t.Error("overlapping window accepted")
	}
}

// End-to-end: the unnest arms mark eligible EXISTS/IN/NOT-IN bodies, and the
// seam decomposes the marked RHS into real leaves + pooled body quals.
func TestFlattenedRHSExistsSingleTable(t *testing.T) {
	pinLegacyPipeline(t)
	cat := analyzedThreeTablesCatalog(t)
	node, err := Plan(parseOne(t,
		"SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2 WHERE t2.z = t1.x AND t2.y > 0)"), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no semi join: %s", planString(node))
	}
	if !j.FlattenedRHS {
		t.Fatalf("FlattenedRHS not set: %s", planString(node))
	}
	scans, widths, _, _, links, ok := extractSearchLeaves(j)
	if !ok || len(scans) != 2 || len(links) != 1 {
		t.Fatalf("ok=%v scans=%d links=%d", ok, len(scans), len(links))
	}
	for i, s := range scans {
		if _, isScan := s.(*SeqScan); !isScan {
			t.Errorf("leaf %d is %T, want *SeqScan (real leaf, not opaque)", i, s)
		}
	}
	lk := links[0]
	if !lk.flattened || lk.rhs != leafRangeRelSet(1, 2) || lk.sjinfo == nil {
		t.Errorf("link flat=%v rhs=%#x sj=%v", lk.flattened, lk.rhs, lk.sjinfo)
	}
	if len(lk.bodyQuals) != 1 {
		t.Errorf("bodyQuals=%d, want 1 (t2.y > 0)", len(lk.bodyQuals))
	}
	// The synthetic RHS leaf is relocated out-of-band after the real
	// 1-column span space.
	spans := buildLeafSpans(widths, links)
	if len(spans) != 2 || spans[0] != (leafSpan{0, 1}) || spans[1] != (leafSpan{1, 3}) {
		t.Errorf("spans=%v, want [{0 1} {1 3}]", spans)
	}
}

func TestFlattenedRHSExistsMultiTableBody(t *testing.T) {
	pinLegacyPipeline(t)
	cat := analyzedThreeTablesCatalog(t)
	node, err := Plan(parseOne(t,
		"SELECT x FROM t1 WHERE EXISTS (SELECT 1 FROM t2, t3 "+
			"WHERE t2.z = t1.x AND t2.y = t3.a AND t2.y > 0 AND t3.b > 0)"), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil || !j.FlattenedRHS {
		t.Fatalf("no flattened semi join: %s", planString(node))
	}
	scans, widths, _, _, links, ok := extractSearchLeaves(j)
	if !ok || len(scans) != 3 || len(links) != 1 {
		t.Fatalf("ok=%v scans=%d links=%d", ok, len(scans), len(links))
	}
	lk := links[0]
	// rhs spans BOTH body leaves — the semijoin fires only when the
	// joinrel covers the whole spliced RHS (PG's min_righthand).
	if !lk.flattened || lk.rhs != leafRangeRelSet(1, 3) {
		t.Errorf("link flat=%v rhs=%#x, want flattened rhs={1,2}", lk.flattened, lk.rhs)
	}
	// bodyQuals: t2.y>0, t3.b>0, and the t2.y=t3.a join qual.
	if len(lk.bodyQuals) != 3 {
		t.Errorf("bodyQuals=%d, want 3", len(lk.bodyQuals))
	}
	spans := buildLeafSpans(widths, links)
	want := []leafSpan{{0, 1}, {1, 3}, {3, 5}}
	if len(spans) != len(want) {
		t.Fatalf("spans=%v", spans)
	}
	for i := range want {
		if spans[i] != want[i] {
			t.Fatalf("spans=%v, want %v", spans, want)
		}
	}
}

func TestFlattenedRHSInExpr(t *testing.T) {
	pinLegacyPipeline(t)
	cat := analyzedThreeTablesCatalog(t)
	node, err := Plan(parseOne(t,
		"SELECT x FROM t1 WHERE t1.x IN (SELECT y FROM t2 WHERE z > 0)"), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil || !j.FlattenedRHS {
		t.Fatalf("no flattened semi join: %s", planString(node))
	}
	// The flattened IN body is re-wrapped in an IsolatedScope
	// positional-identity Project — the executor's leaf-concat row
	// contract AND the structural NLI protection (pickInnerSide needs a
	// bare *SeqScan right side).
	p, isProj := j.Right.(*Project)
	if !isProj || !p.IsolatedScope || !projectIsPositionalIdentityAnyScope(p) {
		t.Fatalf("j.Right=%T iso/identity=%v — flattened IN body lost its scope wrapper", j.Right, isProj)
	}
	// The seam still decomposes through the wrapper into real leaves.
	scans, _, _, _, links, ok := extractSearchLeaves(j)
	if !ok || len(scans) != 2 || len(links) != 1 || !links[0].flattened {
		t.Fatalf("ok=%v scans=%d links=%d", ok, len(scans), len(links))
	}
}

// A body whose planned inner projections narrow the leaf-concat schema is
// not flattenable — it stays one opaque RHS leaf, the pre-step-2 behaviour.
func TestFlattenedRHSDeclinesPrunedBody(t *testing.T) {
	cat := analyzedThreeTablesCatalog(t)
	node, err := Plan(parseOne(t,
		"SELECT x FROM t1 WHERE t1.x IN (SELECT y FROM t2, t3 WHERE t2.z = t3.a)"), cat)
	if err != nil {
		t.Fatal(err)
	}
	j := findFirstJoinByType(node, JoinTypeSemi)
	if j == nil {
		t.Fatalf("no semi join: %s", planString(node))
	}
	if j.FlattenedRHS {
		t.Fatalf("pruned body flattened anyway: %s", planString(node))
	}
	// The opaque RHS still extracts as one synthetic leaf.
	scans, _, _, _, links, ok := extractSearchLeaves(j)
	if !ok || len(scans) != 2 || len(links) != 1 || links[0].flattened {
		t.Fatalf("ok=%v scans=%d links=%v", ok, len(scans), links)
	}
}

// A demoted LEFT->ANTI link sits INSIDE the FROM chain, so a real leaf
// (date_dim/t3) can follow its synthetic RHS leaf in walk order — the
// mid-chain shape c2's tail-slot construction contract cannot express
// (`scans[nprefix:]` must BE the synthetic set). The seam declines it and
// the chain stays on the marker-join fallback; Q78 (TPC-DS) pinned this
// when a spurious offset-check pass let construction proceed and a join
// clause reached createPlan on a real-only layout. Unnest-produced
// Semi/Anti links are always chain-top, so their synthetic leaves are
// always the tail and this declines nothing the splice emits.
func TestSeamDeclinesRealLeafAfterSyntheticLeaf(t *testing.T) {
	cat := analyzedThreeTablesCatalog(t)
	sql := "SELECT x FROM t1 LEFT JOIN t2 ON t2.z = t1.x " +
		"JOIN t3 ON t3.a = t1.x WHERE t2.z IS NULL"
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatal(err)
	}
	// The fallback keeps the anti join nested under the trailing inner
	// join — no searched splice of the synthetic leaf.
	j := findFirstJoinByType(node, JoinTypeAnti)
	if j == nil {
		t.Fatalf("no anti join (the LEFT+IS NULL demote is missing): %s", planString(node))
	}
	inner := findFirstJoinByType(node, JoinTypeInner)
	if inner == nil {
		t.Fatalf("no inner join over the anti: %s", planString(node))
	}
}
